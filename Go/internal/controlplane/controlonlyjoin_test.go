package controlplane

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// controlOnlyHost enrols a machine with the control role alone (#7314) and links it.
func (p *plane) controlOnlyHost(machine string) *fakeHost {
	p.t.Helper()
	root, private, hello := p.t.TempDir(), controllink.NewPrivateKey(), fakeHello(machine)
	member, err := controllink.EnrollAs(context.Background(), root, p.code(true), private, hello, []string{records.RoleControl})
	if err != nil {
		p.t.Fatal(err)
	}
	return p.serveHost(root, member, private, hello, nil)
}

// A team's control plane enrols a machine with the control role alone, and keeps its link
// for what reads the machine itself; no client's channel opens to it, and no client's
// request reaches it (#7314).
func TestAControlOnlyMachineEnrolsWithATeam(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	p.asked = func(_, method string, _ json.RawMessage) (any, bool) {
		if method == wire.CallMachineUsage.Name {
			return goodUsage(), true
		}
		return nil, false
	}
	_, window := p.client("Window")
	p.invite(window, "Sam", wire.UserRoleMember)
	box := p.controlOnlyHost("MACHINE-7")
	host := box.member.Host
	h := p.s.records.Host(host)
	if h == nil || !slices.Equal(h.Roles, []string{records.RoleControl}) || !h.ControlOnly() || host == "mac" {
		t.Fatalf("%+v", h)
	}
	if slices.ContainsFunc(p.s.records.Hosts(), func(r records.MachineRecord) bool { return r.ID == host }) {
		t.Fatal("a control-only machine is listed as one that runs agents")
	}
	// Agent work is refused at the control plane, before the machine.
	for _, method := range []string{"agents/list", "sessions/start", "folders/add"} {
		if e := callError(t, window, host, method, map[string]any{}); !strings.Contains(e, "runs the control plane and no agents") {
			t.Fatalf("%s: %s", method, e)
		}
	}
	// A client that pairs after the machine linked opens no channel to it either.
	_, later := p.client("Later")
	if e := callError(t, later, host, "agents/list", nil); !strings.Contains(e, "no agents") {
		t.Fatal(e)
	}
	box.mu.Lock()
	opened := len(box.opened)
	box.mu.Unlock()
	if opened != 0 {
		t.Fatalf("%d channels opened to a control-only machine", opened)
	}
	// Its link answers what control/eligible reads (machine/usage): only the address is missing.
	var got wire.ControlEligible
	must(t, json.Unmarshal(call(t, window, "", "control/eligible", map[string]any{"machine": host}), &got))
	if len(got.Reasons) != 1 || got.Reasons[0].Reason != wire.ControlIneligibilityNotReachable {
		t.Fatalf("%+v", got)
	}
	// machines/list shows it to an admin with its one role.
	var rows []wire.ControlMachine
	must(t, json.Unmarshal(call(t, window, "", "machines/list", nil), &rows))
	if i := slices.IndexFunc(rows, func(r wire.ControlMachine) bool { return string(r.ID) == host }); i < 0 || !slices.Equal(rows[i].Roles, []string{records.RoleControl}) {
		t.Fatalf("%+v", rows)
	}
}

// A machine that runs this control plane already is not enrolled a second time with the
// control role alone: it has its record (#6609).
func TestTheControlMachineDoesNotJoinAsControlOnly(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	p.client("Window")
	code := p.code(true)
	_, err := controllink.EnrollAs(context.Background(), t.TempDir(), code, controllink.NewPrivateKey(), fakeHello(p.s.cfg.MachineID), []string{records.RoleControl})
	if err == nil || !strings.Contains(err.Error(), "runs this control plane already") {
		t.Fatalf("%v", err)
	}
	// The code is kept for another machine.
	if _, err := controllink.EnrollAs(context.Background(), t.TempDir(), code, controllink.NewPrivateKey(), fakeHello("MACHINE-8"), []string{records.RoleControl}); err != nil {
		t.Fatal(err)
	}
}
