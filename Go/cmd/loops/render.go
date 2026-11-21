package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// indent is where a speaker's words start: "  agent  " is nine cells (wireframe 1).
const indent = "         "

// transcript turns entries into the lines the terminal shows (wireframes 1 and 2). An
// agent message comes in chunks with one message id; the line that is not ended yet is
// partial, which the screen draws under the lines and puts in them once it ends.
type transcript struct {
	st      style
	msgID   string // the agent message being written, or ""
	writing bool   // an agent message is being written
	head    string // the start of its line that has no end yet: a speaker, or the indent
	text    string // the words of that line so far
	width   int    // the terminal's width, to wrap at under the indent; 0 wraps nothing
	seen    map[wire.UUID]bool
	// seenText is a prompt this terminal sent and showed itself: its entry is not shown
	// again when it comes.
	seenText string
}

func newTranscript(st style) *transcript {
	return &transcript{st: st, seen: map[wire.UUID]bool{}}
}

// speaker is a speaker's name, padded to the indent, in its colour.
func (t *transcript) speaker(who string) string {
	pad := "  " + who + strings.Repeat(" ", len(indent)-2-len(who))
	if who == "you" {
		return t.st.paint(roleYou, pad)
	}
	return t.st.paint(roleBrand, pad)
}

// partial is the line of the agent message that has no end yet, or "".
func (t *transcript) partial() string {
	if !t.writing {
		return ""
	}
	return t.head + t.text
}

// end puts the partial line in the lines and ends the agent message.
func (t *transcript) end() []string {
	if !t.writing {
		return nil
	}
	out := []string{t.head + t.text}
	t.head, t.text, t.writing, t.msgID = "", "", false, ""
	return out
}

// room is how many cells of words fit after the indent; 0 is no limit.
func (t *transcript) room() int {
	if t.width <= len(indent)+10 {
		return 0
	}
	return t.width - len(indent) - 1
}

// rows is a line of words under a head, wrapped at the room there is, the rest indented.
func (t *transcript) rows(head, text string) []string {
	parts := wrap(text, t.room())
	out := []string{head + parts[0]}
	for _, p := range parts[1:] {
		out = append(out, indent+p)
	}
	return out
}

// entry is the lines e adds; an entry seen before (from the page read on attach, then
// live) adds nothing.
func (t *transcript) entry(e wire.TranscriptEntry) []string {
	if e.ID != "" {
		if t.seen[e.ID] {
			return nil
		}
		t.seen[e.ID] = true
	}
	k := e.Kind
	if k.AgentMessage != nil {
		return t.agentChunk(k.AgentMessage)
	}
	if e.SubagentID != nil {
		return nil
	}
	switch {
	case k.UserMessage != nil:
		if t.seenText != "" && strings.TrimSpace(k.UserMessage.Value) == strings.TrimSpace(t.seenText) {
			t.seenText = ""
			return t.end()
		}
		return append(t.end(), t.block(t.speaker("you"), k.UserMessage.Value)...)
	case k.ToolCall != nil:
		return append(t.end(), indent+t.st.paint(roleQuiet, "· "+oneLine(k.ToolCall.Value.Title)))
	case k.ToolCallUpdate != nil:
		tc := k.ToolCallUpdate.Value
		if tc.Status != nil && *tc.Status == "failed" {
			return append(t.end(), indent+t.st.paint(roleFailure, "✗ "+oneLine(tc.Title)+" failed"))
		}
		return nil
	case k.Notice != nil:
		n := k.Notice.Value
		line := "! " + oneLine(n.Title)
		if n.Detail != nil && *n.Detail != "" {
			line += ": " + oneLine(*n.Detail)
		}
		ro := roleWarning
		if n.Severity == "error" {
			ro = roleFailure
		}
		return append(t.end(), indent+t.st.paint(ro, line))
	case k.PolicyStopped != nil:
		return append(t.end(), indent+t.st.paint(roleFailure, "! The session's policy stopped it."))
	}
	return nil
}

// agentChunk adds a chunk of an agent message: a new message starts under "agent".
func (t *transcript) agentChunk(m *wire.TranscriptEntryKindAgentMessage) []string {
	id := ""
	if m.MessageID != nil {
		id = *m.MessageID
	}
	var out []string
	if !t.writing || id != t.msgID {
		out = t.end()
		t.writing, t.msgID = true, id
		t.head = t.speaker("agent")
	}
	text := strings.ReplaceAll(m.Text, "\r\n", "\n")
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			t.text += text
			break
		}
		out = append(out, t.rows(t.head, t.text+text[:i])...)
		t.head, t.text = indent, ""
		text = text[i+1:]
	}
	// A line longer than the room goes up a row at a time; its last row stays partial.
	if rows := t.rows(t.head, t.text); len(rows) > 1 {
		out = append(out, rows[:len(rows)-1]...)
		t.head, t.text = indent, strings.TrimPrefix(rows[len(rows)-1], indent)
	}
	return out
}

// block is text under a speaker: its first line after the name, the rest indented.
func (t *transcript) block(head, text string) []string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	out := t.rows(head, lines[0])
	for _, l := range lines[1:] {
		out = append(out, t.rows(indent, l)...)
	}
	return out
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// box is a card (wireframe 3): a frame in the attention colour, a title on its top edge
// and a foot on its bottom edge, the body lines inside, at most width cells wide.
func box(st style, title string, body []string, foot string, width int) []string {
	inner := 56
	for _, l := range body {
		inner = max(inner, visibleWidth(l)+2)
	}
	inner = min(inner, max(width-2, 20))
	edge := func(left, text, right string) string {
		label := "─ " + text + " "
		fill := max(inner-visibleWidth(label), 0)
		return st.paint(roleAttention, left+label+strings.Repeat("─", fill)+right)
	}
	out := []string{edge("╭", st.bold(title), "╮")}
	bar := st.paint(roleAttention, "│")
	for _, l := range body {
		for _, part := range wrap(l, inner-2) {
			out = append(out, bar+" "+part+strings.Repeat(" ", max(inner-2-visibleWidth(part), 0))+" "+bar)
		}
	}
	return append(out, edge("╰", foot, "╯"))
}

// wrap cuts a line of plain text (or one coloured whole) to at most n cells a piece.
func wrap(line string, n int) []string {
	if n <= 0 || visibleWidth(line) <= n || strings.ContainsRune(line, 0x1b) {
		return []string{line}
	}
	var out []string
	r := []rune(line)
	for len(r) > n {
		cut := n
		for i := n; i > n/2; i-- {
			if r[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " "))
		r = []rune(strings.TrimLeft(string(r[cut:]), " "))
	}
	return append(out, string(r))
}

// card is a question or a permission that waits, as the terminal asks it. A form asks its
// fields one at a time; step is the field it asks now.
type card struct {
	permission  *wire.PermissionRequest
	elicitation *wire.ElicitationRequest
	step        int
	content     map[string]wire.JSONValue
}

func (c *card) id() wire.UUID {
	if c.permission != nil {
		return c.permission.ID
	}
	return c.elicitation.ID
}

// answerable says the terminal can answer it: a permission, or a form (not a secret, a
// step in the browser or a page to open, which another client answers).
func (c *card) answerable() bool {
	if c.permission != nil {
		return true
	}
	e := c.elicitation
	return e.Mode.Form != nil && e.Secret == nil && e.HandOff == nil
}

func (c *card) field() *wire.ElicitationSchemaProperty {
	if c.elicitation == nil || c.elicitation.Mode.Form == nil {
		return nil
	}
	props := c.elicitation.Mode.Form.Value.Properties
	if c.step >= len(props) {
		return nil
	}
	return &props[c.step]
}

// choices are a field's numbered answers: its choices, or yes and no.
func choices(p *wire.ElicitationSchemaProperty) []wire.ElicitationSchemaPropertyChoice {
	switch k := p.Kind; {
	case k.String != nil && len(k.String.Choices) > 0:
		return k.String.Choices
	case k.MultiSelect != nil:
		return k.MultiSelect.Items
	case k.Boolean != nil:
		return []wire.ElicitationSchemaPropertyChoice{{Value: "true", Title: "Yes"}, {Value: "false", Title: "No"}}
	}
	return nil
}

const cardFoot = "answer here, or on any client (Needs you)"

// lines is the card's box.
func (c *card) lines(st style, folder string, width int) []string {
	num := func(i int, text string) string { return fmt.Sprintf(" %s  %s", st.bold(strconv.Itoa(i)), text) }
	if c.permission != nil {
		p := c.permission
		body := []string{permissionAsk(p.ToolCall, folder)}
		for i, o := range p.Options {
			body = append(body, num(i+1, o.Name))
		}
		return box(st, "Permission", body, cardFoot, width)
	}
	e := c.elicitation
	var body []string
	if e.Message != nil && *e.Message != "" {
		body = append(body, strings.Split(strings.TrimSpace(*e.Message), "\n")...)
	}
	switch {
	case e.Secret != nil:
		body = append(body, "It asks for the secret "+e.Secret.Name+".")
		return box(st, "Secret", body, "answer on a client (Needs you)", width)
	case e.HandOff != nil:
		body = append(body, "A step for you in the machine's browser: "+oneLine(e.HandOff.Step))
		return box(st, "Your step", body, "answer on a client (Needs you)", width)
	case e.Mode.URL != nil:
		body = append(body, "Open: "+e.Mode.URL.Value)
		return box(st, "Question", body, "answer on a client (Needs you)", width)
	}
	if p := c.field(); p != nil {
		if p.Title != nil && *p.Title != "" && (e.Message == nil || *p.Title != *e.Message) {
			body = append(body, st.bold(*p.Title))
		}
		if p.Description != nil && *p.Description != "" {
			body = append(body, *p.Description)
		}
		cs := choices(p)
		for i, ch := range cs {
			body = append(body, num(i+1, ch.Title))
		}
		switch {
		case p.Kind.MultiSelect != nil:
			body = append(body, st.paint(roleQuiet, "type the numbers, as 1,3"))
		case len(cs) == 0:
			body = append(body, st.paint(roleQuiet, "type the answer"))
		}
		if !p.IsRequired {
			body = append(body, st.paint(roleQuiet, "⏎ alone to leave it out"))
		}
	}
	title := "Question"
	if n := len(e.Mode.Form.Value.Properties); n > 1 {
		title = fmt.Sprintf("Question %d of %d", c.step+1, n)
	}
	return box(st, title, body, cardFoot, width)
}

// permissionAsk is what a permission asks, in one line: "Run scripts/x.py in agent-loops?"
func permissionAsk(tc wire.ToolCall, folder string) string {
	what := oneLine(tc.Title)
	if what == "" {
		what = "this tool"
	}
	if folder != "" {
		return fmt.Sprintf("%s in %s?", what, folder)
	}
	return what + "?"
}

// answer reads what the person typed for the card's current question: done says the card
// is answered whole (then content, or the option, is ready); a problem says what to type.
func (c *card) answer(text string) (optionID string, done bool, problem string) {
	text = strings.TrimSpace(text)
	if c.permission != nil {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > len(c.permission.Options) {
			return "", false, fmt.Sprintf("Type 1 to %d, or answer on any client.", len(c.permission.Options))
		}
		return c.permission.Options[n-1].OptionID, true, ""
	}
	p := c.field()
	if p == nil {
		return "", true, ""
	}
	if c.content == nil {
		c.content = map[string]wire.JSONValue{}
	}
	value, problem := fieldValue(p, text)
	if problem != "" {
		return "", false, problem
	}
	if value != nil {
		c.content[p.Name] = value
	}
	c.step++
	return "", c.field() == nil, ""
}

// fieldValue is the JSON of a typed answer to one field; nil leaves out a field that is
// not required.
func fieldValue(p *wire.ElicitationSchemaProperty, text string) (wire.JSONValue, string) {
	if text == "" {
		if p.IsRequired {
			return nil, "This one needs an answer."
		}
		return nil, ""
	}
	cs := choices(p)
	pick := func(s string) (string, bool) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > len(cs) {
			return "", false
		}
		return cs[n-1].Value, true
	}
	k := p.Kind
	switch {
	case k.Boolean != nil:
		v, ok := pick(text)
		if !ok {
			return nil, "Type 1 for yes or 2 for no."
		}
		return wire.JSONValue(v), ""
	case k.MultiSelect != nil:
		var picked []string
		for _, s := range strings.Split(text, ",") {
			v, ok := pick(s)
			if !ok {
				return nil, fmt.Sprintf("Type numbers from 1 to %d, as 1,3.", len(cs))
			}
			picked = append(picked, v)
		}
		b, _ := json.Marshal(picked)
		return b, ""
	case len(cs) > 0:
		v, ok := pick(text)
		if !ok {
			return nil, fmt.Sprintf("Type 1 to %d.", len(cs))
		}
		b, _ := json.Marshal(v)
		return b, ""
	case k.Integer != nil:
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return nil, "Type a whole number."
		}
		return wire.JSONValue(text), ""
	case k.Number != nil:
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return nil, "Type a number."
		}
		return wire.JSONValue(text), ""
	}
	b, _ := json.Marshal(text)
	return b, ""
}
