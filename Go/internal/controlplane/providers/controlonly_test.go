package providers

import (
	"context"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A machine that only runs the control plane is refused by every provider action that names
// it, from a client and from an agent, and is left as it was (#7304).
func TestAControlOnlyMachineIsRefusedEveryProviderAction(t *testing.T) {
	const control = "control"
	h := newHarness(t)
	id := h.add()
	h.providers.hooks.OwnerOf = func(string) string { return h.caller.Person }
	must(t, h.records.SaveHost(context.Background(),
		records.MachineRecord{ID: control, Name: "Box", Roles: []string{records.RoleControl}}))
	h.router.setOnline(control, true)
	refused := func(what string, err error) {
		t.Helper()
		if f := failureOf(err); f == nil || !strings.Contains(f.Message, "runs the control plane and no agents") {
			t.Errorf("%s: %v", what, err)
		}
	}
	for method, params := range map[string]map[string]any{
		MethodHostsLink:       {"provider": id, "workspace": "agents-devbox"},
		MethodHostsUnlink:     {},
		MethodHostsSetOptions: {"startWhenUsed": true},
		MethodHostsStart:      {},
		MethodHostsStop:       {},
		MethodHostsDelete:     {},
		MethodHostsKeep:       {"method": "agents/prompt", "params": map[string]any{}},
	} {
		params["host"] = control
		_, err := h.call(method, params)
		refused(method, err)
	}
	for _, method := range []string{MethodHostsStart, MethodHostsStop, MethodHostsDelete, MethodSessionOnMachine,
		MethodSessionCall, MethodSendFolder, MethodBotKey, MethodProjectFolders, MethodProjectSessions} {
		_, err := h.providers.AgentCall(devbox, "claude", method, rawOf(map[string]any{"host": control}), nil)
		refused("an agent's "+method, err)
	}
	machine := wire.MachineRef(control)
	_, err := h.providers.carrier(ProviderAdd{Machine: &machine}, nil, h.caller)
	refused("carry a provider", err)
	if r := h.records.Host(control); r == nil || LinkOf(r) != nil || !r.ControlOnly() {
		t.Fatalf("%+v", r)
	}
}
