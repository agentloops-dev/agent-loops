package agentsd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A policy over the largest applies, as its kill switch must, and is not kept; with the version
// seen, the machine starts no work after a restart (#6751).
func TestAPolicyOverTheLargestAppliesAndIsNotKept(t *testing.T) {
	t.Parallel()

	k := newControlKeys(t)
	root := t.TempDir()
	d, _ := joined(t, root, k)
	d.Told(wire.NotifyMachinePolicy.Name, k.signed(t, 1, map[string]any{"permissionMode": "plan"}))
	d.Told(wire.NotifyMachinePolicy.Name, k.signed(t, 2, map[string]any{"later": strings.Repeat("x", orgPolicyMostBytes)}))
	if p := d.orgPolicy(); p == nil || p.Version != 2 {
		t.Fatalf("not applied: %+v", p)
	}
	if b, err := os.ReadFile(d.orgPath(orgPolicyFile)); err != nil || len(b) > orgPolicyMostBytes {
		t.Fatalf("kept %d bytes: %v", len(b), err)
	}
	d.Shutdown()
	d2, _ := joined(t, root, k)
	if d2.orgPolicy() != nil || d2.workRefusal() == nil {
		t.Fatal("the older policy came back")
	}
	d2.Shutdown()
}

func TestTheControlPlanesCopiesAreBounded(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	u, err := json.Marshal(wire.MachineUser{User: "b1", Kind: "bot", Name: "b", Labels: []string{"do-1"}, Bots: []string{strings.Repeat("x", hostUserMostBytes)}})
	must(t, err)
	d.toldHostUser(u)
	var kept wire.MachineUser
	b, err := os.ReadFile(d.hostUserPath())
	must(t, err)
	must(t, json.Unmarshal(b, &kept))
	if kept.User != "b1" || kept.Kind != "bot" || len(kept.Labels) != 1 || kept.Bots != nil {
		t.Fatalf("kept %+v", kept)
	}
	d.heardProjects(wire.MachineProjects{Projects: []wire.ProjectTools{{ID: "p", Name: strings.Repeat("x", projectToolsMostBytes)}}})
	if fileExists(d.projectToolsPath()) {
		t.Fatal("project-tools.json is over its largest")
	}
}
