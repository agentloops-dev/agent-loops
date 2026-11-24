package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// step is what the host has after one more read of the session: its transcript so far
// and its state.
type step struct {
	entries []wire.TranscriptEntry
	state   wire.AgentState
	ended   wire.EndedReason
}

func chunk(id, text string) wire.TranscriptEntry {
	return wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{MessageID: &id, Text: text}}}
}

func said(text string) wire.TranscriptEntry {
	return wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: text}}}
}

func tool(title string) wire.TranscriptEntry {
	return wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{ToolCall: &wire.TranscriptEntryKindToolCall{Value: wire.ToolCall{Title: title}}}}
}

func asks(title string) wire.TranscriptEntry {
	return wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{PermissionAsked: &wire.TranscriptEntryKindPermissionAsked{Value: wire.PermissionRequest{ToolCall: wire.ToolCall{Title: title}}}}}
}

// hostFake is a host with one folder that starts session S and goes through steps, one per
// agents/list; the last step holds.
func hostFake(t *testing.T, steps []step) (*fake, *wire.StartRequest) {
	t.Helper()
	f := &fake{answer: map[string]func(string, any) (any, error){}}
	var started wire.StartRequest
	at := 0
	var all []wire.TranscriptEntry
	f.answer["agents/start"] = func(_ string, params any) (any, error) {
		started = params.(wire.StartRequest)
		return wire.UUID("AB12CD34-0000-0000-0000-000000000000"), nil
	}
	f.answer["presence/report"] = func(string, any) (any, error) { return wire.Empty{}, nil }
	f.answer["agents/transcript"] = func(_ string, params any) (any, error) {
		all = steps[min(at, len(steps)-1)].entries
		return daemonPage(all, params.(wire.TranscriptRequest)), nil
	}
	f.answer["agents/list"] = func(string, any) (any, error) {
		s := steps[min(at, len(steps)-1)]
		at++
		a := wire.Agent{ID: "AB12CD34-0000-0000-0000-000000000000", State: s.state}
		if s.ended != "" {
			a.EndedReason = &s.ended
		}
		return []wire.Agent{a}, nil
	}
	return f, &started
}

// daemonPage is a transcript page as agentsd's transcript gives it: the last limit entries
// before before (or the end), no earlier than from.
func daemonPage(all []wire.TranscriptEntry, r wire.TranscriptRequest) wire.TranscriptPage {
	total := int64(len(all))
	end := total
	if r.Before != nil {
		end = min(*r.Before, total)
	}
	limit := r.Limit
	if r.From != nil {
		limit = max(0, min(limit, end-*r.From))
	}
	start := end - min(limit, end)
	return wire.TranscriptPage{FirstIndex: start, Total: total, Entries: all[start:end]}
}

var here = place{machine: wire.ControlMachine{ID: "h1", Name: "Alex's MacBook Pro"}, folder: "file:///src/agent-loops/"}

// printRun runs print mode on the fake with no notes, so each read comes by the recheck.
func printRun(t *testing.T, f *fake, o options) (string, error) {
	t.Helper()
	was := recheck
	recheck = time.Millisecond
	t.Cleanup(func() { recheck = was })
	var b bytes.Buffer
	err := startAndFollow(context.Background(), &b, f, make(chan controllink.Note), here, o, "summarise yesterday's CI failures")
	return b.String(), err
}

// A whole turn: two replies with a tool call between, each in chunks.
var turnSteps = []step{
	{state: wire.AgentStateStarting},
	{entries: []wire.TranscriptEntry{said("summarise yesterday's CI failures"), chunk("m1", "Reading the CI runs "), chunk("m1", "since 2026-11-19…")}, state: wire.AgentStateRunning},
	{entries: []wire.TranscriptEntry{said("summarise yesterday's CI failures"), chunk("m1", "Reading the CI runs "), chunk("m1", "since 2026-11-19…"),
		tool("gh run list"), chunk("m2", "3 failures, all TestStopWaits "), chunk("m2", "under load (#6385).")}, state: wire.AgentStateFinished},
}

func TestPrintModePrintsOnlyTheLastReply(t *testing.T) {
	f, started := hostFake(t, turnSteps)
	out, err := printRun(t, f, options{print: true})
	if err != nil || out != "3 failures, all TestStopWaits under load (#6385).\n" {
		t.Fatalf("got %q %v", out, err)
	}
	if started.RuntimeID != "auto" || started.Cwd != here.folder || started.StartedVia == nil || *started.StartedVia != wire.StartedViaCLIPrint ||
		started.Prompt != "summarise yesterday's CI failures" {
		t.Fatalf("the start: %+v", started)
	}
}

func TestPrintModeJSONIsOneObjectPerLineSessionFirstEndLast(t *testing.T) {
	f, _ := hostFake(t, turnSteps)
	out, err := printRun(t, f, options{print: true, json: true})
	if err != nil {
		t.Fatal(err)
	}
	var types, texts []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not one object a line: %q", l)
		}
		types = append(types, m["type"].(string))
		if s, ok := m["text"].(string); ok {
			texts = append(texts, s)
		}
		if m["type"] == "session" && (m["id"] != "AB12CD34-0000-0000-0000-000000000000" || m["startedVia"] != "cli-print" || m["folder"] != "agent-loops") {
			t.Fatalf("the session line: %s", l)
		}
		if m["type"] == "end" && (m["state"] != "done" || m["exit"] != float64(0)) {
			t.Fatalf("the end line: %s", l)
		}
	}
	if strings.Join(types, " ") != "session entry entry entry entry end" {
		t.Fatalf("lines: %v\n%s", types, out)
	}
	want := []string{"summarise yesterday's CI failures", "Reading the CI runs since 2026-11-19…", "gh run list", "3 failures, all TestStopWaits under load (#6385)."}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("the chunks of a message are one entry: %q", texts)
	}
}

func TestOnAskFailEndsWithExitThreeAndAnswersNothing(t *testing.T) {
	f, _ := hostFake(t, []step{{entries: []wire.TranscriptEntry{asks("Run scripts/docs-parts.py")}, state: wire.AgentStateWaitingOnUser}})
	out, err := printRun(t, f, options{print: true, json: true, onAsk: onAskFail})
	if codeOf(err) != exitAsked || !strings.Contains(err.Error(), "Needs you") {
		t.Fatalf("exit %d: %v", codeOf(err), err)
	}
	if !strings.HasSuffix(out, `{"card":"Permission: Run scripts/docs-parts.py","exit":3,"state":"needs-you","type":"end"}`+"\n") {
		t.Fatalf("the end line: %s", out)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "answer") {
			t.Fatalf("the CLI answered a card: %v", f.calls)
		}
	}
}

func TestOnAskWaitWaitsForAnAnswerFromAnyClient(t *testing.T) {
	card := []wire.TranscriptEntry{asks("Run make")}
	answered := append(append([]wire.TranscriptEntry{}, card...),
		wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{PermissionAnswered: &wire.TranscriptEntryKindPermissionAnswered{OptionID: "allow"}}}, chunk("m", "Built."))
	f, _ := hostFake(t, []step{
		{entries: card, state: wire.AgentStateWaitingOnUser},
		{entries: card, state: wire.AgentStateWaitingOnUser},
		{entries: answered, state: wire.AgentStateFinished},
	})
	out, err := printRun(t, f, options{print: true, json: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, `"type":"card"`) != 1 || !strings.Contains(out, `"text":"Built."`) || !strings.Contains(out, `"state":"done"`) {
		t.Fatalf("one card line, then the end: %s", out)
	}
}

func TestTimeoutEndsWithExitFourAndLeavesTheSession(t *testing.T) {
	f, _ := hostFake(t, []step{{state: wire.AgentStateRunning}})
	_, err := printRun(t, f, options{print: true, timeout: 30 * time.Millisecond})
	if codeOf(err) != exitTimedOut || !strings.Contains(err.Error(), "loops -r ab12cd") {
		t.Fatalf("exit %d: %v", codeOf(err), err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "agents/stop") {
			t.Fatal("a timeout stopped the session")
		}
	}
}

func TestAStoppedTurnExitsOne(t *testing.T) {
	f, _ := hostFake(t, []step{{state: wire.AgentStateStopped, ended: wire.EndedReasonProcessDied}})
	out, err := printRun(t, f, options{print: true, json: true})
	if codeOf(err) != exitError || !strings.Contains(out, `"reason":"processDied"`) {
		t.Fatalf("exit %d: %v\n%s", codeOf(err), err, out)
	}
}

func TestDetachPrintsTheIDAndFollowsNothing(t *testing.T) {
	f, _ := hostFake(t, turnSteps)
	out, err := printRun(t, f, options{detach: true})
	if err != nil || out != "AB12CD34-0000-0000-0000-000000000000\n" {
		t.Fatalf("got %q %v", out, err)
	}
	// The kept levels are read before the start (#6651); after it, nothing is followed.
	if len(f.calls) == 0 || !strings.HasSuffix(f.calls[len(f.calls)-1], " agents/start") {
		t.Fatalf("agents/start is the last call: %v", f.calls)
	}
	for _, c := range f.calls[:len(f.calls)-1] {
		if !strings.HasSuffix(c, " preferences/get") {
			t.Fatalf("only the kept levels, then agents/start: %v", f.calls)
		}
	}
}

func TestPrintFlagsNeedPrintMode(t *testing.T) {
	for _, args := range [][]string{{"--json", "x"}, {"--on-ask=fail", "x"}, {"--timeout", "1m", "x"}, {"-p", "--on-ask=maybe", "x"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); codeOf(err) != exitError {
			t.Errorf("%v: %v", args, err)
		}
	}
	o, rest, err := parse([]string{"-p", "--json", "--on-ask=fail", "--timeout", "10m", "fix", "it"})
	if err != nil || !o.print || !o.json || o.onAsk != onAskFail || o.timeout != 10*time.Minute || strings.Join(rest, " ") != "fix it" {
		t.Fatalf("%+v %v %v", o, rest, err)
	}
	if p, err := promptOf(rest, nil); err != nil || p != "fix it" {
		t.Fatalf("the words are the prompt: %q %v", p, err)
	}
	if _, err := promptOf(nil, nil); codeOf(err) != exitError {
		t.Fatalf("no prompt: %v", err)
	}
}

// A streamed reply sends a note for each chunk. Print mode gathers them and reads the
// transcript once for the lot, and the session's record only for a note that is not an
// entry: two reads a chunk used up a client's rate at the control plane (#6473). The
// gather ends long before the burst is taken (a nanosecond), as under load (#6554): the
// notes queued by then are still the same read.
func TestPrintModeReadsOnceForABurstOfEntryNotes(t *testing.T) {
	was, wasGather := recheck, gather
	recheck, gather = time.Hour, time.Nanosecond
	t.Cleanup(func() { recheck, gather = was, wasGather })
	id := wire.UUID("AB12CD34-0000-0000-0000-000000000000")
	running := step{entries: []wire.TranscriptEntry{chunk("m1", "Line 1. ")}, state: wire.AgentStateRunning}
	f, _ := hostFake(t, []step{running, {entries: []wire.TranscriptEntry{chunk("m1", "Line 1. "), chunk("m1", "Line 2.")}, state: wire.AgentStateFinished}})
	notes := make(chan controllink.Note, 1024)
	for range 1000 {
		b, _ := json.Marshal(wire.EntryNotification{AgentID: id})
		notes <- controllink.Note{Host: "h1", Method: wire.NotifyAgentEntry.Name, Params: b}
	}
	b, _ := json.Marshal(wire.Agent{ID: id, State: wire.AgentStateFinished})
	notes <- controllink.Note{Host: "h1", Method: wire.NotifyAgentChanged.Name, Params: b}
	var out bytes.Buffer
	if err := startAndFollow(context.Background(), &out, f, notes, here, options{print: true}, "go"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Line 1. Line 2.\n" {
		t.Fatalf("got %q", out.String())
	}
	reads, lists := 0, 0
	for _, c := range f.calls {
		switch c {
		case "h1 agents/transcript":
			reads++
		case "h1 agents/list":
			lists++
		}
	}
	if reads > 2 || lists > 2 {
		t.Fatalf("1001 notes, %d transcript reads and %d record reads: %v", reads, lists, f.calls)
	}
}

// More than a page of entries since the last read: print mode reads them forward from the
// first not read, so none in the middle is skipped. The daemon's page with from alone is
// the last page, which skipped 57 lines of a reply on a relink (#6478).
func TestPrintModeReadsEveryEntryWhenMoreThanAPageCame(t *testing.T) {
	var all []wire.TranscriptEntry
	want := ""
	for i := range 3*pageLimit + 7 {
		text := fmt.Sprintf("%d ", i)
		all = append(all, chunk("m1", text))
		want += text
	}
	f, _ := hostFake(t, []step{{state: wire.AgentStateRunning}, {entries: all, state: wire.AgentStateFinished}})
	out, err := printRun(t, f, options{print: true})
	if err != nil || out != strings.TrimSpace(want)+"\n" {
		t.Fatalf("got %q %v", out, err)
	}
}
