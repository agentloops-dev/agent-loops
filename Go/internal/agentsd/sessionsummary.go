package agentsd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/acp"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A session's summary (#7321, epic #7318): a short paragraph of what is going on, written by
// one call to the session's own runtime with its fastest model and no tools, when a client
// opens the session in summary mode (agents/summarise). Not the agent: it sends it no prompt
// (#479). Nothing is written until a client asks, and the flag that shows the view is the
// client's.

const (
	// summaryTextLimit is the most characters of text a summary keeps.
	summaryTextLimit = 600
	// summaryInputLimit is the most bytes of the last summary and the turns a call reads.
	summaryInputLimit = 8 * 1024
	// summaryAnswerLimit is the most bytes read back from the runtime before the text is cut.
	summaryAnswerLimit = 16 * 1024
	// summaryWait is how long one call has to start, answer and stop.
	summaryWait = 120 * time.Second
)

type summaryBook struct {
	summaryMu      sync.Mutex
	summaryRunning map[wire.UUID]bool
	// summaryWrite is the call a write makes; nil is askRuntimeForSummary. A test's.
	summaryWrite func(ctx context.Context, d *Daemon, a wire.Agent, prompt string) (text, model string, err error)
}

// summarise is agents/summarise.
func (d *Daemon) summarise(r wire.AgentRequest) (wire.SessionSummary, error) {
	a := d.Store.Agent(r.AgentID)
	if a == nil {
		return wire.SessionSummary{}, &Error{Code: CodeNoSuchAgent, Message: "no agent " + string(r.AgentID)}
	}
	have := wire.SessionSummary{State: wire.SessionSummaryStateWritten}
	if a.Summary != nil {
		have = *a.Summary
	}
	page, err := d.turns(wire.TurnsRequest{AgentID: r.AgentID, Limit: turnsLimitCeiling})
	if err != nil {
		return have, err
	}
	fresh := summaryTurnsSince(page.Turns, have.ThroughTurn)
	if len(fresh) == 0 {
		return have, nil
	}
	d.summaryMu.Lock()
	if d.summaryRunning[r.AgentID] {
		d.summaryMu.Unlock()
		have.State = wire.SessionSummaryStateWriting
		return have, nil
	}
	if d.summaryRunning == nil {
		d.summaryRunning = map[wire.UUID]bool{}
	}
	d.summaryRunning[r.AgentID] = true
	d.summaryMu.Unlock()
	through := fresh[len(fresh)-1].ID
	prompt := summaryPrompt(have.Text, fresh)
	d.goJob("session summary", func() { d.writeSummary(*a, through, prompt) })
	have.State = wire.SessionSummaryStateWriting
	d.keepSummary(r.AgentID, have)
	return have, nil
}

func (d *Daemon) endSummary(id wire.UUID) {
	d.summaryMu.Lock()
	delete(d.summaryRunning, id)
	d.summaryMu.Unlock()
}

// keepSummary saves the summary on the agent's record, and says so (agents/changed).
func (d *Daemon) keepSummary(id wire.UUID, s wire.SessionSummary) {
	d.changeAgent(id, false, func(a *wire.Agent) { a.Summary = &s })
}

func (d *Daemon) writeSummary(a wire.Agent, through wire.UUID, prompt string) {
	defer d.endSummary(a.ID)
	have := wire.SessionSummary{State: wire.SessionSummaryStateWritten}
	if a.Summary != nil {
		have = *a.Summary
	}
	write := d.summaryWrite
	if write == nil {
		write = askRuntimeForSummary
	}
	ctx, cancel := context.WithTimeout(d.ctx, summaryWait)
	defer cancel()
	text, model, err := write(ctx, d, a, prompt)
	text = cleanSummary(text)
	if err != nil || text == "" {
		if err == nil {
			err = fmt.Errorf("the runtime wrote no text")
		}
		log.Printf("agent %s: its summary was not written: %v", a.ID, err)
		have.State = wire.SessionSummaryStateFailed
		d.keepSummary(a.ID, have)
		return
	}
	d.keepSummary(a.ID, wire.SessionSummary{Text: text, At: new(now()), ThroughTurn: &through,
		State: wire.SessionSummaryStateWritten, Model: &model})
}

// cleanSummary is the answer as a summary keeps it: trimmed, and cut to its limit of characters.
func cleanSummary(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > summaryTextLimit {
		s = strings.TrimSpace(string(r[:summaryTextLimit-1])) + "…"
	}
	return s
}

// summaryTurnsSince is the turns that ended after the one the summary last read; all of
// them when it read none, or when that turn is gone (trimmed).
func summaryTurnsSince(turns []wire.TurnSummary, through *wire.UUID) []wire.TurnSummary {
	if through != nil {
		for i, t := range turns {
			if t.ID == *through {
				return turns[i+1:]
			}
		}
	}
	return turns
}

// summaryPrompt is what one call reads: the last summary, then the turns since it, the
// newest kept when they do not fit summaryInputLimit.
func summaryPrompt(last string, turns []wire.TurnSummary) string {
	head := "You keep a short summary of a coding agent's session for the person who runs it. " +
		"Write the new summary: two to four plain sentences, at most 600 characters, saying what the " +
		"agent is doing, what is done and whether anything waits for the person. " +
		"Reply with the summary only, and use no tools.\n\n"
	if last != "" {
		head += "Last summary:\n" + store.CutText(last, summaryTextLimit*4) + "\n\n"
	}
	head += "Turns since:\n"
	room := summaryInputLimit - len(head)
	var parts []string
	for i := len(turns) - 1; i >= 0 && room > 0; i-- {
		p := store.CutText(summaryTurnText(turns[i]), room)
		parts = append([]string{p}, parts...)
		room -= len(p) + 1
	}
	return head + strings.Join(parts, "\n")
}

// summaryTurnText is one turn as a call reads it: what was asked, how it ended, its steps.
func summaryTurnText(t wire.TurnSummary) string {
	var b strings.Builder
	if t.Ask != nil && t.Ask.Kind.UserMessage != nil {
		b.WriteString("- Asked: " + store.CutText(t.Ask.Kind.UserMessage.Value, 1500) + "\n")
	}
	for _, o := range t.Outcome {
		if m := o.Kind.AgentMessage; m != nil {
			b.WriteString("  Outcome: " + store.CutText(m.Text, 2000) + "\n")
		}
	}
	if t.Steps != nil {
		fmt.Fprintf(&b, "  Steps: %d\n", *t.Steps)
	}
	return b.String()
}

// summaryListener is the session a summary call opens: it hears the answer, and refuses
// every request, so the call has no tools.
type summaryListener struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *summaryListener) Update(n sdk.SessionNotification) {
	if c := n.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
		l.mu.Lock()
		if l.text.Len() < summaryAnswerLimit {
			l.text.WriteString(c.Content.Text.Text)
		}
		l.mu.Unlock()
	}
}
func (l *summaryListener) Permission(context.Context, sdk.RequestPermissionRequest) string {
	return ""
}
func (l *summaryListener) Served(string, string) {}
func (l *summaryListener) Elicitation(context.Context, json.RawMessage) (string, map[string]any) {
	return "cancel", nil
}
func (l *summaryListener) ElicitationComplete(string) {}

// askRuntimeForSummary starts the agent's runtime in an empty folder of its own, on its
// Fast model (the Auto cell), says the prompt as one turn, and stops it.
func askRuntimeForSummary(ctx context.Context, d *Daemon, a wire.Agent, prompt string) (string, string, error) {
	rt := runtimeByID(a.RuntimeID)
	if rt == nil {
		return "", "", fmt.Errorf("no runtime %s", a.RuntimeID)
	}
	dir, err := os.MkdirTemp("", "summary-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)
	account, err := d.accountFor(&a)
	if err != nil {
		return "", "", err
	}
	launch, err := d.launchAs(rt, dir, account)
	if err != nil {
		return "", "", err
	}
	l := &summaryListener{}
	meta := sessionMeta(rt.ID)
	s, err := acp.Start(ctx, launch, l, nil, meta, meta, "", "")
	if err != nil {
		return "", "", err
	}
	defer s.Stop()
	offers := configOptions(s.NewResult.ConfigOptions)
	model := rt.Name
	cell := cellMapping(rt.ID, wire.AutoFieldModel, string(wire.ModelLevelFast), d.autoSettings(), offers)
	if cell.Native != nil {
		for id, v := range cell.Native.Options {
			var val string
			if err := json.Unmarshal(v, &val); err != nil {
				continue
			}
			if _, err := s.SetOption(ctx, id, val); err != nil {
				return "", "", fmt.Errorf("could not set %s: %w", id, err)
			}
			if id == modelOptionID(offers) {
				model = val
			}
		}
	}
	if _, err := s.Prompt(ctx, []sdk.ContentBlock{sdk.TextBlock(prompt)}); err != nil {
		return "", "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String(), model, nil
}

func (d *Daemon) registerSummary() {
	On(d, wire.CallAgentsSummarise, func(ctx context.Context, c *Client, r wire.AgentRequest) (wire.SessionSummary, error) {
		return d.summarise(r)
	})
}
