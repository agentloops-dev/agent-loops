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
	here, there := p.host("MACHINE-1"), p.host("MACHINE-2")
	must(t, p.s.records.SavePerson(context.Background(), records.Person{ID: records.NewPersonID(), Name: "Sam", Kind: records.KindPerson, Role: records.RoleMember}))
	_ = owner
	params, _ := json.Marshal(fakeHello("MACHINE-1"))
	if _, err := p.s.methods.HostSaid(there.member.Host, "machine/hello", params); err == nil || !strings.Contains(err.Error(), machine.BothRolesRefused) {
		t.Fatalf("%v", err)
	}
	if h := p.s.records.Host(there.member.Host); h.MachineID == nil || *h.MachineID != "MACHINE-2" {
		t.Fatalf("%+v", h)
	}
	if _, err := p.s.methods.HostSaid(here.member.Host, "machine/hello", params); err != nil {
		t.Fatal(err)
	}
	key := p.s.records.Host(here.member.Host).PublicKey
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
