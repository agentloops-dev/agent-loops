package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func tooLarge(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(asRPC(err).Message, "too large") {
		t.Fatalf("%v", err)
	}
}

// The configuration in one record each is refused over its largest, and kept at it (#6773,
// #6777, #6778, #6779).
func TestConfigurationOverItsLargestIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	s, owner := p.s, p.s.localOwner()
	// The most a person may allow fits: 50 domains and 50 organisations.
	var domains, orgs []string
	for n := range 50 {
		domains = append(domains, fmt.Sprintf("%02d.%s", n, strings.Repeat("a", 250)))
		orgs = append(orgs, fmt.Sprintf("%02d%s", n, strings.Repeat("b", 37)))
	}
	if _, err := s.signIns.configure(ctx, wire.SignInConfigure{Provider: "google", Domains: domains}, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.signIns.configure(ctx, wire.SignInConfigure{Provider: "github", Organisations: orgs}, owner); err != nil {
		t.Fatal(err)
	}

	sink := func(n int) wire.AuditSink {
		return wire.AuditSink{Kind: wire.AuditSinkKindHttp, Address: "https://siem.example.com/" + strings.Repeat("x", n)}
	}
	base, _ := json.Marshal(auditSinksRecord{Sinks: []wire.AuditSink{sink(0)}})
	if err := s.setAuditSinks(ctx, []wire.AuditSink{sink(auditSinksMostBytes - len(base))}); err != nil {
		t.Fatal(err)
	}
	tooLarge(t, s.setAuditSinks(ctx, []wire.AuditSink{sink(auditSinksMostBytes - len(base) + 1)}))
	var five []wire.AuditSink
	for n := range 5 {
		five = append(five, sink(n))
	}
	if err := s.setAuditSinks(ctx, five[:4]); err != nil {
		t.Fatal(err)
	}
	if err := s.setAuditSinks(ctx, five); err == nil {
		t.Fatal("five sinks")
	}

	long := func(n int) wire.ReportViews {
		project := strings.Repeat("x", n)
		return wire.ReportViews{Views: []wire.ReportView{{ID: "v", Scope: wire.ReportScope{Kind: wire.ReportScopeKindMe, Project: &project}}}}
	}
	base, _ = records.Encode(long(0))
	if _, err := s.keepReportViews(ctx, owner, long(mostViewsBytes-len(base))); err != nil {
		t.Fatal(err)
	}
	_, err := s.keepReportViews(ctx, owner, long(mostViewsBytes-len(base)+1))
	tooLarge(t, err)

	// Only the organisation's budget is kept, whatever is sent, so the record stays small.
	many := map[string]float64{orgBudget: mostBudgetUSD}
	for n := range 1000 {
		many[fmt.Sprintf("group-%d", n)] = 1
	}
	if _, err := s.setReportPolicy(ctx, wire.ReportPolicy{BudgetsUSD: many}); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.Get(ctx, reportPolicyKey); o == nil || len(o.Data) > reportPolicyMostBytes {
		t.Fatalf("%v", o)
	}
}

// A policy is refused over 64 KiB; one over it already can be changed to one no larger, and
// the kill switch is never refused for the size (#6772).
func TestAPolicyOverItsLargest(t *testing.T) {
	t.Parallel()
	// One time, so that each policy's dates are as long.
	now, by := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC), wire.AuditActor{User: "a"}
	var kept keptPolicy
	change := func(change func(*wire.OrgPolicy)) error {
		next, raw, err := nextPolicy(kept, now, by, func(_ time.Time, current *wire.OrgPolicy) (wire.OrgPolicy, error) {
			p := wire.OrgPolicy{}
			if current != nil {
				p = *current
			}
			change(&p)
			return p, nil
		})
		if err == nil {
			kept = keptPolicy{raw: raw, policy: &next}
		}
		return err
	}
	set := func(n int) error {
		return change(func(p *wire.OrgPolicy) { p.Sandbox = new(strings.Repeat("x", n)) })
	}
	must(t, set(1))
	room := mostPolicyBytes - len(kept.raw) + 1
	must(t, set(room))
	if len(kept.raw) != mostPolicyBytes {
		t.Fatal(len(kept.raw))
	}
	if err := set(room + 1); err == nil || !strings.Contains(err.Error(), "larger than 64 KiB") {
		t.Fatalf("%v", err)
	}
	must(t, change(func(p *wire.OrgPolicy) {
		p.StopAll = &wire.PolicyStopAll{On: true, By: by, At: wire.DateOf(now)}
	}))
	if len(kept.raw) <= mostPolicyBytes {
		t.Fatal(len(kept.raw))
	}
	must(t, set(room-1))
	if err := set(room); err == nil {
		t.Fatal("a policy over its largest grew")
	}
}

// The largest route record and the largest sealed record are kept, and one more byte is
// refused (#6776).
func TestARouteOverItsLargestIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	record := func(n int) orgSecretRecord {
		return orgSecretRecord{ID: "r", SealVersion: 1, Sealed: map[string]map[string]wire.SealedEnvelope{strings.Repeat("m", n): {}}}
	}
	base, _ := records.Encode(record(0))
	if _, err := p.s.writeRouteSecrets(ctx, record(mostSealedRecord-len(base)), ""); err != nil {
		t.Fatal(err)
	}
	_, err := p.s.writeRouteSecrets(ctx, orgSecretRecord{ID: "q", Sealed: record(mostSealedRecord).Sealed}, "")
	tooLarge(t, routeKeyFailure(err))

	_, admin := p.client("Window")
	routes := wire.OrgRoute{Name: "Big", Kind: wire.OrgRouteKindKey, Vendor: vendor(wire.OrgRouteVendorAnthropic), Runtimes: []string{"claude"}, Models: map[string]string{}}
	for n := range mostRouteModels {
		routes.Models[fmt.Sprintf("%03d%s", n, strings.Repeat("é", 190))] = strings.Repeat("語", 200)
	}
	p.host("BOX")
	sealed := sealRoute(t, admin, routes, map[string]string{"apiKey": "k"}, 1)
	_, err = p.s.setOrgRoute(ctx, wire.AuditActor{User: "a"}, "orgRoutes/set", routes, sealed, nil, false)
	if err != nil {
		t.Fatalf("the most models fit: %v", err)
	}
	routes.Models = map[string]string{}
	for n := range mostRouteModels {
		routes.Models[fmt.Sprintf("%03d%s", n, strings.Repeat("😀", 197))] = strings.Repeat("😀", 200)
	}
	routes.Name = "Bigger"
	_, err = p.s.setOrgRoute(ctx, wire.AuditActor{User: "a"}, "orgRoutes/set", routes, sealed, nil, false)
	tooLarge(t, err)
}

// A removed org provider's tombstone goes at 90 days, once no person and no SCIM token names
// it; before that its id is refused (#6774).
func TestTheSweepDeletesRemovedOrgProviders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	now := time.Now()
	put := func(key string, v any) {
		t.Helper()
		data, err := records.Encode(v)
		must(t, err)
		_, err = st.Put(ctx, key, data, store.Always)
		must(t, err)
	}
	removed := func(id string, days int) {
		at := records.At(now.Add(-time.Duration(days) * 24 * time.Hour))
		put(orgProviderKey(id), orgProviderStored{OrgProvider: wire.OrgProvider{ID: id}, Removed: &at})
	}
	removed("old", 91)
	removed("new", 89)
	removed("signed", 91)
	removed("scim-named", 91)
	removed("token", 91)
	put(orgProviderKey("live"), orgProviderStored{OrgProvider: wire.OrgProvider{ID: "live"}})
	yes := true
	put(records.PersonKey("p"), records.Person{ID: "p", Name: "p", Removed: &yes, SignIns: []records.SignIn{{Provider: "signed", Account: "a"}}})
	put(scimNameKey("scim-named", "a"), map[string]string{})
	put(scimTokenKey("x"), scimTokenRecord{Provider: "token"})
	rs := records.New(st)
	if n, _, err := sweepRemovedOrgProviders(ctx, rs, now, 10); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	for id, want := range map[string]bool{"old": false, "new": true, "signed": true, "scim-named": true, "token": true, "live": true} {
		if o, _ := st.Get(ctx, orgProviderKey(id)); (o != nil) != want {
			t.Errorf("%s: kept %v", id, o != nil)
		}
	}
	// A person record that cannot be read may name any.
	must(t, st.Delete(ctx, scimNameKey("scim-named", "a")))
	if _, err := st.Put(ctx, records.PersonKey("q"), []byte("{"), store.Absent); err != nil {
		t.Fatal(err)
	}
	if n, _, err := sweepRemovedOrgProviders(ctx, rs, now, 10); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
}

// An org provider is refused past the most, and past the most removed ones; its record and a
// claim's are far below their largest (#6774, #6775).
func TestOrgProvidersAndDomainsRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	put := func(key string, v any) {
		t.Helper()
		data, err := records.Encode(v)
		must(t, err)
		_, err = st.Put(ctx, key, data, store.Always)
		must(t, err)
	}
	at := records.Now()
	for n := range mostRemovedOrgProviders {
		id := fmt.Sprintf("gone-%d", n)
		if n == mostRemovedOrgProviders-1 {
			must(t, p.s.signIns.orgProviderRoom(ctx))
		}
		put(orgProviderKey(id), orgProviderStored{OrgProvider: wire.OrgProvider{ID: id}, Removed: &at})
	}
	if err := p.s.signIns.orgProviderRoom(ctx); err == nil || !strings.Contains(err.Error(), "were removed") {
		t.Fatalf("%v", err)
	}
	put(orgProviderKey("live"), orgProviderStored{OrgProvider: wire.OrgProvider{ID: "live"}})
	claim := func(n int) error {
		_, err := p.s.signIns.claimDomain(ctx, wire.SignInDomainClaim{Domain: fmt.Sprintf("d%d.example.com", n), Provider: "live"}, wire.AuditActor{User: "a"})
		return err
	}
	for n := range mostDomains {
		must(t, claim(n))
	}
	if err := claim(mostDomains); err == nil || !strings.Contains(err.Error(), "20 domains at most") {
		t.Fatalf("%v", err)
	}
	_, err := p.s.signIns.claimDomain(ctx, wire.SignInDomainClaim{Domain: "d0.example.com", Provider: "live"}, wire.AuditActor{User: "a"})
	must(t, err)
	must(t, st.Delete(ctx, domainKey("d1.example.com")))
	_, err = p.s.signIns.claimDomain(ctx, wire.SignInDomainClaim{Domain: "big.example.com", Provider: "live"}, wire.AuditActor{User: strings.Repeat("u", domainMostBytes)})
	if large := (*records.TooLargeError)(nil); !errors.As(err, &large) {
		t.Fatalf("%v", err)
	}
}
