package agentsd

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #5976: a session started on a machine that is off, or deleted, held its running place
// for ever, and stop_session and archive_session on it failed.
func TestSessionsOnADownOrDeletedMachineHoldNoRunningPlace(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	title := "lead"
	dir := t.TempDir()
	a := seeded(t, d, dir, func(a *wire.Agent) {
		a.State, a.EndedReason, a.RuntimeID, a.Title = wire.AgentStateRunning, nil, "claude", &title
	})
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)

	var mu sync.Mutex
	hosts := []wire.AgentMachinesHost{{Name: "pc", State: "online"}, {Name: "machine-35", State: "online"}}
	d.SetControlAsk(func(_ context.Context, _ string, params any) (json.RawMessage, error) {
		call, _ := params.(wire.MachineWorkspaceCall)
		mu.Lock()
		defer mu.Unlock()
		switch call.Method {
		case "machines/forAgent":
			return json.Marshal(wire.AgentMachines{Providers: []wire.AgentMachinesProvider{}, Hosts: hosts})
		case "machines/sessionCall":
			var s wire.MachineSessionCall
			b, _ := json.Marshal(call.Params)
			_ = json.Unmarshal(b, &s)
			for _, h := range hosts {
				if h.Name == s.Host {
					return json.Marshal(wire.SessionCallReply{Said: "working"})
				}
			}
			return nil, &controllink.AskError{Code: wire.FailureNoSuchHost, Message: "No machine is called " + s.Host + "."}
		}
		return nil, &controllink.AskError{Code: -32010, Message: "Agent Loops can’t ask that of the control plane."}
	})
	set := func(h ...wire.AgentMachinesHost) {
		mu.Lock()
		hosts = h
		mu.Unlock()
		d.thereMachines.mu.Lock()
		d.thereMachines.at = d.thereMachines.at.AddDate(0, 0, -1) // the last list is stale
		d.thereMachines.mu.Unlock()
		d.countProject(dir)
	}
	const gone = wire.UUID("22222222-3333-4444-5555-666666666666")
	d.noteStartedThere(startedThere{Machine: "pc", Session: there, Starter: a.ID, Folder: dir, There: "/home/me/app", Title: "on pc", At: now()})
	d.noteStartedThere(startedThere{Machine: "machine-35", Session: gone, Starter: a.ID, Folder: dir, There: "/workspaces/app", Title: "on a codespace", At: now()})

	d.countProject(dir)
	if p := d.helperPlaces(dir); p != "2 of 3 running, 2 of 5 not archived" {
		t.Fatal(p)
	}

	// pc goes offline: its session is not running, but still not archived.
	set(wire.AgentMachinesHost{Name: "pc", State: "offline"}, wire.AgentMachinesHost{Name: "machine-35", State: "online"})
	if p := d.helperPlaces(dir); p != "1 of 3 running, 2 of 5 not archived" {
		t.Fatal(p)
	}

	// machine-35 is deleted: its session has ended with it.
	set(wire.AgentMachinesHost{Name: "pc", State: "offline"})
	if p := d.helperPlaces(dir); p != "0 of 3 running, 2 of 5 not archived" {
		t.Fatal(p)
	}
	if text, _ := m.tool(2, "list_sessions", map[string]any{"started_by": "me"}); !strings.Contains(text, "“on a codespace” on machine-35, in /workspaces/app — gone") {
		t.Fatal(text)
	}
	if text, isErr := m.tool(3, "stop_session", map[string]any{"id": string(gone)}); isErr || !strings.Contains(text, "Nothing to stop: machine-35 is not there any more") {
		t.Fatal(text)
	}
	if text, isErr := m.tool(4, "archive_session", map[string]any{"id": string(gone)}); isErr || !strings.HasPrefix(text, "Archived: machine-35 is not there any more") {
		t.Fatal(text)
	}
	if p := d.helperPlaces(dir); p != "0 of 3 running, 1 of 5 not archived" {
		t.Fatal(p)
	}

	// pc comes back: its session runs again.
	set(wire.AgentMachinesHost{Name: "pc", State: "online"})
	if p := d.helperPlaces(dir); p != "1 of 3 running, 1 of 5 not archived" {
		t.Fatal(p)
	}
}
