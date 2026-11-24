package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func commandOption(id, name, category, current, options string) wire.ConfigOption {
	return wire.ConfigOption{ID: id, Name: name, Category: ptr(category), Type: "select", CurrentValue: jsonString(current), Options: wire.JSONValue(options)}
}

func TestLoopsSlashCommandsBesideTheRuntimes(t *testing.T) {
	id := wire.UUID("B21E9000-0000-0000-0000-000000000000")
	f := newChatFake()
	f.answer["agents/list"] = func(params any) any {
		req := params.(wire.ListRequest)
		if req.AgentID != nil {
			return []wire.Agent{{ID: id, Title: ptr("Slash command check"), State: wire.AgentStateFinished, RuntimeID: "claude"}}
		}
		return []wire.Agent{{ID: id, Title: ptr("Slash command check"), State: wire.AgentStateFinished, RuntimeID: "claude"}}
	}
	f.answer["agents/options"] = func(params any) any {
		req := params.(wire.OptionsRequest)
		if req.RuntimeID != "claude" {
			t.Errorf("options for %q", req.RuntimeID)
		}
		return wire.OptionsResponse{
			DraftID: "D1",
			Options: []wire.ConfigOption{
				commandOption("model", "Model", "model", "opus-5.5", `[{"value":"opus-5.5","name":"Opus 5.5"},{"value":"sonnet-5.5","name":"Sonnet 5.5"}]`),
				commandOption("mode", "Permission mode", "mode", "ask", `[{"value":"ask","name":"Ask"},{"value":"approveForMe","name":"Approve for me"}]`),
				commandOption("reasoning_effort", "Effort", "thought_level", "medium", `[{"value":"low","name":"Low"},{"value":"high","name":"High"}]`),
			},
			Commands: []wire.SlashCommand{
				{Name: "compact", Description: ptr("Compact the conversation")},
				{Name: "mode", Description: ptr("The runtime's own mode")},
			},
		}
	}
	keys := make(chan key, 32)
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), f, chatHere, options{}, chatArgs{}, terminal{out: out, st: plain, keys: keys, raw: true, width: func() int { return 80 }})
	}()
	send := func(text string) {
		keys <- key{kind: keyText, text: text}
		keys <- key{kind: keyEnter}
	}
	send("/mode edits")
	send("/model 2")
	send("/effort high")
	send("/rename Slash command check")
	send("/status")
	send("/help")
	send("/mode nope")
	send("/unknown")
	send("/exit")
	waitFor(t, "the slash commands", func() bool {
		return len(f.called("agents/rename")) == 1 && len(f.called("agents/prompt")) == 1 &&
			len(f.called("agents/setLevels")) == 1 && len(f.called("agents/setOption")) == 2
	})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/exit did not leave")
	}
	if len(f.called("agents/options")) != 1 || len(f.called("agents/discardDraft")) != 1 {
		t.Fatalf("options were read once and the draft discarded: %d, %d", len(f.called("agents/options")), len(f.called("agents/discardDraft")))
	}
	if len(f.called("agents/stop")) != 0 {
		t.Fatal("leaving never stops the session")
	}
	levels := f.called("agents/setLevels")[0].(wire.SetLevelsRequest)
	if levels.AgentID != id || levels.Levels.Permission == nil || *levels.Levels.Permission != wire.PermissionLevelApproveForMe {
		t.Fatalf("/mode edits sets approve-for-me on the session: %+v", levels)
	}
	first, second := f.called("agents/setOption")[0].(wire.SetOptionRequest), f.called("agents/setOption")[1].(wire.SetOptionRequest)
	if first.AgentID != id || first.OptionID != "model" || string(first.Value) != `"sonnet-5.5"` {
		t.Fatalf("/model 2 chooses the second model: %+v", first)
	}
	if second.AgentID != id || second.OptionID != "reasoning_effort" || string(second.Value) != `"high"` {
		t.Fatalf("/effort high uses the effort option: %+v", second)
	}
	if rename := f.called("agents/rename")[0].(wire.RenameRequest); rename.AgentID != id || rename.Title != "Slash command check" {
		t.Fatalf("the rename: %+v", rename)
	}
	if prompt := f.called("agents/prompt")[0].(wire.PromptRequest); prompt.Text != "/unknown" {
		t.Fatalf("an unknown slash command goes to the runtime: %+v", prompt)
	}
	got := out.String()
	for _, want := range []string{
		"› /mode edits", "Mode set to approve-for-me. It applies from the next step.",
		"Model set to Sonnet 5.5. It applies from the next step.",
		"Effort set to High. It applies from the next step.",
		`Renamed to "Slash command check".`,
		"Session b21e90", "runtime: claude", "model:   Sonnet 5.5", "mode:    approve-for-me", "effort:  High",
		"folder:  agent-loops", "machine: Alex's MacBook Pro",
		"loops commands", "/mode [name]", "This runtime's commands", "/compact Compact the conversation",
		`"nope" is not a mode. Offered: ask, approveForMe.`,
		"claude · Sonnet 5.5 · approve-for-me · High",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "› /help  compact") || strings.Contains(got, "› /mode edits  compact") {
		t.Fatal("a loops command is not sent to the runtime too")
	}
}

func TestNewAndResumeFromThePrompt(t *testing.T) {
	id := wire.UUID("C40D1100-0000-0000-0000-000000000001")
	resumed := wire.UUID("C40D1100-0000-0000-0000-000000000002")
	f := newChatFake()
	starts := 0
	f.answer["agents/start"] = func(params any) any {
		starts++
		return fmt.Sprintf("%08X-0000-0000-0000-00000000000%d", starts, starts)
	}
	f.answer["agents/list"] = func(params any) any {
		req := params.(wire.ListRequest)
		if req.AgentID != nil {
			return []wire.Agent{{ID: id, Title: ptr("First"), State: wire.AgentStateFinished, RuntimeID: "claude"}}
		}
		return []wire.Agent{
			{ID: id, Title: ptr("First"), State: wire.AgentStateFinished, RuntimeID: "claude"},
			{ID: resumed, Title: ptr("Resumed session"), State: wire.AgentStateFinished, RuntimeID: "codex"},
		}
	}
	keys := make(chan key, 16)
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), f, chatHere, options{}, chatArgs{}, terminal{out: out, st: plain, keys: keys, raw: true, width: func() int { return 80 }})
	}()
	send := func(text string) {
		keys <- key{kind: keyText, text: text}
		keys <- key{kind: keyEnter}
	}
	send("/new")
	send("second")
	send("/resume")
	send("2")
	keys <- key{kind: keyEnter}
	send("/exit")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/exit did not leave")
	}
	if starts != 1 {
		t.Fatalf("one new start, not %d", starts)
	}
	if len(f.called("agents/stop")) != 0 {
		t.Fatal("/new leaves a session; it does not stop it")
	}
	got := out.String()
	for _, want := range []string{"Left c40d11", "Started a new session; type what it should do.", "Started \"second\"", "Resume a session by its number", `Continuing "Resumed session"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
}

func TestCliCommandsNeedASessionExceptHelpAndExit(t *testing.T) {
	f := newChatFake()
	f.answer["agents/list"] = func(any) any { return []wire.Agent{} }
	out := &syncBuffer{}
	keys := make(chan key, 16)
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), f, chatHere, options{}, chatArgs{}, terminal{out: out, st: plain, keys: keys, raw: true, width: func() int { return 80 }})
	}()
	for _, text := range []string{"/help", "/mode ask", "/model fast", "/effort high", "/status", "/rename X", "/stop", "/resume"} {
		keys <- key{kind: keyText, text: text}
		keys <- key{kind: keyEnter}
	}
	keys <- key{kind: keyEOF}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("^D did not leave with no session")
	}
	got := out.String()
	for _, want := range []string{"loops commands", "Start a session to see its runtime's commands."} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
	if n := strings.Count(got, "start a session first"); n != 7 {
		t.Fatalf("every command but /help needs a session, got %d: %s", n, got)
	}
	if len(f.called("agents/options")) != 0 || len(f.called("agents/setLevels")) != 0 || len(f.called("agents/setOption")) != 0 {
		t.Fatal("no command changed settings with no session")
	}
}

func TestCliChoicesAcceptNumbersValuesAndNames(t *testing.T) {
	o := commandOption("model", "Model", "model", "bal", `[{"value":"fast","name":"Fast","options":[{"value":"bal","name":"Balanced"}]}]`)
	if got := optionFor([]wire.ConfigOption{commandOption("effort", "Effort", "thought_level", "low", `[]`), o}, "model"); got == nil || got.ID != "model" {
		t.Fatalf("the model option: %+v", got)
	}
	if got := optionFor([]wire.ConfigOption{o, commandOption("permissions", "Mode", "mode", "ask", `[]`)}, "mode"); got == nil || got.ID != "permissions" {
		t.Fatalf("the mode option: %+v", got)
	}
	choices := choicesOfOption(&o)
	if len(choices) != 2 || choices[0].value != "fast" || choices[1].name != "Balanced" {
		t.Fatalf("flat and nested choices: %+v", choices)
	}
	for _, c := range []struct {
		arg  string
		want string
	}{{"1", "fast"}, {"Balanced", "bal"}, {"FAST", "fast"}} {
		got, ok := resolveChoice(choices, c.arg)
		if !ok || got.value != c.want {
			t.Fatalf("%q chose %+v, %v", c.arg, got, ok)
		}
	}
	if _, ok := resolveChoice(choices, "3"); ok {
		t.Fatal("an out-of-range number chooses nothing")
	}
	if permissionName(wire.PermissionLevelApproveForMe) != "approve-for-me" || modelName(wire.ModelLevelSmart) != "smart" {
		t.Fatal("level names")
	}
}
