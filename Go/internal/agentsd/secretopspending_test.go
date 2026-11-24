package agentsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// pendingSecretOpsOnDisk is what secret-ops-pending.json keeps, the oldest first.
func pendingSecretOpsOnDisk(t *testing.T, root string) []wire.SecretOperation {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "secret-ops-pending.json"))
	if os.IsNotExist(err) {
		return nil
	}
	var ops map[string]wire.SecretOperation
	if err != nil || diskjson.Unmarshal(raw, &ops) != nil {
		t.Fatalf("secret-ops-pending.json: %v %s", err, raw)
	}
	return oldestSecretOps(ops)
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
	waitFor(t, "none kept once told", func() bool {
		again.secretOps.mu.Lock()
		defer again.secretOps.mu.Unlock()
		return len(pendingSecretOpsOnDisk(t, root)) == 0
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
	waitFor(t, "none kept once told", func() bool {
		d.secretOps.mu.Lock()
		defer d.secretOps.mu.Unlock()
		return len(pendingSecretOpsOnDisk(t, root)) == 0 && len(d.secretOps.unsaved) == 0
	})
}

// The operations that wait are bounded by age, count and size (#6464, #6721), the oldest first,
// with a log line; a file from before the limits, a list, keeps its operations.
func TestThePendingSecretOpsAreBounded(t *testing.T) {
	root := t.TempDir()
	var logged bytes.Buffer
	var logMu sync.Mutex
	t.Cleanup(captureLog(&logged, &logMu))
	clock := atTime(t, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	old := []wire.SecretOperation{{Name: "OLD", What: wire.SecretOperationWhatAdded, At: wire.DateOf(nowFunc())}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret-ops-pending.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	d, _ := start(t, root)
	keep := func(op wire.SecretOperation) {
		d.secretOps.mu.Lock()
		defer d.secretOps.mu.Unlock()
		op.What, op.At = wire.SecretOperationWhatAdded, wire.DateOf(nowFunc())
		d.keepSecretOpLocked(op)
	}

	clock.add(store.MaxAge - time.Hour)
	long := strings.Repeat("<", 20<<10)
	keep(wire.SecretOperation{Name: long, Place: &long})
	kept := pendingSecretOpsOnDisk(t, root)
	if len(kept) != 2 || kept[0].Name != "OLD" || *kept[0].ID == "" || len(kept[1].Name) != secretOpMostName || len(*kept[1].Place) != secretOpMostPlace {
		t.Fatalf("%d kept: %.2000v", len(kept), kept)
	}

	// 90 days after it, the old one goes on the next write, and says so.
	clock.add(time.Hour)
	keep(wire.SecretOperation{Name: "NEW"})
	if kept = pendingSecretOpsOnDisk(t, root); len(kept) != 2 || kept[1].Name != "NEW" {
		t.Fatalf("after 90 days: %d kept", len(kept))
	}
	logMu.Lock()
	out := logged.String()
	logMu.Unlock()
	if !strings.Contains(out, "retention: 1 entries of secret-ops-pending.json past its limits") {
		t.Fatalf("not logged: %s", out)
	}

	// Past the count, the sweep takes the oldest.
	d.secretOps.mu.Lock()
	for i := range secretOpsPendingMost {
		id := fmt.Sprint(i)
		d.secretOps.unsaved[id] = wire.SecretOperation{ID: &id, Name: "MORE", What: wire.SecretOperationWhatAdded, At: wire.DateOf(nowFunc().Add(time.Second))}
	}
	clock.add(time.Minute)
	d.savePendingSecretOpsLocked()
	d.secretOps.mu.Unlock()
	d.sweepSmallStores(nowFunc())
	if kept = pendingSecretOpsOnDisk(t, root); len(kept) != secretOpsPendingMost || kept[0].Name != "MORE" {
		t.Fatalf("after the sweep: %d kept, first %.40s", len(kept), kept[0].Name)
	}

	// A quiet machine's go in the sweep.
	clock.add(store.MaxAge)
	d.sweepSmallStores(nowFunc())
	if kept = pendingSecretOpsOnDisk(t, root); len(kept) != 0 {
		t.Fatalf("after 90 quiet days: %d kept", len(kept))
	}
}

// More than one send's operations all go, the oldest first, and none twice.
func TestManyPendingSecretOpsGoInParts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d, _ := start(t, root)
	ops := map[string]wire.SecretOperation{}
	for i := range secretOpsSendMost + 1 {
		id := fmt.Sprintf("%04d", i)
		ops[id] = wire.SecretOperation{ID: &id, Name: "N", What: wire.SecretOperationWhatAdded, At: wire.DateOf(nowFunc())}
	}
	d.secretOps.mu.Lock()
	d.secretOps.unsaved = ops
	d.secretOps.mu.Unlock()
	told := toldSecretOps(d)
	d.sendSecretOps()
	if got := told(); len(got) != secretOpsSendMost+1 || *got[0].ID != "0000" || len(pendingSecretOpsOnDisk(t, root)) != 0 {
		t.Fatalf("told %d", len(got))
	}
}

// A file that cannot be written keeps the operations in memory, and they still go.
func TestAWriteThatFailsStillTellsTheAudit(t *testing.T) {
	setFor(t, &secretOpsRetry, time.Hour)
	root := t.TempDir()
	d, _ := start(t, root)
	if err := os.Mkdir(d.secretOpsPendingPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	told := toldSecretOps(d)
	d.secretChanged("", "NEW_TOKEN", wire.SecretOperationWhatAdded, true)
	waitFor(t, "the operation told", func() bool { return len(told()) == 1 })
}
