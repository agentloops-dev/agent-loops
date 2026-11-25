package agentsd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/execfile"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// seedPlane is the control plane between two machines: here sends, and there receives. An
// origin is a local bare repository, which git gets to with a program in place of ssh.
type seedPlane struct {
	here, there *Daemon
	origin      string // the bare repository's path
	src, folder string // the repository here, and the folder there

	mu       sync.Mutex
	knows    bool
	refuse   error
	steps    []string
	uploaded int
	origins  []string
}

const seedSSH = `#!/bin/sh
for last; do :; done
case "$*" in *"BatchMode=yes"*) ;; *) echo "it could ask" >&2; exit 1;; esac
case "$*" in
*gone.test*) echo "ssh: connect to host gone.test port 22: Connection refused" >&2; exit 255;;
*hung.test*) sleep 300 & echo $! > "$SEED_TEST_PIDS"; wait;;
*) exec sh -c "$last";;
esac
`

func newSeedPlane(t *testing.T) *seedPlane {
	t.Helper()
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	must(t, execfile.Write(ssh, []byte(seedSSH), 0o700))
	t.Setenv("GIT_SSH_COMMAND", ssh)
	t.Setenv("SEED_TEST_PIDS", filepath.Join(bin, "pid"))
	p := &seedPlane{src: repo(t), knows: true}
	p.here, _ = start(t, t.TempDir())
	p.there, _ = start(t, t.TempDir())
	t.Cleanup(p.here.Shutdown)
	t.Cleanup(p.there.Shutdown)
	p.origin = filepath.Join(canonical(t.TempDir()), "app.git")
	p.folder = filepath.Join(canonical(t.TempDir()), "app")
	putText(t, filepath.Join(p.src, "large.bin"), randomText())
	sendGit(t, p.src, nil, "add", "-A")
	sendGit(t, p.src, nil, "commit", "-q", "-m", "large")
	sendGit(t, p.src, nil, "clone", "-q", "--bare", p.src, p.origin)
	p.remote(t, "origin.test")
	p.push(t, "main")
	p.here.SetControlAsk(p.ask)
	p.here.SetControlCapabilities(func() []string {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.knows {
			return []string{wire.CapFoldersSendSeed1}
		}
		return nil
	})
	return p
}

// randomText is 200 KB that does not compress much.
func randomText() string {
	var b strings.Builder
	for b.Len() < 200_000 {
		b.WriteString(string(store.NewUUID()))
	}
	return b.String()
}

// remote sets the origin of the repository here, on host.
func (p *seedPlane) remote(t *testing.T, host string) {
	t.Helper()
	url := "ssh://git:secret@" + host + p.origin
	if _, err := git(p.src, "remote", "set-url", "origin", url); err != nil {
		sendGit(t, p.src, nil, "remote", "add", "origin", url)
	}
}

func (p *seedPlane) ask(_ context.Context, _ string, params any) (json.RawMessage, error) {
	call, ok := params.(wire.MachineWorkspaceCall)
	if !ok || call.Method != sendFolderMethod {
		return json.RawMessage(`{}`), nil
	}
	b, _ := json.Marshal(call)
	if strings.Contains(string(b), "secret") {
		return nil, &controllink.AskError{Code: 1, Message: "a credential is on the wire"}
	}
	b, _ = json.Marshal(call.Params)
	var s wire.MachineFolderSend
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out wire.MachineFolderSent
	if s.Seed != nil {
		name := "seed"
		if s.Seed.Stop != nil {
			name = "stop"
		}
		if len(p.steps) == 0 || p.steps[len(p.steps)-1] != name {
			p.steps = append(p.steps, name)
		}
		p.origins = append(p.origins, s.Seed.Origin)
		if p.refuse != nil {
			return nil, p.refuse
		}
		got, err := p.there.receiveSeed(*s.Seed)
		if err != nil {
			return nil, err
		}
		out.Seed = &got
		return json.Marshal(out)
	}
	if s.Upload != nil {
		p.uploaded += len(s.Upload.Data)
	}
	return sendThereFolderStep(p.there, s, &p.steps)
}

// send sends main, with its uncommitted changes, and is the steps and the bytes uploaded.
func (p *seedPlane) send(t *testing.T) (string, int, error) {
	t.Helper()
	p.mu.Lock()
	p.steps, p.uploaded = nil, 0
	p.mu.Unlock()
	facts, err := p.here.sendFacts(wire.SendFactsRequest{Folder: store.PathURL(p.src)})
	must(t, err)
	heads, err := p.here.readyThere(personSending, "pc", p.folder)
	must(t, err)
	err = p.here.sendThere(personSending, "pc", p.folder,
		sendingBranch{path: p.src, branch: *facts.Current, origin: facts.Origin, heads: heads})
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.steps, ","), p.uploaded, err
}

// arrived checks that the folder there has the commit and the uncommitted changes here.
func (p *seedPlane) arrived(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"status", "--porcelain"}} {
		if a, b := sendGit(t, p.src, nil, args...), sendGit(t, p.folder, nil, args...); a != b {
			t.Fatalf("%v: %q here, %q there", args, a, b)
		}
	}
	sendGit(t, p.folder, nil, "fsck", "--connectivity-only")
	waitFor(t, "the seeds to go", func() bool {
		left, _ := os.ReadDir(p.there.seedsFolder())
		return len(left) == 0
	})
}

// push pushes the commit here to a branch of the origin, with no program in place of ssh.
func (p *seedPlane) push(t *testing.T, branch string) {
	t.Helper()
	sendGit(t, p.src, nil, "push", "-q", p.origin, "HEAD:refs/heads/"+branch)
	sendGit(t, p.src, nil, "fetch", "-q", p.origin, "+refs/heads/*:refs/remotes/origin/*")
}

func (p *seedPlane) commit(t *testing.T, name string) {
	t.Helper()
	putText(t, filepath.Join(p.src, name), name)
	sendGit(t, p.src, nil, "add", "-A")
	sendGit(t, p.src, nil, "commit", "-q", "-m", name)
}

const seedSmall = 20_000

// #7119: a new folder and one that is there get what the origin has from the origin, and the
// send carries the unpushed commits and the uncommitted changes.
func TestASendCarriesOnlyWhatTheOriginHasNot(t *testing.T) {
	p := newSeedPlane(t)
	sendOK := func(want string) int {
		t.Helper()
		steps, bytes, err := p.send(t)
		must(t, err)
		if steps != want {
			t.Fatalf("%s, not %s", steps, want)
		}
		p.arrived(t)
		return bytes
	}
	if size := sizeLacked(p.src, []string{"main"}, nil); size < seedSmall || size > 1_000_000 {
		t.Fatalf("%d bytes to get", size)
	}
	// Nothing unpushed: nothing to upload. The origin's newer commit stops nothing.
	p.push(t, "ahead")
	if n := sendOK("state,seed,receive"); n != 0 {
		t.Fatalf("%d bytes", n)
	}
	if got := sendGit(t, p.folder, nil, "remote", "get-url", "origin"); got != "ssh://git@origin.test"+p.origin {
		t.Fatalf("origin there: %s", got)
	}
	if got := sendGit(t, p.folder, nil, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/agents-send"); got != "refs/heads/main" {
		t.Fatalf("refs there: %s", got)
	}
	// Uncommitted changes, with no commit that the folder lacks: no seed.
	putText(t, filepath.Join(p.src, "notes.txt"), "not committed\n")
	if n := sendOK("state,upload,receive"); n > seedSmall {
		t.Fatalf("%d bytes", n)
	}
	// The folder that is there: a large pushed commit by a fetch, an unpushed one in the bundle.
	must(t, os.WriteFile(filepath.Join(p.src, "large2.bin"), []byte(randomText()), 0o600))
	sendGit(t, p.src, nil, "add", "large2.bin")
	sendGit(t, p.src, nil, "commit", "-q", "-m", "large2")
	p.push(t, "main")
	sendGit(t, p.src, nil, "commit", "-q", "--allow-empty", "-m", "unpushed")
	if n := sendOK("state,seed,upload,upload,receive"); n == 0 || n > seedSmall {
		t.Fatalf("%d bytes", n)
	}
	// An unpushed commit only: the origin does not help, and nobody asks it.
	p.commit(t, "unpushed2")
	if n := sendOK("state,upload,receive"); n == 0 || n > seedSmall {
		t.Fatalf("%d bytes", n)
	}
	for _, o := range p.origins {
		if o != "ssh://git@origin.test"+p.origin {
			t.Fatalf("origin on the wire: %s", o)
		}
	}
	// A branch of the folder's own there stays, and its commits stop the send, as ever.
	sendGit(t, p.folder, nil, "-c", "user.name=m", "-c", "user.email=m@x", "commit", "-q", "--allow-empty", "-m", "mine")
	mine := sendGit(t, p.folder, nil, "rev-parse", "HEAD")
	p.commit(t, "more")
	p.push(t, "main")
	if steps, _, err := p.send(t); err == nil || !strings.Contains(err.Error(), "commits of its own") || !strings.HasSuffix(steps, "receive,stop") {
		t.Fatalf("%s %v", steps, err)
	}
	if got := sendGit(t, p.folder, nil, "rev-parse", "HEAD"); got != mine {
		t.Fatal("a commit there was overwritten")
	}
}

// #7119: the branch here can be behind the origin's: the folder gets it where it is here.
func TestASeedMovesNoBranch(t *testing.T) {
	p := newSeedPlane(t)
	before := sendGit(t, p.src, nil, "rev-parse", "HEAD")
	p.commit(t, "pushed")
	p.push(t, "main")
	sendGit(t, p.src, nil, "reset", "-q", "--hard", before)
	if steps, n, err := p.send(t); err != nil || steps != "state,seed,receive" || n != 0 {
		t.Fatalf("%s %d %v", steps, n, err)
	}
	p.arrived(t)
}

// #7119: with no seed a send carries everything, as before, and a failed seed leaves no folder.
func TestASendWithNoSeedCarriesEverything(t *testing.T) {
	for name, c := range map[string]struct {
		set   func(t *testing.T, p *seedPlane)
		steps string
	}{
		"unreachable origin": {func(t *testing.T, p *seedPlane) { p.remote(t, "gone.test") }, "state,seed,upload,receive"},
		"no origin": {func(t *testing.T, p *seedPlane) {
			sendGit(t, p.src, nil, "remote", "remove", "origin")
		}, "state,upload,receive"},
		"an origin git must not use": {func(t *testing.T, p *seedPlane) {
			sendGit(t, p.src, nil, "remote", "set-url", "origin", p.origin)
		}, "state,upload,receive"},
		"old machine": {func(_ *testing.T, p *seedPlane) {
			p.refuse = &controllink.AskError{Code: wire.FailureNotInLock, Message: "not one of its steps"}
		}, "state,seed,upload,receive"},
		"old control plane": {func(_ *testing.T, p *seedPlane) { p.knows = false }, "state,upload,receive"},
		"busy machine": {func(_ *testing.T, p *seedPlane) {
			p.there.seedMu.Lock()
			defer p.there.seedMu.Unlock()
			p.there.seeds = map[wire.UUID]*seedJob{"a": {}, "b": {}, "c": {}, "d": {}}
		}, "state,seed,upload,receive"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newSeedPlane(t)
			c.set(t, p)
			steps, n, err := p.send(t)
			if err != nil || steps != c.steps || n < seedSmall {
				t.Fatalf("%s %d %v", steps, n, err)
			}
			p.arrived(t)
		})
	}
}

// #7119: a pool machine gets a project from its origin, and the rest from the source.
func TestAPoolMachineSeedsFromTheOrigin(t *testing.T) {
	p := newSeedPlane(t)
	p.commit(t, "unpushed")
	var said []string
	receive, err := func() (wire.ReceiveRequest, error) {
		p.there.SetControlAsk((&setupPlane{d: p.here}).ask)
		return p.there.packedThere("mac-id", p.src, p.folder, wire.ReceiveState{}, func(s string) { said = append(said, s) })
	}()
	must(t, err)
	if receive.Seed == nil || receive.Bundle == nil || strings.Contains(*receive.Origin, "secret") {
		t.Fatalf("%s", mustJSON(receive))
	}
	file, _ := p.there.incomingFile(*receive.Bundle)
	if fi, err := os.Stat(file); err != nil || fi.Size() > seedSmall {
		t.Fatalf("%v %v", fi, err)
	}
	if _, err := p.there.receiveInto(receive, false); err != nil {
		t.Fatal(err)
	}
	p.arrived(t)
}

// #7119: a seed that hangs ends when its sender says so, with git's own children, and makes
// no folder.
func TestAStoppedSeedLeavesNothing(t *testing.T) {
	p := newSeedPlane(t)
	r := wire.ReceiveSeedRequest{Seed: store.NewUUID(), Folder: p.folder, Origin: "ssh://git@hung.test" + p.origin}
	if s, err := p.there.receiveSeed(r); err != nil || s.Finished {
		t.Fatalf("%+v %v", s, err)
	}
	pid := os.Getenv("SEED_TEST_PIDS")
	waitFor(t, "the program to run", func() bool { return isFile(pid) })
	stop := r
	stop.Stop = new(true)
	if s, err := p.there.receiveSeed(stop); err != nil || !s.Finished {
		t.Fatalf("%+v %v", s, err)
	}
	waitFor(t, "the seed to go", func() bool {
		left, _ := os.ReadDir(p.there.seedsFolder())
		return len(left) == 0
	})
	if _, err := os.Stat(p.folder); err == nil {
		t.Fatal("it made the folder")
	}
	other := r
	other.Seed, other.Origin = "../../x", "ssh://git@origin.test"+p.origin
	if _, err := p.there.receiveSeed(other); err == nil {
		t.Fatal("took a seed that is a path")
	}
}

func TestSeedOrigins(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{ //nolint:gosec // G101: no credentials
		"https://github.com/a/b.git":             "https://github.com/a/b.git",
		"https://tok@github.com/a/b?private=1#x": "https://github.com/a/b",
		"HTTPS://u:p@github.com/a/b":             "",
		"https://u:p@ss@host.test:8443/a/b":      "https://host.test:8443/a/b",
		"https://u:p/w@host.test/a/b":            "",
		"ssh://git:secret@host.test:22/a/b":      "ssh://git@host.test:22/a/b",
		"git@github.com:a/b.git":                 "git@github.com:a/b.git",
		"git:secret@github.com:a/b.git":          "",
		"ext::sh":                                "",
		"ssh://-oProxyCommand=x/a/b":             "",
		"--upload-pack=x":                        "",
		"/a/b":                                   "",
		"file:///a/b":                            "",
		"http://github.com/a/b":                  "",
		"https://github.com/a\x01/b":             "",
		"https://github.com/":                    "",
	} {
		r, ok := seedOrigin(in)
		if ok != (want != "") || r.url != want {
			t.Errorf("%q: %q %t", in, r.url, ok)
		}
	}
	for in, want := range map[string]string{
		"http://u:p@host/a": "http://host/a", "/a/b@c": "/a/b@c", "HTTPS://u:p@host/a": "HTTPS://host/a", "ssh://git@host/a": "ssh://git@host/a",
	} {
		if got := withoutCredential(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if done, ok := seedBytes("Receiving objects:  50% (5/10), 2.00 MiB | 1.00 MiB/s"); !ok || done != 2<<20 {
		t.Error(done, ok)
	}
}

// #7119: a send brings no credential, so this machine's own stays in its origin.
func TestAReceiveKeepsTheFoldersOwnCredential(t *testing.T) {
	t.Parallel()
	dir := repo(t)
	sendGit(t, dir, nil, "remote", "add", "origin", "https://me:token@host.test/a/b")
	must(t, receiveSetOrigin(dir, "https://host.test/a/b"))
	if got := sendGit(t, dir, nil, "remote", "get-url", "origin"); got != "https://me:token@host.test/a/b" { //nolint:gosec // G101: no credential
		t.Fatal(got)
	}
	must(t, receiveSetOrigin(dir, "https://host.test/a/c"))
	if got := sendGit(t, dir, nil, "remote", "get-url", "origin"); got != "https://host.test/a/c" {
		t.Fatal(got)
	}
}

// #7119: a seed whose receive never comes is bounded by its own ids: a wait for a finished one
// answers at once.
func TestAFinishedSeedAnswersAtOnce(t *testing.T) {
	p := newSeedPlane(t)
	r := wire.ReceiveSeedRequest{Seed: store.NewUUID(), Folder: p.folder, Origin: "ssh://git@gone.test" + p.origin}
	var s wire.ReceiveSeed
	for !s.Finished {
		var err error
		s, err = p.there.receiveSeed(r)
		must(t, err)
	}
	begun := time.Now()
	if again, err := p.there.receiveSeed(r); err != nil || again.Problem == nil || time.Since(begun) > seedAnswer/2 {
		t.Fatalf("%+v %v", again, err)
	}
}
