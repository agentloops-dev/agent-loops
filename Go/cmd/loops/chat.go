package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// attachEntries is how many entries loops reads when it attaches (bounds, Stage 3).
const attachEntries = 50

// autoRuntime is the runtime a session starts on with no --runtime: Auto picks one, as a
// new session's page does.
const autoRuntime = "auto"

// noter is what the chat needs of a control plane: calls, and the notes the hosts send.
type noter interface {
	caller
	notes() <-chan controllink.Note
}

// terminal is where the chat is drawn and read: stdout and stdin, and whether each is a
// terminal. A test gives its own.
type terminal struct {
	out      io.Writer
	keys     <-chan key // nil when stdin is not a terminal: no prompt
	raw      bool
	width    func() int
	st       style
	askTheme bool // ask the terminal for its background before drawing
}

// chatArgs is what a chat command was asked: the prompt, -c, -r.
type chatArgs struct {
	text   string
	cont   bool
	resume string
	pick   bool
}

var terminalAvailable = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

var errNoTerminal = &exit{code: exitError, msg: `loops needs a terminal to choose a session. Use: loops -c, loops -r <id>, or loops "…"`}

const pickerPrompt = "Resume a session by its number, or press Enter for a new one: "

// chat is loops, loops "…", loops -c and loops -r (wireframes 1 to 3).
func chat(ctx context.Context, o options, a chatArgs) error {
	s, p, err := connect(ctx, &o)
	if err != nil {
		return err
	}
	l := newLink(s, o)
	defer l.Close()
	if p.folder == "" && a.resume == "" {
		return notInFolder(p)
	}
	t, restore, err := openTerminal()
	if err != nil {
		return err
	}
	defer restore()
	err = runChat(ctx, l, p, o, a, t)
	if t.raw {
		_, _ = io.WriteString(t.out, "\x1b[?2004l")
	}
	return err
}

// openTerminal makes stdin raw when both ends are a terminal, and reads its keys.
func openTerminal() (terminal, func(), error) {
	outTTY := term.IsTerminal(int(os.Stdout.Fd()))
	inTTY := term.IsTerminal(int(os.Stdin.Fd()))
	t := terminal{out: os.Stdout, st: detectStyle(outTTY, osEnv), width: func() int {
		w, _, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil {
			return 0
		}
		return w
	}}
	if !outTTY || !inTTY {
		return t, func() {}, nil
	}
	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return t, func() {}, nil
	}
	t.raw = true
	t.askTheme = t.st.on && os.Getenv("LOOPS_THEME") == "" && os.Getenv("COLORFGBG") == ""
	keys := make(chan key, 64)
	go func() {
		var kr keyReader
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			for _, k := range kr.feed(buf[:n]) {
				keys <- k
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()
	t.keys = keys
	_, _ = io.WriteString(os.Stdout, "\x1b[?2004h")
	return t, func() { _ = term.Restore(int(os.Stdin.Fd()), old) }, nil
}

// live is one chat with one session: its state, what it shows and the cards that wait.
type live struct {
	c       noter
	p       place
	o       options
	t       terminal
	sc      *screen
	tr      *transcript
	id      wire.UUID // "" until the first prompt starts a session
	state   wire.AgentState
	title   string
	cards   []*card
	since   time.Time // when the turn began, for the spinner
	sent    bool      // a prompt was sent and its turn has not ended
	working bool      // the turn of sent began
	stopAt  time.Time // when ^C last stopped the turn: a second ^C soon after leaves
	frame   int
	hinted  bool  // the keys' line is shown
	next    int64 // the index after the last entry read, where a relink reads from (#6478)
}

func runChat(ctx context.Context, c noter, p place, o options, a chatArgs, t terminal) error {
	f := &live{c: c, p: p, o: o, t: t, sc: &screen{w: t.out, raw: t.raw, width: t.width}}
	if t.askTheme {
		f.askTheme(ctx)
	}
	f.tr = newTranscript(f.t.st)
	target, others, err := f.pick(ctx, a)
	if errors.Is(err, errLeave) {
		f.sc.end()
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case target != nil:
		if err := f.attach(ctx, *target, others); err != nil {
			return err
		}
		// With no terminal to type in and nothing to send, an idle session has nothing more
		// to show: the command ends, as the page it read (it waited for ever, #6473).
		if a.text == "" && t.keys == nil && !f.sent && len(f.cards) == 0 {
			return nil
		}
		if a.text != "" {
			if err := f.send(ctx, a.text); err != nil {
				return err
			}
		}
	case a.text != "":
		if err := f.start(ctx, a.text); err != nil {
			return err
		}
	case a.pick:
	case t.keys == nil:
		return &exit{code: exitError, msg: fmt.Sprintf("%s has no sessions yet, and there is nothing to send.\nStart one: loops \"what this folder needs\"", p.folderName())}
	default:
		f.print(fmt.Sprintf("%s · %s · %s", f.t.st.bold(f.t.st.paint(roleBrand, "Agent Loops")), p.folderName(), p.machineName()),
			f.t.st.paint(roleQuiet, "No sessions here yet. Type what this one should do."), "")
	}
	return f.loop(ctx)
}

// askTheme asks the terminal for its background, and waits a moment for the answer.
func (f *live) askTheme(ctx context.Context) {
	_, _ = io.WriteString(f.t.out, askBackground)
	select {
	case k, ok := <-f.t.keys:
		if ok && k.kind == keyBackground {
			f.t.st.dark = k.dark
		}
	case <-time.After(150 * time.Millisecond):
	case <-ctx.Done():
	}
}

// pick is the session to continue, or nil to start one: -r names one, -c takes the newest,
// a bare loops asks for a number, and a prompt alone starts a new one. others counts the
// folder's other sessions on the first page; it is -1 when the page was full, so there are more.
func (f *live) pick(ctx context.Context, a chatArgs) (*wire.Agent, int, error) {
	if a.pick {
		if f.t.keys == nil {
			return nil, 0, errNoTerminal
		}
		return f.pickRecent(ctx)
	}
	if a.resume != "" {
		got, err := f.find(ctx, a.resume)
		return got, 0, err
	}
	if a.text != "" && !a.cont {
		return nil, 0, nil
	}
	list, more, err := listSessions(ctx, f.c, f.p, false)
	if err != nil {
		return nil, 0, err
	}
	if len(list) == 0 {
		return nil, 0, nil
	}
	if more {
		return &list[0], -1, nil
	}
	return &list[0], len(list) - 1, nil
}

func (f *live) pickRecent(ctx context.Context) (*wire.Agent, int, error) {
	list, err := recentSessions(ctx, f.c, f.p)
	if err != nil {
		return nil, 0, err
	}
	if len(list) == 0 {
		f.print(fmt.Sprintf("No sessions in %s yet.", f.p.folderName()))
		return nil, 0, nil
	}
	var b strings.Builder
	printRecent(&b, f.t.st, f.p, list, time.Now())
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	f.sc.prefix = pickerPrompt
	f.sc.input.set("")
	defer func() {
		f.sc.prefix = ""
		f.sc.input.set("")
	}()
	f.print(lines...)
	for {
		select {
		case <-ctx.Done():
			f.sc.end()
			return nil, 0, ctx.Err()
		case k, ok := <-f.t.keys:
			if !ok {
				return nil, 0, errLeave
			}
			switch k.kind {
			case keyInterrupt, keyEOF:
				return nil, 0, errLeave
			case keyBackground:
				f.t.st.dark = k.dark
				f.sc.update()
			case keyEnter:
				text := strings.TrimSpace(f.sc.input.text)
				f.sc.input.set("")
				if text == "" {
					return nil, 0, nil
				}
				n, err := strconv.Atoi(text)
				if err != nil || n < 1 || n > len(list) {
					f.print(fmt.Sprintf("Enter 1 to %d, or press Enter for a new one.", len(list)))
					continue
				}
				return &list[n-1], len(list) - 1, nil
			case keyNewline:
			case keyText:
				k.text = strings.ReplaceAll(k.text, "\n", "")
				if f.sc.input.apply(k) {
					f.sc.update()
				}
			default:
				if f.sc.input.apply(k) {
					f.sc.update()
				}
			}
		}
	}
}

// find is the session -r names: an id or the start of one (as loops sessions list shows it), or a
// title, whole or a part that only one has.
func (f *live) find(ctx context.Context, want string) (*wire.Agent, error) {
	p := f.p
	limit := int64(pageSize)
	req := wire.ListRequest{Limit: &limit, Lean: true, IncludeArchived: true}
	if p.folder != "" {
		req.Folder = &p.folder
	}
	// A whole id, as print mode's --json gives a script, is one read, not every page.
	if id, ok := wholeID(want); ok {
		req.AgentID, req.Limit = &id, nil
	}
	var list []wire.Agent
	for {
		r, err := f.c.Call(ctx, string(p.machine.ID), "agents/list", req)
		if err != nil {
			return nil, unreachable(p.machineName(), err)
		}
		var page []wire.Agent
		if err := json.Unmarshal(r, &page); err != nil {
			return nil, err
		}
		list = append(list, page...)
		if req.AgentID != nil || len(page) < pageSize {
			break
		}
		last := page[len(page)-1]
		req.After = &wire.ListCursor{LastActivityAt: last.LastActivityAt, ID: last.ID}
	}
	return match(list, want)
}

// wholeID is want as a session's whole id (32 hex digits, with or without dashes, after
// an "s_"), in the daemon's form.
func wholeID(want string) (wire.UUID, bool) {
	hex := strings.ToUpper(strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(want), "s_"), "-", ""))
	if len(hex) != 32 {
		return "", false
	}
	for _, r := range hex {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return "", false
		}
	}
	return wire.UUID(hex[:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:]), true
}

// match is the one session of list that want names (see find).
func match(list []wire.Agent, want string) (*wire.Agent, error) {
	key := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(want, "s_"), "-", ""))
	var hits []int
	for i, a := range list {
		if key != "" && strings.HasPrefix(strings.ToLower(strings.ReplaceAll(string(a.ID), "-", "")), key) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		for i, a := range list {
			if a.Title != nil && strings.EqualFold(oneLine(*a.Title), oneLine(want)) {
				hits = append(hits, i)
			}
		}
	}
	if len(hits) == 0 {
		for i, a := range list {
			if a.Title != nil && strings.Contains(strings.ToLower(*a.Title), strings.ToLower(oneLine(want))) {
				hits = append(hits, i)
			}
		}
	}
	switch len(hits) {
	case 1:
		return &list[hits[0]], nil
	case 0:
		return nil, &exit{code: exitError, msg: fmt.Sprintf("No session %q here.\nSee them: loops sessions list", want)}
	default:
		ids := shortIDs(list)
		var names []string
		for _, i := range hits[:min(len(hits), 5)] {
			names = append(names, fmt.Sprintf("  %s  %s", ids[i], titleOf(list[i])))
		}
		return nil, &exit{code: exitError, msg: fmt.Sprintf("%d sessions match %q; name one by its id:\n%s", len(hits), want, strings.Join(names, "\n"))}
	}
}

func (f *live) host() string { return string(f.p.machine.ID) }

// show says to the session's machine that this terminal shows the session, so its
// entries come here (presence/report, as a client's open page says it).
func (f *live) show(ctx context.Context) error {
	id := f.id
	if _, err := f.c.Call(ctx, f.host(), "presence/report", wire.PresenceReport{Active: true, Showing: &id}); err != nil {
		return unreachable(f.p.machineName(), err)
	}
	return nil
}

// attach is wireframe 1: the header, the last entries, the cards that wait.
func (f *live) attach(ctx context.Context, a wire.Agent, others int) error {
	f.id, f.state, f.title = a.ID, a.State, titleOf(a)
	if err := f.show(ctx); err != nil {
		return err
	}
	st := f.t.st
	head := fmt.Sprintf("%s · %s (%s) · %s", st.bold(st.paint(roleBrand, "Agent Loops")), f.p.folderName(), f.p.machineName(), a.RuntimeID)
	if m := modelOf(a); m != "" {
		head += " · " + m
	}
	line := fmt.Sprintf("Continuing %q · %s %s", f.title, stateWord(a.State), strings.TrimSuffix(ago(time.Now(), a.LastActivityAt.Time()), " ago"))
	if a.State == wire.AgentStateRunning || a.State == wire.AgentStateStarting {
		line = fmt.Sprintf("Continuing %q · working", f.title)
	}
	switch {
	case others < 0:
		line += fmt.Sprintf(" · %d+ other sessions here (loops sessions list)", pageSize-1)
	case others == 1:
		line += " · 1 other session here (loops sessions list)"
	case others > 1:
		line += fmt.Sprintf(" · %d other sessions here (loops sessions list)", others)
	}
	f.print(head, st.paint(roleQuiet, line), "")
	r, err := f.c.Call(ctx, f.host(), "agents/transcript", wire.TranscriptRequest{AgentID: a.ID, Limit: attachEntries})
	if err != nil {
		return unreachable(f.p.machineName(), err)
	}
	var page wire.TranscriptPage
	if err := json.Unmarshal(r, &page); err != nil {
		return err
	}
	if page.FirstIndex > 0 {
		f.print(st.paint(roleQuiet, fmt.Sprintf("  … %d earlier entries", page.FirstIndex)))
	}
	f.next = page.FirstIndex + int64(len(page.Entries))
	for _, e := range page.Entries {
		f.print(f.tr.entry(e)...)
	}
	f.print(f.tr.end()...)
	f.pending(ctx)
	if f.state == wire.AgentStateRunning || f.state == wire.AgentStateStarting {
		f.since, f.sent, f.working = time.Now(), true, true
	}
	return nil
}

// pending reads the cards that wait for this session, as it attaches. all says both
// lists were read, so waiting holds every card that waits.
func (f *live) pending(ctx context.Context) (waiting map[wire.UUID]bool, all bool) {
	waiting, all = map[wire.UUID]bool{}, true
	if r, err := f.c.Call(ctx, f.host(), "permissions/pending", wire.Empty{}); err == nil {
		var list []wire.PermissionRequest
		if json.Unmarshal(r, &list) == nil {
			for i := range list {
				if sameUUID(list[i].AgentID, f.id) {
					f.ask(&card{permission: &list[i]})
					waiting[list[i].ID] = true
				}
			}
		} else {
			all = false
		}
	} else {
		all = false
	}
	if r, err := f.c.Call(ctx, f.host(), "elicitations/pending", wire.Empty{}); err == nil {
		var list []wire.ElicitationRequest
		if json.Unmarshal(r, &list) == nil {
			for i := range list {
				if sameUUID(list[i].AgentID, f.id) {
					f.ask(&card{elicitation: &list[i]})
					waiting[list[i].ID] = true
				}
			}
		} else {
			all = false
		}
	} else {
		all = false
	}
	return waiting, all
}

func sameUUID(a, b wire.UUID) bool { return strings.EqualFold(string(a), string(b)) }

// modelOf is the model the session's start chose, when it says one.
func modelOf(a wire.Agent) string {
	raw, ok := a.StartOptions.Values["model"]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// start is wireframe 2: a new session here with the prompt.
func (f *live) start(ctx context.Context, text string) error {
	runtime := f.o.runtime
	if runtime == "" {
		runtime = autoRuntime
	}
	via := wire.StartedViaCLI
	req := wire.StartRequest{RuntimeID: runtime, Cwd: f.p.folder, Prompt: text, Attachments: []wire.Attachment{},
		StartOptions:          wire.StartOptions{Values: map[string]wire.JSONValue{}, ExtraArguments: []string{}},
		AdditionalDirectories: []wire.URL{}, McpServers: []wire.MCPServer{}, Labels: []string{}, StartedVia: &via,
		Levels: startLevels(ctx, f.c, f.host(), f.o.mode)}
	r, err := f.c.Call(ctx, f.host(), "agents/start", req)
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Could not start a session in %s: %v", f.p.folderName(), err)}
	}
	var id wire.UUID
	if err := json.Unmarshal(r, &id); err != nil {
		return err
	}
	f.id, f.state = id, wire.AgentStateStarting
	if err := f.show(ctx); err != nil {
		return err
	}
	st := f.t.st
	short := shortIDs([]wire.Agent{{ID: id}})[0]
	f.print(fmt.Sprintf("%s %s · %s · %s · in %s", st.paint(roleDone, "Started"), titleLine(text), short, runtime, f.p.folderName()),
		st.paint(roleQuiet, "Continue it anywhere: loops -r "+short+", or on any client"), "")
	f.since, f.sent, f.working = time.Now(), true, false
	f.hint()
	// The first prompt is the session's first entry; it is shown here, so not again.
	f.print(f.tr.block(f.tr.speaker("you"), text)...)
	f.tr.seenText = text
	return nil
}

// titleLine is a prompt as a title before the session has one: its first line, quoted.
func titleLine(text string) string {
	t := oneLine(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	if r := []rune(t); len(r) > 48 {
		t = string(r[:47]) + "…"
	}
	return fmt.Sprintf("%q", t)
}

// send is a prompt to the session (agents/prompt; it waits in line when a turn runs).
func (f *live) send(ctx context.Context, text string) error {
	if f.id == "" {
		return f.start(ctx, text)
	}
	if _, err := f.c.Call(ctx, f.host(), "agents/prompt", wire.PromptRequest{AgentID: f.id, Text: text, Attachments: []wire.Attachment{}, From: wire.PromptOriginPerson}); err != nil {
		if lost(err) {
			// The notes end too, and the loop dials again; the person sends it again then.
			f.print(indent + f.t.st.paint(roleFailure, "Not sent: the link to the control plane dropped. Send it again once it is back."))
			return nil
		}
		return &exit{code: exitError, msg: fmt.Sprintf("Could not send it: %v", err)}
	}
	if !f.sent {
		f.since = time.Now()
	}
	f.sent = true
	return nil
}

// ask puts a card in line; the first in line is shown.
func (f *live) ask(c *card) {
	for _, have := range f.cards {
		if have.id() == c.id() {
			return
		}
	}
	f.cards = append(f.cards, c)
	if len(f.cards) == 1 {
		f.showCard()
	}
}

func (f *live) showCard() {
	if len(f.cards) == 0 {
		return
	}
	w := 0
	if f.t.width != nil {
		w = f.t.width()
	}
	if w <= 0 {
		w = 80
	}
	f.print(f.tr.end()...)
	f.print(f.cards[0].lines(f.t.st, f.p.folderName(), w)...)
}

// answered takes a card out of line: answered here, or on another client.
func (f *live) answered(id wire.UUID, here bool) {
	for i, c := range f.cards {
		if c.id() != id {
			continue
		}
		f.cards = append(f.cards[:i], f.cards[i+1:]...)
		if !here {
			f.print(indent + f.t.st.paint(roleDone, "✓ answered on another client"))
		}
		if i == 0 {
			f.showCard()
		}
		return
	}
}

// answer is what the person typed for the card in front.
func (f *live) answer(ctx context.Context, text string) error {
	c := f.cards[0]
	if !c.answerable() {
		f.print(indent + f.t.st.paint(roleQuiet, "Answer this one on a client (Needs you); ^D leaves."))
		return nil
	}
	option, done, problem := c.answer(text)
	if problem != "" {
		f.print(indent + f.t.st.paint(roleFailure, problem))
		return nil
	}
	if !done {
		f.showCard()
		return nil
	}
	var err error
	if c.permission != nil {
		_, err = f.c.Call(ctx, f.host(), "permissions/answer", wire.AnswerRequest{PermissionID: c.permission.ID, OptionID: option})
	} else {
		content := c.content
		if content == nil {
			content = map[string]wire.JSONValue{}
		}
		_, err = f.c.Call(ctx, f.host(), "elicitations/answer", wire.AnswerElicitationRequest{RequestID: c.elicitation.ID, Action: wire.AnswerElicitationRequestActionAccept, Content: content})
	}
	if err != nil {
		f.print(indent + f.t.st.paint(roleFailure, "Could not answer: "+err.Error()))
		return nil
	}
	f.print(indent + f.t.st.paint(roleDone, "✓ answered"))
	f.answered(c.id(), true)
	return nil
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// status is the live line under the chat: working, waiting in line, needs you.
func (f *live) status() string {
	st := f.t.st
	switch {
	case len(f.cards) > 0 || f.state == wire.AgentStateWaitingOnUser:
		return "  " + st.paint(roleAttention, "● needs you")
	case f.state == wire.AgentStateQueued:
		return "  " + st.paint(roleQuiet, "◌ waiting for a place")
	case f.sent:
		d := time.Since(f.since).Truncate(time.Second)
		return fmt.Sprintf("  %s %s", st.paint(roleBrand, spinner[f.frame%len(spinner)]), st.paint(roleQuiet, fmt.Sprintf("working · %d:%02d", int(d.Minutes()), int(d.Seconds())%60)))
	}
	return ""
}

// print puts lines in the scrollback; the live part under them is drawn as it is now.
func (f *live) print(lines ...string) {
	f.tr.width = f.cols()
	f.sc.partial = f.tr.partial()
	f.sc.status = f.status()
	f.sc.prompt = f.t.keys != nil
	f.sc.print(lines...)
}

// cols is the terminal's width, or 0 when it has none.
func (f *live) cols() int {
	if f.t.width == nil || !f.t.raw {
		return 0
	}
	return f.t.width()
}

func (f *live) redraw() {
	f.tr.width = f.cols()
	f.sc.partial = f.tr.partial()
	f.sc.status = f.status()
	f.sc.prompt = f.t.keys != nil
	f.sc.update()
}

// errLeave ends the loop with no error: ^D, or ^C twice.
var errLeave = errors.New("leave")

func (f *live) loop(ctx context.Context) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	notes := f.c.notes()
	keys := f.t.keys
	f.hint()
	f.redraw()
	for {
		select {
		case <-ctx.Done():
			f.sc.end()
			return ctx.Err()
		case n, ok := <-notes:
			if !ok {
				ended, err := f.relink(ctx)
				if err != nil || ended {
					f.sc.end()
					return err
				}
				notes = f.c.notes()
				continue
			}
			if f.note(n) {
				f.sc.end()
				return nil
			}
		case k, ok := <-keys:
			if !ok {
				keys = nil
				continue
			}
			err := f.key(ctx, k)
			if errors.Is(err, errLeave) {
				f.leave()
				return nil
			}
			if err != nil {
				f.sc.end()
				return err
			}
		case <-tick.C:
			if f.sent {
				f.frame++
				if f.status() != f.sc.status {
					f.redraw()
				}
			}
			continue
		}
		f.redraw()
	}
}

func (f *live) short() string {
	if f.id == "" {
		return ""
	}
	return shortIDs([]wire.Agent{{ID: f.id}})[0]
}

// hint is the keys' line, once, before the live chat.
func (f *live) hint() {
	if f.t.keys == nil || f.hinted {
		return
	}
	f.hinted = true
	f.print(f.t.st.paint(roleQuiet, "  ⏎ send · ⌥⏎ new line · ^C stop the turn · ^D leave (the session goes on)"))
}

// leave is ^D: the terminal goes, the session goes on.
func (f *live) leave() {
	f.sc.end()
	if f.id == "" {
		return
	}
	_, _ = io.WriteString(f.t.out, f.t.st.paint(roleQuiet, "The session goes on. Continue it: loops -r "+f.short())+f.sc.eol())
}

// key is one key at the prompt.
func (f *live) key(ctx context.Context, k key) error {
	if k.kind == keyEOF && f.sc.input.text == "" {
		return errLeave
	}
	if f.sc.input.apply(k) {
		return nil
	}
	switch k.kind {
	case keyInterrupt:
		return f.interrupt(ctx)
	case keyEnter:
		text := f.sc.input.text
		f.sc.input.set("")
		if len(f.cards) > 0 {
			f.print(f.t.st.paint(roleQuiet, "› ") + text)
			return f.answer(ctx, text)
		}
		if strings.TrimSpace(text) == "" {
			return nil
		}
		if f.id != "" {
			f.print(f.tr.end()...)
			f.print(f.tr.block(f.tr.speaker("you"), text)...)
			f.tr.seenText = text
		}
		return f.send(ctx, text)
	case keyBackground:
		f.t.st.dark = k.dark
	}
	return nil
}

// interrupt is ^C: with words typed, they go; in a turn, it stops (as Stop in the
// window); a second ^C soon after, or one with no turn, leaves.
func (f *live) interrupt(ctx context.Context) error {
	if f.sc.input.text != "" {
		f.sc.input.set("")
		return nil
	}
	if f.id == "" || !f.sent || time.Since(f.stopAt) < 2*time.Second {
		return errLeave
	}
	f.stopAt = time.Now()
	if _, err := f.c.Call(ctx, f.host(), "agents/stop", wire.AgentStopRequest{AgentID: f.id}); err != nil {
		f.print(indent + f.t.st.paint(roleFailure, "Could not stop the turn: "+err.Error()))
		return nil
	}
	f.print(f.tr.end()...)
	f.print(indent + f.t.st.paint(roleQuiet, "Stopping the turn. ^C again leaves."))
	return nil
}

// note is one notification; it says true when the chat ends: a turn ended and there is
// no prompt to type the next one in.
func (f *live) note(n controllink.Note) bool {
	if n.Host != f.host() || f.id == "" {
		return false
	}
	switch n.Method {
	case wire.NotifyAgentEntry.Name:
		var e wire.EntryNotification
		if json.Unmarshal(n.Params, &e) != nil || !sameUUID(e.AgentID, f.id) {
			return false
		}
		if e.Index != nil {
			f.next = max(f.next, *e.Index+1)
		}
		f.print(f.tr.entry(e.Entry)...)
	case wire.NotifyAgentPermission.Name:
		var p wire.PermissionNotification
		if json.Unmarshal(n.Params, &p) != nil || !sameUUID(p.AgentID, f.id) {
			return false
		}
		if p.Request != nil {
			f.ask(&card{permission: p.Request})
		} else {
			f.answered(p.RequestID, false)
		}
	case wire.NotifyAgentElicitation.Name:
		var e wire.ElicitationNotification
		if json.Unmarshal(n.Params, &e) != nil || !sameUUID(e.AgentID, f.id) {
			return false
		}
		if e.Request != nil {
			f.ask(&card{elicitation: e.Request})
		} else {
			f.answered(e.RequestID, false)
		}
	case wire.NotifyAgentChanged.Name:
		var a wire.Agent
		if json.Unmarshal(n.Params, &a) != nil || !sameUUID(a.ID, f.id) {
			return false
		}
		return f.changed(a)
	}
	return false
}

// changed is the session's new state: a turn that began, or one that ended.
func (f *live) changed(a wire.Agent) bool {
	f.state = a.State
	if a.Title != nil {
		f.title = titleOf(a)
	}
	switch a.State {
	case wire.AgentStateStarting, wire.AgentStateRunning, wire.AgentStateQueued:
		if !f.sent {
			f.since = time.Now()
		}
		f.sent, f.working = true, true
	case wire.AgentStateFinished, wire.AgentStateStopped, wire.AgentStateArchived:
		if !f.sent || !f.working {
			return false
		}
		f.sent, f.working = false, false
		f.print(f.tr.end()...)
		d := time.Since(f.since).Truncate(time.Second)
		word := "done"
		ro := roleDone
		if a.State == wire.AgentStateStopped {
			word, ro = "stopped", roleWarning
		}
		f.print(indent+f.t.st.paint(ro, fmt.Sprintf("● %s · %d:%02d", word, int(d.Minutes()), int(d.Seconds())%60)), "")
		return f.t.keys == nil
	}
	return false
}

// relink is a dropped link in the chat (#6478): say "reconnecting…", dial again for up to
// relinkFor, show the session again, and read what came while it was away: the entries
// from the last index read (the transcript shows each entry once), the session's state
// and its cards. ended says the turn ended while it was away and there is no prompt.
func (f *live) relink(ctx context.Context) (ended bool, err error) {
	rl, ok := f.c.(relinker)
	if ok {
		err = rl.relink(ctx, relinkFor, func() {
			f.print(indent + f.t.st.paint(roleWarning, "Lost the link to the control plane; reconnecting…"))
		})
	}
	if !ok || err != nil {
		return false, lostForGood(f.o.controlName, relinkFor, f.short(), err)
	}
	if f.id != "" {
		// The host joins the control plane again after it, so it may be offline a while.
		hctx, cancel := context.WithTimeout(ctx, relinkFor)
		id := f.id
		err := waitHost(hctx, f.c, f.host(), wire.PresenceReport{Active: true, Showing: &id})
		cancel()
		if lost(err) || hostOffline(err) {
			return false, lostForGood(f.p.machineName(), relinkFor, f.short(), err)
		}
		if ended, err = f.catchUp(ctx); err != nil {
			return false, err
		}
	}
	f.print(indent + f.t.st.paint(roleDone, "✓ reconnected"))
	return ended, nil
}

// catchUp reads the entries from f.next on, the session's record and its cards.
func (f *live) catchUp(ctx context.Context) (bool, error) {
	for {
		page, err := readFrom(ctx, f.c, f.host(), f.id, f.next)
		if err != nil {
			return false, unreachable(f.p.machineName(), err)
		}
		for _, e := range page.Entries {
			f.print(f.tr.entry(e)...)
		}
		f.next = page.FirstIndex + int64(len(page.Entries))
		if len(page.Entries) == 0 || f.next >= page.Total {
			break
		}
	}
	waiting, all := f.pending(ctx)
	if all {
		for _, c := range append([]*card(nil), f.cards...) {
			if !waiting[c.id()] {
				f.answered(c.id(), false)
			}
		}
	}
	id := f.id
	r, err := f.c.Call(ctx, f.host(), "agents/list", wire.ListRequest{AgentID: &id, Lean: true, IncludeArchived: true})
	if err != nil {
		return false, unreachable(f.p.machineName(), err)
	}
	var list []wire.Agent
	if err := json.Unmarshal(r, &list); err != nil {
		return false, err
	}
	for _, a := range list {
		if sameUUID(a.ID, f.id) {
			return f.changed(a), nil
		}
	}
	return false, nil
}
