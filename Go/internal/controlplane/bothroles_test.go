package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A team's control plane refuses a host on its own machine (#7305), and keeps the code.
func TestATeamsControlMachineTakesNoAgents(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	_, owner := p.client("Window")
	p.invite(owner, "Sam", wire.UserRoleMember)
	code := p.code(true)
	_, err := controllink.Enroll(context.Background(), t.TempDir(), code, controllink.NewPrivateKey(), fakeHello("MACHINE-1"))
	if err == nil || !strings.Contains(err.Error(), machine.BothRolesRefused) {
		t.Fatalf("%v", err)
	}
	if h := p.s.records.Host(p.s.controlMachineID()); h == nil || !h.ControlOnly() {
		t.Fatalf("%+v", h)
	}
	// Another machine takes the code.
	if _, err := controllink.Enroll(context.Background(), t.TempDir(), code, controllink.NewPrivateKey(), fakeHello("MACHINE-2")); err != nil {
		t.Fatal(err)
	}
}

// A machine that another copy runs on is refused too: its record has the control role.
func TestATeamsOtherCopysMachineTakesNoAgents(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	_, owner := p.client("Window")
	p.invite(owner, "Sam", wire.UserRoleMember)
	mid := "MACHINE-9"
	must(t, p.s.records.AddHost(context.Background(), records.MachineRecord{ID: "other", Name: "other", MachineID: &mid, Roles: []string{records.RoleControl}}))
	if err := p.s.refuseBothRoles(mid, nil); err == nil {
		t.Fatal("not refused")
	}
}

// An approval is refused as a code is, and the request stays open.
func TestATeamsControlMachineIsNotApproved(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	_, owner := p.client("Window")
	p.invite(owner, "Sam", wire.UserRoleMember)
	mid := "MACHINE-1"
	ticket, err := p.s.joins.ask(context.Background(), wire.MachineJoinAsk{Name: "box", Platform: "Linux", PublicKey: mustPublic(t), MachineID: &mid}, "1.2.3.4")
	must(t, err)
	if e := callError(t, owner, "", "machines/approveJoin", wire.JoinRequestDecide{Request: ticket.Request}); !strings.Contains(e, machine.BothRolesRefused) {
		t.Fatal(e)
	}
	var waiting []wire.JoinRequest
	must(t, json.Unmarshal(call(t, owner, "", "machines/joinRequests", nil), &waiting))
	if len(waiting) != 1 {
		t.Fatalf("%+v", waiting)
	}
}

// A host that joined as another machine is refused when it says it is on this one; one that
// had joined from it before the team stays, and so does its replayed join.
func TestATeamsControlMachineHellos(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	_, owner := p.client("Window")
	p.invite(owner, "Sam", wire.UserRoleMember)
	here, there := controlHostBeforeTheRule(p), p.host("MACHINE-2")
	params, _ := json.Marshal(fakeHello("MACHINE-1"))
	if _, err := p.s.methods.HostSaid(there.member.Host, "machine/hello", params); err == nil || !strings.Contains(err.Error(), machine.BothRolesRefused) {
		t.Fatalf("%v", err)
	}
	if h := p.s.records.Host(there.member.Host); h.MachineID == nil || *h.MachineID != "MACHINE-2" {
		t.Fatalf("%+v", h)
	}
	if _, err := p.s.methods.HostSaid(here, "machine/hello", params); err != nil {
		t.Fatal(err)
	}
	key := p.s.records.Host(here).PublicKey
	if err := p.s.refuseBothRoles("MACHINE-1", key); err != nil {
		t.Fatal(err)
	}
}

// One person with a bot is single-user: both roles are allowed.
func TestABotIsNoSecondPerson(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	p.client("Window")
	must(t, p.s.records.SavePerson(context.Background(), records.Person{ID: records.NewPersonID(), Name: "Bot", Kind: records.KindBot, Role: records.RoleMember}))
	if p.s.records.SeveralPeople() {
		t.Fatal("a bot is counted")
	}
	p.host("MACHINE-1")
}

// controlHostBeforeTheRule makes the plane's machine a set-up from before #7305: it runs agents too.
func controlHostBeforeTheRule(p *plane) string {
	id := p.s.controlMachineID()
	must(p.t, p.s.records.ChangeHost(context.Background(), id, func(h *records.MachineRecord) bool {
		h.Roles, h.PublicKey = []string{records.RoleAgents, records.RoleControl}, mustPublic(p.t)
		return true
	}))
	return id
}

// A control plane whose machine runs agents takes no second person, by any way in, and takes
// one when the agents are gone; a bot is no person.
func TestBothRolesTakeNoSecondPerson(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	_, owner := p.client("Window")
	// An invite from before the host joined.
	var invited wire.PersonInvited
	must(t, json.Unmarshal(call(t, owner, "", "people/invite", wire.PersonInvite{Name: "Sam", Role: wire.UserRoleMember}), &invited))
	early, err := controllink.ParseCode(invited.Text)
	must(t, err)
	box := p.host("MACHINE-1")
	if e := callError(t, owner, "", "people/invite", wire.PersonInvite{Name: "Kim", Role: wire.UserRoleMember}); !strings.Contains(e, machine.SecondPersonRefused) {
		t.Fatal(e)
	}
	if _, err := controllink.PairClient(context.Background(), early, "Sam's Mac"); err == nil || !strings.Contains(err.Error(), machine.SecondPersonRefused) {
		t.Fatalf("%v", err)
	}
	ctx := context.Background()
	// A sign-in and SCIM both make a person by this write.
	if err := p.s.records.SavePerson(ctx, records.Person{ID: records.NewPersonID(), Name: "Kim", Kind: records.KindPerson, Role: records.RoleMember}); err == nil {
		t.Fatal("a second person was kept")
	}
	must(t, p.s.records.SavePerson(ctx, records.Person{ID: records.NewPersonID(), Name: "Bot", Kind: records.KindBot, Role: records.RoleMember}))
	call(t, owner, "", "machines/remove", map[string]any{"host": box.member.Host})
	if _, err := controllink.PairClient(ctx, early, "Sam's Mac"); err != nil {
		t.Fatal(err)
	}
}

// A team with both roles from before the rule keeps them, takes a third person, and is
// warned of at each start.
func TestATeamWithBothRolesStays(t *testing.T) {
	t.Parallel()
	st, key := store.NewMemory(), controllink.NewPrivateKey()
	p := startPlane(t, st, key)
	_, owner := p.client("Window")
	p.invite(owner, "Sam", wire.UserRoleMember)
	id := controlHostBeforeTheRule(p)
	p.invite(owner, "Kim", wire.UserRoleMember)
	p.s.Stop()
	again := startPlane(t, st, key)
	if h := again.s.records.Host(id); h == nil || !h.HasRole(records.RoleAgents) || !h.HasRole(records.RoleControl) {
		t.Fatalf("%+v", h)
	}
	for {
		select {
		case l := <-again.lines:
			if strings.Contains(l, machine.BothRolesWarning) {
				return
			}
		default:
			t.Fatal("no warning at the start")
		}
	}
}
