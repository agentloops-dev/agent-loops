package agentsd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// pendingSecretOpsOnDisk is what secret-ops-pending.json keeps, none when it is gone.
func pendingSecretOpsOnDisk(t *testing.T, root string) []wire.SecretOperation {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "secret-ops-pending.json"))
	if os.IsNotExist(err) {
		return nil
	}
	var ops []wire.SecretOperation
	if err != nil || json.Unmarshal(raw, &ops) != nil {
		t.Fatalf("secret-ops-pending.json: %v %s", err, raw)
	}
	return ops
}

// A stop with reads still in their batch loses none (#6464): they go to disk with their count
// and an id, and the next start sends them when its link comes up.
func TestAStopKeepsTheReadsBatchForTheNextStart(t *testing.T) {
	setFor(t, &secretOpsBatch, time.Hour)
	root := t.TempDir()
	d, _ := start(t, root)
	told := toldSecretOps(d)
	for range 3 {
		d.secretRead("", "API_TOKEN", wire.SecretOperationUseCommand, terminalPlace)
	}
	d.Shutdown()
	if len(told()) != 0 {
		t.Fatalf("told as the host stopped: %+v", told())
	}
	kept := pendingSecretOpsOnDisk(t, root)
	if len(kept) != 1 || kept[0].ID == nil || *kept[0].ID == "" || kept[0].Count == nil || *kept[0].Count != 3 {
		t.Fatalf("%+v", kept)
	}

	again, _ := start(t, root)
	toldAgain := toldSecretOps(again)
	again.ControlUplinkChanged(true)
	waitFor(t, "the last run's read told", func() bool { return len(toldAgain()) == 1 })
	if op := toldAgain()[0]; *op.ID != *kept[0].ID || op.Name != "API_TOKEN" || *op.Count != 3 {
		t.Fatalf("%+v", op)
	}
	waitFor(t, "the file gone once told", func() bool {
		again.secretOps.mu.Lock()
		defer again.secretOps.mu.Unlock()
		return pendingSecretOpsOnDisk(t, root) == nil
	})
}

// A send that fails keeps its operations, and they go again with the same id (#6464).
func TestAFailedSendGoesAgain(t *testing.T) {
	setFor(t, &secretOpsRetry, 10*time.Millisecond)
	root := t.TempDir()
	d, _ := start(t, root)
	t.Cleanup(d.Shutdown)
	var mu sync.Mutex
	var tries [][]wire.SecretOperation
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		call, ok := params.(wire.MachineSecretOpsCall)
		if !ok || method != wire.CallMachineSecretOps.Name {
			return json.RawMessage(`{}`), nil
		}
		mu.Lock()
		defer mu.Unlock()
		tries = append(tries, call.Ops)
		if len(tries) == 1 {
			return nil, errors.New("the link dropped")
		}
		return json.RawMessage(`{}`), nil
	})
	d.secretChanged("", "NEW_TOKEN", wire.SecretOperationWhatAdded, true)
	waitFor(t, "the send tried again", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(tries) >= 2
	})
	mu.Lock()
	first, second := tries[0], tries[1]
	mu.Unlock()
	if len(first) != 1 || len(second) != 1 || first[0].ID == nil || *first[0].ID != *second[0].ID || second[0].Name != "NEW_TOKEN" {
		t.Fatalf("%+v %+v", first, second)
	}
	waitFor(t, "the file gone once told", func() bool {
		d.secretOps.mu.Lock()
		defer d.secretOps.mu.Unlock()
		return pendingSecretOpsOnDisk(t, root) == nil && len(d.secretOps.pending) == 0
	})
}

// The operations that wait are bounded: the oldest go first (#6464).
func TestThePendingSecretOpsAreBounded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d, _ := start(t, root)
	d.secretOps.mu.Lock()
	for i := range secretOpsPendingMost + 4 {
		d.secretOps.pending = append(d.secretOps.pending, wire.SecretOperation{Name: "OLD", What: wire.SecretOperationWhatAdded, At: wire.DateOf(time.Unix(int64(i), 0))})
	}
	d.secretOps.pendingLoaded = true
	d.keepSecretOpLocked(wire.SecretOperation{Name: "NEW", What: wire.SecretOperationWhatAdded, At: wire.DateOf(time.Unix(int64(secretOpsPendingMost+4), 0))})
	first := d.secretOps.pending[0].At
	d.secretOps.mu.Unlock()
	kept := pendingSecretOpsOnDisk(t, root)
	if len(kept) != secretOpsPendingMost || first != wire.DateOf(time.Unix(5, 0)) || kept[0].At != first || kept[len(kept)-1].Name != "NEW" {
		t.Fatalf("%d kept, the oldest at %v", len(kept), first)
	}
}
