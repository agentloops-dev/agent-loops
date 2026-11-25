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
	for _, method := range []string{"machines/deletePreview", "machines/retrySetup"} {
		if _, err := window.Call(context.Background(), "", method, map[string]any{"host": id}); err == nil ||
			!strings.Contains(err.Error(), "runs the control plane and no agents") {
			t.Errorf("%s: %v", method, err)
		}
	}
	// It has no host to update, no runtime or git to lend or be lent, and runs no work a
	// label picks it for (#7310).
	for method, params := range map[string]any{
		"machines/update":     wire.MachineUpdate{Host: wire.MachineRef(id), Destination: "box"},
		"machines/lendSignIn": wire.LendSignIn{Host: wire.MachineRef(id), From: wire.MachineRef(id), Runtime: "claude", Allowed: true},
		"machines/lendGit":    wire.LendGit{Host: wire.MachineRef(id), From: wire.MachineRef(id), Server: "github.com", Allowed: true},
		"machines/setLabels":  wire.MachineLabelsRequest{Machines: []wire.MachineRef{wire.MachineRef(id)}, Add: []string{"gpu"}},
	} {
		if _, err := window.Call(context.Background(), "", method, params); err == nil ||
			!strings.Contains(err.Error(), "runs the control plane and no agents") {
			t.Errorf("%s: %v", method, err)
		}
	}
	if h := p.s.records.Host(id); h == nil || len(h.Labels) > 0 || len(h.SignInFrom) > 0 || len(h.GitFrom) > 0 {
		t.Fatalf("a refused call changed the record: %+v", h)
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

// A Linux control plane's record has the id control, and a host that joins from its machine
// gets the id mac: the host's record gets both roles and the other is dropped (#7304).
func TestAHostTakesOverALinuxControlMachine(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	mid := "MACHINE-1"
	must(t, records.New(st).AddHost(context.Background(), records.MachineRecord{ID: "control", Name: "Box",
		Platform: "Linux", MachineID: &mid, Roles: []string{records.RoleControl}}))
	p := startPlane(t, st, controllink.NewPrivateKey())
	if got := p.s.controlMachineID(); got != "control" {
		t.Fatalf("the copy names %q before the join", got)
	}
	box := p.host(mid)
	all := p.s.records.Machines()
	if len(all) != 1 || all[0].ID != "mac" || box.member.Host != "mac" ||
		!slices.Equal(all[0].Roles, []string{records.RoleAgents, records.RoleControl}) || len(all[0].PublicKey) == 0 {
		t.Fatalf("%+v, joined as %s", all, box.member.Host)
	}
	if got := p.s.controlMachineID(); got != "mac" {
		t.Fatalf("the copy names %q", got)
	}
}
