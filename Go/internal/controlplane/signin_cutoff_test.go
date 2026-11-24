package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/signin"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/signin/signintest"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// ageOutTombstone removes the person, checks that the sign-in is refused, then sweeps past
// the 90 days and checks that no record of them stays.
func ageOutTombstone(t *testing.T, p *plane, person string, refused func() error) {
	t.Helper()
	ctx := context.Background()
	must(t, p.s.records.ChangePeople(ctx, func(all map[string]records.Person) ([]records.Person, error) {
		return []records.Person{all[person].Tombstone(records.Now())}, nil
	}))
	if err := refused(); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("with the tombstone: %v", err)
	}
	if _, err := sweep(ctx, p.s.records, time.Now().Add(removedSignInsKept+time.Hour), mostPerSweep, nil); err != nil {
		t.Fatal(err)
	}
	if o, err := p.s.cfg.Store.Get(ctx, records.PersonKey(person)); o != nil || err != nil {
		t.Fatalf("the tombstone stayed: %v", err)
	}
}

// #6756: an allowed domain does not take a removed person back when their tombstone goes.
func TestARemovedGoogleSignInStaysRefusedAfterItsTombstone(t *testing.T) {
	t.Parallel()
	f := newProvidersFake(t)
	p, _, owner := signInPlane(t, f)
	call(t, owner, "", "signIn/configure", wire.SignInConfigure{Provider: signin.Google, Domains: []string{"example.com"}})
	claims := signintest.Claims{"hd": "example.com", "email": "kim@example.com", "email_verified": true}
	signIn := func() error {
		_, err := p.signInAs(signin.Google, func(n string) string { return f.mint(signin.Google, "kim", n, claims) }, nil)
		return err
	}
	member, err := p.signInAs(signin.Google, func(n string) string { return f.mint(signin.Google, "kim", n, claims) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Someone else who is here already still signs in afterwards.
	leeToken := func(n string) string {
		return f.mint(signin.Google, "lee", n, signintest.Claims{"hd": "example.com", "email": "lee@example.com", "email_verified": true})
	}
	if _, err := p.signInAs(signin.Google, leeToken, nil); err != nil {
		t.Fatal(err)
	}
	ageOutTombstone(t, p, personOf(t, p, member), signIn)
	if err := signIn(); err == nil || !strings.Contains(err.Error(), "invite") {
		t.Fatalf("the old sign-in: %v", err)
	}
	if _, err := p.signInAs(signin.Google, leeToken, nil); err != nil {
		t.Fatal(err)
	}
}

// #6756: nor does an org provider that anyone may join by, also when SCIM makes them again;
// removing the provider takes its cutoff.
func TestARemovedOrgSignInStaysRefusedAfterItsTombstone(t *testing.T) {
	t.Parallel()
	prov := signintest.NewProvider(t, "RS256", "")
	p, _, owner, _ := orgPlane(t, prov, nil)
	addOrg(t, owner, prov, wire.OrgProviderJoinAnyone)
	b := &browser{prov: prov}
	prov.SignInAs(signintest.Claims{"sub": "00u-kim", "email": "kim@acme.test"})
	member, err := p.signInOrg(t, b)
	if err != nil {
		t.Fatal(err)
	}
	signIn := func() error { _, err := p.signInOrg(t, b); return err }
	ageOutTombstone(t, p, personOf(t, p, member), signIn)
	if err := signIn(); err == nil || !strings.Contains(err.Error(), "invite") {
		t.Fatalf("the old sign-in: %v", err)
	}
	ctx := context.Background()
	kept, err := p.s.signIns.readOrgProvider(ctx, "acme-okta")
	if err != nil {
		t.Fatal(err)
	}
	next := kept.p
	next.Scim = &wire.OrgProviderSCIM{On: true}
	data, err := records.Encode(orgProviderStored{OrgProvider: next})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.cfg.Store.Put(ctx, orgProviderKey("acme-okta"), data, store.Always); err != nil {
		t.Fatal(err)
	}
	must(t, p.s.records.SavePerson(ctx, records.Person{ID: "u-again", Name: "Kim", Kind: records.KindPerson, Role: records.RoleMember,
		Provisioned: true, SCIM: &records.PersonSCIM{Provider: "acme-okta", UserName: "kim@acme.test", ExternalID: "00u-kim"}}))
	if err := signIn(); err == nil || !strings.Contains(err.Error(), "invite") {
		t.Fatalf("the old sign-in, made again by SCIM: %v", err)
	}
	call(t, owner, "", "signIn/orgProviders/remove", wire.OrgProviderRequest{ID: "acme-okta"})
	if o, err := p.s.cfg.Store.Get(ctx, joinCutoffKey("acme-okta")); o != nil || err != nil {
		t.Fatalf("the cutoff outlived its provider: %v", err)
	}
}

// #6756: a local owner with no record is an admin, so a removed one's tombstone stays.
func TestARemovedLocalOwnersTombstoneStays(t *testing.T) {
	t.Parallel()
	prov := signintest.NewProvider(t, "RS256", "")
	p, _, _, _ := orgPlane(t, prov, nil)
	ctx := context.Background()
	id := p.s.localOwner()
	must(t, p.s.records.ChangePeople(ctx, func(all map[string]records.Person) ([]records.Person, error) {
		return []records.Person{all[id].Tombstone(records.Now())}, nil
	}))
	if _, err := sweep(ctx, p.s.records, time.Now().Add(removedSignInsKept+time.Hour), mostPerSweep, nil); err != nil {
		t.Fatal(err)
	}
	if p.s.records.Person(id) == nil || p.s.isAdmin(id) {
		t.Fatal("the removed local owner is an admin again")
	}
}
