package controlplane

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/releasesig"
)

// A release's SHA256SUMS.sig passes through the control plane, which checks it only before
// it unpacks a release itself.

const linuxName = "agentsd-linux-x86_64-0.12.0.tar.gz"

func releaseSig(t *testing.T, key, sums string) string {
	t.Helper()
	b, err := os.ReadFile("../releasesig/testdata/test-" + key + ".pem") //nolint:gosec // G304: a test's own data
	if err != nil {
		t.Fatal(err)
	}
	k, err := releasesig.ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(releasesig.Sign(k, []byte(sums)))
}

func otherSig(t *testing.T, sums string) string {
	t.Helper()
	k, _, err := releasesig.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return string(releasesig.Sign(k, []byte(sums)))
}

func TestTheSignaturePassesThrough(t *testing.T) {
	rf, hits := releaseRig(t, "0.12.0", map[string]string{"a-0.12.0.zip": "zip"}, nil)
	for range 2 {
		code, sig := getRelease(rf, "0.12.0", "SHA256SUMS.sig")
		_, sums := getRelease(rf, "0.12.0", "SHA256SUMS")
		if code != 200 || sig != releaseSig(t, "current", sums) {
			t.Fatalf("%d %q", code, sig)
		}
	}
	if hits.Load() != 2 { // SHA256SUMS and its signature, once
		t.Fatalf("%d requests", hits.Load())
	}
	if code, _ := getRelease(rf, "0.11.0", "SHA256SUMS.sig"); code != 404 {
		t.Fatal(code)
	}
}

// A machine asks for SHA256SUMS first: its signature is kept with it.
func TestTheSignatureIsKeptWithTheSums(t *testing.T) {
	rf, _ := releaseRig(t, "0.12.0", map[string]string{"a-0.12.0.zip": "zip"}, nil)
	if code, _ := getRelease(rf, "0.12.0", "SHA256SUMS"); code != 200 {
		t.Fatal(code)
	}
	if !fileExists(filepath.Join(rf.folder, "0.12.0", "SHA256SUMS.sig")) {
		t.Fatal("not kept")
	}
}

func TestAReleaseWithNoSignature(t *testing.T) {
	rf, _ := releaseRig(t, "0.12.0", map[string]string{linuxName: linuxTarball(t, map[string]string{"agentsd-linux-x86_64": "host"}),
		"SHA256SUMS.sig": ""}, nil)
	if code, _ := getRelease(rf, "0.12.0", "SHA256SUMS"); code != 200 {
		t.Fatal(code)
	}
	if code, _ := getRelease(rf, "0.12.0", "SHA256SUMS.sig"); code != 404 {
		t.Fatal(code)
	}
	if _, err := rf.linux(context.Background(), "x86_64"); err == nil {
		t.Fatal("unpacked")
	}
}

// A folder filled by hand serves a release, with no internet.
func TestAFilledFolderServesASignedRelease(t *testing.T) {
	tarball := linuxTarball(t, map[string]string{"agentsd-linux-x86_64": "host"})
	sums := hexSum(tarball) + "  " + linuxName + "\n"
	for _, tc := range []struct {
		name, sums, sig, tarball string
		good                     bool
	}{
		{"current key", sums, releaseSig(t, "current", sums), tarball, true},
		{"spare key", sums, releaseSig(t, "spare", sums), tarball, true},
		{"another key", sums, otherSig(t, sums), tarball, false},
		{"no signature", sums, "", tarball, false},
		{"sums changed", hexSum(tarball+"x") + "  " + linuxName + "\n", releaseSig(t, "current", sums), tarball + "x", false},
		{"tarball changed", sums, releaseSig(t, "current", sums), tarball + "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENTS_RELEASE_KEY", "")
			down := httptest.NewServer(http.NotFoundHandler())
			down.Close()
			rf := newReleaseFiles(Config{Version: "0.12.0", ReleaseFolder: t.TempDir(), ReleaseSource: down.URL})
			dir := filepath.Join(rf.folder, "0.12.0")
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{"SHA256SUMS": tc.sums, "SHA256SUMS.sig": tc.sig, linuxName: tc.tarball} {
				if body == "" {
					continue
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				// A machine takes each as it is.
				if code, got := getRelease(rf, "0.12.0", name); code != 200 || got != body {
					t.Fatalf("%s: %d", name, code)
				}
			}
			_, err := rf.linux(context.Background(), "x86_64")
			if (err == nil) != tc.good {
				t.Fatal(err)
			}
			if fileExists(filepath.Join(dir, "linux-x86_64", "agentsd-linux-x86_64")) != tc.good {
				t.Fatal("unpacked")
			}
		})
	}
}

func TestTheInstallScriptHasTheReleaseKeys(t *testing.T) {
	if !strings.Contains(InstallScript, "release_keys=\""+releasesig.CurrentKey+"\n"+releasesig.SpareKey+"\"\n") {
		t.Fatal("install.sh's keys are not releasesig's: copy them again")
	}
}

// installScriptForTest trusts the test keys, whatever the release's keys are.
func installScriptForTest() string {
	return strings.NewReplacer(releasesig.CurrentKey, releasesig.TestCurrentKey,
		releasesig.SpareKey, releasesig.TestSpareKey).Replace(InstallScript)
}

// openSSL3Path is a PATH whose openssl can check a signature, or "": macOS's own cannot, and
// Homebrew's can.
func openSSL3Path() string {
	for _, path := range []string{os.Getenv("PATH"), "/opt/homebrew/opt/openssl@3/bin:" + os.Getenv("PATH")} {
		cmd := exec.Command("sh", "-c", "openssl pkeyutl -help 2>&1")
		cmd.Env = []string{"PATH=" + path}
		if out, _ := cmd.Output(); strings.Contains(string(out), "-rawin") {
			return path
		}
	}
	return ""
}

// installFromReleases runs install.sh against release files, on a fake Linux machine, with
// the PATH's own folders after its bin. ran is whether a program of the release ran.
func installFromReleases(t *testing.T, path, tarball, sums, sig string, env ...string) (out string, ran bool, err error) {
	t.Helper()
	releases := fakeReleaseDownloads(linuxName, sums, tarball, sig)
	defer releases.Close()
	control := controlWithNoServerFolder()
	defer control.Close()
	dir := t.TempDir()
	bin, home := fakeLinuxBox(t, dir)
	address := strings.ReplaceAll(strings.ReplaceAll(control.URL, ":", "%3A"), "/", "%2F")
	cmd := exec.Command("sh", "-s", "--", "agents-control:2:h:-:key:secret:"+address+":-:test", "box") //nolint:gosec // G204: a test's own script
	cmd.Stdin = strings.NewReader(installScriptForTest())
	cmd.Env = append([]string{"PATH=" + bin + ":" + path, "HOME=" + home, "TMPDIR=" + dir,
		"AGENTS_RELEASE_DOWNLOADS=" + releases.URL}, env...)
	b, err := cmd.CombinedOutput()
	_, stat := os.Stat(filepath.Join(home, "started-args"))
	if _, installed := os.Lstat(filepath.Join(home, ".agents-server", "bin", "current")); (installed == nil) != (stat == nil) {
		t.Errorf("installed but not started, or the other way round\n%s", b)
	}
	return string(b), stat == nil, err
}

func TestTheInstallScriptChecksTheSignature(t *testing.T) {
	path := openSSL3Path()
	if path == "" {
		t.Skip("openssl 3 is not here")
	}
	tarball := linuxTarball(t, map[string]string{"agentsd-linux-x86_64": "#!/bin/sh\nprintf x > \"$HOME/started-args\"\n"})
	sums := hexSum(tarball) + "  " + linuxName + "\n"
	own, pemText, err := releasesig.NewPrivateKey()
	if err != nil || len(pemText) == 0 {
		t.Fatal(err)
	}
	line, err := releasesig.PublicText(own.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	ownFile := filepath.Join(t.TempDir(), "own.pem")
	if err := os.WriteFile(ownFile, []byte("-----BEGIN PUBLIC KEY-----\n"+line+"\n-----END PUBLIC KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ownSig := string(releasesig.Sign(own, []byte(sums)))
	for _, tc := range []struct {
		name, sums, sig, tarball, env string
		good                          bool
	}{
		{"current key", sums, releaseSig(t, "current", sums), tarball, "", true},
		{"spare key", sums, releaseSig(t, "spare", sums), tarball, "", true},
		{"another key", sums, ownSig, tarball, "", false},
		{"own key", sums, ownSig, tarball, "AGENTS_RELEASE_KEY=" + ownFile, true},
		{"bad", sums, "bad\n", tarball, "", false},
		{"missing", sums, "", tarball, "", false},
		{"sums changed", hexSum(tarball+"x") + "  " + linuxName + "\n", releaseSig(t, "current", sums), tarball + "x", "", false},
		{"tarball changed", sums, releaseSig(t, "current", sums), tarball + "x", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{}
			if tc.env != "" {
				env = append(env, tc.env)
			}
			out, ran, err := installFromReleases(t, path, tc.tarball, tc.sums, tc.sig, env...)
			if (err == nil) != tc.good || ran != tc.good {
				t.Fatalf("%v, ran %v\n%s", err, ran, out)
			}
			if tc.good && !strings.Contains(out, "signature is good") || !tc.good && !strings.Contains(out, "did not match its") {
				t.Fatal(out)
			}
		})
	}
}

// With no OpenSSL 3 the script says that only the checksum is checked.
func TestTheInstallScriptSaysWhenItCannotCheckTheSignature(t *testing.T) {
	tarball := linuxTarball(t, map[string]string{"agentsd-linux-x86_64": "#!/bin/sh\nprintf x > \"$HOME/started-args\"\n"})
	sums := hexSum(tarball) + "  " + linuxName + "\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openssl"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec // G306: a test's script
		t.Fatal(err)
	}
	out, ran, err := installFromReleases(t, dir+":"+os.Getenv("PATH"), tarball, sums, "")
	if err != nil || !ran || !strings.Contains(out, "signature is not checked") {
		t.Fatalf("%v %v\n%s", err, ran, out)
	}
}
