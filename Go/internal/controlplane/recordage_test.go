package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// keepJSON puts v at key.
func keepJSON(t *testing.T, st store.Store, key string, v any) {
	t.Helper()
	data, err := records.Encode(v)
	must(t, err)
	_, err = st.Put(t.Context(), key, data, store.Always)
	must(t, err)
}

// keptKeys fails for each key that is not there as want says.
func keptKeys(t *testing.T, st store.Store, want map[string]bool) {
	t.Helper()
	for key, kept := range want {
		if o, err := st.Get(t.Context(), key); err != nil || (o != nil) != kept {
			t.Errorf("%s: kept %v, want %v (%v)", key, o != nil, kept, err)
		}
	}
}

const ageDay = 24 * time.Hour

// #6765: a version goes 90 days after the next one replaced it; the one in force stays,
// however old, and the count holds.
func TestPolicyVersionsAgeOut(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	now := time.Now()
	key := func(v int64) string { return fmt.Sprintf("%s%010d.json", policyVersionsKey, v) }
	put := func(v int64, age time.Duration) {
		keepJSON(t, st, key(v), wire.OrgPolicy{Version: v, Issued: wire.DateOf(now.Add(-age)), Requires: []string{}})
	}
	put(1, 300*ageDay)
	put(2, 200*ageDay) // in force for 120 days, replaced 80 days ago
	put(3, 80*ageDay)
	must(t, p.s.sweepPolicyVersions(t.Context(), now))
	keptKeys(t, st, map[string]bool{key(1): false, key(2): true, key(3): true})
	must(t, p.s.sweepPolicyVersions(t.Context(), now.Add(400*ageDay)))
	keptKeys(t, st, map[string]bool{key(2): false, key(3): true})

	for v := int64(4); v <= policyVersionsKept+5; v++ {
		put(v, 0)
	}
	must(t, p.s.sweepPolicyVersions(t.Context(), now))
	keptKeys(t, st, map[string]bool{key(5): false, key(6): true, key(policyVersionsKept + 5): true})
}

// #6766: a live SCIM token stays, however old; one revoked or ended 90 days ago goes, and
// so do a removed provider's.
func TestSCIMTokensAgeOut(t *testing.T) {
	t.Parallel()
	p, owner, _, token, base := scimPlane(t, wire.OrgProviderJoinScim)
	st, now := p.store, time.Now()
	stamp := func(age time.Duration) wire.OrgRouteStamp {
		return wire.OrgRouteStamp{At: wire.DateOf(now.Add(-age))}
	}
	put := func(hash string, revoked, ends time.Duration) {
		r := scimTokenRecord{ID: hash, SHA256: hash, Provider: "acme-okta", Made: stamp(400 * ageDay)}
		if revoked > 0 {
			r.Revoked = new(stamp(revoked))
		}
		if ends != 0 {
			r.Ends = new(wire.DateOf(now.Add(-ends)))
		}
		keepJSON(t, st, scimTokenKey(hash), r)
	}
	put("live", 0, 0)
	put("ends-later", 0, -ageDay)
	put("revoked-now", ageDay, 0)
	put("revoked-old", 91*ageDay, 0)
	put("ended-old", 0, 91*ageDay)
	must(t, p.s.scimTokens.sweep(t.Context(), now))
	want := map[string]bool{scimTokenKey("live"): true, scimTokenKey("ends-later"): true,
		scimTokenKey("revoked-now"): true, scimTokenKey("revoked-old"): false, scimTokenKey("ended-old"): false}
	keptKeys(t, st, want)
	for n := range mostSCIMTokens + 5 {
		put(fmt.Sprintf("more-%02d", n), time.Hour, 0)
	}
	must(t, p.s.scimTokens.sweep(t.Context(), now))
	if keys, _ := st.List(t.Context(), scimTokensPrefix); len(keys) > mostSCIMTokens {
		t.Fatalf("%d tokens", len(keys))
	}
	keptKeys(t, st, map[string]bool{scimTokenKey("live"): true})
	if status, _ := scimDo(t, base, token, "GET", "/Users", nil); status != 200 {
		t.Fatal(status)
	}

	call(t, owner, "", "signIn/orgProviders/remove", wire.OrgProviderRequest{ID: "acme-okta"})
	must(t, p.s.scimTokens.sweep(t.Context(), now))
	if keys, _ := st.List(t.Context(), scimTokensPrefix); len(keys) != 0 {
		t.Fatalf("a removed provider's tokens stayed: %v", keys)
	}
}

// #6767: a live read token stays, however old; one revoked 90 days ago goes, and the count
// holds.
func TestReadTokensAgeOut(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	now := time.Now()
	put := func(hash string, revoked time.Duration) {
		r := readTokenRecord{ID: hash, SHA256: hash, Made: wire.OrgRouteStamp{At: wire.DateOf(now.Add(-400 * ageDay))}}
		if revoked > 0 {
			r.Revoked = &wire.OrgRouteStamp{At: wire.DateOf(now.Add(-revoked))}
		}
		keepJSON(t, st, readTokenKey(hash), r)
	}
	put("live", 0)
	put("revoked-now", ageDay)
	put("revoked-old", 91*ageDay)
	must(t, p.s.readAPI.sweep(t.Context(), now))
	keptKeys(t, st, map[string]bool{readTokenKey("live"): true, readTokenKey("revoked-now"): true, readTokenKey("revoked-old"): false})
	for n := range mostReadTokens + 5 {
		put(fmt.Sprintf("more-%02d", n), time.Hour)
	}
	must(t, p.s.readAPI.sweep(t.Context(), now))
	if keys, _ := st.List(t.Context(), readTokensPrefix); len(keys) != mostReadTokens {
		t.Fatalf("%d tokens", len(keys))
	}
	keptKeys(t, st, map[string]bool{readTokenKey("live"): true})
}

// #6764: an approval in force has no age and a record over its largest size is refused; a
// request nobody answered goes at 90 days.
func TestGovernanceAgesOutRequestsAndNotApprovals(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	now := time.Now()
	user := p.s.localOwner()
	a := wire.CatalogApproval{Kind: wire.CatalogKindMCP, Name: "server", User: &user, By: "admin", At: dateOf(now.Add(-400 * ageDay))}
	must(t, p.s.keepApproval(t.Context(), a))
	must(t, p.s.sweepApprovals(t.Context()))
	keptKeys(t, st, map[string]bool{governanceApprovalKey(a.Kind, a.Name, "", user): true})
	a.Name, a.By = "large", strings.Repeat("x", governanceRecordBytes)
	if err := p.s.keepApproval(t.Context(), a); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatal(err)
	}
	for id, age := range map[string]time.Duration{"old": 91 * ageDay, "new": 89 * ageDay} {
		keepJSON(t, st, governanceRequestKey(id), wire.CatalogRequest{ID: id, Kind: wire.CatalogKindMCP, Name: "server",
			User: user, State: wire.CatalogRequestStateWaiting, At: dateOf(now.Add(-age))})
	}
	_, err := p.s.governanceRequests(t.Context(), now)
	must(t, err)
	keptKeys(t, st, map[string]bool{governanceRequestKey("old"): false, governanceRequestKey("new"): true})
}

// #6768: a machine that sends no more loses its nodes at 90 days, in the sweep; a node over
// its largest size is not kept, and the rest of its send is.
func TestLineageNodesAgeOutInTheSweep(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	host := p.host("OWNERS-MAC").member.Host
	now := time.Now()
	node := func(ref string, age time.Duration) wire.LineageCostNode {
		return wire.LineageCostNode{Node: wire.LineageNode{Kind: "issue", Ref: "github:issue:o/r#" + ref},
			Banked: wire.LineageCost{Cost: map[string]float64{"USD": 1}}, LastAt: wire.DateOf(now.Add(-age))}
	}
	large := node("3", 0)
	large.Files = []string{strings.Repeat("x", lineageNodeBytes)}
	// Its machine's clock is ahead.
	ahead := node("4", -365*ageDay)
	must(t, p.s.keepWork(t.Context(), host, []wire.LineageCostNode{node("1", 60*ageDay), node("2", ageDay), large, ahead}, now))
	refs := func() string {
		object, err := st.Get(t.Context(), lineageKey(host))
		must(t, err)
		var kept storedLineage
		must(t, json.Unmarshal(object.Data, &kept))
		out := ""
		for _, n := range kept.Nodes {
			out += n.Node.Ref[strings.Index(n.Node.Ref, "#"):]
		}
		return out
	}
	if got := refs(); got != "#4#2#1" {
		t.Fatal(got)
	}
	for _, step := range []struct {
		days, wrote int
		left        string
	}{{0, 0, "#4#2#1"}, {31, 1, "#4#2"}, {31, 0, "#4#2"}, {91, 1, ""}} {
		n, err := sweepLineage(t.Context(), st, p.s.records, now.Add(time.Duration(step.days)*ageDay), 10)
		if got := refs(); err != nil || n != step.wrote || got != step.left {
			t.Fatalf("%d days: %d %q %v", step.days, n, got, err)
		}
	}
	// The count and the bytes: the oldest go.
	many := make([]wire.LineageCostNode, lineageNodesKept+1)
	for i := range many {
		many[i] = node(fmt.Sprint(i), time.Duration(i)*time.Second)
	}
	if got, _ := boundLineage(many, now); len(got) != lineageNodesKept || !strings.HasSuffix(got[0].Node.Ref, "#0") {
		t.Fatalf("%d", len(got))
	}
	many = many[:lineageBytes/(lineageNodeBytes/2)+1]
	for i := range many {
		many[i].Files = []string{strings.Repeat("x", lineageNodeBytes/2)}
	}
	if got, tooLarge := boundLineage(many, now); len(got) >= len(many) || tooLarge != 0 {
		t.Fatalf("%d of %d, %d too large", len(got), len(many), tooLarge)
	}
}
