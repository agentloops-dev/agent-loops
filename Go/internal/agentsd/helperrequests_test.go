package agentsd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7064: a helper on its starter's own named runtime is answered as its starter is.

// requestsFixture is a daemon whose Settings always approve on every runtime the app answers for.
type requestsFixture struct {
	t    *testing.T
	d    *Daemon
	c    *conn
	dir  string
	call int
}

func newRequestsFixture(t *testing.T) *requestsFixture {
	t.Helper()
	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	t.Cleanup(d.Shutdown)
	d.SettingsDir = t.TempDir()
	must(t, os.WriteFile(filepath.Join(d.SettingsDir, "client-permissions.json"),
		[]byte(`{"cursor":"alwaysApprove","grok":"alwaysApprove","opencode":"alwaysApprove"}`), 0o600))
	return &requestsFixture{t: t, d: d, c: c, dir: folder(t, c, 1), call: 10}
}

// lead is a person's own session on the runtime, in mode when it has one.
func (f *requestsFixture) lead(runtime, mode string, edit func(a *wire.Agent)) wire.Agent {
	f.t.Helper()
	return seeded(f.t, f.d, f.dir, func(a *wire.Agent) {
		a.RuntimeID = runtime
		if mode != "" {
			a.AdvertisedOptions = []wire.ConfigOption{{ID: "mode", Name: "Mode", Type: "select", CurrentValue: jsonString(mode)}}
		}
		if edit != nil {
			edit(a)
		}
	})
}

// startSession is start_session by lead, and the id it gave.
func (f *requestsFixture) startSession(lead wire.Agent, args map[string]any) wire.UUID {
	f.t.Helper()
	m := appClient(f.t, f.d, lead.ID)
	m.post("initialize", map[string]any{}, 1)
	f.call++
	args["prompt"] = "x"
	text, isErr := m.tool(f.call, "start_session", args)
	if isErr {
		f.t.Fatal(text)
	}
	return helperID(text)
}

func (f *requestsFixture) approves(id wire.UUID) bool {
	f.t.Helper()
	a := f.d.Store.Agent(id)
	if a == nil {
		f.t.Fatal("no session ", id)
	}
	return f.d.agentAlwaysApproves(a)
}

func asking(a *wire.Agent) { a.ClientPermission = requestCards }

func TestAHelperOnItsStartersRuntimeInheritsAlwaysApprove(t *testing.T) {
	t.Parallel()

	for _, runtime := range []string{"opencode", "cursor", "grok"} {
		t.Run(runtime, func(t *testing.T) {
			t.Parallel()

			f := newRequestsFixture(t)
			// Settings' answer, in a native mode that Ask and Approve for me share on OpenCode.
			if id := f.startSession(f.lead(runtime, "build", nil), map[string]any{}); !f.approves(id) {
				t.Fatal("the helper asks")
			}
			// With no mode told, as Grok.
			if id := f.startSession(f.lead(runtime, "", nil), map[string]any{"runtime": runtime}); !f.approves(id) {
				t.Fatal("with no mode: the helper asks")
			}
			// A starter that asks, though Settings approve.
			if id := f.startSession(f.lead(runtime, "build", asking), map[string]any{}); f.approves(id) {
				t.Fatal("looser than its starter")
			}
		})
	}
	t.Run("a workflow's session", func(t *testing.T) {
		t.Parallel()

		// Settings never answer for it; its own Always Approve does.
		f := newRequestsFixture(t)
		wf := func(p *wire.ClientPermissionMode) func(*wire.Agent) {
			return func(a *wire.Agent) { a.StartedByWorkflow, a.ClientPermission = new("wf"), p }
		}
		if id := f.startSession(f.lead("opencode", "build", wf(yesToAll)), map[string]any{}); !f.approves(id) {
			t.Fatal("the starter's own Always Approve")
		}
		if id := f.startSession(f.lead("opencode", "build", wf(nil)), map[string]any{}); f.approves(id) {
			t.Fatal("it asks, and so does its helper")
		}
	})
	t.Run("a helper's helper", func(t *testing.T) {
		t.Parallel()

		f := newRequestsFixture(t)
		first := f.startSession(f.lead("opencode", "", nil), map[string]any{})
		second := f.startSession(*f.d.Store.Agent(first), map[string]any{})
		if !f.approves(second) {
			t.Fatal("it asks")
		}
		// Its own later choice.
		f.c.call(t, 3, "agents/setClientPermission", map[string]any{"agentID": second, "mode": "default"})
		if f.approves(second) {
			t.Fatal("Request cards on the helper")
		}
	})
}

func TestAStricterModeNamedForAHelperStillAsks(t *testing.T) {
	t.Parallel()

	f := newRequestsFixture(t)
	lead := f.lead("cursor", "agent", nil)
	for _, mode := range []string{"plan", "ask"} {
		id := f.startSession(lead, map[string]any{"permission_mode": mode})
		if f.approves(id) || stringValue(f.d.Store.Agent(id).StartOptions.Values["mode"]) != mode {
			t.Fatalf("%s named: %+v", mode, f.d.Store.Agent(id))
		}
	}
	// The starter's own mode, named, is no stricter.
	if id := f.startSession(lead, map[string]any{"permission_mode": "agent"}); !f.approves(id) {
		t.Fatal("the starter's own mode, named")
	}
}

func TestAlwaysApproveIsNotInheritedElsewhere(t *testing.T) {
	t.Parallel()

	f := newRequestsFixture(t)
	// Another runtime.
	if id := f.startSession(f.lead("cursor", "agent", nil), map[string]any{"runtime": "opencode"}); f.approves(id) {
		t.Fatal("another runtime")
	}
	// A workflow's start: no starter. The run marks its session as the workflow's after it starts.
	id, err := f.d.startFor(helperRequest("opencode", f.dir, "x", "", nil, nil, nil), startedBy{owner: wire.SessionLabelOwnerAgent})
	must(t, err)
	f.d.change(id, func(a *wire.Agent) { a.StartedByWorkflow = new("wf") })
	if f.approves(id) {
		t.Fatal("a workflow's start")
	}
	// The start's own answer wins, and a starter whose record is gone gives none.
	lead := f.lead("opencode", "", nil)
	r := helperRequest("opencode", f.dir, "x", "", nil, nil, nil)
	r.Levels = &wire.Levels{PermissionRequests: requestCards}
	id, err = f.d.startFor(r, startedBy{agent: &lead.ID, owner: wire.SessionLabelOwnerAgent})
	if err != nil || f.approves(id) {
		t.Fatal("the start's own Request cards", err)
	}
	gone := store.NewUUID()
	id, err = f.d.startFor(helperRequest("opencode", f.dir, "x", "", nil, nil, nil), startedBy{agent: &gone, owner: wire.SessionLabelOwnerAgent})
	if err != nil || f.approves(id) {
		t.Fatal("a starter that is gone", err)
	}
}

func TestAQueuedHelperInheritsWhenItStarts(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		change func(f *requestsFixture, lead, queued wire.UUID)
		want   bool
	}{
		"as it was": {nil, true},
		"the starter asks now": {func(f *requestsFixture, lead, _ wire.UUID) {
			f.d.change(lead, asking)
		}, false},
		"the starter plans now": {func(f *requestsFixture, lead, _ wire.UUID) {
			f.d.change(lead, func(a *wire.Agent) { a.AdvertisedOptions[0].CurrentValue = jsonString("plan") })
		}, false},
		"the helper has Request cards": {func(f *requestsFixture, _, queued wire.UUID) {
			f.c.call(f.t, 5, "agents/setClientPermission", map[string]any{"agentID": queued, "mode": "default"})
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newRequestsFixture(t)
			f.c.call(t, 2, "policy/setFolder", map[string]any{"folder": "file://" + f.dir + "/", "rules": map[string]any{"helpers": map[string]any{"mostRunning": 1}}})
			lead := f.lead("opencode", "build", nil)
			busy := seeded(t, f.d, f.dir, func(a *wire.Agent) { a.State = wire.AgentStateRunning; a.StartedByAgent = &lead.ID })
			queued := f.startSession(lead, map[string]any{})
			if f.d.Store.Agent(queued).State != wire.AgentStateQueued {
				t.Fatal("not queued")
			}
			if c.change != nil {
				c.change(f, lead.ID, queued)
			}
			f.d.change(busy.ID, func(a *wire.Agent) { a.State = wire.AgentStateFinished })
			f.d.startFromQueue(f.dir)
			waitFor(t, "the queued helper started", func() bool { return f.d.Store.Agent(queued).State != wire.AgentStateQueued })
			if f.approves(queued) != c.want {
				t.Fatalf("approves: %v", !c.want)
			}
		})
	}
}

// A queue pass takes its list, then Request cards is chosen, then the session starts.
func TestAQueuedHelpersChoiceAfterThePassTookItsListIsKept(t *testing.T) {
	t.Parallel()

	f := newRequestsFixture(t)
	f.c.call(t, 2, "policy/setFolder", map[string]any{"folder": "file://" + f.dir + "/", "rules": map[string]any{"helpers": map[string]any{"mostRunning": 1}}})
	lead := f.lead("opencode", "build", nil)
	seeded(t, f.d, f.dir, func(a *wire.Agent) { a.State = wire.AgentStateRunning; a.StartedByAgent = &lead.ID })
	queued := f.startSession(lead, map[string]any{})
	listed := *f.d.Store.Agent(queued)
	f.c.call(t, 5, "agents/setClientPermission", map[string]any{"agentID": queued, "mode": "default"})
	f.d.startQueued(listed, f.dir)
	if f.d.Store.Agent(queued).State == wire.AgentStateQueued || f.approves(queued) {
		t.Fatalf("%+v", f.d.Store.Agent(queued))
	}
}

func TestInheritedAlwaysApproveIsUnderThePolicies(t *testing.T) {
	t.Parallel()

	t.Run("yours", func(t *testing.T) {
		t.Parallel()

		f := newRequestsFixture(t)
		lead := f.lead("opencode", "build", nil)
		before := f.startSession(lead, map[string]any{})
		setYourRules(t, f.d, wire.PolicyRules{PermissionRequests: requestCards})
		if after := f.startSession(lead, map[string]any{}); f.approves(before) || f.approves(after) {
			t.Fatal("above your policy")
		}
	})
	t.Run("the organisation's", func(t *testing.T) {
		t.Parallel()

		fakeRuntimes(t)
		k := newControlKeys(t)
		d, c := joined(t, t.TempDir(), k)
		must(t, os.MkdirAll(d.SettingsDir, 0o700))
		must(t, os.WriteFile(filepath.Join(d.SettingsDir, "client-permissions.json"), []byte(`{"opencode":"alwaysApprove"}`), 0o600))
		f := &requestsFixture{t: t, d: d, c: c, dir: t.TempDir(), call: 10}
		lead := f.lead("opencode", "", nil)
		begin := func() wire.UUID {
			id, err := d.startFor(helperRequest("opencode", f.dir, "x", "", nil, nil, nil), startedBy{agent: &lead.ID, owner: wire.SessionLabelOwnerAgent})
			must(t, err)
			return id
		}
		before := begin()
		if !f.approves(before) {
			t.Fatal("with no ceiling")
		}
		d.Told(wire.NotifyMachinePolicy.Name, k.signed(t, 1, map[string]any{"permissionMode": "approveForMe"}))
		if f.approves(before) || f.approves(begin()) {
			t.Fatal("above the ceiling")
		}
	})
}

// A start on another machine takes the starter's answer along, as it takes the mode: its Settings
// answer for a session that has no starter there.
func TestAStartOnAnotherMachineCarriesAlwaysApprove(t *testing.T) {
	t.Parallel()

	f := newRequestsFixture(t)
	var sent []wire.StartRequest
	f.d.SetControlAsk(func(_ context.Context, _ string, params any) (json.RawMessage, error) {
		call, _ := params.(wire.MachineWorkspaceCall)
		if call.Method != "machines/startSession" {
			return nil, &controllink.AskError{Code: -32010, Message: "Not that."}
		}
		var s wire.MachineSessionStart
		b, _ := json.Marshal(call.Params)
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		sent = append(sent, s.Start)
		return jsonString(string(store.NewUUID())), nil
	})
	for _, c := range []struct {
		edit    func(*wire.Agent)
		runtime string
		mode    string
	}{{nil, "cursor", "agent"}, {asking, "cursor", "agent"}, {nil, "cursor", "plan"}, {nil, "opencode", "build"}} {
		lead := f.lead("cursor", "agent", c.edit)
		r := helperRequest(c.runtime, "", "x", "", &c.mode, nil, nil)
		if _, err := f.d.startThere(&lead, f.dir, "pc", "/home/me/app", "x", r, nil); err != nil {
			t.Fatal(err)
		}
	}
	answers := func(i int, want wire.ClientPermissionMode) bool {
		l := sent[i].Levels
		return l != nil && l.PermissionRequests != nil && *l.PermissionRequests == want
	}
	// The starter asks, or a stricter mode was named: it asks, whatever that machine's Settings
	// say. Another runtime is as it was.
	if len(sent) != 4 || !answers(0, wire.ClientPermissionModeAlwaysApprove) || !answers(1, wire.ClientPermissionModeDefault) ||
		!answers(2, wire.ClientPermissionModeDefault) || sent[3].Levels != nil {
		t.Fatalf("%+v", sent)
	}
}
