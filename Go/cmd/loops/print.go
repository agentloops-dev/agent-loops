package main

// Print mode for scripts (#6447, state 8 of #6443's wireframes): loops -p starts a session in
// the folder here, follows its turn to the end with no TUI, and prints the agent's last reply
// (--json: one object per line, the session first and the end last). --detach prints the
// session's id and exits at once. A card (a question or a permission) waits for an answer
// from any client (Needs you), or with --on-ask=fail ends the command with exit 3. The CLI
// never answers a card itself.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// How --on-ask meets a card.
const (
	onAskWait = "wait" // wait for an answer from any client, until --timeout
	onAskFail = "fail" // end at once with exit 3
)

// recheck is how often print mode reads the session again when nothing is heard: a note
// can be dropped (controllink keeps 4096), and a host may say little while it works.
var recheck = 15 * time.Second

// gather is how long print mode collects notes before it reads: a streamed reply sends a
// note for each chunk, and a read for each note (two calls) used up a client's rate at the
// control plane in a few seconds of a long reply (#6473). At most four reads a second.
var gather = 250 * time.Millisecond

// pageLimit is the entries one agents/transcript read asks for (bounds, Stage 3).
const pageLimit = 50

// stdinLimit bounds a prompt read from a pipe.
const stdinLimit = 1 << 20

// checkPrintFlags refuses the print-mode flags without -p or --detach, and an unknown
// --on-ask.
func checkPrintFlags(o options) error {
	switch o.onAsk {
	case "", onAskWait, onAskFail:
	default:
		return &exit{code: exitError, msg: fmt.Sprintf("--on-ask is wait or fail, not %q", o.onAsk)}
	}
	if o.print || o.detach {
		return nil
	}
	for _, f := range []struct {
		set  bool
		name string
	}{{o.json, "--json"}, {o.onAsk != "", "--on-ask"}, {o.timeout != 0, "--timeout"}} {
		if f.set {
			return &exit{code: exitError, msg: f.name + " goes with -p or --detach: loops -p " + f.name + " \"…\""}
		}
	}
	return nil
}

// promptOf is the words after the flags, else what a pipe gives on stdin.
func promptOf(rest []string, stdin *os.File) (string, error) {
	text := strings.TrimSpace(strings.Join(rest, " "))
	if text == "" && stdin != nil {
		if fi, err := stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
			b, err := io.ReadAll(io.LimitReader(stdin, stdinLimit+1))
			if err != nil {
				return "", &exit{code: exitError, msg: fmt.Sprintf("Could not read the prompt from stdin: %v", err)}
			}
			if len(b) > stdinLimit {
				return "", &exit{code: exitError, msg: "The prompt on stdin is over 1 MB."}
			}
			text = strings.TrimSpace(string(b))
		}
	}
	if text == "" {
		return "", &exit{code: exitError, msg: "Print mode needs a prompt: loops -p \"what to do\" (or on stdin)"}
	}
	return text, nil
}

// printMode is loops -p and loops --detach.
func printMode(ctx context.Context, w io.Writer, o options, rest []string) error {
	prompt, err := promptOf(rest, os.Stdin)
	if err != nil {
		return err
	}
	s, p, err := connect(ctx, o)
	if err != nil {
		return err
	}
	defer s.Close()
	if p.folder == "" {
		return notInFolder(p)
	}
	return startAndFollow(ctx, w, s, s.Notes, p, o, prompt)
}

// startAndFollow starts the session and, unless --detach, follows its turn to the end.
func startAndFollow(ctx context.Context, w io.Writer, c caller, notes <-chan controllink.Note, p place, o options, prompt string) error {
	id, err := startSession(ctx, c, p, o.runtime, prompt)
	if err != nil {
		return err
	}
	out := &printer{w: w, json: o.json}
	out.session(id, p)
	if o.detach {
		if !o.json {
			fmt.Fprintln(w, id)
		}
		return nil
	}
	// The host sends a session's entries to the connections that show it. Showing is not
	// watching: it marks nothing read, and an inactive report never hides the Mac window.
	if _, err := c.Call(ctx, string(p.machine.ID), "presence/report", wire.PresenceReport{Active: false, Showing: &id}); err != nil {
		fmt.Fprintf(os.Stderr, "loops: the host may not send this session's entries as they come: %v\n", err)
	}
	f := &follow{c: c, host: string(p.machine.ID), id: id, out: out, onAsk: o.onAsk}
	fctx := ctx
	if o.timeout > 0 {
		var cancel context.CancelFunc
		fctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	return f.run(fctx, notes, o.timeout)
}

// startSession is agents/start in the folder, by Auto (or --runtime), marked as started by
// a script.
func startSession(ctx context.Context, c caller, p place, runtime, prompt string) (wire.UUID, error) {
	via := wire.StartedViaCLIPrint
	if runtime == "" {
		runtime = autoRuntime
	}
	req := wire.StartRequest{
		RuntimeID: runtime, Cwd: p.folder, Prompt: prompt,
		Attachments:           []wire.Attachment{},
		StartOptions:          wire.StartOptions{Values: map[string]wire.JSONValue{}, ExtraArguments: []string{}},
		AdditionalDirectories: []wire.URL{}, McpServers: []wire.MCPServer{}, Labels: []string{},
		StartedVia: &via,
	}
	r, err := c.Call(ctx, string(p.machine.ID), "agents/start", req)
	if err != nil {
		return "", &exit{code: exitError, msg: fmt.Sprintf("Could not start a session in %s on %s: %v", p.folderName(), p.machineName(), err)}
	}
	var id wire.UUID
	if err := json.Unmarshal(r, &id); err != nil || id == "" {
		return "", &exit{code: exitError, msg: fmt.Sprintf("The host started no session: %s", r)}
	}
	return id, nil
}

// follow reads one session's transcript in order, from the first entry not read yet, and
// its state, each time the host says something of it.
type follow struct {
	c     caller
	host  string
	id    wire.UUID
	out   *printer
	onAsk string

	next     int64  // the index of the first entry not read
	card     string // the open card's words, from its permissionAsked or elicitationAsked
	toldCard string // the card the person was last told of, so each is told once
}

func (f *follow) run(ctx context.Context, notes <-chan controllink.Note, timeout time.Duration) error {
	tick := time.NewTicker(recheck)
	defer tick.Stop()
	state := true
	for {
		done, err := f.check(ctx, state)
		if done || err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if ctx.Err() != nil {
			return f.timedOut(timeout)
		}
		var ok bool
		if ok, state = f.wait(ctx, notes, tick.C); !ok {
			if ctx.Err() != nil {
				return f.timedOut(timeout)
			}
			return f.end("gone", exitError, "The control plane closed the connection. The session goes on: loops -r "+short(f.id))
		}
	}
}

// wait is until the notes about this session have come for gather since the first, a
// tick, or the end of ctx. state says the session's record may have changed (a note other
// than an entry, or a tick), so it is read again; an entry alone needs only the
// transcript. ok is false when the connection ended or ctx did.
func (f *follow) wait(ctx context.Context, notes <-chan controllink.Note, tick <-chan time.Time) (ok, state bool) {
	var gathered <-chan time.Time
	for {
		select {
		case n, open := <-notes:
			if !open {
				return false, false
			}
			if !f.about(n) {
				continue
			}
			state = state || n.Method != wire.NotifyAgentEntry.Name
			if gathered == nil {
				gathered = time.After(gather)
			}
		case <-gathered:
			return true, state
		case <-tick:
			return true, true
		case <-ctx.Done():
			return false, false
		}
	}
}

// about is whether a note is this session's.
func (f *follow) about(n controllink.Note) bool {
	if n.Host != f.host {
		return false
	}
	var p struct {
		ID      wire.UUID `json:"id"`
		AgentID wire.UUID `json:"agentID"`
	}
	if json.Unmarshal(n.Params, &p) != nil {
		return false
	}
	return strings.EqualFold(string(p.AgentID), string(f.id)) || n.Method == "agent/changed" && strings.EqualFold(string(p.ID), string(f.id))
}

// check reads the new entries, then (with state) the session's record, and says whether
// the command is over.
func (f *follow) check(ctx context.Context, state bool) (bool, error) {
	if err := f.read(ctx); err != nil {
		return false, err
	}
	if !state {
		return false, nil
	}
	a, err := f.agent(ctx)
	if err != nil {
		return false, err
	}
	return f.settle(a)
}

// read prints the entries from next on, a page at a time.
func (f *follow) read(ctx context.Context) error {
	for {
		from := f.next
		r, err := f.c.Call(ctx, f.host, "agents/transcript", wire.TranscriptRequest{AgentID: f.id, From: &from, Limit: pageLimit})
		if err != nil {
			return f.unreachable(err)
		}
		var page wire.TranscriptPage
		if err := json.Unmarshal(r, &page); err != nil {
			return err
		}
		for _, e := range page.Entries {
			f.take(e)
		}
		f.next = page.FirstIndex + int64(len(page.Entries))
		if len(page.Entries) < pageLimit || f.next >= page.Total {
			return nil
		}
	}
}

// take is one entry: the printer keeps it, and a card opens or closes.
func (f *follow) take(e wire.TranscriptEntry) {
	k := e.Kind
	switch {
	case k.PermissionAsked != nil:
		f.card = "Permission: " + k.PermissionAsked.Value.ToolCall.Title
	case k.ElicitationAsked != nil:
		f.card = "Question"
		if m := k.ElicitationAsked.Value.Message; m != nil && strings.TrimSpace(*m) != "" {
			f.card = "Question: " + strings.TrimSpace(*m)
		}
	case k.PermissionAnswered != nil, k.ElicitationAnswered != nil:
		f.card = ""
	}
	f.out.entry(e)
}

// agent is the session's record, from agents/list with its id.
func (f *follow) agent(ctx context.Context) (wire.Agent, error) {
	id := f.id
	r, err := f.c.Call(ctx, f.host, "agents/list", wire.ListRequest{AgentID: &id, Lean: true, IncludeArchived: true})
	if err != nil {
		return wire.Agent{}, f.unreachable(err)
	}
	var list []wire.Agent
	if err := json.Unmarshal(r, &list); err != nil {
		return wire.Agent{}, err
	}
	for _, a := range list {
		if strings.EqualFold(string(a.ID), string(f.id)) {
			return a, nil
		}
	}
	return wire.Agent{}, f.end("gone", exitError, "The session is gone from its host (deleted?).")
}

// settle is what a state means: the turn ended (exit 0, or 1 when it ended badly), a card
// waits, or the turn goes on.
func (f *follow) settle(a wire.Agent) (bool, error) {
	switch a.State {
	case wire.AgentStateFinished:
		return true, f.done()
	case wire.AgentStateStopped, wire.AgentStateArchived:
		reason := ""
		if a.EndedReason != nil {
			reason = string(*a.EndedReason)
		}
		if reason == "" || reason == string(wire.EndedReasonEndTurn) || reason == string(wire.EndedReasonStoppedByAgent) {
			return true, f.done()
		}
		f.out.flush()
		return true, f.end("stopped", exitError, fmt.Sprintf("The session stopped (%s).", reason), "reason", reason)
	case wire.AgentStateWaitingOnUser:
		return f.asked()
	}
	return false, nil
}

// asked is a card that waits: exit 3 with --on-ask=fail, else tell the person once and wait.
func (f *follow) asked() (bool, error) {
	card := f.card
	if card == "" {
		card = "A card"
	}
	f.out.flush()
	if f.onAsk == onAskFail {
		return true, f.end("needs-you", exitAsked, fmt.Sprintf("%s waits for an answer.\nAnswer it on any client (Needs you), then: loops -r %s", card, short(f.id)), "card", card)
	}
	if card != f.toldCard {
		f.toldCard = card
		f.out.card(card)
		fmt.Fprintf(os.Stderr, "loops: %s waits for an answer on any client (Needs you).\n", card)
	}
	return false, nil
}

// done is the turn's end: the last reply, or with --json the end line.
func (f *follow) done() error {
	f.out.flush()
	if f.out.json {
		f.out.line(map[string]any{"type": "end", "state": "done", "exit": exitOK})
		return nil
	}
	if f.out.last != "" {
		fmt.Fprintln(f.out.w, f.out.last)
	}
	return nil
}

func (f *follow) timedOut(after time.Duration) error {
	f.out.flush()
	return f.end("timed-out", exitTimedOut, fmt.Sprintf("Timed out after %s. The session goes on: loops -r %s", after, short(f.id)))
}

// end is a command that ends with a code other than 0: the end line with --json, and the
// words on stderr (main says them).
func (f *follow) end(state string, code int, msg string, extra ...string) error {
	if f.out.json {
		m := map[string]any{"type": "end", "state": state, "exit": code}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		f.out.line(m)
	}
	return &exit{code: code, msg: msg}
}

func (f *follow) unreachable(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	f.out.flush()
	return f.end("gone", exitError, fmt.Sprintf("Lost the session's host (%v). The session goes on: loops -r %s", err, short(f.id)))
}

// short is a session id as loops ls shows it.
func short(id wire.UUID) string { return shortIDs([]wire.Agent{{ID: id}})[0] }

// printer writes what print mode prints. The chunks of one agent message come as entries of
// their own; it joins them into one reply.
type printer struct {
	w    io.Writer
	json bool

	reply     strings.Builder // the agent message being joined
	replyID   *string         // its messageID
	replyOpen bool
	last      string // the last whole reply
}

func (p *printer) line(v any) {
	if !p.json {
		return
	}
	b, _ := json.Marshal(v)
	fmt.Fprintf(p.w, "%s\n", b)
}

func (p *printer) session(id wire.UUID, pl place) {
	p.line(map[string]any{"type": "session", "id": id, "folder": pl.folderName(), "machine": pl.machineName(), "startedVia": wire.StartedViaCLIPrint})
}

func (p *printer) card(text string) { p.line(map[string]any{"type": "card", "text": text}) }

// entry keeps an entry: a chunk joins the open reply; a message or a tool call closes it.
func (p *printer) entry(e wire.TranscriptEntry) {
	k := e.Kind
	switch {
	case k.AgentMessage != nil:
		if p.replyOpen && sameID(p.replyID, k.AgentMessage.MessageID) {
			p.reply.WriteString(k.AgentMessage.Text)
			return
		}
		p.flush()
		p.replyOpen, p.replyID = true, k.AgentMessage.MessageID
		p.reply.WriteString(k.AgentMessage.Text)
	case k.UserMessage != nil:
		p.flush()
		p.line(map[string]any{"type": "entry", "role": "user", "text": k.UserMessage.Value})
	case k.ToolCall != nil:
		p.flush()
		p.line(map[string]any{"type": "entry", "role": "tool", "text": k.ToolCall.Value.Title})
	}
}

// flush ends the open reply: it is the last reply, and with --json a line.
func (p *printer) flush() {
	if !p.replyOpen {
		return
	}
	text := strings.TrimSpace(p.reply.String())
	p.reply.Reset()
	p.replyOpen, p.replyID = false, nil
	if text == "" {
		return
	}
	p.last = text
	p.line(map[string]any{"type": "entry", "role": "agent", "text": text})
}

func sameID(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
