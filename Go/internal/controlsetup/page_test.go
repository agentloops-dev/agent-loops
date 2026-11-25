package controlsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/web"
	"github.com/agentloops-dev/agent-loops/Go/internal/linuxrelease"
)

// unpackRelease makes a release's tarball as release-host.sh does, for this architecture,
// unpacks it, and returns its agentsd.
func unpackRelease(t *testing.T, program, page string) string {
	t.Helper()
	servers, dist, out := t.TempDir(), t.TempDir(), t.TempDir()
	put := func(dir, name, text string) {
		t.Helper()
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o700); err != nil { //nolint:gosec // G306: the stand-in program must run
			t.Fatal(err)
		}
	}
	for _, p := range append([]string{"agents-control"}, linuxrelease.Programs...) {
		put(servers, p+"-linux-"+releaseArch(), program)
		put(servers, p+"-linux-"+releaseArch()+".sha256", "sum")
	}
	put(servers, "VERSION", "0.0.0")
	manifest := "agents-web 1\n"
	for _, name := range []string{"index.html", "assets/app.js"} {
		put(dist, name, page+name)
		sum := sha256.Sum256([]byte(page + name))
		manifest += "out " + hex.EncodeToString(sum[:]) + " Web/dist/" + name + "\n"
	}
	put(dist, "MANIFEST", manifest)
	tarball := filepath.Join(out, "release.tar.gz")
	script := filepath.Join("..", "..", "..", "scripts", "release-linux-tarball.sh")
	if b, err := exec.Command("sh", script, servers, dist, releaseArch(), tarball).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if b, err := exec.Command("tar", "-xzf", tarball, "-C", out).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return filepath.Join(out, "agentsd-linux-"+releaseArch())
}

// servedPage is what the page flag of the unit in home gives at /, and the flag.
func servedPage(t *testing.T, home string) (flag, page string) {
	t.Helper()
	unit, err := os.ReadFile(LayoutOf(home).Unit)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(` (--web|--public-web) (\S+)`).FindStringSubmatch(string(unit))
	if m == nil {
		t.Fatalf("the unit serves no page:\n%s", unit)
	}
	l, err := web.Start(web.Config{Folder: strings.Replace(m[2], "%h", home, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()
	res, err := http.Get(l.Origin() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("%s: %v", res.Status, err)
	}
	return m[1], string(b)
}

// An install from the release's tarball serves its web page, and one run again from a newer
// release updates agents-control and the page together (#7288).
func TestAnInstallFromTheReleaseServesThePage(t *testing.T) {
	home := t.TempDir()
	d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run}
	install := func(o Options) {
		t.Helper()
		o.Home = home
		if err := Install(context.Background(), o, d); err != nil {
			t.Fatal(err)
		}
	}
	install(Options{Executable: unpackRelease(t, "one", "first ")})
	if flag, page := servedPage(t, home); flag != "--web" || page != "first index.html" {
		t.Fatalf("%s %q", flag, page)
	}
	install(Options{Executable: unpackRelease(t, "two", "second "), Domain: "agents.example.com"})
	if flag, page := servedPage(t, home); flag != "--public-web" || page != "second index.html" {
		t.Fatalf("%s %q", flag, page)
	}
	l := LayoutOf(home)
	if b, _ := os.ReadFile(l.Binary()); string(b) != "two" {
		t.Fatalf("agents-control is %q", b)
	}
	// From the installed host, which has no release beside it, both stay.
	install(Options{Executable: filepath.Join(l.Bin, "current")})
	if _, page := servedPage(t, home); page != "second index.html" {
		t.Fatalf("%q", page)
	}
	for _, left := range []string{l.Web + ".new", l.Web + ".old"} {
		if _, err := os.Stat(left); err == nil {
			t.Errorf("%s is left", left)
		}
	}
}

func TestAnInstallRefusesAChangedPage(t *testing.T) {
	home := t.TempDir()
	exe := unpackRelease(t, "one", "first ")
	if err := os.WriteFile(filepath.Join(filepath.Dir(exe), "web", "index.html"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run}
	err := Install(context.Background(), Options{Home: home, Executable: exe}, d)
	if err == nil || !strings.Contains(err.Error(), "index.html does not match its manifest") {
		t.Fatal(err)
	}
}

func TestAnInstallWithNoPageSaysSo(t *testing.T) {
	home, exe := setup(t)
	var out bytes.Buffer
	d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run, Out: &out}
	if err := Install(context.Background(), Options{Home: home, Executable: exe}, d); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(LayoutOf(home).Unit)
	if !strings.Contains(out.String(), "No web page") || strings.Contains(string(unit), "web") {
		t.Fatalf("%s\n%s", out.String(), unit)
	}
}
