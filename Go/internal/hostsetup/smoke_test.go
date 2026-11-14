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
