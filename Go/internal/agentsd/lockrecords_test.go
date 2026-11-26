package agentsd

import (
	"crypto/ecdsa"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func signedRecord(t *testing.T, k *ecdsa.PrivateKey, s *machinelock.State, subject, scope string, seq int64, content string) wire.LockRecord {
	t.Helper()
	r, err := machinelock.SignRecord(k, s, machinelock.KindSetupScript, subject, scope, seq, []byte(content))
	must(t, err)
	return r
}

// A machine takes a record of its lock and for it, at a number that only goes up.
func TestAMachineTakesASignedRecord(t *testing.T) {
	t.Parallel()
	d, phone, peer, state := lockedMachine(t)
	defer d.Shutdown()
	path := filepath.Join(d.Store.Loc.Root, "lock-records.json")
	want := func(r wire.LockRecord, want error) {
		t.Helper()
		if _, err := d.takeRecord(r, false); !errors.Is(err, want) {
			t.Fatalf("%s %s %d: %v", r.Kind, r.Scope, r.Seq, err)
		}
	}
	two := signedRecord(t, phone, state, "setup", "m:h-1", 2, "b")
	if _, err := d.checkRecord(two, false); err != nil || fileExists(path) {
		t.Fatalf("a check notes nothing: %v", err)
	}
	signer, err := d.takeRecord(two, false)
	if err != nil || signer.ID != phoneClient {
		t.Fatalf("signer %q, %v", signer.ID, err)
	}
	info, err := os.Stat(path)
	must(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	want(two, nil)
	want(signedRecord(t, phone, state, "setup", "m:h-1", 2, "c"), machinelock.ErrTwo)
	want(signedRecord(t, phone, state, "setup", "m:h-1", 1, "a"), machinelock.ErrOlder)
	want(signedRecord(t, phone, state, "setup", "m:h-2", 3, "a"), machinelock.ErrOtherPlace)
	want(signedRecord(t, peer, state, "setup", "m:h-1", 3, "a"), machinelock.ErrNotMember)
	want(signedRecord(t, phone, state, "root", "made", 1, "a"), machinelock.ErrOtherPlace)
	if _, err := d.takeRecord(signedRecord(t, phone, state, "root", "made", 1, "a"), true); err != nil {
		t.Fatal(err)
	}
	// A machine that was just made, and one that is named, share the counter.
	want(signedRecord(t, phone, state, "root", "m:h-1", 1, "b"), machinelock.ErrTwo)
	want(signedRecord(t, phone, state, "setup", "made", 1, "b"), machinelock.ErrOtherPlace)

	// With no lock, no record is taken.
	d.ResetLock()
	want(two, errNoLock)
}

func TestARecordIsTakenOnlyOnCountersThatRead(t *testing.T) {
	t.Parallel()
	d, phone, _, state := lockedMachine(t)
	defer d.Shutdown()
	path := filepath.Join(d.Store.Loc.Root, "lock-records.json")
	r := signedRecord(t, phone, state, "setup", "m:h-1", 1, "a")
	for _, bad := range []string{"", "{", "null", "[]", `{"a":{"seq":0,"content":""}}`} {
		writeFile(t, path, bad)
		if _, err := d.takeRecord(r, false); err == nil {
			t.Fatalf("%q: a record was taken", bad)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != bad {
			t.Fatalf("%q became %q, %v", bad, b, err)
		}
	}
	// Counters that cannot be written: the record is not taken.
	must(t, os.Remove(path))
	must(t, os.Mkdir(path, 0o700))
	if _, err := d.takeRecord(r, false); err == nil {
		t.Fatal("a record was taken with no counters")
	}
	must(t, os.Remove(path))
	if _, err := d.takeRecord(r, false); err != nil {
		t.Fatalf("a missing file has no counters: %v", err)
	}
}

// The counters hold after the machine's lock is reset and the same lock is pinned again.
func TestTheCountersStayWhenTheLockIsReset(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	mac := newLockKey(t)
	lock := wire.Lock{Person: "sam", Entries: []wire.LockEntry{lockEntry(t, mac, wire.LockEntryKindRoot, mac, nil)}}
	if !d.TakeLock(lock) {
		t.Fatal("the lock was not taken")
	}
	state, _ := d.heldLock()
	if _, err := d.takeRecord(signedRecord(t, mac, state, "setup", "made", 2, "b"), true); err != nil {
		t.Fatal(err)
	}
	d.ResetLock()
	if !d.TakeLock(lock) {
		t.Fatal("the lock was not taken again")
	}
	if _, err := d.takeRecord(signedRecord(t, mac, state, "setup", "made", 1, "a"), true); !errors.Is(err, machinelock.ErrOlder) {
		t.Fatal(err)
	}
	// A new lock has its own.
	d.ResetLock()
	if !d.TakeLock(wire.Lock{Person: "sam", Entries: []wire.LockEntry{lockEntry(t, mac, wire.LockEntryKindRoot, mac, nil)}}) {
		t.Fatal("the new lock was not taken")
	}
	next, _ := d.heldLock()
	if _, err := d.takeRecord(signedRecord(t, mac, state, "setup", "made", 3, "c"), true); !errors.Is(err, machinelock.ErrOtherLock) {
		t.Fatal(err)
	}
	if _, err := d.takeRecord(signedRecord(t, mac, next, "setup", "made", 1, "c"), true); err != nil {
		t.Fatal(err)
	}
}
