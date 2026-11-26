package agentsd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Signed records on a machine (#7167): checkRecord says whether this machine may take one, and
// takeRecord takes it. The number last taken of each kind and subject is in
// <root>/lock-records.json before the content is used. It stays in the data folder, as the
// lock's pin does: one synced from another machine would be a trust source here. `agentsd
// lock reset` leaves it, as each counter names its lock. A missing file has no counters;
// with one that cannot be read, no record is taken.

const (
	lockRecordsFile = "lock-records.json"
	// lockRecordsMostBytes bounds the file: the counters and their longest names.
	lockRecordsMostBytes = 1 << 20
)

// errNoLock is a record on a machine that holds no lock.
var errNoLock = errors.New("this machine has no lock to check a record against")

type lockRecords struct {
	recordMu sync.Mutex
}

// checkRecord checks a record against the lock this machine holds and the counters, and
// gives back the client that signed it. made says that this machine was just made. It notes
// nothing.
func (d *Daemon) checkRecord(r wire.LockRecord, made bool) (machinelock.Member, error) {
	d.recordMu.Lock()
	defer d.recordMu.Unlock()
	signer, counters, err := d.checkRecordLocked(r, made)
	if err != nil {
		return machinelock.Member{}, err
	}
	return signer, counters.Check(r)
}

// takeRecord is checkRecord, and notes the record as taken before it answers: use the content
// only once it did.
func (d *Daemon) takeRecord(r wire.LockRecord, made bool) (machinelock.Member, error) {
	d.recordMu.Lock()
	defer d.recordMu.Unlock()
	signer, counters, err := d.checkRecordLocked(r, made)
	if err != nil {
		return machinelock.Member{}, err
	}
	changed, err := counters.Take(r)
	if errors.Is(err, machinelock.ErrTwo) {
		log.Printf("lock: two signed records of %s %s have the number %d; the first stays", r.Kind, r.Subject, r.Seq)
	}
	if err == nil && changed {
		err = d.keepRecordCounters(counters)
	}
	if err != nil {
		return machinelock.Member{}, err
	}
	return signer, nil
}

func (d *Daemon) checkRecordLocked(r wire.LockRecord, made bool) (machinelock.Member, machinelock.Counters, error) {
	state, _ := d.heldLock()
	if state == nil {
		return machinelock.Member{}, nil, errNoLock
	}
	signer, err := machinelock.CheckRecord(state, r)
	if err != nil {
		return signer, nil, err
	}
	if err := machinelock.TakesRecord(r, d.ownHostID(), made); err != nil {
		return signer, nil, err
	}
	counters, err := d.recordCounters()
	return signer, counters, err
}

func (d *Daemon) recordCountersPath() string {
	return filepath.Join(d.Store.Loc.Root, lockRecordsFile)
}

func (d *Daemon) recordCounters() (machinelock.Counters, error) {
	counters := machinelock.Counters{}
	b, err := os.ReadFile(d.recordCountersPath())
	if errors.Is(err, fs.ErrNotExist) {
		return counters, nil
	}
	if err == nil && len(b) > lockRecordsMostBytes {
		err = fmt.Errorf("%s is over %d bytes", lockRecordsFile, lockRecordsMostBytes)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &counters); err != nil {
		return nil, fmt.Errorf("%s does not read: %w", lockRecordsFile, err)
	}
	if counters == nil || !counters.Valid() {
		return nil, fmt.Errorf("%s does not hold counters", lockRecordsFile)
	}
	return counters, nil
}

func (d *Daemon) keepRecordCounters(counters machinelock.Counters) error {
	b, err := json.Marshal(counters)
	if err != nil {
		return err
	}
	if err := tooLarge(lockRecordsFile, b, lockRecordsMostBytes); err != nil {
		return err
	}
	// store-bounds: kind=key-value; count=2000 counters; size=1MiB; age=until-deleted; applied=on write (refused)
	return store.WriteAtomically(d.recordCountersPath(), b, 0o600)
}
