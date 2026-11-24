package agentsd

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/flaky"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Ports WorkspaceToolsTests (#1444): an agent's workspace tools go to the control plane as
// machine/machines, named for its log; off a control plane they say there are none; making
// one asks the person first, and nothing is made without their yes.

func TestAnAgentsWorkspaceTools(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	title := "fix-login"
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.State, a.EndedReason, a.RuntimeID, a.Title = wire.AgentStateRunning, nil, "claude", &title
	})
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)

	if text, isErr := m.tool(2, "list_machines", map[string]any{}); !isErr || text != "This machine isn’t on a control plane, so it has no machines." {
		t.Fatal(text)
	}

	host := "devbox"
	listed := wire.AgentMachines{Providers: []wire.AgentMachinesProvider{{ID: "do", Name: "DigitalOcean", CanMake: true, CanDelete: true,
		Workspaces: []wire.AgentMachinesMachine{{ID: "w1", Name: "devbox", State: "running", Host: &host, HostState: new("online")}}}}}
	var mu sync.Mutex
	var asked []wire.MachineWorkspaceCall
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if method == wire.CallUserSetPolicy.Name {
			// The keeper's layer, saved the first time anything reads it (#2957).
			return json.RawMessage(`{}`), nil
		}
		call := params.(wire.MachineWorkspaceCall)
		mu.Lock()
		asked = append(asked, call)
		mu.Unlock()
		if method != "machine/machines" {
			t.Errorf("method %s", method)
		}
		switch call.Method {
		case "machines/forAgent":
			b, _ := json.Marshal(listed)
			return b, nil
		case "machines/start":
			return json.RawMessage(`{"state":"online"}`), nil
		case "providers/create":
			return json.RawMessage(`{"workspace":{"id":"w2","name":"second"},"joining":true}`), nil
		}
		return nil, &controllink.AskError{Code: -32010, Message: "Agent Loops can’t ask that of the control plane."}
	})

	text, isErr := m.tool(3, "list_machines", map[string]any{})
	if isErr || !strings.HasPrefix(text, "DigitalOcean (provider do): can make machines (make_machine), can delete them\n  - devbox: running — host devbox, online") {
		t.Fatal(text)
	}
	if text, _ := m.tool(4, "start_machine", map[string]any{"host": "devbox"}); text != "devbox is running already." {
		t.Fatal(text)
	}
	if text, isErr := m.tool(5, "stop_machine", map[string]any{"host": "devbox"}); !isErr || text != "Agent Loops can’t ask that of the control plane." {
		t.Fatal(text)
	}
	if text, isErr := m.tool(6, "make_machine", map[string]any{"provider": "Nobody", "name": "x"}); !isErr ||
		text != "Nothing was made: no provider is called Nobody. There are DigitalOcean." {
		t.Fatal(text)
	}
	mu.Lock()
	if len(asked) == 0 || asked[0].Agent != "Claude (fix-login)" {
		t.Fatalf("%+v", asked)
	}
	mu.Unlock()

	// Making one asks the person, on the agent's card, while it works.
	l := d.live(a.ID)
	l.mu.Lock()
	l.inTurn = true
	l.mu.Unlock()
	answered := make(chan string, 1)
	go func() {
		text, _ := m.tool(7, "make_machine", map[string]any{"provider": "do", "name": "second"})
		answered <- text
	}()
	var pending []wire.ElicitationRequest
	waitFor(t, "the card", func() bool {
		mustUnmarshal(t, c.call(t, 1, "elicitations/pending", nil), &pending)
		return len(pending) == 1
	})
	if f := pending[0].Mode.Form.Value; *pending[0].Message != "Claude (fix-login) wants to make a machine" ||
		*f.Properties[0].Title != "Make a new machine, second, at DigitalOcean?" {
		t.Fatalf("%+v", pending[0])
	}
	c.call(t, 2, "elicitations/answer", map[string]any{"requestID": pending[0].ID, "action": "accept", "content": map[string]any{"go": "yes"}})
	if text := <-answered; text != "Made second. It joins as the host second on its first boot: wait for server.online." {
		t.Fatal(text)
	}

	// With no name, the control plane names it machine-N (#3972).
	go func() { text, _ := m.tool(9, "make_machine", map[string]any{"provider": "do"}); answered <- text }()
	waitFor(t, "the nameless card", func() bool {
		mustUnmarshal(t, c.call(t, 5, "elicitations/pending", nil), &pending)
		return len(pending) == 1
	})
	if f := pending[0].Mode.Form.Value; *f.Properties[0].Title != "Make a new machine at DigitalOcean?" {
		t.Fatalf("%+v", pending[0])
	}
	c.call(t, 6, "elicitations/answer", map[string]any{"requestID": pending[0].ID, "action": "accept", "content": map[string]any{"go": "yes"}})
	<-answered
	mu.Lock()
	var creates []string
	for _, call := range asked {
		if call.Method == "providers/create" {
			creates = append(creates, string(call.Params))
		}
	}
	mu.Unlock()
	if len(creates) != 2 || creates[0] != `{"id":"do","name":"second"}` || creates[1] != `{"id":"do"}` {
		t.Fatalf("%v", creates)
	}

	go func() { text, _ := m.tool(8, "delete_machine", map[string]any{"host": "devbox"}); answered <- text }()
	waitFor(t, "the second card", func() bool {
		mustUnmarshal(t, c.call(t, 3, "elicitations/pending", nil), &pending)
		return len(pending) == 1
	})
	c.call(t, 4, "elicitations/answer", map[string]any{"requestID": pending[0].ID, "action": "accept", "content": map[string]any{"go": "no"}})
	if text := <-answered; text != "Nothing was deleted: the person said no, or didn’t answer." {
		t.Fatal(text)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, call := range asked {
		if call.Method == "machines/deleteMachine" {
			t.Fatal("deleted without a yes")
		}
	}
}

// A yes that comes after the card's hold (#4261): the call says the card is still open and
// nothing is made yet; the late yes makes the machine and tells the agent, and a late no
// deletes nothing.
func TestAYesAfterTheHoldStillMakesTheMachine(t *testing.T) {
	fakeRuntimes(t)
	_, left := formHolds(t)
	left() // the call must outlast its hold
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	title := "fix-login"
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.State, a.EndedReason, a.RuntimeID, a.Title = wire.AgentStateRunning, nil, "claude", &title
	})
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	host := "devbox"
	listed := wire.AgentMachines{Providers: []wire.AgentMachinesProvider{{ID: "do", Name: "DigitalOcean", CanMake: true, CanDelete: true,
		Workspaces: []wire.AgentMachinesMachine{{ID: "w1", Name: "devbox", State: "running", Host: &host, HostState: new("online")}}}}}
	var mu sync.Mutex
	var done []string
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if method == wire.CallUserSetPolicy.Name {
			return json.RawMessage(`{}`), nil
		}
		call := params.(wire.MachineWorkspaceCall)
		switch call.Method {
		case "machines/forAgent":
			b, _ := json.Marshal(listed)
			return b, nil
		case "providers/create":
			mu.Lock()
			done = append(done, call.Method+" "+string(call.Params))
			mu.Unlock()
			return json.RawMessage(`{"workspace":{"id":"w2","name":"second"},"joining":true}`), nil
		}
		mu.Lock()
		done = append(done, call.Method)
		mu.Unlock()
		return nil, &controllink.AskError{Code: -32010, Message: "Agent Loops can’t ask that of the control plane."}
	})
	did := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(done)
	}
	l := d.live(a.ID)
	l.mu.Lock()
	l.inTurn = true
	l.mu.Unlock()
	if text, isErr := m.tool(2, "make_machine", map[string]any{"provider": "do", "name": "second"}); isErr || text != yesStillOpen {
		t.Fatal(text)
	}
	if got := did(); len(got) != 0 {
		t.Fatalf("made before the yes: %v", got)
	}
	if text, isErr := m.tool(3, "delete_machine", map[string]any{"host": "devbox"}); isErr || text != yesStillOpen {
		t.Fatal(text)
	}
	l.closeForms(false) // the turn stops; the cards stay
	l.mu.Lock()
	l.inTurn = false
	l.mu.Unlock()
	var pending []wire.ElicitationRequest
	mustUnmarshal(t, c.call(t, 1, "elicitations/pending", nil), &pending)
	if len(pending) != 2 {
		t.Fatalf("%+v", pending)
	}
	for i, p := range pending {
		answer := "no"
		if *p.Message == "Claude (fix-login) wants to make a machine" {
			answer = "yes"
		}
		c.call(t, 2+i, "elicitations/answer", map[string]any{"requestID": p.ID, "action": "accept", "content": map[string]any{"go": answer}})
	}
	waitFor(t, "both results in its chat", func() bool {
		said := userMessages(d, a.ID)
		return slices.Contains(said, "The person answered your card \"Claude (fix-login) wants to make a machine\". "+
			"Made second. It joins as the host second on its first boot: wait for server.online.") &&
			slices.Contains(said, "The person answered your card \"Claude (fix-login) wants to delete a machine\". "+
				"Nothing was deleted: the person said no, or didn’t answer.")
	})
	if got := did(); len(got) != 1 || got[0] != `providers/create {"id":"do","name":"second"}` {
		t.Fatalf("%v", got)
	}
}

// A make that failed says why in list_machines (#4706), not that it is no host.
func TestAFailedMakeSaysWhyInListMachines(t *testing.T) {
	t.Parallel()

	listed := wire.AgentMachines{Providers: []wire.AgentMachinesProvider{{ID: "cs", Name: "GitHub Codespaces", CanMake: true,
		Workspaces: []wire.AgentMachinesMachine{
			{ID: "failed-machine-21", Name: "machine-21", State: "failed", Reason: new("GitHub said 403: Resource not accessible by integration.")},
			{ID: "c1", Name: "machine-12", State: "stopped"}}}}}
	note := workspacesNote(listed)
	if !strings.Contains(note, "  - machine-21: failed — GitHub said 403: Resource not accessible by integration.\n") {
		t.Errorf("%s", note)
	}
	if !strings.Contains(note, "  - machine-12: stopped — not a host") {
		t.Errorf("%s", note)
	}
}

// What it takes to show the card is part of the hold (#6993): the card's record and its request
// go to disk first, and a slow disk must not put the call past the tool's limit.
func TestASlowCardIsPartOfTheHold(t *testing.T) {
	fakeRuntimes(t)
	formHolds(t)
	formHoldLimit = time.Hour
	clock := atTime(t, time.Now())
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.State, a.EndedReason, a.RuntimeID = wire.AgentStateRunning, nil, "claude"
	})
	l := d.live(a.ID)
	l.mu.Lock()
	l.inTurn = true
	l.mu.Unlock()

	// The card is held up as it is shown, until the hold is over.
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	d.requests.mu.Lock()
	go func() {
		text, err := d.personSaysYes(a.ID, "wants to", "Go?", "Go", func(bool) (string, error) { return "went", nil })
		done <- result{text, err}
	}()
	// Its record comes before its request, so the call has begun.
	waitFor(t, "the card's record", func() bool { return slices.Contains(kinds(d, a.ID), "elicitationAsked") })
	clock.add(2 * time.Hour)
	d.requests.mu.Unlock()
	select {
	case r := <-done:
		if r.err != nil || r.text != yesStillOpen {
			t.Fatalf("%q, %v", r.text, r.err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the call is still held: the hold did not count showing the card")
	}
}

// A slow list before the card (#4986): the card is held only for what is left of the hold, so
// the call ends inside the tool's limit; a second call shows the same card, not a second one;
// list_machines says the make waits; and a list slower than its patience asks nobody.
func TestASlowListLeavesTheCardTheRestOfTheHold(t *testing.T) {
	flaky.UnderLoad(t, "#6993")
	fakeRuntimes(t)
	formHolds(t)
	// A hold that only the test's clock ends: a call that does not count what it took waits past
	// the test's own time limit. The margin was wall time, which a slow disk took (#6993).
	formHoldLimit = time.Hour
	clock := atTime(t, time.Now())
	setFor(t, &cardCheckPatience, time.Minute)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	title := "fix-login"
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.State, a.EndedReason, a.RuntimeID, a.Title = wire.AgentStateRunning, nil, "claude", &title
	})
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	listed := wire.AgentMachines{Providers: []wire.AgentMachinesProvider{{ID: "do", Name: "DigitalOcean", CanMake: true}}}
	var mu sync.Mutex
	slow := time.Duration(0)
	d.SetControlAsk(func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		if method == wire.CallUserSetPolicy.Name {
			return json.RawMessage(`{}`), nil
		}
		if call := params.(wire.MachineWorkspaceCall); call.Method == "machines/forAgent" {
			mu.Lock()
			wait := slow
			mu.Unlock()
			clock.add(2 * time.Hour) // the list took longer than the hold
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			b, _ := json.Marshal(listed)
			return b, nil
		}
		return nil, &controllink.AskError{Code: -32010, Message: "Agent Loops can’t ask that of the control plane."}
	})
	l := d.live(a.ID)
	l.mu.Lock()
	l.inTurn = true
	l.mu.Unlock()

	if text, isErr := m.tool(2, "make_machine", map[string]any{"provider": "do", "name": "second"}); isErr || text != yesStillOpen {
		t.Fatal(text)
	}
	if text, isErr := m.tool(3, "make_machine", map[string]any{"provider": "do", "name": "second"}); isErr || text != yesStillOpen {
		t.Fatal(text)
	}
	var pending []wire.ElicitationRequest
	mustUnmarshal(t, c.call(t, 1, "elicitations/pending", nil), &pending)
	if len(pending) != 1 {
		t.Fatalf("asked %d times: %+v", len(pending), pending)
	}
	text, isErr := m.tool(4, "list_machines", map[string]any{})
	if isErr || !strings.HasSuffix(text, "\nWaiting for the person's yes, on Claude (fix-login)'s card: "+
		"Make a new machine, second, at DigitalOcean? Do not ask again.") {
		t.Fatal(text)
	}

	mu.Lock()
	slow = time.Minute
	mu.Unlock()
	cardCheckPatience = 100 * time.Millisecond
	if text, isErr := m.tool(5, "make_machine", map[string]any{"provider": "do", "name": "third"}); !isErr ||
		text != "Nothing was done, and the person was not asked: the control plane did not list the machines within 100ms. Call it again." {
		t.Fatal(text)
	}
	mustUnmarshal(t, c.call(t, 2, "elicitations/pending", nil), &pending)
	if len(pending) != 1 {
		t.Fatalf("%+v", pending)
	}
}

// delete_machine with the name of a make that failed (#6078): the person is asked, and a
// yes deletes only its row, by providers/deleteWorkspace; no machine is deleted.
func TestDeleteMachineOnAFailedMakeDeletesItsRow(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	title := "tidy"
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.State, a.EndedReason, a.RuntimeID, a.Title = wire.AgentStateRunning, nil, "claude", &title
	})
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	listed := wire.AgentMachines{Providers: []wire.AgentMachinesProvider{{ID: "do", Name: "DigitalOcean", CanMake: true, CanDelete: true,
		Workspaces: []wire.AgentMachinesMachine{{ID: "failed-pool-1-1b71be", Name: "pool-1-1b71be", State: "failed", Reason: new("403")}}}}}
	var mu sync.Mutex
	var asked []wire.MachineWorkspaceCall
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if method == wire.CallUserSetPolicy.Name {
			return json.RawMessage(`{}`), nil
		}
		call := params.(wire.MachineWorkspaceCall)
		mu.Lock()
		asked = append(asked, call)
		mu.Unlock()
		switch call.Method {
		case "machines/forAgent":
			b, _ := json.Marshal(listed)
			return b, nil
		case "providers/deleteWorkspace":
			return json.RawMessage(`{}`), nil
		}
		return nil, &controllink.AskError{Code: -32010, Message: "Agent Loops can’t ask that of the control plane."}
	})
	l := d.live(a.ID)
	l.mu.Lock()
	l.inTurn = true
	l.mu.Unlock()
	answered := make(chan string, 1)
	go func() {
		text, _ := m.tool(2, "delete_machine", map[string]any{"host": "pool-1-1b71be"})
		answered <- text
	}()
	var pending []wire.ElicitationRequest
	waitFor(t, "the card", func() bool {
		_ = json.Unmarshal(c.call(t, 1, "elicitations/pending", nil), &pending)
		return len(pending) == 1
	})
	if f := pending[0].Mode.Form.Value; !strings.Contains(*f.Properties[0].Title, "DigitalOcean never made it, so nothing is deleted there") {
		t.Fatalf("%s", *f.Properties[0].Title)
	}
	c.call(t, 2, "elicitations/answer", map[string]any{"requestID": pending[0].ID, "action": "accept", "content": map[string]any{"go": "yes"}})
	if text := <-answered; text != "Deleted the row of pool-1-1b71be, a make that failed. Nothing was deleted at DigitalOcean." {
		t.Fatal(text)
	}
	mu.Lock()
	defer mu.Unlock()
	var deletes []string
	for _, call := range asked {
		switch call.Method {
		case "providers/deleteWorkspace":
			deletes = append(deletes, string(call.Params))
		case "machines/deleteMachine":
			t.Fatal("a machine was deleted")
		}
	}
	if !slices.Equal(deletes, []string{`{"id":"do","workspace":"failed-pool-1-1b71be"}`}) {
		t.Fatalf("%v", deletes)
	}
}

// A machine is found by its name in list_machines or by its host's name (#6080): a host that
// Add as Machine… linked chose its own name, so the two differ. One with no host can't be
// started or stopped, but can be deleted at its provider; a name two providers have is asked
// about, and provider says which.
func TestAMachineIsFoundByEitherName(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	title := "fix-login"
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.State, a.EndedReason, a.RuntimeID, a.Title = wire.AgentStateRunning, nil, "claude", &title
	})
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	listed := wire.AgentMachines{Providers: []wire.AgentMachinesProvider{
		{ID: "cs", Name: "GitHub Codespaces", CanDelete: true, Workspaces: []wire.AgentMachinesMachine{
			{ID: "c1", Name: "fluffy-space-x", State: "running", Host: new("codespaces-1a2b"), HostState: new("offline")},
			{ID: "c2", Name: "pool-1", State: "stopped"},
			{ID: "c3", Name: "shared", State: "running", Host: new("shared-cs"), HostState: new("online")}}},
		{ID: "do", Name: "DigitalOcean", CanDelete: true, Workspaces: []wire.AgentMachinesMachine{
			{ID: "d1", Name: "shared", State: "running", Host: new("shared-do"), HostState: new("online")}}}}}
	var mu sync.Mutex
	var asked []string
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if method == wire.CallUserSetPolicy.Name {
			return json.RawMessage(`{}`), nil
		}
		call := params.(wire.MachineWorkspaceCall)
		if call.Method == "machines/forAgent" {
			b, _ := json.Marshal(listed)
			return b, nil
		}
		mu.Lock()
		asked = append(asked, call.Method+" "+string(call.Params))
		mu.Unlock()
		return json.RawMessage(`{}`), nil
	})
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(asked) == 0 {
			return ""
		}
		return asked[len(asked)-1]
	}

	for i, tc := range []struct{ tool, name, provider, asks string }{
		{"start_machine", "fluffy-space-x", "", `machines/start {"host":"codespaces-1a2b"}`},
		{"start_machine", "codespaces-1a2b", "", `machines/start {"host":"codespaces-1a2b"}`},
		{"stop_machine", "FLUFFY-space-x", "", `machines/stop {"host":"codespaces-1a2b"}`},
		{"stop_machine", "shared", "DigitalOcean", `machines/stop {"host":"shared-do"}`},
		{"start_machine", "shared", "cs", `machines/start {"host":"shared-cs"}`},
		// Not a provider's: a host's id, for the control plane to find.
		{"start_machine", "h-1234", "", `machines/start {"host":"h-1234"}`},
	} {
		args := map[string]any{"host": tc.name}
		if tc.provider != "" {
			args["provider"] = tc.provider
		}
		if text, isErr := m.tool(10+i, tc.tool, args); isErr {
			t.Fatalf("%s %s: %s", tc.tool, tc.name, text)
		}
		if got := last(); got != tc.asks {
			t.Errorf("%s %s asked %s, want %s", tc.tool, tc.name, got, tc.asks)
		}
	}
	before := last()
	if text, isErr := m.tool(20, "start_machine", map[string]any{"host": "pool-1"}); !isErr ||
		text != "Nothing was started: the machine pool-1 at GitHub Codespaces has no host, so Agent Loops does not run there. list_machines says why." {
		t.Fatal(text)
	}
	if text, isErr := m.tool(21, "stop_machine", map[string]any{"host": "shared"}); !isErr ||
		text != "Nothing was stopped: shared is a machine at GitHub Codespaces (provider cs) and at DigitalOcean (provider do). Say which, with provider." {
		t.Fatal(text)
	}
	if text, isErr := m.tool(22, "delete_machine", map[string]any{"host": "shared", "provider": "Nobody"}); !isErr ||
		text != "Nothing was deleted: no provider is called Nobody. There are GitHub Codespaces, DigitalOcean." {
		t.Fatal(text)
	}
	if got := last(); got != before {
		t.Fatalf("asked %s", got)
	}

	// Deleting asks the person, then deletes by the host, or at the provider when it has none.
	l := d.live(a.ID)
	l.mu.Lock()
	l.inTurn = true
	l.mu.Unlock()
	for i, tc := range []struct{ name, question, said, asks string }{
		{"fluffy-space-x", "Delete the machine codespaces-1a2b runs on, with everything on its disk, and remove the machine codespaces-1a2b with its agents?",
			"Deleted the machine codespaces-1a2b ran on, and removed the machine.", `machines/deleteMachine {"host":"codespaces-1a2b"}`},
		{"codespaces-1a2b", "Delete the machine codespaces-1a2b runs on, with everything on its disk, and remove the machine codespaces-1a2b with its agents?",
			"Deleted the machine codespaces-1a2b ran on, and removed the machine.", `machines/deleteMachine {"host":"codespaces-1a2b"}`},
		{"pool-1", "Delete the machine pool-1 at GitHub Codespaces, with everything on its disk?",
			"Deleted the machine pool-1 at GitHub Codespaces.", `providers/deleteWorkspace {"id":"cs","workspace":"c2"}`},
	} {
		answered := make(chan string, 1)
		go func() { text, _ := m.tool(30+i, "delete_machine", map[string]any{"host": tc.name}); answered <- text }()
		var pending []wire.ElicitationRequest
		waitFor(t, "the card for "+tc.name, func() bool {
			return json.Unmarshal(c.call(t, 40+2*i, "elicitations/pending", nil), &pending) == nil && len(pending) == 1
		})
		if q := *pending[0].Mode.Form.Value.Properties[0].Title; q != tc.question {
			t.Errorf("%s: card %q", tc.name, q)
		}
		c.call(t, 41+2*i, "elicitations/answer", map[string]any{"requestID": pending[0].ID, "action": "accept", "content": map[string]any{"go": "yes"}})
		if text := <-answered; text != tc.said {
			t.Errorf("%s: %s", tc.name, text)
		}
		if got := last(); got != tc.asks {
			t.Errorf("%s asked %s, want %s", tc.name, got, tc.asks)
		}
	}
}
