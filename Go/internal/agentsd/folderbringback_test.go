package agentsd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/flaky"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Bring Back from ‹machine›… (#4640): folders/bringBackFrom answers at once and this host
// brings the branches back on its own, through machines/sendFolder, saying how it goes with
// bringBack/changed; a window that connects mid-way finds it with folders/bringingBack. An
// offline machine, or a branch it does not have, refuses at once, and nothing comes.

// nextBringBack is the next bringBack/changed c hears, or nil after wait.
func nextBringBack(c *conn, wait time.Duration) *wire.BringBackNotification {
	deadline := time.After(wait)
	for {
		select {
		case line := <-c.out:
			var m message
			if json.Unmarshal([]byte(line), &m) != nil || m.Method != wire.NotifyBringBackChanged.Name {
				continue
			}
			var n wire.BringBackNotification
			if json.Unmarshal(m.Params, &n) != nil {
				continue
			}
			return &n
		case <-deadline:
			return nil
		}
	}
}

// bringBackPeer is the other machine of a bring-back test: this host, which
// answers the bring-back's steps with its own methods. It keeps each step and
// who asked. While offline, it refuses. It holds the last part until held closes.
type bringBackPeer struct {
	d          *Daemon
	mu         sync.Mutex
	steps, who []string
	offline    bool
	held       chan struct{}
}

// ask is the control plane's ask for the bring-back's steps.
func (p *bringBackPeer) ask(_ context.Context, method string, params any) (json.RawMessage, error) {
	// Only the bring-back's steps: the host's other calls to the control plane go nowhere.
	call, ok := params.(wire.MachineWorkspaceCall)
	if !ok || call.Method != sendFolderMethod {
		return json.RawMessage(`{}`), nil
	}
	b, _ := json.Marshal(call.Params)
	p.mu.Lock()
	p.who = append(p.who, call.Agent)
	if p.offline {
		p.mu.Unlock()
		return nil, &controllink.AskError{Code: wire.FailureHostOffline, Message: "pc is offline."}
	}
	p.mu.Unlock()
	var s wire.MachineFolderSend
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	out, step, err := p.step(s)
	p.mu.Lock()
	p.steps = append(p.steps, step)
	p.mu.Unlock()
	if err != nil {
		code, msg := errMessage(err)
		return nil, &controllink.AskError{Code: code, Message: msg}
	}
	r, _ := json.Marshal(out)
	return r, nil
}

// step does one step of the bring-back on this host, and says its name.
func (p *bringBackPeer) step(s wire.MachineFolderSend) (wire.MachineFolderSent, string, error) {
	var out wire.MachineFolderSent
	var err error
	step := ""
	switch {
	case s.Facts != nil:
		step = "facts"
		f, e := p.d.sendFacts(*s.Facts)
		out.Facts, err = &f, e
	case s.Pack != nil:
		step = "pack"
		pk, e := p.d.sendPack(*s.Pack)
		out.Pack, err = &pk, e
	case s.Read != nil:
		step = "read"
		r, e := p.d.sendRead(*s.Read)
		out.Read, err = &r, e
		// The last part is held until the test has seen the bring-back under way.
		if e == nil && r.End && len(r.Data) > 0 && len(r.Data) < sendPartSize && s.Read.Offset > 0 {
			<-p.held
		}
	}
	return out, step, err
}

// checkAsked fails the test when the steps were not want (joined by commas), or
// when a step was not asked as You.
func (p *bringBackPeer) checkAsked(t *testing.T, want string) {
	t.Helper()
	p.mu.Lock()
	if strings.Join(p.steps, ",") != want {
		t.Fatalf("steps %v", p.steps)
	}
	for _, w := range p.who {
		if w != "You" {
			t.Fatalf("asked as %q", w)
		}
	}
	p.mu.Unlock()
}

// waitBringBackFinished waits, from last, for the bringBack/changed that says
// the bring-back is finished.
func waitBringBackFinished(t *testing.T, c *conn, last *wire.BringBackNotification) *wire.BringBackNotification {
	t.Helper()
	for last == nil || !last.Finished {
		if last = nextBringBack(c, 10*time.Second); last == nil {
			t.Fatal("no finished bringBack/changed")
		}
	}
	return last
}

func TestFoldersBringBackFromBringsOnItsOwn(t *testing.T) {
	flaky.UnderLoad(t, "#7341")
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	src := repo(t)
	sendGit(t, src, nil, "checkout", "-q", "-b", "fix-login")
	// The other machine is this one, at another path: the steps go to its own methods.
	there := filepath.Join(canonical(t.TempDir()), "app")
	sendGit(t, filepath.Dir(there), nil, "clone", "-q", "-b", "fix-login", src, there)
	sendGit(t, there, nil, "config", "user.email", "t@example.com")
	sendGit(t, there, nil, "config", "user.name", "T")
	// More than one part of folders/sendRead, so the bring-back says how far it is.
	big := make([]byte, sendPartSize+sendPartSize/2)
	rand.Read(big)
	must(t, os.WriteFile(filepath.Join(there, "big.bin"), big, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	sendGit(t, there, nil, "add", "-A")
	sendGit(t, there, nil, "commit", "-q", "-m", "big")
	must(t, os.WriteFile(filepath.Join(there, "notes.txt"), []byte("not committed\n"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	head := sendGit(t, there, nil, "rev-parse", "HEAD")

	peer := &bringBackPeer{d: d, held: make(chan struct{})}
	d.SetControlAsk(peer.ask)
	from := map[string]any{"folder": store.PathURL(src), "machine": "pc-id", "machineName": "pc", "source": there,
		"branches": []string{"fix-login"}, "includeChanges": true}

	// Offline: refused at once, and nothing is under way.
	peer.mu.Lock()
	peer.offline = true
	peer.mu.Unlock()
	if e := c.callError(t, 1, "folders/bringBackFrom", from); e.Message != "Nothing was brought back from pc: pc is offline." {
		t.Fatal(e.Message)
	}
	peer.mu.Lock()
	peer.offline = false
	peer.mu.Unlock()

	// A branch it does not have: refused at once.
	nope := map[string]any{}
	for k, v := range from {
		nope[k] = v
	}
	nope["branches"] = []string{"nope"}
	if e := c.callError(t, 2, "folders/bringBackFrom", nope); e.Message != "Nothing was brought back from pc: "+there+" on pc has no branch nope." {
		t.Fatal(e.Message)
	}
	peer.mu.Lock()
	peer.steps = nil
	peer.mu.Unlock()

	// It answers before the bring-back is done.
	var s wire.BringBackSummary
	mustUnmarshal(t, c.call(t, 3, "folders/bringBackFrom", from), &s)
	if s.Machine != "pc-id" || s.MachineName != "pc" || s.Source != there || s.Folder != store.PathURL(src) {
		t.Fatalf("%+v", s)
	}
	// The parts come, and each says how far it is.
	var last *wire.BringBackNotification
	for last == nil || last.Total == 0 || last.Read < int64(sendPartSize) {
		if last = nextBringBack(c, 10*time.Second); last == nil {
			t.Fatal("no bringBack/changed")
		}
		if last.Finished {
			t.Fatalf("finished before the last part: %+v", last)
		}
	}
	if last.BringBack.ID != s.ID || last.Total < int64(len(big)) {
		t.Fatalf("%+v", last)
	}
	// A window that connects now finds it, and a second bring-back into the same folder is
	// refused.
	var under []wire.BringBackNotification
	mustUnmarshal(t, c.call(t, 4, "folders/bringingBack", nil), &under)
	if len(under) != 1 || under[0].BringBack.ID != s.ID || under[0].Read != last.Read {
		t.Fatalf("%+v", under)
	}
	if e := c.callError(t, 5, "folders/bringBackFrom", from); e.Message != shortPath(src)+" is already bringing back." {
		t.Fatal(e.Message)
	}
	close(peer.held)
	last = waitBringBackFinished(t, c, last)
	if last.Problem != nil || last.Result == nil || strings.Join(last.Result.Moved, ",") != "fix-login" || !last.Result.Applied {
		t.Fatalf("%+v", last)
	}
	peer.checkAsked(t, "facts,pack,read,read,read")
	if got := sendGit(t, src, nil, "rev-parse", "HEAD"); got != head {
		t.Fatalf("here is at %s, not %s", got, head)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "notes.txt")); string(b) != "not committed\n" {
		t.Fatalf("notes.txt here: %q", b)
	}
	for _, folder := range []string{d.incomingFolder(), d.outgoingFolder()} {
		if left, _ := os.ReadDir(folder); len(left) != 0 {
			t.Fatalf("left in %s: %v", folder, left)
		}
	}
	mustUnmarshal(t, c.call(t, 6, "folders/bringingBack", nil), &under)
	if len(under) != 0 {
		t.Fatalf("still under way: %+v", under)
	}

	// Now uncommitted changes here: the bring-back stops, says why, and changes nothing.
	mustUnmarshal(t, c.call(t, 7, "folders/bringBackFrom", from), &s)
	last = waitBringBackFinished(t, c, nil)
	if last.Problem == nil || !strings.HasPrefix(*last.Problem, "Nothing came back into "+filepath.Base(src)+" from pc: ") {
		t.Fatalf("%+v", last)
	}
	if got := sendGit(t, src, nil, "rev-parse", "HEAD"); got != head {
		t.Fatalf("here moved to %s", got)
	}
}
