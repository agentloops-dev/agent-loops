package controlplane

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestRolesOfARecord(t *testing.T) {
	t.Parallel()
	if got := (records.MachineRecord{}); !got.HasRole(records.RoleAgents) || got.HasRole(records.RoleControl) || got.ControlOnly() {
		t.Fatal("a record with no roles runs agents only")
	}
	only := records.MachineRecord{ID: "c", Roles: []string{records.RoleControl}}
	if !only.ControlOnly() || only.HasRole(records.RoleAgents) {
		t.Fatal("control only")
	}
	data, err := json.Marshal(records.MachineRecord{ID: "a", Roles: []string{"agents"}})
	if err != nil || string(data) == "" {
		t.Fatal(err)
	}
	var back records.MachineRecord
	if err := json.Unmarshal(data, &back); err != nil || len(back.Roles) != 0 {
		t.Fatalf("the default is not written: %s", data)
	}
	data, _ = json.Marshal(records.MachineRecord{ID: "b", Roles: []string{"control", "agents", "junk"}})
	if err := json.Unmarshal(data, &back); err != nil || !slices.Equal(back.Roles, []string{"agents", "control"}) {
		t.Fatalf("%s %v", data, back.Roles)
	}
}

// The control plane writes a machine record for its own machine (#6609), shows it to an
// admin, names it in its copy record, and refuses to remove it.
func TestTheControlPlaneIsAMachine(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, window := p.client("Window")
	var list []wire.ControlMachine
	must(t, json.Unmarshal(call(t, window, "", "machines/list", nil), &list))
	if len(list) != 1 || !slices.Equal(list[0].Roles, []string{"control"}) || list[0].MachineID == nil || *list[0].MachineID != "MACHINE-1" {
		t.Fatalf("%+v", list)
	}
	id := string(list[0].ID)
	if p.s.controlMachineID() != id {
		t.Fatalf("copy names %q, record is %q", p.s.controlMachineID(), id)
	}
	// Nothing dials in with it, so no host runs on it.
	if len(p.s.records.Hosts()) != 0 {
		t.Fatal("a control-only machine is no host")
	}
	if _, err := window.Call(context.Background(), "", "machines/remove", map[string]any{"host": id}); err == nil {
		t.Fatal("a machine with only the control role was removed")
	}
	// Another start finds it and writes no second one.
	must(t, p.s.ensureControlMachine(context.Background()))
	if n := len(p.s.records.Machines()); n != 1 {
		t.Fatalf("%d machine records", n)
	}
}

// A host that joins from the control plane's machine takes its record over: one machine, both roles.
func TestAHostOnTheControlMachineHasBothRoles(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	box := p.host("MACHINE-1")
	_, window := p.client("Window")
	var list []wire.ControlMachine
	must(t, json.Unmarshal(call(t, window, "", "machines/list", nil), &list))
	if len(list) != 1 || string(list[0].ID) != box.member.Host || !slices.Equal(list[0].Roles, []string{"agents", "control"}) {
		t.Fatalf("%+v", list)
	}
	if p.s.controlMachineID() != box.member.Host {
		t.Fatal("the copy names another record")
	}
}
