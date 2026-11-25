package agentsd

import (
	"os"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A machine that holds its person's lock answers git/fill only for a request of that lock,
// and the control plane's own is not one: git is not asked. With no lock it answers.
func TestGitFillOnAMachineWithALock(t *testing.T) {
	asked := 0
	setFor(t, &fillGitCredential, func(string) (string, error) {
		asked++
		return "username=alex\npassword=s3cret\n", nil
	})
	fill := func(d *Daemon) wire.GitCredential {
		t.Helper()
		out, ok := d.Asked("git/fill", mustJSON(wire.GitCredentialRequest{Protocol: "https", Host: "github.com"}))
		if !ok {
			t.Fatal("git/fill was not answered")
		}
		return out.(wire.GitCredential)
	}

	unlocked, _, _ := joinedMachine(t)
	if got := fill(unlocked); got.Password != "s3cret" || asked != 1 {
		t.Fatalf("with no lock: filled %t, git asked %d times", got.Password != "", asked)
	}

	locked, _, _, _ := lockedMachine(t)
	if got := fill(locked); got.Password != "" || got.Username != "" || asked != 1 {
		t.Fatalf("with a lock: filled %t, git asked %d times", got.Password != "", asked)
	}
}

// A lock that is pinned but not read yet, as after a restart, is a lock.
func TestGitFillWhileTheLockIsNotReadYet(t *testing.T) {
	asked := false
	setFor(t, &fillGitCredential, func(string) (string, error) {
		asked = true
		return "username=alex\npassword=s3cret\n", nil
	})
	d, _, _, _ := lockedMachine(t)
	must(t, os.Remove(d.lockFilePath()))
	d.lockMu.Lock()
	d.lockHeld, d.lockRead = nil, false
	d.lockMu.Unlock()
	if state, pinned := d.heldLock(); state != nil || !pinned {
		t.Fatalf("state %v, pinned %t", state, pinned)
	}
	out, ok := d.Asked("git/fill", mustJSON(wire.GitCredentialRequest{Protocol: "https", Host: "github.com"}))
	if !ok || out.(wire.GitCredential).Password != "" || asked {
		t.Fatalf("answered %t, git asked %t", ok, asked)
	}
}
