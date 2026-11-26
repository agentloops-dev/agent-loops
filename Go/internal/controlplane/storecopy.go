package controlplane

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// ErrDestinationInUse is StoreCopy.Failure.destinationInUse: the destination is already
// some control plane's store.
var ErrDestinationInUse = errors.New("The new store already holds a control plane's records, so nothing was copied into it.")

// copySkipped is never carried across: leases and copies are rebuilt when a copy starts,
// and each copy keeps the control key its own way (a Mac in a file or the keychain, a cloud
// machine sealed in its store by its own keeper, #1458), so a sealed key stays where it is.
// store-bounds: not a store: the prefixes that a copy leaves out
var copySkipped = []string{"v1/leases/", "v1/copies/", "v1/keys/"}

// backupSkipped is what a backup leaves out (#2137): only what a copy rebuilds when it
// starts. A sealed key stays in, as a restore at the same keeper needs it.
// store-bounds: not a store: the prefixes that a backup leaves out
var backupSkipped = []string{"v1/leases/", "v1/copies/"}

// leftOut says a key is under one of the prefixes skipped.
func leftOut(key string, skipped []string) bool {
	for _, prefix := range skipped {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// CopyStore is StoreCopy (contracts/store.md "Copying a store"): every record under v1/
// from one store into an empty one, create only, then checked by count and content. Switch
// Store… runs it with no copy serving; a handover runs it between a frozen copy and a
// receiving one, each end a PeerStore (#1490).
func CopyStore(ctx context.Context, from, to store.Store) (copied, skipped int, err error) {
	return copyStoreLeaving(ctx, from, to, copySkipped)
}

// BackupStore is CopyStore for a backup (#2137): the sealed keys go too.
func BackupStore(ctx context.Context, from, to store.Store) (copied, skipped int, err error) {
	return copyStoreLeaving(ctx, from, to, backupSkipped)
}

// bulkStore is a store that takes many records in one call and says the hashes of many in
// one call: a copy over a handover session (PeerStore), where each call is a round trip
// between two machines while the old copy is frozen (#7394).
type bulkStore interface {
	// createAll creates each record, create only, in order; a key held already is a
	// *store.ConflictError.
	createAll(ctx context.Context, batch []copyRecord) error
	// sums is the SHA-256 of each key's bytes; a key with no record is not in it.
	sums(ctx context.Context, keys []string) (map[string][sha256.Size]byte, error)
}

// copyRecord is one record that a copy writes.
type copyRecord struct {
	Key  string
	Data []byte
}

// The bounds of one call to a bulkStore (#7394). A batch is sent when it holds
// copyBatchRecords records or copyBatchBytes bytes, so a call stays far below the handover
// session's largest message (peerMessageMost, 64 MiB, with base64 on top); a record larger
// than the bytes goes alone. sumsBatchKeys keys are checked in one call.
const (
	copyBatchRecords = 1000
	copyBatchBytes   = 4 << 20
	sumsBatchKeys    = 2000
)

// copyStoreLeaving writes each record's bytes as they are, with no largest record of its own
// (#7251): the source can be the only copy of a record from an older build, so a copy, a
// backup or a restore never refuses one. A declared store's next write and its sweep bound it.
// Into a bulkStore it writes and checks in batches: one call per batch, not one per record.
func copyStoreLeaving(ctx context.Context, from, to store.Store, leaving []string) (copied, skipped int, err error) {
	if held, err := to.Get(ctx, records.SettingsKey); err != nil {
		return 0, 0, err
	} else if held != nil {
		return 0, 0, ErrDestinationInUse
	}
	keys, err := from.List(ctx, "v1/")
	if err != nil {
		return 0, 0, err
	}
	// A store of a build before #2316 keeps machines under v1/hosts (#2662): they land
	// under v1/machines, unless the store has both.
	oldMachines := true
	for _, k := range keys {
		if strings.HasPrefix(k.Key, records.MachinesPrefix) {
			oldMachines = false
		}
	}
	bulk, _ := to.(bulkStore)
	// Each object is held only while it is written (or while its batch is): what is kept to
	// check the copy is a hash per key, so a backup of a large store (server backups
	// included) does not hold the whole store in memory (#3650).
	written := map[string][sha256.Size]byte{}
	var batch []copyRecord
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := bulk.createAll(ctx, batch)
		batch, batchBytes = batch[:0], 0
		return err
	}
	for _, k := range keys {
		if leftOut(k.Key, leaving) {
			skipped++
			continue
		}
		object, err := from.Get(ctx, k.Key)
		if err != nil {
			return 0, 0, err
		}
		if object == nil {
			continue
		}
		key := k.Key
		if oldMachines && strings.HasPrefix(key, records.OldMachinesPrefix) {
			key = records.MachinesPrefix + strings.TrimPrefix(key, records.OldMachinesPrefix)
		}
		written[key] = sha256.Sum256(object.Data)
		if bulk == nil {
			// Create only: a destination written to meanwhile is a conflict, not overwritten.
			if _, err := to.Put(ctx, key, object.Data, store.Absent); err != nil {
				return 0, 0, err
			}
			continue
		}
		if len(batch) > 0 && (len(batch) >= copyBatchRecords || batchBytes+len(object.Data) > copyBatchBytes) {
			if err := flush(); err != nil {
				return 0, 0, err
			}
		}
		batch = append(batch, copyRecord{Key: key, Data: object.Data})
		batchBytes += len(object.Data)
	}
	if bulk != nil {
		if err := flush(); err != nil {
			return 0, 0, err
		}
	}
	landed, err := to.List(ctx, "v1/")
	if err != nil {
		return 0, 0, err
	}
	count := 0
	for _, k := range landed {
		if !leftOut(k.Key, leaving) {
			count++
		}
	}
	mismatch := func(what string) error {
		return fmt.Errorf("The copy did not come out the same (%s). The old store is untouched; the new one should be emptied before trying again.", what)
	}
	if count != len(written) {
		return 0, 0, mismatch(fmt.Sprintf("%d records copied, %d found", len(written), count))
	}
	if bulk != nil {
		if err := checkSums(ctx, bulk, written, mismatch); err != nil {
			return 0, 0, err
		}
		return len(written), skipped, nil
	}
	for key, sum := range written {
		back, err := to.Get(ctx, key)
		if err != nil {
			return 0, 0, err
		}
		if back == nil || sha256.Sum256(back.Data) != sum {
			return 0, 0, mismatch(key)
		}
	}
	return len(written), skipped, nil
}

// checkSums checks what landed in a bulkStore against the hashes written, sumsBatchKeys keys
// a call.
func checkSums(ctx context.Context, bulk bulkStore, written map[string][sha256.Size]byte, mismatch func(string) error) error {
	keys := slices.Sorted(maps.Keys(written))
	for chunk := range slices.Chunk(keys, sumsBatchKeys) {
		back, err := bulk.sums(ctx, chunk)
		if err != nil {
			return err
		}
		for _, key := range chunk {
			if sum, ok := back[key]; !ok || sum != written[key] {
				return mismatch(key)
			}
		}
	}
	return nil
}
