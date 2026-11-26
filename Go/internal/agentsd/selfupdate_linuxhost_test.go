package agentsd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/linuxrelease"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A Linux machine updates itself (#5788, epic #5785): it takes the release's tarball from
// the control plane, or from the public releases, checks it, points bin/current at the new
// agentsd between turns, and goes back to the old one when the new one does not stay up.

func linuxSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// newHost is an agentsd that says it is version (as --version does).
func newHost(version string) []byte {
	return []byte("#!/bin/sh\necho '" + version + " (go)'\n")
}

// linuxReleaseTarball is release-host.sh's tarball for x86_64: each program beside its
// .sha256.
func linuxReleaseTarball(t *testing.T, programs map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, body []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range programs {
		add(name, body)
		add(name+".sha256", []byte(linuxSum(body)+"\n"))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type linuxRig struct {
	u        *selfUpdater
	bin      string
	current  string
	oldName  string
	restarts atomic.Int64
}

// newLinuxRig is ~/.agents-server/bin with agentsd 0.11.0 in bin/current, and the release
// 0.12.0's tarball, with the sum of sumOf, served as the control plane serves it
// (/v1/releases/<version>/) and as the public releases do (/v<version>/). The first answer
// of each counts in fromControl and fromPublic.
func newLinuxRig(t *testing.T, tarball, sumOf []byte) (r *linuxRig, fromControl, fromPublic *atomic.Int64) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	old := newHost("0.11.0")
	r = &linuxRig{bin: bin, current: filepath.Join(bin, "current"), oldName: "agentsd-" + linuxSum(old)}
	must(t, os.WriteFile(filepath.Join(bin, r.oldName), old, 0o700)) //nolint:gosec // G306: a program, which runs
	must(t, os.WriteFile(filepath.Join(bin, "github-mcp"), []byte("old github"), 0o700)) //nolint:gosec // G306: a program, which runs
	if err := os.Symlink(r.oldName, r.current); err != nil {
		t.Fatal(err)
	}
	name := linuxrelease.Tarball("x86_64", "0.12.0")
	fromControl, fromPublic = new(atomic.Int64), new(atomic.Int64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rest, ok := strings.CutPrefix(req.URL.Path, "/v1/releases/0.12.0/")
		hits := fromControl
		if !ok {
			rest, ok = strings.CutPrefix(req.URL.Path, "/v0.12.0/")
			hits = fromPublic
		}
		switch {
		case ok && rest == "SHA256SUMS":
			hits.Add(1)
			_, _ = w.Write([]byte(linuxSum(sumOf) + "  Agent-Loops-0.12.0.zip\n" + linuxSum(sumOf) + "  " + name + "\n"))
		case ok && rest == "SHA256SUMS.sig":
			_, _ = w.Write(releaseSig(t, "current", []byte(linuxSum(sumOf)+"  Agent-Loops-0.12.0.zip\n"+linuxSum(sumOf)+"  "+name+"\n")))
		case ok && rest == name:
			_, _ = w.Write(tarball)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENTS_RELEASE_DOWNLOADS", "")
	t.Setenv("AGENTS_RELEASE_KEY", "")
	u := newSelfUpdater(filepath.Join(t.TempDir(), selfUpdateFile), r.current, "0.11.0")
	u.ctx, u.pause = t.Context(), 0
	u.turns = func() int { return 0 }
	u.control = func() []releaseSource { return []releaseSource{{base: srv.URL}} }
	u.public = srv.URL
	u.asLinux(r.current, "x86_64")
	u.restart = func() error { r.restarts.Add(1); return nil }
	r.u = u
	return r, fromControl, fromPublic
}

func (r *linuxRig) target(t *testing.T) string {
	t.Helper()
	target, err := os.Readlink(r.current)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func (r *linuxRig) until(t *testing.T, state wire.SelfUpdateState) wire.SelfUpdate {
	t.Helper()
	return (&selfUpdateRig{u: r.u}).until(t, state)
}

func (r *linuxRig) leftovers() []string {
	m, _ := filepath.Glob(filepath.Join(r.bin, updateFolderPrefix+"*"))
	return m
}

func TestLinuxSelfUpdatePointsCurrentAtTheNewHost(t *testing.T) {
	host := newHost("0.12.0")
	tarball := linuxReleaseTarball(t, map[string][]byte{"agentsd-linux-x86_64": host, "github-mcp-linux-x86_64": []byte("new github")})
	r, fromControl, fromPublic := newLinuxRig(t, tarball, tarball)
	r.u.Named("0.12.0")
	r.until(t, wire.SelfUpdateStateRestarting)
	for deadline := time.Now().Add(5 * time.Second); r.restarts.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if r.restarts.Load() != 1 {
		t.Fatalf("%d restarts", r.restarts.Load())
	}
	if fromControl.Load() != 1 || fromPublic.Load() != 0 {
		t.Fatalf("control %d public %d", fromControl.Load(), fromPublic.Load())
	}
	if got, want := r.target(t), "agentsd-"+linuxSum(host); got != want {
		t.Fatalf("bin/current is %s, not %s", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(r.bin, "github-mcp")); string(b) != "new github" {
		t.Fatalf("the MCP server is %q", b)
	}
	// The old host stays until the new one has stayed up, and the update's folder holds
	// a link to it to go back to.
	if _, err := os.Stat(filepath.Join(r.bin, r.oldName)); err != nil {
		t.Fatal("the old host is gone")
	}
	if len(r.leftovers()) != 1 {
		t.Fatalf("%v", r.leftovers())
	}
	if keep, back := settleSelfUpdates(r.current, "0.12.0", r.u.file, exchangePaths); len(keep) != 1 || back {
		t.Fatalf("%v %v", keep, back)
	}
	pruneOldHosts(r.current)
	if _, err := os.Stat(filepath.Join(r.bin, r.oldName)); err == nil {
		t.Fatal("the old host stayed after the new one stayed up")
	}
	if _, err := os.Stat(filepath.Join(r.bin, r.target(t))); err != nil {
		t.Fatal("the new host was taken away")
	}
}

func TestLinuxSelfUpdateFallsBackToThePublicReleases(t *testing.T) {
	host := newHost("0.12.0")
	tarball := linuxReleaseTarball(t, map[string][]byte{"agentsd-linux-x86_64": host})
	r, fromControl, fromPublic := newLinuxRig(t, tarball, tarball)
	// A control plane that does not answer.
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	r.u.control = func() []releaseSource { return []releaseSource{{base: down.URL}} }
	r.u.Named("0.12.0")
	r.until(t, wire.SelfUpdateStateRestarting)
	if got := r.target(t); got != "agentsd-"+linuxSum(host) || fromControl.Load() != 0 || fromPublic.Load() != 1 {
		t.Fatalf("bin/current is %s; control %d public %d", got, fromControl.Load(), fromPublic.Load())
	}
}

func TestLinuxSelfUpdateRefusesABadSum(t *testing.T) {
	tarball := linuxReleaseTarball(t, map[string][]byte{"agentsd-linux-x86_64": newHost("0.12.0")})
	r, _, _ := newLinuxRig(t, tarball, []byte("another tarball"))
	r.u.Named("0.12.0")
	s := r.until(t, wire.SelfUpdateStateFailed)
	if s.Message == nil || !strings.Contains(*s.Message, "checksum") {
		t.Fatalf("%+v", s)
	}
	if r.target(t) != r.oldName || len(r.leftovers()) != 0 {
		t.Fatalf("a bad download changed the host: %s %v", r.target(t), r.leftovers())
	}
}

func TestLinuxSelfUpdateRefusesAHostOfAnotherVersion(t *testing.T) {
	tarball := linuxReleaseTarball(t, map[string][]byte{"agentsd-linux-x86_64": newHost("0.13.0")})
	r, _, _ := newLinuxRig(t, tarball, tarball)
	r.u.Named("0.12.0")
	s := r.until(t, wire.SelfUpdateStateFailed)
	if s.Message == nil || !strings.Contains(*s.Message, "is not Agent Loops 0.12.0") || r.target(t) != r.oldName {
		t.Fatalf("%+v %s", s, r.target(t))
	}
}

// A new host that starts selfUpdateTries times without staying up gives bin/current back to
// the old one (#2710).
func TestLinuxSelfUpdateGoesBackToTheOldHost(t *testing.T) {
	tarball := linuxReleaseTarball(t, map[string][]byte{"agentsd-linux-x86_64": newHost("0.12.0")})
	r, _, _ := newLinuxRig(t, tarball, tarball)
	r.u.Named("0.12.0")
	r.until(t, wire.SelfUpdateStateRestarting)
	for i := 1; i <= selfUpdateTries; i++ {
		if _, back := settleSelfUpdates(r.current, "0.12.0", r.u.file, exchangePaths); back {
			t.Fatalf("start %d went back", i)
		}
	}
	if _, back := settleSelfUpdates(r.current, "0.12.0", r.u.file, exchangePaths); !back {
		t.Fatal("not rolled back")
	}
	if r.target(t) != r.oldName || len(r.leftovers()) != 0 {
		t.Fatalf("bin/current is %s, %v", r.target(t), r.leftovers())
	}
	if s := readSelfUpdateSettings(r.u.file); s.RolledBack != "0.12.0" {
		t.Fatalf("%+v", s)
	}
}

func TestLinuxArch(t *testing.T) {
	for goarch, want := range map[string]string{"amd64": "x86_64", "arm64": "aarch64", "riscv64": "riscv64"} {
		if got := linuxArch(goarch); got != want {
			t.Fatalf("%s: %s", goarch, got)
		}
	}
}

func TestRestartsItselfUnderASupervisor(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOCATION_ID", "")
	if restartsItself(root) {
		t.Fatal("no service and no supervisor, but it says it restarts")
	}
	must(t, os.WriteFile(filepath.Join(root, "supervise.lock"), []byte("1"), 0o600))
	if os.Getppid() != 1 && restartsItself(root) {
		t.Fatal("another process's supervisor was taken for this one's")
	}
	must(t, os.WriteFile(filepath.Join(root, "supervise.lock"), []byte(strconv.Itoa(os.Getppid())), 0o600))
	if !restartsItself(root) {
		t.Fatal("the supervisor was not seen")
	}
	_ = os.Remove(filepath.Join(root, "supervise.lock"))
	t.Setenv("INVOCATION_ID", "abc")
	if !restartsItself(root) {
		t.Fatal("the systemd service was not seen")
	}
}
