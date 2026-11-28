package agentsd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Ports AppToolsEndpointTests and what AppServiceTests hold of the tools this host does:
// a token per runtime in its header, 404 for one nobody holds, sessions from initialize,
// the Swift host's tools word for word, and a call that changes the agent's own record.

type mcpClient struct {
	t       *testing.T
	client  *http.Client
	url     string
	token   string
	session string
}

// newMCPClient has a connection pool of its own, closed when the test ends, and a time
// limit: on http.DefaultClient a call hung for 9 minutes under -race (#4593).
func newMCPClient(t *testing.T, url, token string) *mcpClient {
	c := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: 2 * time.Minute}
	t.Cleanup(c.CloseIdleConnections)
	return &mcpClient{t: t, client: c, url: url, token: token}
}

func (m *mcpClient) post(method string, params any, id int) (int, map[string]json.RawMessage) {
	m.t.Helper()
	code, out, err := m.tryPost(method, params, id)
	if err != nil {
		m.t.Fatal(err)
	}
	return code, out
}

// tryPost is post that says what failed rather than failing the test, for a goroutine
// that may still run after its test ends (#6495).
func (m *mcpClient) tryPost(method string, params any, id int) (int, map[string]json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	req, _ := http.NewRequest("POST", m.url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")
	if m.session != "" {
		req.Header.Set("Mcp-Session-Id", m.session)
	}
	res, err := m.client.Do(req)
	if err != nil {
		// A call that gets no answer is a daemon goroutine blocked somewhere: say where (#6395).
		var stacks bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2) // a bytes.Buffer does not fail
		return 0, nil, fmt.Errorf("%s %s: %w\n%s", method, m.url, err, stacks.String())
	}
	defer res.Body.Close()
	if s := res.Header.Get("Mcp-Session-Id"); s != "" {
		m.session = s
	}
	b, _ := io.ReadAll(res.Body)
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil && len(b) > 0 {
		return res.StatusCode, nil, fmt.Errorf("%s %s: %d %s: %w", method, m.url, res.StatusCode, b, err)
	}
	return res.StatusCode, out, nil
}

func appClient(t *testing.T, d *Daemon, id wire.UUID) *mcpClient {
	t.Helper()
	servers := d.mcpServersFor(id)
	if len(servers) != 1 || servers[0].Http == nil || servers[0].Http.Name != "agents" {
		t.Fatalf("%+v", servers)
	}
	h := servers[0].Http
	if !strings.HasPrefix(h.Url, "http://127.0.0.1:") || !strings.HasSuffix(h.Url, "/mcp/agents") || len(h.Headers) != 1 {
		t.Fatalf("%+v", h)
	}
	return newMCPClient(t, h.Url, strings.TrimPrefix(h.Headers[0].Value, "Bearer "))
}

func TestTheAppsToolsAreServedToEachRuntime(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	if _, err := d.saveDesktopControl(wire.DriveApprovalOff); err != nil {
		t.Fatal(err)
	}
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.Labels = []wire.SessionLabel{{Value: "Mine", Owner: wire.SessionLabelOwnerPerson, AddedAt: now()}}
	})
	m := appClient(t, d, a.ID)

	stranger := newMCPClient(t, m.url, "nobody")
	if code, _ := stranger.post("initialize", map[string]any{}, 1); code != 404 {
		t.Fatalf("a stranger's token: %d", code)
	}
	code, init := m.post("initialize", map[string]any{"protocolVersion": "2025-03-26"}, 1)
	if code != 200 || m.session == "" || !strings.Contains(string(init["result"]), `"protocolVersion":"2025-03-26"`) {
		t.Fatalf("%d %s", code, init["result"])
	}
	_, list := m.post("tools/list", nil, 2)
	var tools struct {
		Tools []struct {
			Name        string                     `json:"name"`
			Description string                     `json:"description"`
			InputSchema map[string]json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	mustUnmarshal(t, list["result"], &tools)
	// drive_screen is the one tool this host may not offer at all: on a host with no
	// agents-drive beside it (#3004, host.desktop-control/1), and on every host while desktop
	// control is Off (FR-004), as it is here. Nor is check_folders, but in the person's home
	// folder (#3355).
	if d.desktopControlOn() || d.offersDriveScreen() {
		t.Fatal("desktop control is on while it is Off")
	}
	// meeting_notes is listed only in a meeting's session (#7583).
	want := len(appToolCalls()) - 3
	// After them, Playwright's browser tools, where this host can install the browser (#3534).
	if d.offersBrowser() {
		list, _ := browserTools()
		want += len(list)
	}
	if len(tools.Tools) != want || tools.Tools[0].Name != "show_file" || tools.Tools[0].Description == "" ||
		!strings.Contains(string(tools.Tools[0].InputSchema["properties"]), `"description"`) {
		t.Fatalf("%s", list["result"])
	}

	call := func(id int, args map[string]any) (string, bool) {
		_, r := m.post("tools/call", map[string]any{"name": "mcp__agents__set_session_labels", "arguments": args}, id)
		var res struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		}
		mustUnmarshal(t, r["result"], &res)
		return res.Content[0].Text, res.IsError
	}
	if text, isErr := call(3, map[string]any{"add": "review"}); isErr || text != "This session's labels: Mine, review." {
		t.Fatal(text)
	}
	if got := d.Store.Agent(a.ID).Labels; got[1].Owner != wire.SessionLabelOwnerAgent {
		t.Fatalf("%+v", got)
	}
	if text, isErr := call(4, map[string]any{"remove": []string{"mine"}}); !isErr || text != "Nothing changed: The label Mine belongs to the person, so an agent cannot change it." {
		t.Fatal(text)
	}
	if text, isErr := call(5, map[string]any{}); !isErr || text != "Nothing changed: give labels in add, remove, or both." {
		t.Fatal(text)
	}
	if _, r := m.post("tools/call", map[string]any{"name": "no_such"}, 6); !strings.Contains(string(r["error"]), "No tool called no_such.") {
		t.Fatalf("%s", r["error"])
	}

	// A new runtime is a new token: the old one is a server that is not there.
	old := *m
	appClient(t, d, a.ID)
	if code, _ := old.post("ping", nil, 7); code != 404 {
		t.Fatalf("the old token: %d", code)
	}
	// A browser page is not for this.
	req, _ := http.NewRequest("POST", m.url, strings.NewReader("{}"))
	req.Header.Set("Origin", "https://example.com")
	if res, _ := m.client.Do(req); res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
}

// An agent's add of a label the person already gave is skipped, with no error: the other
// labels in the same call are added, and the person stays the owner (#6177).
func TestAnAgentsAddOfAPersonsLabelIsSkipped(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.Labels = []wire.SessionLabel{
			{Value: "#5", Owner: wire.SessionLabelOwnerPerson, AddedAt: now()},
			{Value: "lead", Owner: wire.SessionLabelOwnerPerson, AddedAt: now()},
		}
	})
	m := appClient(t, d, a.ID)
	if code, _ := m.post("initialize", map[string]any{"protocolVersion": "2025-03-26"}, 1); code != 200 {
		t.Fatal(code)
	}
	_, r := m.post("tools/call", map[string]any{"name": "mcp__agents__set_session_labels",
		"arguments": map[string]any{"add": []string{"#5", "lead", "P#9"}}}, 2)
	var res struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	if err := json.Unmarshal(r["result"], &res); err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != "This session's labels: #5, lead, P#9." {
		t.Fatalf("%s", r["result"])
	}
	got := d.Store.Agent(a.ID).Labels
	want := []wire.SessionLabelOwner{wire.SessionLabelOwnerPerson, wire.SessionLabelOwnerPerson, wire.SessionLabelOwnerAgent}
	if len(got) != 3 || got[2].Value != "P#9" {
		t.Fatalf("%+v", got)
	}
	for i, l := range got {
		if l.Owner != want[i] {
			t.Fatalf("%d: %+v", i, got)
		}
	}
}

// offeredTools leaves drive_screen out unless desktop control is both available on this host
// and turned on (#3004, FR-004): an agent never sees a tool it cannot use.
func TestDriveScreenIsOfferedOnlyWhileDesktopControlIsOn(t *testing.T) {
	t.Parallel()

	offers := func(desktopControl bool) bool {
		for _, raw := range offeredTools(false, false, desktopControl, false) {
			if strings.Contains(string(raw), `"name":"drive_screen"`) {
				return true
			}
		}
		return false
	}
	if offers(false) {
		t.Fatal("drive_screen offered while desktop control is off")
	}
	if !offers(true) {
		t.Fatal("drive_screen not offered while desktop control is on")
	}
}

// tools/list marks the tools that nearly every lane calls with Claude Code's
// anthropic/alwaysLoad, so Claude does not defer them and refuse the first call (#5187).
// No other tool has the mark: each one would add its schema to every prompt.
func TestTheToolsEveryLaneCallsAreAlwaysLoaded(t *testing.T) {
	t.Parallel()

	marked := map[string]bool{}
	for _, raw := range offeredTools(false, false, false, false) {
		var tool struct {
			Name string `json:"name"`
			Meta struct {
				AlwaysLoad *bool `json:"anthropic/alwaysLoad"`
			} `json:"_meta"`
		}
		mustUnmarshal(t, raw, &tool)
		if tool.Meta.AlwaysLoad != nil {
			if !*tool.Meta.AlwaysLoad {
				t.Fatalf("%s: alwaysLoad false", tool.Name)
			}
			marked[tool.Name] = true
		}
	}
	want := []string{"set_session_labels", "lease_resource", "release_resource", "wait_for_event", "message_session", "commit_trailers"}
	for _, name := range want {
		if !marked[name] {
			t.Errorf("%s is not always loaded", name)
		}
	}
	if len(marked) != len(want) {
		t.Errorf("always loaded: %v, want only %v", marked, want)
	}
}
