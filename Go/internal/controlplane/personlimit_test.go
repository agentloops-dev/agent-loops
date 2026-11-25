package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/signin/signintest"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7019: at the people's limit an invite, an automatic join and a SCIM user are refused,
// each with words; a person who is here is still suspended by SCIM, and an invite's code is
// not spent.
func TestNewPeopleAreRefusedAtTheLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, owner, prov, token, base := scimPlane(t, wire.OrgProviderJoinAnyone)
	_, made := scimDo(t, base, token, "POST", "/Users", newUser("raj@acme.test", "00u-raj"))
	raj, _ := made["id"].(string)
	var invited wire.PersonInvited
	must(t, json.Unmarshal(call(t, owner, "", "people/invite", wire.PersonInvite{Name: "Sam", Role: wire.UserRoleMember}), &invited))

	// To a few bytes of the limit, so nobody fits.
	var filler []string
	for size := 15 << 10; size > 8; {
		id := records.NewPersonID()
		err := p.s.records.SavePerson(ctx, records.Person{ID: id, Name: strings.Repeat("n", size)})
		var full *records.PersonLimitError
		switch {
		case err == nil:
			filler = append(filler, id)
		case errors.As(err, &full):
			size /= 2
		default:
			t.Fatal(err)
		}
	}

	const words = "keeps as many people as it can"
	if e := callError(t, owner, "", "people/invite", wire.PersonInvite{Name: "Eve", Role: wire.UserRoleMember}); !strings.Contains(e, words) {
		t.Fatal(e)
	}
	code, err := controllink.ParseCode(invited.Text)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controllink.PairClient(ctx, code, "Sam's Mac"); err == nil || !strings.Contains(err.Error(), words) {
		t.Fatalf("the invite: %v", err)
	}
	prov.SignInAs(signintest.Claims{"sub": "00u-kim", "email": "kim@acme.test"})
	if _, err := p.signInOrg(t, &browser{prov: prov}); err == nil || !strings.Contains(err.Error(), words) {
		t.Fatalf("an automatic join: %v", err)
	}
	status, out := scimDo(t, base, token, "POST", "/Users", newUser("lee@acme.test", "00u-lee"))
	if status != http.StatusForbidden || !strings.Contains(fmt.Sprint(out["detail"]), words) {
		t.Fatal(status, out)
	}
	// Larger and inactive in one write: the access goes, and the rest is not kept.
	larger := newUser("raj@acme.test", "00u-raj")
	larger["displayName"] = strings.Repeat("R", 200)
	if status, out := scimDo(t, base, token, "PUT", "/Users/"+raj, larger); status != http.StatusForbidden {
		t.Fatal(status, out)
	}
	larger["active"] = false
	if status, out := scimDo(t, base, token, "PUT", "/Users/"+raj, larger); status != http.StatusOK {
		t.Fatal(status, out)
	}
	if q := p.s.records.Person(raj); !q.IsSuspended() || q.Name != "raj" {
		t.Fatalf("not suspended: %+v", q)
	}
	// Once there is room, the same code works.
	call(t, owner, "", "people/remove", wire.PersonRequest{User: filler[0]})
	if _, err := controllink.PairClient(ctx, code, "Sam's Mac"); err != nil {
		t.Fatal(err)
	}
	if status, _ := scimDo(t, base, token, "POST", "/Users", newUser("lee@acme.test", "00u-lee")); status != http.StatusCreated {
		t.Fatal(status)
	}
	p.waitFor(func() bool { return storeHolds(t, p.store, "people/refusedFull") }, "the audit says the people are at their limit")
}
