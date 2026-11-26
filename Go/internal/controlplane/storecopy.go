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
	createAll(ctx context.Context, batch []storeRecord) error
	// sums is the SHA-256 of each key's bytes; a key with no record is not in it.
	sums(ctx context.Context, keys []string) (map[string][sha256.Size]byte, error)
}

// storeRecord is one record that a copy writes.
type storeRecord struct {
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
	return copyStoreReporting(ctx, from, to, leaving, nil)
}

// copyProgress hears how a copy stands: the records written so far, of the total it takes.
// Into a bulkStore it is called after each batch lands; into another store, after each record.
type copyProgress func(copied, total int)

// copyStoreReporting is copyStoreLeaving, and tells progress, when not nil, how it stands
// (#7395): a move's strip counts the records.
func copyStoreReporting(ctx context.Context, from, to store.Store, leaving []string, progress copyProgress) (copied, skipped int, err error) {
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
	oldMachines := !slices.ContainsFunc(keys, func(k store.Key) bool { return strings.HasPrefix(k.Key, records.MachinesPrefix) })
	// Each object is held only while it is written (or while its batch is): what is kept to
	// check the copy is a hash per key, so a backup of a large store (server backups
	// included) does not hold the whole store in memory (#3650).
	w := &copyWriter{to: to, progress: progress}
	w.bulk, _ = to.(bulkStore)
	for _, k := range keys {
		if !leftOut(k.Key, leaving) {
			w.total++
		}
	}
	w.tell()
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
		if err := w.write(ctx, key, object.Data); err != nil {
			return 0, 0, err
		}
	}
	if err := w.flush(ctx); err != nil {
		return 0, 0, err
	}
	if err := w.check(ctx, leaving); err != nil {
		return 0, 0, err
	}
	return len(w.written), skipped, nil
}

// copyWriter is one copy's writes into its destination, by record or, into a bulkStore, by
// batch, and the hash of each record it wrote.
type copyWriter struct {
	to         store.Store
	bulk       bulkStore
	written    map[string][sha256.Size]byte
	batch      []storeRecord
	batchBytes int
	// landed is the records written so far, of total; progress hears both.
	landed, total int
	progress      copyProgress
}

// tell says how the copy stands to progress, if any.
func (w *copyWriter) tell() {
	if w.progress != nil {
		w.progress(w.landed, w.total)
	}
}

func (w *copyWriter) write(ctx context.Context, key string, data []byte) error {
	if w.written == nil {
		w.written = map[string][sha256.Size]byte{}
	}
	w.written[key] = sha256.Sum256(data)
	if w.bulk == nil {
		// Create only: a destination written to meanwhile is a conflict, not overwritten.
		if _, err := w.to.Put(ctx, key, data, store.Absent); err != nil {
			return err
		}
		w.landed++
		w.tell()
		return nil
	}
	if len(w.batch) > 0 && (len(w.batch) >= copyBatchRecords || w.batchBytes+len(data) > copyBatchBytes) {
		if err := w.flush(ctx); err != nil {
			return err
		}
	}
	w.batch = append(w.batch, storeRecord{Key: key, Data: data})
	w.batchBytes += len(data)
	return nil
}

// flush sends the batch held, if any.
func (w *copyWriter) flush(ctx context.Context) error {
	if len(w.batch) == 0 {
		return nil
	}
	err := w.bulk.createAll(ctx, w.batch)
	if err == nil {
		w.landed += len(w.batch)
		w.tell()
	}
	w.batch, w.batchBytes = nil, 0
	return err
}

// check is the copy's check: as many records landed as were written, each with its bytes.
func (w *copyWriter) check(ctx context.Context, leaving []string) error {
	landed, err := w.to.List(ctx, "v1/")
	if err != nil {
		return err
	}
	count := 0
	for _, k := range landed {
		if !leftOut(k.Key, leaving) {
			count++
		}
	}
	if count != len(w.written) {
		return copyMismatch(fmt.Sprintf("%d records copied, %d found", len(w.written), count))
	}
	if w.bulk != nil {
		return checkSums(ctx, w.bulk, w.written)
	}
	for key, sum := range w.written {
		back, err := w.to.Get(ctx, key)
		if err != nil {
			return err
		}
		if back == nil || sha256.Sum256(back.Data) != sum {
			return copyMismatch(key)
		}
	}
	return nil
}

func copyMismatch(what string) error {
	return fmt.Errorf("The copy did not come out the same (%s). The old store is untouched; the new one should be emptied before trying again.", what)
}

// checkSums checks what landed in a bulkStore against the hashes written, sumsBatchKeys keys
// a call.
func checkSums(ctx context.Context, bulk bulkStore, written map[string][sha256.Size]byte) error {
	keys := slices.Sorted(maps.Keys(written))
	for chunk := range slices.Chunk(keys, sumsBatchKeys) {
		back, err := bulk.sums(ctx, chunk)
		if err != nil {
			return err
		}
		for _, key := range chunk {
			if sum, ok := back[key]; !ok || sum != written[key] {
				return copyMismatch(key)
			}
		}
	}
	return nil
}
