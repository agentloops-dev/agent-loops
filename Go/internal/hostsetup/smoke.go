package hostsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// SmokePrompt is what step 8 asks: a short answer, then the app's own end-of-turn call.
const SmokePrompt = "This is a check that this machine can run you. Reply with one short sentence that says hello, then call finish_turn. Do not read or change any file."

// SmokeTimeout is how long step 8 waits for the answer (the spec: 2 minutes).
const SmokeTimeout = 2 * time.Minute

var cheapWords = []string{"haiku", "nano", "flash-lite", "lite", "mini", "flash", "small"}

// CheapestModel is the cheapest model a runtime offers, by the first of cheapWords that any
// of its models carries, or nil (the runtime's own default) when none does.
func CheapestModel(options []wire.ConfigOption) *string {
	var model *wire.ConfigOption
	for i := range options {
		if options[i].Type == "select" && options[i].Category != nil && *options[i].Category == "model" {
			model = &options[i]
			break
		}
	}
	if model == nil {
		for i := range options {
			if options[i].Type == "select" && options[i].ID == "model" {
				model = &options[i]
				break
			}
		}
	}
	if model == nil {
		return nil
	}
	type choice struct{ value, words string }
	var choices []choice
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			return
		}
		for _, item := range list {
			var c struct {
				Value   json.RawMessage `json:"value"`
				Name    string          `json:"name"`
				Options json.RawMessage `json:"options"`
			}
			_ = json.Unmarshal(item, &c)
			var v string
			if json.Unmarshal(c.Value, &v) == nil {
				choices = append(choices, choice{v, strings.ToLower(v + " " + c.Name)})
			}
			if len(c.Options) > 0 {
				walk(c.Options)
			}
		}
	}
	walk(json.RawMessage(model.Options))
	for _, w := range cheapWords {
		for _, c := range choices {
			if strings.Contains(c.words, w) {
				v := c.value
				return &v
			}
		}
	}
	return nil
}

func call[T any](d Deps, method string, params any) (T, error) {
	var out T
	raw, err := d.Call(method, params)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

// waitSignedIn waits until the runtime says it is available. A runtime that needs a sign-in
// is step 7's: this step says nothing until it is done, as the control plane takes step 8
// only after step 7.
func waitSignedIn(ctx context.Context, d Deps, runtime string) error {
	deadline := time.Now().Add(d.SignInWait)
	for {
		raw, err := d.Call(wire.CallRuntimesList.Name, wire.Empty{})
		if err != nil {
			return err
		}
		var all []wire.RuntimeStatus
		if err := json.Unmarshal(raw, &all); err != nil {
			return err
		}
		known := false
		for _, r := range all {
			if r.Runtime.ID != runtime {
				continue
			}
			known = true
			if r.Availability.Available != nil {
				return nil
			}
		}
		if !known {
			return fmt.Errorf("the host does not know a runtime called %s", runtime)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s was not signed in within %s", runtime, d.SignInWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Poll):
		}
	}
}

// ScratchProjectOf is the folder the smoke session runs in when the machine has no project:
// inside the host's own folder, so the machine user's home never becomes a folder.
func ScratchProjectOf(home string) string { return filepath.Join(LayoutOf(home).Dir, "smoke") }

// makeScratchProject makes it empty.
func makeScratchProject(home string) (string, error) {
	dir := ScratchProjectOf(home)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("the scratch project: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("the scratch project: %w", err)
	}
	return dir, nil
}

// Smoke is step 8: one short session with the cheapest model of the first runtime, in the
// first project (else a scratch project of its own, never the machine user's home), with a
// fixed prompt. It must answer and end
// its turn; the step says the time to the first word. The session is archived at the end,
// whatever came of it. A step that failed says the runtime's own error, after SmokeTimeout.
func Smoke(ctx context.Context, o Options, d Deps, rep *Reporter, say func(string, ...any)) error {
	d.defaults()
	step := wire.MachineSetupStepIDSmoke
	runtime := DefaultRuntimes[0]
	if len(o.Runtimes) > 0 {
		runtime = o.Runtimes[0]
	}
	var folder string
	if len(o.Projects) > 0 {
		folder = o.Projects[0]
	} else {
		scratch, err := makeScratchProject(o.Home)
		if err != nil {
			rep.Say(step, wire.MachineSetupStateFailed, "", err.Error())
			say("%s: %v", step, err)
			return fmt.Errorf("%s: %w", step, err)
		}
		folder = scratch
		defer func() { _ = os.RemoveAll(scratch) }()
	}
	fail := func(err error) error {
		rep.Say(step, wire.MachineSetupStateFailed, "", err.Error())
		say("%s: %v", step, err)
		return fmt.Errorf("%s: %w", step, err)
	}
	if err := waitSignedIn(ctx, d, runtime); err != nil {
		// Not step 8's failure: it did not begin. Step 7 says why the runtime is not signed in.
		say("Smoke session: not started: %v", err)
		return fmt.Errorf("smoke session: %w", err)
	}
	rep.Say(step, wire.MachineSetupStateRunning, "Starting a session with "+runtime, "")
	cwd := wire.URL((&url.URL{Scheme: "file", Path: folder}).String())
	opts, err := call[wire.OptionsResponse](d, wire.CallAgentsOptions.Name, wire.OptionsRequest{RuntimeID: runtime, Cwd: cwd, McpServers: []wire.MCPServer{}})
	if err != nil {
		return fail(err)
	}
	start := wire.StartRequest{RuntimeID: runtime, Cwd: cwd, Prompt: SmokePrompt, DraftID: &opts.DraftID,
		Attachments: []wire.Attachment{}, AdditionalDirectories: []wire.URL{}, McpServers: []wire.MCPServer{},
		Labels: []string{"machine-smoke"}}
	model := CheapestModel(opts.Options)
	if model != nil {
		raw, _ := json.Marshal(*model)
		start.StartOptions = wire.StartOptions{Values: map[string]wire.JSONValue{"model": raw}}
	}
	began := time.Now()
	id, err := call[wire.UUID](d, wire.CallAgentsStart.Name, start)
	if err != nil {
		return fail(err)
	}
	// Archive at the end, also when the run was cut off.
	defer func() {
		_, _ = d.Call(wire.CallAgentsArchive.Name, wire.AgentRequest{AgentID: id})
	}()

	deadline := began.Add(d.SmokeWait)
	var first time.Duration
	for {
		agents, err := call[[]wire.Agent](d, wire.CallAgentsList.Name, wire.ListRequest{})
		if err != nil {
			return fail(err)
		}
		agent := findAgent(agents, id)
		if agent == nil {
			return fail(fmt.Errorf("the session is not on the host any more"))
		}
		page, err := call[wire.TranscriptPage](d, wire.CallAgentsTranscript.Name, wire.TranscriptRequest{AgentID: id, Limit: 200})
		if err != nil {
			return fail(err)
		}
		answered, finished := smokeReplied(page.Entries)
		if answered && first == 0 {
			first = time.Since(began)
			rep.Say(step, wire.MachineSetupStateRunning, "First word after "+shortDuration(first), "")
		}
		ended := agent.State == wire.AgentStateFinished || agent.State == wire.AgentStateStopped || agent.State == wire.AgentStateArchived
		switch {
		case answered && (finished || ended):
			text := smokeAnswered(first, finished, model)
			rep.Say(step, wire.MachineSetupStateDone, text, "")
			say("Smoke session: %s", text)
			return nil
		case ended:
			why := "the session ended with no answer"
			if agent.EndedReason != nil {
				why += " (" + string(*agent.EndedReason) + ")"
			}
			return fail(fmt.Errorf("%s", why))
		case agent.State == wire.AgentStateWaitingOnUser:
			return fail(fmt.Errorf("the session stopped to ask a question; it was told to only say hello"))
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("the session did not answer in %s", d.SmokeWait))
		}
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(d.Poll):
		}
	}
}

// findAgent is the session with this id in the host's list, or nil.
func findAgent(agents []wire.Agent, id wire.UUID) *wire.Agent {
	var agent *wire.Agent
	for i := range agents {
		if agents[i].ID == id {
			agent = &agents[i]
		}
	}
	return agent
}

// smokeReplied says whether the transcript has an answer with words, and whether the session
// called finish_turn.
func smokeReplied(entries []wire.TranscriptEntry) (answered, finished bool) {
	for _, e := range entries {
		switch {
		case e.Kind.AgentMessage != nil && strings.TrimSpace(e.Kind.AgentMessage.Text) != "":
			answered = true
		case e.Kind.ToolCall != nil:
			c := e.Kind.ToolCall.Value
			name := c.Title
			if c.Name != nil {
				name = *c.Name
			}
			if strings.HasSuffix(name, "finish_turn") {
				finished = true
			}
		}
	}
	return answered, finished
}

// smokeAnswered is the done line of step 8.
func smokeAnswered(first time.Duration, finished bool, model *string) string {
	text := "Answered, first word after " + shortDuration(first)
	if !finished {
		text += " (it did not call finish_turn)"
	}
	if model != nil {
		text += ", model " + *model
	}
	return text
}

func shortDuration(d time.Duration) string {
	if d < time.Second {
		return "under a second"
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}
