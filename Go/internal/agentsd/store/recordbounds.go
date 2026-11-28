package store

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The largest record of the stores that hold a session (#7244, epic #6669). A session is
// never dropped, and its save is never refused, for a part of it that is large: the large
// part is cut, and the log says so.

const (
	// AgentRecordMost is the most an agent's agent.json is.
	AgentRecordMost = 1 << 20
	// summaryMost is the most one archived agent's summary is in archived-summaries.json. A
	// record over it is left out, and read in full at the next start.
	summaryMost = 256 << 10
	// summariesMost is the most archived-summaries.json is. The newest records are kept.
	summariesMost = 64 << 20
	// cutText is the longest a string is when a line is cut to size.
	cutText = 1 << 10
)

// boundedRecord is a's agent.json: whole when it is at most AgentRecordMost. Else its large
// parts are cut, the largest kinds first, until it fits; and the log says what was cut.
func boundedRecord(a *wire.Agent) ([]byte, error) {
	b, err := diskjson.Marshal(a)
	if err != nil || len(b) <= AgentRecordMost {
		return b, err
	}
	c := *a
	var cut []string
	steps := []struct {
		name string
		do   func()
	}{
		{"commands", func() { c.AvailableCommands = noCommands }},
		{"advertised options", func() { c.AdvertisedOptions = []wire.ConfigOption{} }},
		{"plans", func() { c.Plans = nil }},
		{"background items", func() { c.Background = nil }},
		{"queued prompts", func() { c.QueuedPrompts = nil }},
		{"suggested prompts", func() { c.SuggestedPrompts = nil }},
		{"MCP servers", func() { c.McpServers = nil }},
		{"report", func() { c.Report = nil }},
		{"parent notices", func() {
			if c.ParentNotices != nil {
				n := *c.ParentNotices
				n.Pending = nil
				c.ParentNotices = &n
			}
		}},
		{"labels", func() { c.Labels = nil }},
		{"directories", func() { c.AdditionalDirectories = nil }},
		{"costs", func() { c.CostToDate = nil }},
		{"start options", func() {
			c.StartOptions = wire.StartOptions{Values: map[string]wire.JSONValue{}, ExtraArguments: []string{}}
		}},
		{"title", func() {
			c.Title = cutString(c.Title)
			c.RuntimeTitle = cutString(c.RuntimeTitle)
			c.ArchiveWhy = cutString(c.ArchiveWhy)
		}},
	}
	for _, step := range steps {
		step.do()
		cut = append(cut, step.name)
		if b, err = diskjson.Marshal(&c); err != nil || len(b) <= AgentRecordMost {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	log.Printf("agent %s: its record is over %d bytes: not kept: %s", a.ID, AgentRecordMost, strings.Join(cut, ", "))
	return b, nil
}

func cutString(s *string) *string {
	if s == nil || len(*s) <= cutText {
		return s
	}
	t := CutText(*s, cutText)
	return &t
}

// CutText is s cut to at most n bytes at a whole character, with … where it was cut.
func CutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := max(0, n-len("…"))
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}

// CutLine is the JSON line b cut to at most limit bytes when it is over: a long string is
// cut, then a long list. A line of what is left that is still over is returned as it is, as
// the cut holds each part to a fixed size, and the second result is whether it was cut.
func CutLine(b []byte, limit int) ([]byte, bool) {
	if len(b) <= limit {
		return b, false
	}
	var tree any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&tree); err != nil {
		return b, false
	}
	for _, pass := range []struct{ text, list int }{{cutText, 1 << 30}, {cutText, 64}, {128, 8}} {
		tree = cutTree(tree, pass.text, pass.list)
		var out bytes.Buffer
		e := json.NewEncoder(&out)
		e.SetEscapeHTML(false)
		if err := e.Encode(tree); err != nil {
			return b, false
		}
		b = bytes.TrimRight(out.Bytes(), "\n")
		if len(b) <= limit {
			break
		}
	}
	return b, true
}

func cutTree(v any, text, list int) any {
	switch v := v.(type) {
	case string:
		return CutText(v, text)
	case []any:
		if len(v) > list {
			v = v[:list]
		}
		for i := range v {
			v[i] = cutTree(v[i], text, list)
		}
		return v
	case map[string]any:
		for k := range v {
			v[k] = cutTree(v[k], text, list)
		}
		return v
	}
	return v
}
