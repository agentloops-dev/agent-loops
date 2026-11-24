package controlplane

// #3748: what the store kept for ever goes, and what had no count has one: expired clients
// and machines, providers' tombstones, approvals of people and groups that are gone,
// probes and move marks, codes and started sign-ins.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/signin/signintest"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A client whose key expired, and a machine whose key expired 90 days ago, is forgotten as
// one forgotten by hand is; a live one, and a client connected here, stay.
func TestTheSweepForgetsClientsAndMachinesLongExpired(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	old, window := p.client("Old iPhone")
	window.Close()
	recent, window := p.client("Recent iPad")
	window.Close()
	live, window := p.client("Live Mac")
	window.Close()
	oldHost := p.host("MACHINE-OLD")
	oldHost.stop()
	liveHost := p.host("MACHINE-LIVE")
	liveHost.stop()
	now := time.Now()
	setExpires(t, p, old.Client, now.Add(-time.Minute))
	setExpires(t, p, recent.Client, now.Add(time.Minute))
	used, inUse := p.client("Connected Mac")
	defer inUse.Close()
	setExpires(t, p, used.Client, now.Add(-time.Minute))
	saveHostExpires(t, p, oldHost.member.Host, now.Add(-91*24*time.Hour))

	if gone := p.s.forgetExpired(context.Background(), now, mostPerSweep); gone != 2 {
		t.Fatalf("%d went, not 2", gone)
	}
	if p.s.records.Client(old.Client) != nil || !p.s.records.WasForgotten(old.Client) {
		t.Fatal("the long-expired client was not forgotten")
	}
	if p.s.records.Host(oldHost.member.Host) != nil {
		t.Fatal("the long-expired machine was not removed")
	}
	if p.s.records.Client(recent.Client) == nil || p.s.records.Client(live.Client) == nil || p.s.records.Client(used.Client) == nil || p.s.records.Host(liveHost.member.Host) == nil {
		t.Fatal("one not long expired went")
	}
	// Renewed at another copy since it was read: it keeps its pairing.
	if went, err := p.s.records.ForgetClientIf(context.Background(), live.Client, func(records.ClientRecord) bool { return false }); went || err != nil {
		t.Fatalf("a renewed client went: %v %v", went, err)
	}
	// At most most a time.
	setExpires(t, p, recent.Client, now.Add(-91*24*time.Hour))
	setExpires(t, p, live.Client, now.Add(-91*24*time.Hour))
	if gone := p.s.forgetExpired(context.Background(), now, 1); gone != 1 {
		t.Fatalf("%d went, not 1", gone)
	}
}

// A removed provider's tombstone goes a week on; one from before forgottenAt is dated
// first; a live provider stays.
func TestTheSweepRemovesProvidersTombstones(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	rs := records.New(s)
	if _, err := rs.SettingsOrMake(ctx, func() records.Settings { return records.Settings{Name: "test", MachineID: "m"} }); err != nil {
		t.Fatal(err)
	}
	yes, now := true, time.Now()
	removedAt := records.At(now)
	for _, r := range []providers.ProviderRecord{
		{ID: "live", Name: "Live", Kind: providers.KindCommands},
		{ID: "removed", Name: "Removed", Kind: providers.KindCommands, Forgotten: &yes, ForgottenAt: &removedAt},
		{ID: "legacy", Name: "Legacy", Kind: providers.KindCommands, Forgotten: &yes},
	} {
		data, _ := records.Encode(r)
		if _, err := s.Put(ctx, providers.ProviderKey(r.ID), data, store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	first, err := sweep(ctx, rs, now, mostPerSweep, nil)
	if err != nil || first.tombstones != 0 || first.dated != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	later, err := sweep(ctx, rs, now.Add(8*24*time.Hour), mostPerSweep, nil)
	if err != nil || later.tombstones != 2 {
		t.Fatalf("%+v %v", later, err)
	}
	for id, want := range map[string]bool{"live": true, "removed": false, "legacy": false} {
		if o, _ := s.Get(ctx, providers.ProviderKey(id)); (o != nil) != want {
			t.Errorf("%s: kept %v", id, o != nil)
		}
	}
}

// A probe or move mark goes an hour after the sweep first saw it, on any store; the
// forwarding note stays.
func TestTheSweepRemovesStrayProbesAndMarks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	rs := records.New(s)
	if _, err := rs.SettingsOrMake(ctx, func() records.Settings { return records.Settings{Name: "test", MachineID: "m"} }); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"probe/copy-1", "handover/mark-abc", "handover/forwarding-x"} {
		if _, err := s.Put(ctx, key, []byte("x"), store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	events := &eventSweep{}
	now := time.Now()
	if report, err := sweep(ctx, rs, now, mostPerSweep, events); err != nil || report.leftovers != 0 {
		t.Fatalf("%+v %v", report, err)
	}
	// Seen again in the same hour: still young.
	if report, _ := sweep(ctx, rs, now.Add(30*time.Minute), mostPerSweep, events); report.leftovers != 0 {
		t.Fatalf("%+v", report)
	}
	if _, err := s.Put(ctx, "probe/copy-2", []byte("x"), store.Absent); err != nil {
		t.Fatal(err)
	}
	if report, err := sweep(ctx, rs, now.Add(time.Hour), mostPerSweep, events); err != nil || report.leftovers != 2 {
		t.Fatalf("%+v %v", report, err)
	}
	for key, want := range map[string]bool{"probe/copy-1": false, "handover/mark-abc": false, "probe/copy-2": true, "handover/forwarding-x": true} {
		if o, _ := s.Get(ctx, key); (o != nil) != want {
			t.Errorf("%s: kept %v", key, o != nil)
		}
	}
	if len(events.strays) != 1 {
		t.Fatalf("the sweep remembers more than what is there: %v", events.strays)
	}
}

// An approval of a removed person or group, or of one with no record, goes; one of a
// live person or group, and the local owner's, stays.
func TestTheSweepRemovesApprovalsOfPeopleAndGroupsThatAreGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	rs := p.s.records
	yes, now := true, records.Now()
	if _, err := rs.ChangeGroup(ctx, "g-live", func(*records.Group) (*records.Group, error) { return &records.Group{ID: "g-live", Name: "Live"}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.ChangeGroup(ctx, "g-gone", func(*records.Group) (*records.Group, error) {
		return &records.Group{ID: "g-gone", Name: "Gone", Removed: &yes, RemovedAt: &now}, nil
	}); err != nil {
		t.Fatal(err)
	}
	must(t, rs.ChangePeople(ctx, func(map[string]records.Person) ([]records.Person, error) {
		return []records.Person{{ID: "u-live", Name: "Live"}, {ID: "u-gone", Name: "Gone", Removed: &yes, RemovedAt: &now}}, nil
	}))
	approve := func(group, user string) string {
		a := wire.CatalogApproval{Kind: wire.CatalogKindMCP, Name: "server", Group: optional(group), User: optional(user), By: "admin"}
		if err := p.s.keepApproval(ctx, a); err != nil {
			t.Fatal(err)
		}
		return governanceApprovalKey(a.Kind, a.Name, group, user)
	}
	want := map[string]bool{
		approve("g-live", ""): true, approve("", "u-live"): true, approve("", p.s.localOwner()): true,
		approve("g-gone", ""): false, approve("", "u-gone"): false, approve("g-never", ""): false, approve("", "u-never"): false,
	}
	if err := p.s.sweepApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	for key, kept := range want {
		if o, _ := p.s.cfg.Store.Get(ctx, key); (o != nil) != kept {
			t.Errorf("%s: kept %v, want %v", key, o != nil, kept)
		}
	}
}

// The store holds at most codesMost codes; the next is refused, and changes nothing.
func TestCodesHaveACount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	st := p.s.cfg.Store
	keys, _ := st.List(ctx, codesPrefix)
	for i, have := 0, countSuffix(keys, ".json"); have+i < codesMost; i++ {
		if _, err := st.Put(ctx, codeKey(fmt.Sprintf("filler-%d", i)), []byte("{}"), store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.s.codes.Issue(ctx, false, false, controllink.CodeLifetime); err == nil || !strings.Contains(err.Error(), "codes already") {
		t.Fatalf("a code over the count: %v", err)
	}
	if keys, _ := st.List(ctx, codesPrefix); countSuffix(keys, ".json") != codesMost {
		t.Fatalf("%d codes", countSuffix(keys, ".json"))
	}
	must(t, st.Delete(ctx, codeKey("filler-0")))
	if _, err := p.s.codes.Issue(ctx, false, false, controllink.CodeLifetime); err != nil {
		t.Fatal(err)
	}
}

// The store holds at most oidcStatesMost started sign-ins; the next is refused.
func TestStartedSignInsHaveACount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prov := signintest.NewProvider(t, "RS256", "/oauth2/default")
	p, _, owner, _ := orgPlane(t, prov, nil)
	addOrg(t, owner, prov, wire.OrgProviderJoinAnyone)
	st := p.s.cfg.Store
	for i := range oidcStatesMost - 1 {
		if _, err := st.Put(ctx, storeKeyHash(oidcStatePrefix, fmt.Sprint(i))+".json", []byte("{}"), store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	s := openSignIn(t, p)
	if orgStartOn(t, s, wire.SignInOrgReturnApp) == "" {
		t.Fatal("the last one under the count was not started")
	}
	_, err := s.Call(ctx, "signIn/orgStart", wire.SignInOrgStart{Provider: "acme-okta", Purpose: wire.SignInOrgPurposePair, ReturnTo: wire.SignInOrgReturnApp})
	if err == nil || !strings.Contains(err.Error(), "Too many sign-ins") {
		t.Fatalf("a sign-in over the count: %v", err)
	}
}
