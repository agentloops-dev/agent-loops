package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// A copy carries every record but leases and copies, and refuses a store already in use.
func TestCopyStoreCarriesRecordsAndRefusesAStoreInUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from, to := store.NewFolder(t.TempDir()), store.NewFolder(t.TempDir())
	for _, key := range []string{records.SettingsKey, "v1/machines/a.json", "v1/leases/a.json", "v1/copies/b.json", "v1/keys/control.sealed"} {
		if _, err := from.Put(ctx, key, []byte(key), store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	// The receiving copy's own sealed key is there already (#1458), and stays.
	if _, err := to.Put(ctx, "v1/keys/control.sealed", []byte("its own"), store.Absent); err != nil {
		t.Fatal(err)
	}
	copied, skipped, err := CopyStore(ctx, from, to)
	if err != nil || copied != 2 || skipped != 3 {
		t.Fatal(copied, skipped, err)
	}
	if o, _ := to.Get(ctx, "v1/machines/a.json"); o == nil || string(o.Data) != "v1/machines/a.json" {
		t.Fatal("the host record did not land")
	}
	if o, _ := to.Get(ctx, "v1/leases/a.json"); o != nil {
		t.Fatal("a lease was copied")
	}
	if o, _ := to.Get(ctx, "v1/keys/control.sealed"); o == nil || string(o.Data) != "its own" {
		t.Fatal("the receiving copy's sealed key changed")
	}
	if _, _, err := CopyStore(ctx, from, to); !errors.Is(err, ErrDestinationInUse) {
		t.Fatal(err)
	}
}

// countingBulk is a store that takes batches, and counts its calls.
type countingBulk struct {
	store.Store
	batches [][]copyRecord
	sumCalls int
}

func (c *countingBulk) createAll(ctx context.Context, batch []copyRecord) error {
	c.batches = append(c.batches, slices.Clone(batch))
	for _, r := range batch {
		if _, err := c.Put(ctx, r.Key, r.Data, store.Absent); err != nil {
			return err
		}
	}
	return nil
}

func (c *countingBulk) sums(ctx context.Context, keys []string) (map[string][sha256.Size]byte, error) {
	c.sumCalls++
	out := map[string][sha256.Size]byte{}
	for _, k := range keys {
		if o, _ := c.Get(ctx, k); o != nil {
			out[k] = sha256.Sum256(o.Data)
		}
	}
	return out, nil
}

// Into a store that takes batches, a copy sends a call per batch, bounded in records and in
// bytes, and checks in a call per sumsBatchKeys keys (#7394). A record larger than a batch
// goes alone.
func TestCopyStoreSendsBoundedBatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from := store.NewMemory()
	to := &countingBulk{Store: store.NewMemory()}
	for i := range 2*copyBatchRecords + 1 {
		_, err := from.Put(ctx, fmt.Sprintf("v1/a/%05d", i), []byte("small"), store.Absent)
		must(t, err)
	}
	_, err := from.Put(ctx, "v1/b/large", bytes.Repeat([]byte("L"), copyBatchBytes+1), store.Absent)
	must(t, err)
	copied, _, err := CopyStore(ctx, from, to)
	if err != nil || copied != 2*copyBatchRecords+2 {
		t.Fatal(copied, err)
	}
	if len(to.batches) != 4 {
		t.Fatal(len(to.batches))
	}
	for _, b := range to.batches {
		size := 0
		for _, r := range b {
			size += len(r.Data)
		}
		if len(b) > copyBatchRecords || len(b) > 1 && size > copyBatchBytes {
			t.Fatal(len(b), size)
		}
	}
	if want := (copied + sumsBatchKeys - 1) / sumsBatchKeys; to.sumCalls != want {
		t.Fatal(to.sumCalls, want)
	}
}

// A record that changed on its way is a mismatch.
func TestCopyStoreFindsABatchThatDidNotLand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	from := store.NewMemory()
	_, err := from.Put(ctx, "v1/a/one", []byte("one"), store.Absent)
	must(t, err)
	to := &wrongBulk{countingBulk{Store: store.NewMemory()}}
	if _, _, err := CopyStore(ctx, from, to); err == nil || !strings.Contains(err.Error(), "did not come out the same") {
		t.Fatal(err)
	}
}

type wrongBulk struct{ countingBulk }

func (w *wrongBulk) createAll(ctx context.Context, batch []copyRecord) error {
	for _, r := range batch {
		if _, err := w.Put(ctx, r.Key, []byte("changed"), store.Absent); err != nil {
			return err
		}
	}
	return nil
}
