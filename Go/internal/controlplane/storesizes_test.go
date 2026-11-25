package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// reports/storeSizes (#6906): an admin reads the last measurement of the control plane's stores;
// a member sees the client stores alone.
func TestStoreSizes(t *testing.T) {
	t.Parallel()
	memory := store.NewMemory()
	p := startPlaneWith(t, memory, controllink.NewPrivateKey(), func(*Config) {})
	_, admin := p.client("Alex's Mac")
	member := p.invite(admin, "Sam", wire.UserRoleMember)
	ctx := context.Background()
	if _, err := memory.Put(ctx, policyVersionsKey+"1.json", make([]byte, 10), store.Always); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Put(ctx, machineSetupTimes, make([]byte, 32<<10+1), store.Always); err != nil {
		t.Fatal(err)
	}
	ask := func(s *controllink.Session) map[string]wire.StoreSize {
		t.Helper()
		return everyStoreSize(t, s)
	}
	if s := ask(admin)["control.policy-versions"]; s.State != wire.StoreSizeStateUnavailable || s.Why == nil {
		t.Errorf("before a measurement: %+v", s)
	}
	now := time.Now()
	if err := p.s.measureStores(ctx, now); err != nil {
		t.Fatal(err)
	}
	got := ask(admin)
	if s := got["control.policy-versions"]; s.State != wire.StoreSizeStateCurrent || *s.Records != 1 || *s.PercentRecords != 1 {
		t.Errorf("under its limit: %+v", s)
	}
	if s := got["control.setup-times"]; s.State != wire.StoreSizeStateOver {
		t.Errorf("over its limit: %+v", s)
	}
	if s := got["control.log"]; s.State != wire.StoreSizeStateUnavailable || s.Why == nil {
		t.Errorf("outside the store: %+v", s)
	}
	for id, s := range ask(member.window) {
		if s.Owner != wire.StoreOwnerClient || s.State != wire.StoreSizeStateUnavailable {
			t.Errorf("a member sees %s: %+v", id, s)
		}
	}
	if len(ask(member.window)) == 0 {
		t.Error("a member sees no client store")
	}
	n := int64(101)
	if e := callError(t, admin, "", "reports/storeSizes", wire.StoreSizesQuery{Limit: &n}); e == "" {
		t.Error("a page of 101")
	}

	// At most once in six hours, whichever copy; then again.
	if err := memory.Delete(ctx, machineSetupTimes); err != nil {
		t.Fatal(err)
	}
	if err := p.s.measureStores(ctx, now.Add(storeSizesEvery-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := ask(admin)["control.setup-times"]; s.State != wire.StoreSizeStateOver {
		t.Errorf("measured before its time: %+v", s)
	}
	if err := p.s.measureStores(ctx, now.Add(storeSizesEvery+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := ask(admin)["control.setup-times"]; s.State != wire.StoreSizeStateCurrent || *s.Bytes != 0 {
		t.Errorf("not measured again: %+v", s)
	}
}

// everyStoreSize is each page of reports/storeSizes, by id.
func everyStoreSize(t *testing.T, s *controllink.Session) map[string]wire.StoreSize {
	t.Helper()
	out := map[string]wire.StoreSize{}
	for q := (wire.StoreSizesQuery{}); ; {
		var page wire.StoreSizes
		if err := json.Unmarshal(call(t, s, "", "reports/storeSizes", q), &page); err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Stores {
			out[s.ID] = s
		}
		if q.Cursor = page.Cursor; q.Cursor == nil {
			return out
		}
	}
}
