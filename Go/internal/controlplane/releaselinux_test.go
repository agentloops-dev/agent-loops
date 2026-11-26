package controlplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A Linux machine's first install takes the control plane's release from the public
// releases when the server folder has no host for it (#5787, epic #5785).

func hexSum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// linuxTarball is a release's agentsd-linux-<arch>-<version>.tar.gz, as release-host.sh
// makes it: each program beside its .sha256. A name in bad gets a .sha256 that does not
// match it.
func linuxTarball(t *testing.T, programs map[string]string, bad ...string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, body string, mode int64) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range programs {
		add(name, body, 0o755)
		sum := hexSum(body)
		for _, b := range bad {
			if b == name {
				sum = hexSum("other")
			}
		}
		add(name+".sha256", sum+"\n", 0o644)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestReleaseVersionIsNamed(t *testing.T) {
	rf, _ := releaseRig(t, "0.12.0", nil, nil)
	rec := httptest.NewRecorder()
	rf.serve(rec, httptest.NewRequest(http.MethodGet, "/v1/releases/VERSION", nil))
	if rec.Code != 200 || rec.Body.String() != "0.12.0\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	dev := newReleaseFiles(Config{Version: "0.12.0-dev"})
	rec = httptest.NewRecorder()
	dev.serve(rec, httptest.NewRequest(http.MethodGet, "/v1/releases/VERSION", nil))
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
}

func TestTheLinuxReleaseIsUnpackedOnceAsTheServerFolder(t *testing.T) {
	tarball := linuxTarball(t, map[string]string{
		"agentsd-linux-aarch64":    "host",
		"github-mcp-linux-aarch64": "github",
		"linear-mcp-linux-aarch64": "linear",
		"agentsd-linux-x86_64":     "the other arch",
		"../escape":                "no",
	}, "linear-mcp-linux-aarch64")
	rf, hits := releaseRig(t, "0.12.0", map[string]string{"agentsd-linux-aarch64-0.12.0.tar.gz": tarball}, nil)
	dir, err := rf.linux(context.Background(), "aarch64")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"VERSION": "0.12.0\n", "agentsd-linux-aarch64": "host",
		"agentsd-linux-aarch64.sha256": hexSum("host") + "\n", "github-mcp-linux-aarch64": "github"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s: %q", name, got)
		}
	}
	// Not this arch, a program whose sum is wrong, and a name the server folder never
	// hands out are all left out.
	for _, name := range []string{"agentsd-linux-x86_64", "linear-mcp-linux-aarch64", "linear-mcp-linux-aarch64.sha256", "escape"} {
		if fileExists(filepath.Join(dir, name)) {
			t.Errorf("%s was unpacked", name)
		}
	}
	if fileExists(filepath.Join(rf.folder, "escape")) {
		t.Error("a name outside the folder was written")
	}
	before := hits.Load()
	if again, err := rf.linux(context.Background(), "aarch64"); err != nil || again != dir || hits.Load() != before {
		t.Fatalf("%s %v %d", again, err, hits.Load()-before)
	}
}

func TestATamperedLinuxReleaseIsRefused(t *testing.T) {
	tarball := linuxTarball(t, map[string]string{"agentsd-linux-x86_64": "host"})
	rf, _ := releaseRig(t, "0.12.0", map[string]string{"agentsd-linux-x86_64-0.12.0.tar.gz": tarball},
		map[string]string{"agentsd-linux-x86_64-0.12.0.tar.gz": "another tarball"})
	if _, err := rf.linux(context.Background(), "x86_64"); err == nil {
		t.Fatal("unpacked")
	}
	if _, err := rf.linux(context.Background(), "riscv64"); err == nil {
		t.Fatal("riscv64")
	}
}

// install.sh, against a control plane with an empty server folder, installs the host and
// its MCP servers from the releases, and refuses a tarball that does not match SHA256SUMS.
func TestTheInstallScriptFallsBackToTheReleases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	for _, tool := range []string{"curl", "tar", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not here")
		}
	}
	// The host records how it was started.
	host := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HOME/started-args\"\n"
	tarball := linuxTarball(t, map[string]string{"agentsd-linux-x86_64": host, "github-mcp-linux-x86_64": "#!/bin/sh\n"})
	for _, tc := range []struct {
		name     string
		tampered bool
	}{{"matches", false}, {"tampered", true}} {
		t.Run(tc.name, func(t *testing.T) {
			name := "agentsd-linux-x86_64-0.12.0.tar.gz"
			sums := hexSum(tarball) + "  " + name + "\n"
			if tc.tampered {
				sums = hexSum("another") + "  " + name + "\n"
			}
			releases := fakeReleaseDownloads(name, sums, tarball, releaseSig(t, "current", sums))
			defer releases.Close()
			// The control plane: no server folder, and no release files of its own.
			control := controlWithNoServerFolder()
			defer control.Close()

			dir := t.TempDir()
			bin, home := fakeLinuxBox(t, dir)
			address := strings.ReplaceAll(strings.ReplaceAll(control.URL, ":", "%3A"), "/", "%2F")
			code := "agents-control:2:h:-:key:secret:" + address + ":-:test"
			cmd := exec.Command("sh", "-s", "--", code, "box")
			cmd.Stdin = strings.NewReader(installScriptForTest())
			cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + dir,
				"AGENTS_RELEASE_DOWNLOADS=" + releases.URL}
			out, err := cmd.CombinedOutput()
			if tc.tampered {
				if err == nil || !strings.Contains(string(out), "did not match its checksum") {
					t.Fatalf("%v\n%s", err, out)
				}
				if _, err := os.Stat(filepath.Join(home, ".agents-server", "bin", "current")); err == nil {
					t.Fatal("installed")
				}
				return
			}
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			assertInstalledFromTheRelease(t, home, host, code, out)
		})
	}
}

// fakeReleaseDownloads serves the release's SHA256SUMS, its signature ("" has none) and tarball, as the release
// downloads do.
func fakeReleaseDownloads(name, sums, tarball, sig string) *httptest.Server {
	var asked []string
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/v0.12.0/SHA256SUMS.sig":
			if sig == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(sig))
		case "/v0.12.0/SHA256SUMS":
			_, _ = w.Write([]byte(sums))
		case "/v0.12.0/" + name:
			_, _ = w.Write([]byte(tarball))
		default:
			http.NotFound(w, r)
		}
	}))
}

// controlWithNoServerFolder serves a control plane's releases with no server folder and no
// release files of its own.
func controlWithNoServerFolder() *httptest.Server {
	rf := newReleaseFiles(Config{Version: "0.12.0"})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/releases/") {
			rf.serve(w, r)
			return
		}
		http.NotFound(w, r)
	}))
}

// fakeLinuxBox makes a bin folder with a uname that says Linux x86_64, a sudo that fails and,
// when this machine has none, a sha256sum; and an empty home. It returns both.
func fakeLinuxBox(t *testing.T, dir string) (bin, home string) {
	t.Helper()
	bin = filepath.Join(dir, "bin")
	home = filepath.Join(dir, "home")
	for _, d := range []string{bin, home} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	fakes := map[string]string{
		"uname": "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; esac\n",
		"sudo":  "#!/bin/sh\nexit 1\n",
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		fakes["sha256sum"] = "#!/bin/sh\nexec shasum -a 256 \"$@\"\n"
	}
	for n, body := range fakes {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(body), 0o755); err != nil { //nolint:gosec // G306: a test's script
			t.Fatal(err)
		}
	}
	return bin, home
}

// assertInstalledFromTheRelease checks the script fetched the release, installed the host and
// its MCP server, and started the host to join with the code.
func assertInstalledFromTheRelease(t *testing.T, home, host, code string, out []byte) {
	t.Helper()
	if !strings.Contains(string(out), "fetching release 0.12.0") {
		t.Fatalf("%s", out)
	}
	installed, _ := os.ReadFile(filepath.Join(home, ".agents-server", "bin", "current"))
	if string(installed) != host {
		t.Fatalf("installed %q\n%s", installed, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents-server", "bin", "github-mcp")); err != nil {
		t.Fatalf("github-mcp: %v\n%s", err, out)
	}
	args, _ := os.ReadFile(filepath.Join(home, "started-args"))
	if !strings.HasPrefix(string(args), "install\n--join\n"+code+"\n") {
		t.Fatalf("started with %q", args)
	}
}
