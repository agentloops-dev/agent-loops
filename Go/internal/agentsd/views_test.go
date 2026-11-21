package agentsd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Ports ACPToolShapeTests, AppViewPolicyTests and ThirdPartyViewsTests: a person's server's
// tool call read by its shape, drawn as an appView entry, its view read once the person
// says Show, and its view's calls passed only to its own server.

func ptr[T any](v T) *T { return &v }

func TestAToolCallIsReadByItsShape(t *testing.T) {
	t.Parallel()

	servers := []string{"ci", "ci-board", "github"}
	exact := shapeCandidates(wire.ToolCall{Title: "x", RawInput: json.RawMessage(`{"server":"ci","tool":"board","arguments":{"n":1}}`)}, servers)
	if len(exact) != 1 || exact[0].server != "ci" || exact[0].tool != "board" || !exact[0].exact || string(exact[0].arguments) != `{"n":1}` {
		t.Fatalf("%+v", exact)
	}
	if got := shapeCandidates(wire.ToolCall{RawInput: json.RawMessage(`{"server":"other","tool":"board"}`)}, servers); got != nil {
		t.Fatalf("an unknown server was read: %+v", got)
	}
	named := shapeCandidates(wire.ToolCall{Title: "mcp__ci-board__show"}, servers)
	if len(named) != 1 || named[0].server != "ci-board" || named[0].tool != "show" || !named[0].exact {
		t.Fatalf("%+v", named)
	}
	// A joined title: the longest server first, and not trusted until it is answered.
	joined := shapeCandidates(wire.ToolCall{Title: "ci-board-show"}, servers)
	if len(joined) != 2 || joined[0].server != "ci-board" || joined[1].server != "ci" || joined[0].exact {
		t.Fatalf("%+v", joined)
	}

	if _, ok := shapeResultOf(wire.ToolCall{Status: ptr("in_progress")}); ok {
		t.Fatal("a running call has a result")
	}
	whole, _ := shapeResultOf(wire.ToolCall{Status: ptr("completed"), RawOutput: json.RawMessage(`{"result":{"content":[],"structuredContent":{"a":1}}}`)})
	if !whole.drawable || string(whole.value) != `{"content":[],"structuredContent":{"a":1}}` {
		t.Fatalf("%+v", whole)
	}
	text, _ := shapeResultOf(wire.ToolCall{Status: ptr("completed"), RawOutput: json.RawMessage(`"{\"a\":1}"`)})
	if !text.drawable || !strings.Contains(string(text.value), `"structuredContent":{"a":1}`) {
		t.Fatalf("%s", text.value)
	}
	plain, _ := shapeResultOf(wire.ToolCall{Status: ptr("completed"), RawOutput: json.RawMessage(`"just words"`)})
	if plain.drawable {
		t.Fatalf("text alone is drawable: %s", plain.value)
	}
	failed, _ := shapeResultOf(wire.ToolCall{Status: ptr("failed")})
	if failed.drawable || !strings.Contains(string(failed.value), `"isError":true`) {
		t.Fatalf("%s", failed.value)
	}
}

func TestAViewsPolicyKeepsOnlyPlainOrigins(t *testing.T) {
	t.Parallel()

	p := viewPolicy(json.RawMessage(`{"connectDomains":["https://API.example.com/","*","https:","https://*.cdn.example.com:8443"],
		"resourceDomains":["'unsafe-eval'","wss://x.example.com; script-src *"],"frameDomains":[3]}`))
	if strings.Join(p.ConnectDomains, " ") != "https://api.example.com https://*.cdn.example.com:8443" ||
		len(p.ResourceDomains) != 0 || len(p.FrameDomains) != 0 || len(p.Refused) != 5 {
		t.Fatalf("%+v", p)
	}
	if line := policyLogLine(viewPolicy(nil)); line != "strict (no network)" {
		t.Fatal(line)
	}
	if v := viewThemeVariables(); len(v) != 76 || v["--font-heading-3xl-size"] != "34px" ||
		v["--color-background-info"] != "light-dark(color-mix(in srgb, #1f5fc8 14%, #f7f8f9), color-mix(in srgb, #7fb0ff 14%, #1c1d20))" {
		t.Fatalf("%d %v", len(v), v["--color-background-info"])
	}
}

// fakeViewServer is a streamable-http MCP server with tools a view may call, a tool only
// the model may call, and a view.
func fakeViewServer(t *testing.T, calls *atomic.Int32, catalogReadCounts ...*atomic.Int32) *httptest.Server {
	html := "<!doctype html><p>board</p>"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		b, _ := io.ReadAll(r.Body)
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
				URI  string `json:"uri"`
			} `json:"params"`
		}
		check(t, json.Unmarshal(b, &m))
		var result any
		switch m.Method {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "ci"}}
		case "tools/list":
			if len(catalogReadCounts) > 0 {
				catalogReadCounts[0].Add(1)
			}
			result = map[string]any{"tools": []any{
				map[string]any{"name": "board", "_meta": map[string]any{"ui": map[string]any{"resourceUri": "ui://ci/board"}},
					"annotations": map[string]any{"readOnlyHint": true}},
				map[string]any{"name": "refresh", "_meta": map[string]any{"ui": map[string]any{"visibility": []string{"app"}}}},
				map[string]any{"name": "details", "_meta": map[string]any{"ui": map[string]any{"visibility": []string{"model", "app"}}}},
				map[string]any{"name": "deploy", "_meta": map[string]any{"ui": map[string]any{"visibility": []string{"model"}}}},
			}}
		case "resources/list":
			result = map[string]any{"resources": []any{map[string]any{"uri": "ui://ci/board", "name": "board", "title": "Pull requests"}}}
		case "resources/read":
			result = map[string]any{"contents": []any{map[string]any{"uri": m.Params.URI, "mimeType": "text/html;profile=mcp-app", "text": html,
				"_meta": map[string]any{"ui": map[string]any{"prefersBorder": true, "csp": map[string]any{"connectDomains": []string{"https://api.ci.dev"}}}}}}}
		case "tools/call":
			calls.Add(1)
			result = map[string]any{"content": []any{}, "structuredContent": map[string]any{"called": m.Params.Name}}
		}
		w.Header().Set("Content-Type", "application/json")
		check(t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestAPersonsServersViewIsDrawnOnceShownAndCallsOnlyItsOwnServer(t *testing.T) {
	t.Parallel()

	var calls, catalogReads atomic.Int32
	server := fakeViewServer(t, &calls, &catalogReads)
	home := t.TempDir()
	personalHomeFor(t, home)
	must(t, os.MkdirAll(filepath.Join(home, ".agents"), 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	//nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, os.WriteFile(filepath.Join(home, ".agents", "mcp.json"), []byte(`{"mcpServers":{"ci":{"url":"`+server.URL+`"},
		"local":{"command":"local-server"}}}`), 0o644))
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	agent := wire.Agent{ID: store.NewUUID(), RuntimeID: "claude", Cwd: store.FolderURL(dir), State: wire.AgentStateFinished, EndedReason: ptr(wire.EndedReasonEndTurn),
		Title: ptr("Board"), AdvertisedOptions: []wire.ConfigOption{}, AvailableCommands: []wire.SlashCommand{}}
	if err := d.Store.Save(agent); err != nil {
		t.Fatal(err)
	}

	v := personsViewDrawn(t, d, agent.ID)

	personsViewShown(t, d, c, agent.ID)

	personsViewCalls(t, d, c, agent.ID, v[0].ID, dir, &calls, &catalogReads)

	// What the view asks the agent to know goes before the person's next words, once.
	c.call(t, 10, "views/context", map[string]any{"agentID": agent.ID, "viewID": v[0].ID,
		"content": []any{map[string]any{"type": "text", "text": "PR 7 is picked"}}})
	q := d.withViewContext(agent.ID, wire.QueuedPrompt{Text: "go on", From: wire.PromptOriginPerson})
	if q.Preface == nil || !strings.HasSuffix(*q.Preface, "PR 7 is picked") {
		t.Fatalf("%+v", q.Preface)
	}
	if q := d.withViewContext(agent.ID, wire.QueuedPrompt{Text: "again", From: wire.PromptOriginPerson}); q.Preface != nil {
		t.Fatal("the context was told twice")
	}
	c.call(t, 11, "views/log", map[string]any{"agentID": agent.ID, "viewID": v[0].ID, "level": "info", "data": "drawn"})

	// Pinned, with the server's own title, and drawn on the folder's page.
	var pins []wire.PinView
	mustUnmarshal(t, c.call(t, 12, "pins/pin", map[string]any{"folder": store.FolderURL(dir), "path": "ui://ci/board",
		"view": map[string]any{"server": "ci", "uri": "ui://ci/board", "tool": "board"}}), &pins)
	if len(pins) != 1 || pins[0].Title != "Pull requests" || pins[0].Missing || pins[0].Kind != wire.PinKindView {
		t.Fatalf("%+v", pins)
	}
	if e := c.callError(t, 13, "pins/pin", map[string]any{"folder": store.FolderURL(dir), "path": "ui://ci/board",
		"view": map[string]any{"server": "ci", "uri": "ui://ci/board", "tool": "refresh"}}); !strings.Contains(e.Message, "does not feed") {
		t.Fatalf("%+v", e)
	}
	// A pin's call names no agent: its folder's pins say which server it is of.
	if r := c.call(t, 14, "views/call", map[string]any{"agentID": store.NewUUID(), "folder": store.FolderURL(dir), "viewID": store.NewUUID(),
		"name": "board", "server": "ci", "feed": true}); !strings.Contains(string(r), `"called":"board"`) {
		t.Fatalf("%s", r)
	}
}

// personsViewDrawn notes the runtime's call of a view tool, named outright, then answered,
// and checks that the view is drawn once. It gives the view's two states.
func personsViewDrawn(t *testing.T, d *Daemon, agentID wire.UUID) []wire.AppViewCall {
	t.Helper()
	// The runtime's call, named outright, then answered.
	d.noteAgentViewCall(agentID, wire.ToolCall{ToolCallID: ptr("t1"), Title: "mcp__ci__board", Status: ptr("in_progress"),
		RawInput: json.RawMessage(`{"pr":7}`)})
	views := func() []wire.AppViewCall {
		entries, _, _ := d.Store.Transcript(agentID, 0)
		var out []wire.AppViewCall
		for _, e := range entries {
			if e.Kind.AppView != nil {
				out = append(out, e.Kind.AppView.Value)
			}
		}
		return out
	}
	waitFor(t, "the running view", func() bool { return len(views()) == 1 })
	d.noteAgentViewCall(agentID, wire.ToolCall{ToolCallID: ptr("t1"), Title: "Tool call", Status: ptr("completed"),
		RawOutput: json.RawMessage(`"{\"prs\":[7]}"`)})
	waitFor(t, "the answered view", func() bool { return len(views()) == 2 })
	v := views()
	if v[0].ID != v[1].ID || v[0].State != wire.AppViewCallStateRunning || v[1].State != wire.AppViewCallStateDone ||
		v[1].Server != "ci" || v[1].ResourceUri != "ui://ci/board" || string(v[1].Arguments) != `{"pr":7}` ||
		v[1].Pinnable == nil || !strings.Contains(string(v[1].Result), `"prs":[7]`) {
		t.Fatalf("%+v", v)
	}
	// A call of a tool without a view writes nothing.
	d.noteAgentViewCall(agentID, wire.ToolCall{ToolCallID: ptr("t2"), Title: "mcp__ci__deploy", Status: ptr("completed"),
		RawOutput: json.RawMessage(`"{}"`)})
	return v
}

// personsViewShown checks that the view is read only once the person shows it, and that
// the approval is kept.
func personsViewShown(t *testing.T, d *Daemon, c *conn, agentID wire.UUID) {
	t.Helper()
	read := map[string]any{"agentID": agentID, "uri": "ui://ci/board", "server": "ci"}
	e := c.callError(t, 2, "views/read", read)
	var ask wire.ViewAsk
	b, _ := json.Marshal(e.Data)
	mustUnmarshal(t, b, &ask)
	if e.Code != wire.FailureViewNeedsShow || !ask.IsNew || ask.Server != "ci" || ask.Hash == "" {
		t.Fatalf("%+v", e)
	}
	c.call(t, 3, "views/show", map[string]any{"agentID": agentID, "server": "ci", "uri": "ui://ci/board", "hash": ask.Hash, "show": true})
	var res wire.ViewResource
	mustUnmarshal(t, c.call(t, 4, "views/read", read), &res)
	if res.HTML != "<!doctype html><p>board</p>" || res.MimeType != viewHTML || res.PrefersBorder == nil || !*res.PrefersBorder ||
		strings.Join(res.Policy.ConnectDomains, " ") != "https://api.ci.dev" || res.Variables["--border-radius-md"] != "10px" {
		t.Fatalf("%+v", res)
	}
	approvals, _ := os.ReadFile(filepath.Join(d.Store.Loc.Root, "mcp-approvals.json"))
	if !strings.Contains(string(approvals), `"personal|ci|ui://ci/board":"show:`+ask.Hash+`"`) {
		t.Fatalf("%s", approvals)
	}
}

// personsViewCalls checks that the view calls only its own server's tools, and that a
// stale catalog is read again for a tool it does not know.
func personsViewCalls(t *testing.T, d *Daemon, c *conn, agentID, viewID wire.UUID, dir string, calls, catalogReads *atomic.Int32) {
	t.Helper()
	call := func(id int, name string, server string) (json.RawMessage, *Error) {
		p := map[string]any{"agentID": agentID, "viewID": viewID, "name": name, "server": server}
		if (name == "refresh" || name == "details") && server == "ci" {
			return c.call(t, id, "views/call", p), nil
		}
		return nil, c.callError(t, id, "views/call", p)
	}
	// A fresh but stale catalog may not know about a tool the server added after it was read.
	s, err := d.readyViewServer("ci", dir)
	if err != nil {
		t.Fatal(err)
	}
	d.keepViewCatalog(s.entry, viewCatalog{Version: viewCatalogVersion, Server: "ci", MadeAt: nowFunc().Add(-time.Hour), AppTools: []string{"refresh"}})
	readsBeforeRefresh := catalogReads.Load()
	if r, _ := call(5, "details", "ci"); !strings.Contains(string(r), `"called":"details"`) || calls.Load() != 1 || catalogReads.Load() != readsBeforeRefresh+1 {
		t.Fatalf("%s: %d call(s), %d catalog read(s) after %d", r, calls.Load(), catalogReads.Load(), readsBeforeRefresh)
	}
	if _, e := call(6, "deploy", "ci"); e.Code != wire.FailureViewRefused || calls.Load() != 1 || catalogReads.Load() != readsBeforeRefresh+1 {
		t.Fatalf("a view called a tool only the model may after the refresh: %+v", e)
	}
	if r, _ := call(7, "refresh", "ci"); !strings.Contains(string(r), `"called":"refresh"`) || calls.Load() != 2 {
		t.Fatalf("%s", r)
	}
	if _, e := call(8, "refresh", "local"); e.Code != wire.FailureViewRefused || !strings.Contains(e.Message, "own server's") {
		t.Fatalf("%+v", e)
	}
	if e := c.callError(t, 9, "views/read", map[string]any{"agentID": agentID, "uri": "ui://x", "server": "local"}); !strings.Contains(e.Message, "local servers") {
		t.Fatalf("%+v", e)
	}
}
