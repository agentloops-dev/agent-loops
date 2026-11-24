package agentsd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Sessions on another machine (#2927): the agent that started one reaches it through the
// control plane, which only its starter may; its end comes back and closes the waits on it;
// and the starter's folder's limits count it.

const there = wire.UUID("11111111-2222-3333-4444-555555555555")

func TestTheStarterReachesItsSessionOnAnotherMachine(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	title := "fix-login"
	dir := t.TempDir()
	a := seeded(t, d, dir, func(a *wire.Agent) {
		a.State, a.EndedReason, a.RuntimeID, a.Title = wire.AgentStateRunning, nil, "claude", &title
	})
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)

	var mu sync.Mutex
	var calls []wire.MachineSessionCall
	reply := wire.SessionCallReply{Said: "working", Title: new("Check the Windows bug")}
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		call, _ := params.(wire.MachineWorkspaceCall)
		if call.Method != "machines/sessionCall" {
			return nil, &controllink.AskError{Code: -32010, Message: "Agent Loops can’t ask that of the control plane."}
		}
		var s wire.MachineSessionCall
		b, _ := json.Marshal(call.Params)
		mustUnmarshal(t, b, &s)
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, s)
		if s.Call.Op != "status" {
			return json.Marshal(wire.SessionCallReply{Said: s.Call.Op + " said"})
		}
		return json.Marshal(reply)
	})
	last := func() wire.MachineSessionCall {
		mu.Lock()
		defer mu.Unlock()
		return calls[len(calls)-1]
	}
	d.noteStartedThere(startedThere{Machine: "pc", Session: there, Starter: a.ID, Folder: dir, There: "/home/me/app", Title: "Check the Windows bug", At: now()})

	// It is counted where its starter is.
	if p := d.helperPlaces(dir); p != "1 of 3 running, 1 of 5 not archived" {
		t.Fatal(p)
	}
	if text, _ := m.tool(2, "list_sessions", map[string]any{"started_by": "me"}); !strings.Contains(text,
		"- 11111111-2222-3333-4444-555555555555: “Check the Windows bug” on pc, in /home/me/app — working") {
		t.Fatal(text)
	}
	if s := last(); s.Host != "pc" || s.Call.Session != there || s.Call.From.Agent != a.ID || s.Call.Op != "status" {
		t.Fatalf("%+v", s)
	}
	if text, _ := m.tool(3, "read_session", map[string]any{"session": string(there)}); text != "read said" {
		t.Fatal(text)
	}
	if text, _ := m.tool(4, "message_session", map[string]any{"to": "Check the Windows bug", "message": "Try the 32-bit build too."}); text != "message said" {
		t.Fatal(text)
	}
	if s := last(); s.Call.Op != "message" || deref(s.Call.Message) != "Try the 32-bit build too." {
		t.Fatalf("%+v", s)
	}
	if text, _ := m.tool(5, "stop_session", map[string]any{"id": string(there)}); text != "stop said" {
		t.Fatal(text)
	}

	// A wait on it closes when its machine says it has ended.
	if text, isErr := m.tool(6, "wait_for_event", map[string]any{"events": []string{"agent.ended"}, "where": map[string]any{"agent": []string{string(there)}}, "until_minutes": 30}); isErr ||
		!strings.Contains(text, "You will be resumed when “Check the Windows bug” has finished") {
		t.Fatal(text)
	}
	if e := c.callError(t, 7, "agents/endedElsewhere", map[string]any{"machine": "pc", "session": string(there), "starter": string(wire.UUID("AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE")),
		"how": map[string]any{"stopped": map[string]any{}}}); e == nil || e.Code != wire.FailureNotYours {
		t.Fatalf("%+v", e)
	}
	c.call(t, 8, "agents/endedElsewhere", map[string]any{"machine": "pc", "session": string(there), "starter": string(a.ID),
		"how": map[string]any{"finished": map[string]any{"outcome": "done", "message": "Fixed."}}})
	waitFor(t, "the wait to close", func() bool {
		r := d.Store.Agent(a.ID).Report
		return r != nil && r.Block != nil && len(r.Block.Waits) == 1 && r.Block.Waits[0].Ending != nil
	})
	if p := d.helperPlaces(dir); p != "0 of 3 running, 1 of 5 not archived" {
		t.Fatal(p)
	}

	// An ended one answers at once and is not waited on; an archived one is let go.
	mu.Lock()
	reply.Ended = &wire.WaitEndingHow{Archived: &struct{}{}}
	mu.Unlock()
	if text, isErr := m.tool(9, "wait_for_event", map[string]any{"events": []string{"agent.ended"}, "where": map[string]any{"agent": []string{string(there)}}, "until_minutes": 30}); isErr ||
		!strings.Contains(text, "on pc (id "+string(there)+"): archived") {
		t.Fatal(text)
	}
	if p := d.helperPlaces(dir); p != "0 of 3 running, 0 of 5 not archived" {
		t.Fatal(p)
	}
}

// P20261030-1: list_sessions must not wait on every session on another machine in turn —
// one slow machine should not cost the others their share of the wait.
func TestListThereAsksEveryMachineAtOnce(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	title := "watcher"
	a := seeded(t, d, dir, func(a *wire.Agent) { a.State, a.RuntimeID, a.Title = wire.AgentStateRunning, "claude", &title })

	const n = 6
	for i := 0; i < n; i++ {
		d.noteStartedThere(startedThere{Machine: fmt.Sprintf("pc%d", i), Session: store.NewUUID(), Starter: a.ID,
			Folder: dir, There: "/home/me/app", Title: fmt.Sprintf("session %d", i), At: now()})
	}
	// Count the asks in flight at once, not the time the call takes, so a busy machine
	// cannot fail it (#6262). Each ask waits until all n are in flight, or for the test's limit: asked
	// at once, all n meet; asked in turn, only one is ever in flight.
	var mu sync.Mutex
	inFlight, most, met := 0, 0, false
	all := make(chan struct{})
	meet := func() { // with mu held
		if !met {
			met = true
			close(all)
		}
	}
	limit := afterLimit(t)
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		call, _ := params.(wire.MachineWorkspaceCall)
		if call.Method != "machines/sessionCall" {
			return nil, &controllink.AskError{Code: -32010, Message: "Agent Loops can’t ask that of the control plane."}
		}
		mu.Lock()
		inFlight++
		most = max(most, inFlight)
		if inFlight == n {
			meet()
		}
		mu.Unlock()
		select {
		case <-all:
		case <-limit: // asked in turn: let every ask go, and the count fails the test
			mu.Lock()
			meet()
			mu.Unlock()
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return json.Marshal(wire.SessionCallReply{Said: "working"})
	})

	lines := d.listThere(a.ID)
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d", len(lines), n)
	}
	mu.Lock()
	defer mu.Unlock()
	if most != n {
		t.Fatalf("at most %d of %d asks were in flight at once: not asked at once", most, n)
	}
}

func TestOnlyTheStarterReachesASessionFromAnotherMachine(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	var mu sync.Mutex
	var ended []wire.SessionEnded
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		call, _ := params.(wire.MachineWorkspaceCall)
		if call.Method == "machines/sessionEnded" {
			var e wire.SessionEnded
			b, _ := json.Marshal(call.Params)
			mustUnmarshal(t, b, &e)
			mu.Lock()
			ended = append(ended, e)
			mu.Unlock()
		}
		return json.RawMessage(`{}`), nil
	})
	dir := folder(t, c, 1)
	starter := "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
	from := map[string]any{"machine": "Alex's Mac", "agent": starter, "who": "Claude (fix-login)"}
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 2, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + dir, "prompt": "Check the Windows bug",
		"startedFrom": from}), &id)
	// Ended as hasEnded says, not only saved Finished: end saves the state before its turn
	// loop lets go, and a status asked between the two has no ending yet (#2973).
	waitFor(t, "its turn to end", func() bool {
		a := d.Store.Agent(id)
		return a != nil && a.State == wire.AgentStateFinished && d.hasEnded(a) != ""
	})

	// Another agent, or the same id from another machine, is not its starter.
	for i, who := range []map[string]any{{"machine": "Alex's Mac", "agent": "BBBBBBBB-BBBB-CCCC-DDDD-EEEEEEEEEEEE", "who": "x"},
		{"machine": "laptop", "agent": starter, "who": "x"}} {
		if e := c.callError(t, 3+i, "agents/fromStarter", map[string]any{"from": who, "session": string(id), "op": "read"}); e == nil || e.Code != wire.FailureNotYours {
			t.Fatalf("%+v", e)
		}
	}
	var reply wire.SessionCallReply
	mustUnmarshal(t, c.call(t, 5, "agents/fromStarter", map[string]any{"from": from, "session": string(id), "op": "status"}), &reply)
	if !strings.HasPrefix(reply.Said, "finished") || reply.Ended == nil || reply.Ended.Finished == nil {
		t.Fatalf("%+v", reply)
	}
	mustUnmarshal(t, c.call(t, 6, "agents/fromStarter", map[string]any{"from": from, "session": string(id), "op": "read"}), &reply)
	if !strings.Contains(reply.Said, "Check the Windows bug") {
		t.Fatal(reply.Said)
	}

	// Its starter's message goes at once, and the end goes back to its machine.
	d.change(id, func(a *wire.Agent) {
		a.Report = &wire.WorkReport{Outcome: wire.WorkOutcomeDone, Message: "Done.", At: now()}
	})
	mu.Lock()
	ended = nil
	mu.Unlock()
	mustUnmarshal(t, c.call(t, 7, "agents/fromStarter", map[string]any{"from": from, "session": string(id), "op": "message", "message": "Try the 32-bit build too."}), &reply)
	if reply.Said != "Sent to “Check the Windows bug”, which is working on it now. Wait for it with wait_for_event on agent.ended, naming it in agent, and read its answer with read_session." {
		t.Fatal(reply.Said)
	}
	waitFor(t, "the message's turn", func() bool {
		for _, m := range userMessages(d, id) {
			if strings.Contains(m, "Try the 32-bit build too.") {
				return true
			}
		}
		return false
	})
	waitFor(t, "the starter to be told", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(ended) > 0
	})
	mu.Lock()
	e := ended[0]
	mu.Unlock()
	if e.Machine != "Alex's Mac" || e.Session != id || string(e.Starter) != starter || e.How.Finished == nil {
		t.Fatalf("%+v", e)
	}
}

// #2977: a status that says what was already held writes nothing; one that changes a record
// writes elsewhere.json again.
func TestHeardThereWritesOnlyWhatChanged(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	a := seeded(t, d, dir, func(a *wire.Agent) { a.State, a.RuntimeID = wire.AgentStateRunning, "claude" })
	d.noteStartedThere(startedThere{Machine: "pc", Session: there, Starter: a.ID, Folder: dir, There: "/home/me/app", Title: "watch", At: now()})
	last := ""
	written := func() bool {
		raw, err := os.ReadFile(d.elsewherePath())
		before := last
		last = string(raw)
		return err == nil && last != before
	}
	if !written() {
		t.Fatal("elsewhere.json was not written when the session was kept")
	}

	for range 3 {
		d.heardThere(there, "pc", "watch", nil)
		d.heardThere(there, "", "", nil)
	}
	if written() {
		t.Fatal("elsewhere.json was written though nothing changed")
	}

	d.heardThere(there, "", "watch v2", nil)
	if !written() {
		t.Fatal("a new title was not written")
	}
	ended := &wire.WaitEndingHow{Stopped: &wire.WaitEndingHowStopped{}}
	d.heardThere(there, "", "", ended)
	if !written() {
		t.Fatal("an ending was not written")
	}
	d.heardThere(there, "", "", &wire.WaitEndingHow{Stopped: &wire.WaitEndingHowStopped{}})
	if written() {
		t.Fatal("the same ending was written again")
	}
}

// #6722: an entry goes 90 days after its machine was last heard, in the sweep too, and one still
// heard of stays, whatever its age. A long ending is cut, so it does not refuse its entry.
func TestElsewhereAgesOutFromWhenItWasHeard(t *testing.T) {
	clock := atTime(t, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	a := seeded(t, d, dir, func(a *wire.Agent) { a.State, a.RuntimeID = wire.AgentStateRunning, "claude" })
	quiet := store.NewUUID()
	for _, id := range []wire.UUID{there, quiet} {
		d.noteStartedThere(startedThere{Machine: "pc", Session: id, Starter: a.ID, Folder: dir, There: "/home/me/app", Title: "watch", At: now()})
	}
	d.noteStartedHere(a.ID, wire.StartedFrom{Machine: "pc", Agent: quiet, Who: "Claude"})

	clock.add(60 * 24 * time.Hour)
	long := strings.Repeat("x", 1<<20)
	d.heardThere(there, "", "", &wire.WaitEndingHow{Finished: &wire.WaitEndingHowFinished{Message: &long}})
	d.heardFromStarter(a.ID)

	clock.add(60 * 24 * time.Hour)
	d.sweepSmallStores(nowFunc())
	left := d.startedThereBy(a.ID)
	if len(left) != 1 || left[0].Session != there || left[0].Ended == nil || len(*left[0].Ended.Finished.Message) > elsewhereMessageMost+8 {
		t.Fatalf("%d left", len(left))
	}
	if d.startedFromOf(a.ID) == nil {
		t.Fatal("let go of a session its starter still spoke of")
	}

	clock.add(60 * 24 * time.Hour)
	d.sweepSmallStores(nowFunc())
	if len(d.startedThereBy(a.ID)) != 0 || d.startedFromOf(a.ID) != nil {
		t.Fatal("kept past 90 days")
	}
}

// #6722: the lists of the file as it was before are carried over.
func TestElsewhereCarriesTheOldListsOver(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	old := `{"from":[{"session":"` + string(there) + `","from":{"machine":"pc","agent":"A","who":"Claude"}}],
"started":[{"machine":"pc","session":"` + string(there) + `","starter":"B","folder":"/a","there":"/b","title":"watch","at":"2026-11-06T11:00:00.000Z"}]}`
	if err := os.WriteFile(filepath.Join(root, "elsewhere.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _ := start(t, root)
	defer d.Shutdown()
	if s := d.startedThereBy("B"); len(s) != 1 || s[0].Title != "watch" || d.startedFromOf(there) == nil {
		t.Fatalf("%+v", s)
	}
}

// #5806: a host with no store (a test's bare Daemon) has no waits to ask about, and the job
// that SetControlAsk starts does not panic.
func TestAskThereWaitsWithNoStore(t *testing.T) {
	t.Parallel()

	(&Daemon{ctx: context.Background()}).askThereWaits()
}
