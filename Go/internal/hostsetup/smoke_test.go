package hostsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// smokeHost is a host whose one session behaves as script says.
type smokeHost struct {
	signedIn   bool
	state      wire.AgentState
	reason     *wire.EndedReason
	message    string
	finishCall bool
	started    *wire.StartRequest
	archived   int
}

func (h *smokeHost) call(method string, params any) (json.RawMessage, error) {
	switch method {
	case wire.CallRuntimesList.Name:
		a := wire.RuntimeAvailability{NeedsSignIn: &wire.RuntimeAvailabilityNeedsSignIn{}}
		if h.signedIn {
			a = wire.RuntimeAvailability{Available: &wire.RuntimeAvailabilityAvailable{Path: "/x"}}
		}
		return json.Marshal([]wire.RuntimeStatus{{Runtime: wire.Runtime{ID: "claude"}, Availability: a}})
	case wire.CallAgentsOptions.Name:
		cat := "model"
		return json.Marshal(wire.OptionsResponse{DraftID: "d1", Options: []wire.ConfigOption{{ID: "model", Name: "Model", Category: &cat, Type: "select",
			Options: json.RawMessage(`[{"value":"opus","name":"Opus"},{"value":"haiku","name":"Haiku"}]`)}}})
	case wire.CallAgentsStart.Name:
		r := params.(wire.StartRequest)
		h.started = &r
		return json.Marshal(wire.UUID("a1"))
	case wire.CallAgentsList.Name:
		return json.Marshal([]wire.Agent{{ID: "a1", State: h.state, EndedReason: h.reason}})
	case wire.CallAgentsTranscript.Name:
		var es []wire.TranscriptEntry
		if h.message != "" {
			es = append(es, wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: h.message}}})
		}
		if h.finishCall {
			name := "mcp__agents__finish_turn"
			es = append(es, wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{ToolCall: &wire.TranscriptEntryKindToolCall{Value: wire.ToolCall{Name: &name}}}})
		}
		return json.Marshal(wire.TranscriptPage{Entries: es})
	case wire.CallAgentsArchive.Name:
		h.archived++
		return json.Marshal(wire.Empty{})
	}
	return nil, fmt.Errorf("unexpected call %s", method)
}

func runSmoke(t *testing.T, h *smokeHost, wait time.Duration) (error, *wire.MachineSetupReport, Layout) {
	t.Helper()
	o, l := options(t)
	rep, err := NewReporter(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{Call: h.call, Poll: time.Millisecond, SignInWait: 30 * time.Millisecond, SmokeWait: wait}
	err = Smoke(context.Background(), o, d, rep, func(string, ...any) {})
	return err, lastOf(reports(t, l), wire.MachineSetupStepIDSmoke), l
}

func TestSmokeAnswersWithTheCheapestModelAndArchives(t *testing.T) {
	h := &smokeHost{signedIn: true, state: wire.AgentStateFinished, message: "Hello.", finishCall: true}
	err, r, _ := runSmoke(t, h, time.Second)
	if err != nil || r == nil || r.State != wire.MachineSetupStateDone || !strings.Contains(*r.Detail, "first word") {
		t.Fatalf("err=%v report=%+v", err, r)
	}
	if string(h.started.StartOptions.Values["model"]) != `"haiku"` {
		t.Errorf("model = %s", h.started.StartOptions.Values["model"])
	}
	if h.archived != 1 {
		t.Errorf("archived %d times", h.archived)
	}
}

func TestSmokeFailsWithTheRuntimesErrorAndStillArchives(t *testing.T) {
	why := wire.EndedReasonRuntimeError
	h := &smokeHost{signedIn: true, state: wire.AgentStateStopped, reason: &why}
	err, r, _ := runSmoke(t, h, time.Second)
	if err == nil || r == nil || r.State != wire.MachineSetupStateFailed || !strings.Contains(*r.Error, "runtimeError") {
		t.Fatalf("err=%v report=%+v", err, r)
	}
	if h.archived != 1 {
		t.Errorf("archived %d times", h.archived)
	}
}

func TestSmokeFailsWhenNothingAnswersInTime(t *testing.T) {
	h := &smokeHost{signedIn: true, state: wire.AgentStateRunning}
	err, r, _ := runSmoke(t, h, 20*time.Millisecond)
	if err == nil || r == nil || r.State != wire.MachineSetupStateFailed || !strings.Contains(*r.Error, "did not answer") {
		t.Fatalf("err=%v report=%+v", err, r)
	}
}

func TestSmokeSaysNothingWhileTheRuntimeNeedsASignIn(t *testing.T) {
	h := &smokeHost{state: wire.AgentStateRunning}
	err, r, _ := runSmoke(t, h, time.Second)
	if err == nil || r != nil || h.started != nil {
		t.Fatalf("err=%v report=%+v started=%v", err, r, h.started)
	}
}

// signInAfterAsked is a host where claude is signed in once step 7 has said needs you, and
// after has passed since then. The sign-in follows what was said and not the test's own
// clock: on a slow machine a clock signs in before the step asks, and running then done is
// right for that (#7105).
func signInAfterAsked(t *testing.T, h *smokeHost, l Layout, after time.Duration) func(string, any) (json.RawMessage, error) {
	t.Helper()
	var asked time.Time
	return func(method string, params any) (json.RawMessage, error) {
		if method == wire.CallRuntimesList.Name {
			r := lastOf(reports(t, l), wire.MachineSetupStepIDSignIn)
			if asked.IsZero() && r != nil && r.State == wire.MachineSetupStateNeedsYou {
				asked = time.Now()
			}
			if !asked.IsZero() && time.Since(asked) >= after {
				h.signedIn = true
			}
		}
		return h.call(method, params)
	}
}

// signInContext ends a SignIn that never asks, so it fails its test and does not hang it.
func signInContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func signInStates(t *testing.T, l Layout) []wire.MachineSetupState {
	t.Helper()
	var states []wire.MachineSetupState
	for _, r := range reports(t, l) {
		if r.Step == wire.MachineSetupStepIDSignIn {
			states = append(states, r.State)
		}
	}
	return states
}

// #5361: step 7 is running while a client writes a key, needs you after that, and done when
// every runtime says it is available.
func TestSignInWaitsForTheClientThenNeedsYouThenIsDone(t *testing.T) {
	h := &smokeHost{}
	o, l := options(t)
	o.Runtimes = []string{"claude"}
	rep, err := NewReporter(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{Call: signInAfterAsked(t, h, l, 0), Poll: time.Millisecond, KeyWait: 5 * time.Millisecond}
	if err := SignIn(signInContext(t), o, d, rep, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	want := []wire.MachineSetupState{wire.MachineSetupStateRunning, wire.MachineSetupStateNeedsYou, wire.MachineSetupStateDone}
	if got := signInStates(t, l); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("states %v, want %v", got, want)
	}
}

// #6853: a person who signs in after SignInWait still finishes step 7. Before, the step
// failed after SignInWait and nothing ran steps 7 and 8 again.
func TestSignInKeepsWaitingPastSignInWait(t *testing.T) {
	h := &smokeHost{}
	o, l := options(t)
	o.Runtimes = []string{"claude"}
	rep, err := NewReporter(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{Call: signInAfterAsked(t, h, l, 50*time.Millisecond), Poll: time.Millisecond, KeyWait: time.Millisecond, SignInWait: 5 * time.Millisecond}
	if err := SignIn(signInContext(t), o, d, rep, func(string, ...any) {}); err != nil {
		t.Fatalf("a sign-in after SignInWait did not finish step 7: %v", err)
	}
	want := []wire.MachineSetupState{wire.MachineSetupStateRunning, wire.MachineSetupStateNeedsYou, wire.MachineSetupStateDone}
	if got := signInStates(t, l); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("states %v, want %v", got, want)
	}
}

// #6853: an installer that is stopped while it waits says so on step 7, so the step does not
// say "Waiting for you" for ever.
func TestSignInSaysFailedWhenTheInstallerStops(t *testing.T) {
	h := &smokeHost{}
	o, l := options(t)
	o.Runtimes = []string{"claude"}
	rep, err := NewReporter(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	d := Deps{Call: h.call, Poll: time.Millisecond, KeyWait: time.Millisecond}
	if err := SignIn(ctx, o, d, rep, func(string, ...any) {}); err == nil {
		t.Fatal("a stopped installer passed step 7")
	}
	r := lastOf(reports(t, l), wire.MachineSetupStepIDSignIn)
	if r == nil || r.State != wire.MachineSetupStateFailed || r.Error == nil || !strings.Contains(*r.Error, "Run it again") {
		t.Fatalf("report %+v", r)
	}
}

// #6340: a daemon that answers slower than KeyWait still gets running, then needs you, then
// done, in order.
func TestSignInSaysEachStateInOrderWhenTheDaemonIsSlow(t *testing.T) {
	h := &smokeHost{}
	o, l := options(t)
	o.Runtimes = []string{"claude"}
	rep, err := NewReporter(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	later := signInAfterAsked(t, h, l, 0)
	slow := func(method string, params any) (json.RawMessage, error) {
		time.Sleep(15 * time.Millisecond)
		return later(method, params)
	}
	d := Deps{Call: slow, Poll: time.Millisecond, KeyWait: time.Millisecond, SignInWait: 10 * time.Millisecond}
	if err := SignIn(signInContext(t), o, d, rep, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	want := []wire.MachineSetupState{wire.MachineSetupStateRunning, wire.MachineSetupStateNeedsYou, wire.MachineSetupStateDone}
	if got := signInStates(t, l); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("states %v, want %v", got, want)
	}
}

// #6855: step 7 says which runtimes a Mac lends its sign-in to; a lent one is lent also
// when the machine has a sign-in of its own (056 D7).
func TestSignedInLineSaysWhichAreLent(t *testing.T) {
	yes := true
	status := func(id, name string, own bool, relayed *bool) wire.RuntimeStatus {
		var s wire.RuntimeStatus
		s.Runtime.ID, s.Runtime.Name = id, name
		s.Availability.Available = &wire.RuntimeAvailabilityAvailable{}
		s.SignIn = &wire.RuntimeSignIn{SignedIn: own, Relayed: relayed}
		return s
	}
	cases := []struct {
		all  []wire.RuntimeStatus
		want string
	}{
		{[]wire.RuntimeStatus{status("claude", "Claude", false, &yes), status("codex", "Codex", true, &yes)},
			"Claude, Codex signed in (lent by a Mac)"},
		{[]wire.RuntimeStatus{status("claude", "Claude", true, nil), status("codex", "Codex", false, &yes)},
			"Claude signed in; Codex signed in (lent by a Mac)"},
		{[]wire.RuntimeStatus{status("claude", "Claude", true, nil), status("codex", "Codex", true, nil)},
			"Claude, Codex signed in"},
	}
	for _, c := range cases {
		waiting, own, lent := splitSignedIn(c.all, []string{"claude", "codex"})
		if len(waiting) != 0 {
			t.Fatalf("waiting %v", waiting)
		}
		if got := signedInLine(own, lent); got != c.want {
			t.Errorf("line %q, want %q", got, c.want)
		}
	}
}
