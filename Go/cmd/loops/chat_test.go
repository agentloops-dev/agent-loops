package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestKeysSendOnEnterAddALineOnOptionEnterAndKeepAPastesLines(t *testing.T) {
	var k keyReader
	got := k.feed([]byte("hi\x1b\rthere\r"))
	want := []key{{kind: keyText, text: "hi"}, {kind: keyNewline}, {kind: keyText, text: "there"}, {kind: keyEnter}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("⌥⏎ as Esc+⏎ is a new line, ⏎ sends: %v", got)
	}
	got = k.feed([]byte("\x1b[200~a\rb\x1b[201~\r\x1b[13;3u\x03\x04\x7f\x15\x1b[A"))
	want = []key{{kind: keyText, text: "a\nb"}, {kind: keyEnter}, {kind: keyNewline}, {kind: keyInterrupt}, {kind: keyEOF}, {kind: keyBackspace}, {kind: keyClear}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("a paste keeps its lines; CSI u ⌥⏎; ^C ^D ⌫ ^U; an arrow is dropped: %v", got)
	}
	// An escape and a character cut in two by a read come whole with the next.
	if got = k.feed([]byte("\x1b[20")); len(got) != 0 {
		t.Fatalf("half an escape is held: %v", got)
	}
	if got = k.feed([]byte("0~é"[:3])); len(got) != 0 || !k.pasting {
		t.Fatalf("the paste begins, half a rune is held: %v", got)
	}
	if got = k.feed([]byte("é"[1:])); len(got) != 1 || got[0].text != "é" {
		t.Fatalf("the rune comes whole: %v", got)
	}
	got = k.feed([]byte("\x1b[201~\x1b]11;rgb:ffff/ffff/ffff\x07\x1b]11;rgb:1a1a/1a1a/1919\x1b\\"))
	if len(got) != 2 || got[0].kind != keyBackground || got[0].dark || !got[1].dark {
		t.Fatalf("the terminal's background, light then dark: %v", got)
	}
}

func TestColourIsTheDesignTokensOnlyOnATerminalAndNeverWithNoColor(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if s := detectStyle(false, env(nil)); s.on {
		t.Fatal("no colour when stdout is not a terminal")
	}
	if s := detectStyle(true, env(map[string]string{"NO_COLOR": "1"})); s.on {
		t.Fatal("NO_COLOR turns colour off")
	}
	full := detectStyle(true, env(map[string]string{"COLORTERM": "truecolor", "LOOPS_THEME": "light"}))
	if !full.on || !full.full || full.dark {
		t.Fatalf("24-bit and light: %+v", full)
	}
	if got := full.paint(roleBrand, "x"); got != "\x1b[38;2;26;82;192mx\x1b[39m" {
		t.Fatalf("the accent is the token's light #1a52c0: %q", got)
	}
	cubed := detectStyle(true, env(map[string]string{"COLORFGBG": "15;0"}))
	if !cubed.dark || cubed.full {
		t.Fatalf("COLORFGBG 15;0 is a dark background, with no COLORTERM the 256 colours: %+v", cubed)
	}
	if got := cubed.paint(roleBrand, "x"); got != "\x1b[38;5;111mx\x1b[39m" {
		t.Fatalf("the accent's dark #86b4ff is cube colour 111: %q", got)
	}
	for _, r := range []role{roleBrand, roleYou, roleQuiet, roleAttention, roleWarning, roleFailure, roleDone} {
		if tokenHex(string(r), false) == "" || tokenHex(string(r), true) == "" {
			t.Fatalf("%s is not a colour token", r)
		}
	}
	if plain.paint(roleBrand, "x") != "x" || visibleWidth(full.paint(roleBrand, "abc")) != 3 {
		t.Fatal("plain adds nothing; escapes take no cells")
	}
}

func TestTheTranscriptStreamsAMessageUnderItsSpeakerAndShowsAnEntryOnce(t *testing.T) {
	tr := newTranscript(plain)
	msg := func(id, mid, text string) wire.TranscriptEntry {
		return wire.TranscriptEntry{ID: wire.UUID(id), Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{MessageID: ptr(mid), Text: text}}}
	}
	var lines []string
	lines = append(lines, tr.entry(wire.TranscriptEntry{ID: "1", Kind: wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: "the test\nstill times out"}}})...)
	lines = append(lines, tr.entry(msg("2", "m", "The stop waits"))...)
	lines = append(lines, tr.entry(msg("3", "m", " on the list.\nI made"))...)
	if tr.partial() != indent+"I made" {
		t.Fatalf("the line not ended yet is partial: %q", tr.partial())
	}
	lines = append(lines, tr.entry(msg("3", "m", "again"))...)
	lines = append(lines, tr.entry(wire.TranscriptEntry{ID: "4", Kind: wire.TranscriptEntryKind{ToolCall: &wire.TranscriptEntryKindToolCall{Value: wire.ToolCall{Title: "Edit stop.go"}}}})...)
	lines = append(lines, tr.entry(wire.TranscriptEntry{ID: "5", Kind: wire.TranscriptEntryKind{ToolCallUpdate: &wire.TranscriptEntryKindToolCallUpdate{Value: wire.ToolCall{Title: "go test", Status: ptr("failed")}}}})...)
	want := []string{
		"  you    the test",
		"         still times out",
		"  agent  The stop waits on the list.",
		"         I made",
		"         · Edit stop.go",
		"         ✗ go test failed",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	tr.width = 30
	got := tr.entry(msg("7", "n", "one two three four five six seven eight nine"))
	if strings.Join(got, "|") != "  agent  one two three four|         five six seven eight" || tr.partial() != indent+"nine" {
		t.Fatalf("a long line wraps under the indent as it comes: %q %q", got, tr.partial())
	}
	tr.width = 0
	tr.end()
	tr.seenText = "and the docs"
	if got := tr.entry(wire.TranscriptEntry{ID: "6", Kind: wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: "and the docs"}}}); len(got) != 0 {
		t.Fatalf("a prompt this terminal showed is not shown again: %q", got)
	}
}

func TestACardTakesANumberForAPermissionAndAsksAFormsFieldsInTurn(t *testing.T) {
	p := &card{permission: &wire.PermissionRequest{ID: "P", ToolCall: wire.ToolCall{Title: "Run scripts/docs-parts.py"},
		Options: []wire.PermissionOption{{OptionID: "once", Name: "Allow once"}, {OptionID: "always", Name: "Allow always in this folder"}, {OptionID: "no", Name: "Deny"}}}}
	box := strings.Join(p.lines(plain, "agent-loops", 80), "\n")
	for _, want := range []string{"╭─ Permission ─", "│ Run scripts/docs-parts.py in agent-loops?", "│  1  Allow once", "│  3  Deny", "╰─ answer here, or on any client (Needs you) ─"} {
		if !strings.Contains(box, want) {
			t.Fatalf("no %q in\n%s", want, box)
		}
	}
	if _, done, problem := p.answer("4"); done || problem == "" {
		t.Fatal("4 of 3 is not an answer")
	}
	if id, done, _ := p.answer(" 2 "); !done || id != "always" {
		t.Fatalf("2 is the second option: %q", id)
	}
	form := &wire.ElicitationRequest{ID: "E", Message: ptr("Which way?"), Mode: wire.ElicitationRequestMode{Form: &wire.ElicitationRequestModeForm{Value: wire.ElicitationSchema{Properties: []wire.ElicitationSchemaProperty{
		{Name: "way", IsRequired: true, Kind: wire.ElicitationSchemaPropertyKind{String: &wire.ElicitationSchemaPropertyKindString{Choices: []wire.ElicitationSchemaPropertyChoice{{Value: "a", Title: "Fix forward"}, {Value: "b", Title: "Revert"}}}}},
		{Name: "go", IsRequired: true, Kind: wire.ElicitationSchemaPropertyKind{Boolean: &struct{}{}}},
		{Name: "why", Kind: wire.ElicitationSchemaPropertyKind{String: &wire.ElicitationSchemaPropertyKindString{}}},
	}}}}}
	c := &card{elicitation: form}
	if box := strings.Join(c.lines(plain, "", 80), "\n"); !strings.Contains(box, "Question 1 of 3") || !strings.Contains(box, "1  Fix forward") {
		t.Fatalf("the first field:\n%s", box)
	}
	if _, done, problem := c.answer(""); done || problem == "" {
		t.Fatal("a required field needs an answer")
	}
	if _, done, _ := c.answer("2"); done {
		t.Fatal("two fields are left")
	}
	if _, done, _ := c.answer("1"); done {
		t.Fatal("one field is left")
	}
	if _, done, _ := c.answer(""); !done {
		t.Fatal("the last field is not required")
	}
	b, _ := json.Marshal(c.content)
	if string(b) != `{"go":true,"way":"b"}` {
		t.Fatalf("the content: %s", b)
	}
	secret := &card{elicitation: &wire.ElicitationRequest{ID: "S", Secret: &wire.ElicitationSecret{Name: "TOKEN"}, Mode: form.Mode}}
	if secret.answerable() {
		t.Fatal("a secret is never typed into the terminal's chat")
	}
}

func TestResumeNamesASessionByTheStartOfItsIDOrByItsTitle(t *testing.T) {
	list := []wire.Agent{
		{ID: "7F3A2C00-0000-0000-0000-000000000001", Title: ptr("Update the parity table")},
		{ID: "7F3A2C11-0000-0000-0000-000000000002", Title: ptr("Fix the flaky stop test")},
		{ID: "C40D1100-0000-0000-0000-000000000003", Title: ptr("Fix the docs")},
	}
	for want, id := range map[string]wire.UUID{"c40d11": list[2].ID, "7f3a2c1": list[1].ID, "update the parity table": list[0].ID, "flaky": list[1].ID} {
		got, err := match(list, want)
		if err != nil || got.ID != id {
			t.Fatalf("%q: %v %v", want, got, err)
		}
	}
	if _, err := match(list, "7f3a2c"); err == nil || !strings.Contains(err.Error(), "2 sessions match") {
		t.Fatalf("a prefix two share names neither: %v", err)
	}
	if _, err := match(list, "nothing"); codeOf(err) != exitError {
		t.Fatalf("no match: %v", err)
	}
}

// syncBuffer is a bytes.Buffer the chat writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// chatFake is a control plane for the chat: calls answered by functions, notes sent by the test.
type chatFake struct {
	mu     sync.Mutex
	answer map[string]func(params any) any
	params map[string][]any
	ch     chan controllink.Note
}

func newChatFake() *chatFake {
	return &chatFake{answer: map[string]func(any) any{}, params: map[string][]any{}, ch: make(chan controllink.Note, 64)}
}

func (f *chatFake) Call(_ context.Context, _, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	f.params[method] = append(f.params[method], params)
	a := f.answer[method]
	f.mu.Unlock()
	if a == nil {
		return json.RawMessage(`{}`), nil
	}
	b, _ := json.Marshal(a(params))
	return b, nil
}

func (f *chatFake) notes() <-chan controllink.Note { return f.ch }

func (f *chatFake) called(method string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.params[method]
}

func (f *chatFake) note(method string, v any) {
	b, _ := json.Marshal(v)
	f.ch <- controllink.Note{Host: "H", Method: method, Params: b}
}

var chatHere = place{machine: wire.ControlMachine{ID: "H", Name: "Alex's MacBook Pro"}, folder: "file:///src/agent-loops/"}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAPromptStartsASessionFromTheCLIAndAScriptFollowsItToTheEndOfItsTurn(t *testing.T) {
	f := newChatFake()
	f.answer["agents/start"] = func(any) any { return "B21E9000-0000-0000-0000-000000000000" }
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), f, chatHere, options{}, chatArgs{text: "update the parity table"}, terminal{out: out, st: plain})
	}()
	waitFor(t, "the start", func() bool { return len(f.called("presence/report")) == 1 })
	start := f.called("agents/start")[0].(wire.StartRequest)
	if start.RuntimeID != autoRuntime || start.Cwd != chatHere.folder || start.Prompt != "update the parity table" || start.StartedVia == nil || *start.StartedVia != wire.StartedViaCLI {
		t.Fatalf("the start: %+v", start)
	}
	if r := f.called("presence/report")[0].(wire.PresenceReport); r.Showing == nil || *r.Showing != "B21E9000-0000-0000-0000-000000000000" {
		t.Fatalf("the terminal shows the new session, so its entries come: %+v", r)
	}
	id := wire.UUID("b21e9000-0000-0000-0000-000000000000")
	f.note("agent/changed", wire.Agent{ID: id, State: wire.AgentStateRunning})
	f.note("agent/entry", wire.EntryNotification{AgentID: id, Entry: wire.TranscriptEntry{ID: "U", Kind: wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: "update the parity table"}}}})
	f.note("agent/entry", wire.EntryNotification{AgentID: "OTHER", Entry: wire.TranscriptEntry{ID: "X", Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: "not this one"}}}})
	f.note("agent/entry", wire.EntryNotification{AgentID: id, Entry: wire.TranscriptEntry{ID: "A", Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{MessageID: ptr("m"), Text: "Reading docs-parts/parity/…"}}}})
	f.note("agent/changed", wire.Agent{ID: id, State: wire.AgentStateFinished})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the chat did not end with the turn")
	}
	got := out.String()
	for _, want := range []string{`Started "update the parity table" · b21e90 · auto · in agent-loops`, "  you    update the parity table\n", "  agent  Reading docs-parts/parity/…\n", "● done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
	if strings.Count(got, "update the parity table\n") != 1 || strings.Contains(got, "not this one") {
		t.Fatalf("the prompt shows once, another session's entries not at all:\n%s", got)
	}
}

func TestContinuingShowsTheLastEntriesAndAnswersAWaitingCardInTheTerminal(t *testing.T) {
	f := newChatFake()
	id := wire.UUID("C40D1100-0000-0000-0000-000000000003")
	f.answer["agents/list"] = func(any) any {
		return []wire.Agent{{ID: id, Title: ptr("Fix the flaky stop test"), RuntimeID: "claude", State: wire.AgentStateWaitingOnUser,
			StartOptions:   wire.StartOptions{Values: map[string]wire.JSONValue{"model": json.RawMessage(`"opus-5.5"`)}},
			LastActivityAt: wire.Date(time.Since(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)).Seconds() - 720)}, {ID: "OTHER"}}
	}
	f.answer["agents/transcript"] = func(any) any {
		return wire.TranscriptPage{FirstIndex: 41, Total: 43, Entries: []wire.TranscriptEntry{
			{ID: "1", Kind: wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: "the test still times out under -race"}}},
			{ID: "2", Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: "I want to run: scripts/docs-parts.py"}}},
		}}
	}
	f.answer["permissions/pending"] = func(any) any {
		return []wire.PermissionRequest{{ID: "P1", AgentID: id, ToolCall: wire.ToolCall{Title: "Run scripts/docs-parts.py"},
			Options: []wire.PermissionOption{{OptionID: "once", Name: "Allow once"}, {OptionID: "no", Name: "Deny"}}}}
	}
	f.answer["elicitations/pending"] = func(any) any { return []wire.ElicitationRequest{} }
	keys := make(chan key, 8)
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), f, chatHere, options{}, chatArgs{}, terminal{out: out, st: plain, keys: keys, raw: true, width: func() int { return 80 }})
	}()
	waitFor(t, "the card", func() bool { return strings.Contains(out.String(), "Allow once") })
	keys <- key{kind: keyText, text: "1"}
	keys <- key{kind: keyEnter}
	waitFor(t, "the answer", func() bool { return len(f.called("permissions/answer")) == 1 })
	if a := f.called("permissions/answer")[0].(wire.AnswerRequest); a.PermissionID != "P1" || a.OptionID != "once" {
		t.Fatalf("1 is Allow once: %+v", a)
	}
	f.note("agent/permission", wire.PermissionNotification{AgentID: id, RequestID: "P1"})
	keys <- key{kind: keyText, text: "and the docs"}
	keys <- key{kind: keyEnter}
	waitFor(t, "the prompt", func() bool { return len(f.called("agents/prompt")) == 1 })
	if pr := f.called("agents/prompt")[0].(wire.PromptRequest); pr.AgentID != id || pr.Text != "and the docs" || pr.From != wire.PromptOriginPerson {
		t.Fatalf("the prompt: %+v", pr)
	}
	keys <- key{kind: keyEOF}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("^D did not leave")
	}
	if len(f.called("agents/stop")) != 0 {
		t.Fatal("leaving never stops the session")
	}
	got := out.String()
	for _, want := range []string{"Agent Loops · agent-loops (Alex's MacBook Pro) · claude · opus-5.5", `Continuing "Fix the flaky stop test" · needs you 12 min · 1 other session here (loops ls)`,
		"… 41 earlier entries", "  agent  I want to run: scripts/docs-parts.py", "✓ answered", "  you    and the docs", "The session goes on. Continue it: loops -r c40d11"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%q", want, got)
		}
	}
	if strings.Contains(got, "answered on another client") {
		t.Fatal("the card answered here is not said to be answered elsewhere")
	}
}

func TestControlCStopsTheTurnAndASecondOneLeaves(t *testing.T) {
	f := newChatFake()
	f.answer["agents/start"] = func(any) any { return "B21E9000-0000-0000-0000-000000000000" }
	keys := make(chan key, 8)
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), f, chatHere, options{}, chatArgs{text: "go"}, terminal{out: out, st: plain, keys: keys, raw: true})
	}()
	keys <- key{kind: keyInterrupt}
	waitFor(t, "the stop", func() bool { return len(f.called("agents/stop")) == 1 })
	keys <- key{kind: keyInterrupt}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second ^C did not leave")
	}
	if len(f.called("agents/stop")) != 1 {
		t.Fatal("the second ^C leaves; it stops nothing more")
	}
}

// A bare loops in a script (no terminal, no prompt) on an idle session prints its last
// entries and ends; it waited for ever (#6473). A full first page says the others are more.
func TestWithNoTerminalAnIdleSessionPrintsItsLastEntriesAndEnds(t *testing.T) {
	f := newChatFake()
	f.answer["agents/list"] = func(any) any {
		list := []wire.Agent{{ID: "C40D1100-0000-0000-0000-000000000003", Title: ptr("Fix the flaky stop test"), State: wire.AgentStateFinished}}
		for len(list) < pageSize {
			list = append(list, wire.Agent{ID: wire.UUID(fmt.Sprintf("%08X-0000-0000-0000-000000000000", len(list)))})
		}
		return list
	}
	f.answer["agents/transcript"] = func(any) any {
		return wire.TranscriptPage{Total: 1, Entries: []wire.TranscriptEntry{{ID: "1", Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: "Tests pass 20 of 20."}}}}}
	}
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), f, chatHere, options{}, chatArgs{}, terminal{out: out, st: plain})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loops with no terminal waited on an idle session")
	}
	got := out.String()
	for _, want := range []string{"Tests pass 20 of 20.", "19+ other sessions here (loops ls)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
}

// -r with a whole id, as a script has it from -p --json, is one agents/list by that id.
func TestResumingByAWholeIDIsOneRead(t *testing.T) {
	f := newChatFake()
	id := wire.UUID("B21E9061-C89B-4511-9842-7374C9FAD876")
	f.answer["agents/list"] = func(any) any {
		return []wire.Agent{{ID: id, Title: ptr("Summarise CI"), State: wire.AgentStateFinished}}
	}
	f.answer["agents/transcript"] = func(any) any { return wire.TranscriptPage{} }
	err := runChat(context.Background(), f, chatHere, options{}, chatArgs{resume: "b21e9061c89b45119842-7374c9fad876"}, terminal{out: &syncBuffer{}, st: plain})
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.called("agents/list")
	if len(reqs) != 1 {
		t.Fatalf("one read, not every page: %d", len(reqs))
	}
	if r := reqs[0].(wire.ListRequest); r.AgentID == nil || *r.AgentID != id || r.Folder == nil {
		t.Fatalf("by the id, in this folder: %+v", r)
	}
}
