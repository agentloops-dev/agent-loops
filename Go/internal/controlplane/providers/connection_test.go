package providers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Reading providers' tools (#691): ToolMappingTests.

func parsed(t *testing.T, text string) any {
	t.Helper()
	v, err := parseJSON([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTheContractsResultsAreRead(t *testing.T) {
	content, err := Content(parsed(t, `{"content": [{"type": "text", "text": "…"}],
		"structuredContent": {"contract": 1, "workspaces": [
			{"id": "a", "name": "devbox", "state": "stopped", "ssh": "alex@h -p 2222", "tunnel": true, "cost": "$1",
				"idleTimeoutMinutes": 240},
			{"id": "b", "name": "odd", "state": "hibernating"},
			{"name": "no id"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	want := []MachineInfo{{ID: "a", Name: "devbox", State: "stopped", SSH: new("alex@h -p 2222"), Tunnel: &yes, Cost: new("$1"),
		IdleTimeoutMinutes: new(int64(240))},
		{ID: "b", Name: "odd", State: "unknown"}}
	if got := ContractMapping.Workspaces(content); !reflect.DeepEqual(got, want) {
		t.Errorf("%+v", got)
	}
}

func TestAnErrorIsTheProvidersOwnWords(t *testing.T) {
	_, err := Content(parsed(t, `{"content": [{"type": "text", "text": "Error: droplet is locked\n"}], "isError": true}`))
	if err == nil || err.Error() != "Error: droplet is locked" {
		t.Errorf("%v", err)
	}
}

func TestAnSSHDestinationWithAPortBecomesOneWord(t *testing.T) {
	if SSHDestination("alex@127.0.0.1 -p 2222") != "ssh://alex@127.0.0.1:2222" || SSHDestination("devstack-1.cws") != "devstack-1.cws" {
		t.Error("ssh destination")
	}
}

// A provider that breaks the contract (#857): MisbehavingProviderTests, with this test
// binary as the stdio server (FAKE_PROVIDER says how it misbehaves).

func fakeProvider(mode string) {
	f := &fakeServer{mode: mode, out: bufio.NewWriter(os.Stdout)}
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var message map[string]any
			if err := json.Unmarshal(line, &message); err != nil {
				fmt.Fprintln(os.Stderr, "fake provider:", err)
				continue
			}
			f.handle(message)
		}
		if err != nil {
			return
		}
	}
}

// fakeServer is the stdio server fakeProvider runs.
type fakeServer struct {
	mode    string
	out     *bufio.Writer
	lock    sync.Mutex
	dropped bool // drops-stop: once the provider was added (its first call), workspace_stop goes
}

func (f *fakeServer) send(m any) {
	f.lock.Lock()
	defer f.lock.Unlock()
	data, _ := json.Marshal(m)
	_, _ = f.out.Write(append(data, '\n'))
	f.out.Flush()
}

func (f *fakeServer) answer(id any, content any, text *string, isError bool) {
	t := ""
	if text != nil {
		t = *text
	} else {
		data, _ := json.Marshal(content)
		t = string(data)
	}
	result := map[string]any{"content": []any{map[string]any{"type": "text", "text": t}}}
	if content != nil && text == nil {
		result["structuredContent"] = content
	}
	if isError {
		result["isError"] = true
	}
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func fakeItem(extra map[string]any) map[string]any {
	out := map[string]any{"id": "devbox", "name": "devbox", "state": "stopped"}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (f *fakeServer) listing() any {
	switch f.mode {
	case "no-contract":
		return map[string]any{"workspaces": []any{fakeItem(nil)}}
	case "contract-2":
		return map[string]any{"contract": 2, "workspaces": []any{fakeItem(nil)}}
	case "odd-items":
		return map[string]any{"contract": 1, "workspaces": []any{fakeItem(map[string]any{"state": "Hibernating"}),
			map[string]any{"name": "no-id", "state": "running"}, fakeItem(map[string]any{"id": "", "name": "empty-id"}),
			fakeItem(map[string]any{"name": "twin", "state": "running"}), "not an object", fakeItem(map[string]any{"id": 7, "name": "numbered"})}}
	}
	return map[string]any{"contract": 1, "workspaces": []any{fakeItem(nil)}}
}

func (f *fakeServer) call(id any, tool string) {
	mode := f.mode
	switch {
	case tool == "workspaces_list" && mode == "not-json":
		text := "devbox is stopped"
		f.answer(id, nil, &text, false)
	case tool == "workspaces_list":
		f.answer(id, f.listing(), nil, false)
	case mode == "crash" && tool == "workspace_start":
		os.Exit(3)
	case mode == "hang" && tool == "workspace_start":
		time.Sleep(30 * time.Second)
	case mode == "refuse":
		text := "Quota exceeded. " + strings.Repeat("x", 3000)
		f.answer(id, nil, &text, true)
	case mode == "silent-refusal":
		text := ""
		f.answer(id, nil, &text, true)
	case mode == "bare-start":
		f.answer(id, map[string]any{}, nil, false)
	default:
		state := "stopped"
		if tool == "workspace_start" {
			state = "running"
		}
		f.answer(id, map[string]any{"workspace": fakeItem(map[string]any{"state": state})}, nil, false)
	}
}

// handle answers one message.
func (f *fakeServer) handle(message map[string]any) {
	method, _ := message["method"].(string)
	id := message["id"]
	switch {
	case method == "initialize":
		if f.mode == "noise" {
			f.lock.Lock()
			_, _ = f.out.WriteString("starting up...\n")
			f.out.Flush()
			f.lock.Unlock()
		}
		f.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake", "version": "1"}}})
	case method == "tools/list":
		names := []string{"workspaces_list", "workspace_start", "workspace_stop"}
		if f.mode == "no-stop" || f.dropped {
			names = names[:2]
		}
		var tools []any
		for _, n := range names {
			tools = append(tools, map[string]any{"name": n, "inputSchema": map[string]any{"type": "object"}})
		}
		f.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": tools}})
	case method == "tools/call":
		params, _ := message["params"].(map[string]any)
		name, _ := params["name"].(string)
		if f.dropped && name == "workspace_stop" {
			f.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "Unknown tool: workspace_stop"}})
			return
		}
		f.dropped = f.mode == "drops-stop"
		go f.call(id, name)
	case id != nil:
		f.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
	}
}

func fake(t *testing.T, mode string, timing CallTiming) *Connection {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The command provider is this test binary: TestMain runs fakeProvider when FAKE_PROVIDER is set.
	r := ProviderRecord{ID: "fake-" + mode, Name: "Fake", Kind: KindCommands, Commands: map[string]any{}}
	c, err := NewConnection(r, map[string]string{"FAKE_PROVIDER": mode}, t.TempDir(), timing, exe, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.End)
	return c
}

var fiveSeconds = CallTiming{Quick: 5 * time.Second, Long: 5 * time.Second}

func said(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestAWellBehavedOneIsReadAsTheContractSays(t *testing.T) {
	c := fake(t, "", fiveSeconds)
	checked, err := c.Check()
	if err != nil || len(checked.Workspaces) != 1 || checked.Workspaces[0].State != "stopped" {
		t.Fatalf("%v %+v", err, checked)
	}
	if w, err := c.Start("devbox"); err != nil || w == nil || w.State != "running" {
		t.Errorf("%v %+v", err, w)
	}
}

func TestAMissingToolIsNamed(t *testing.T) {
	_, err := fake(t, "no-stop", fiveSeconds).Check()
	if said(err) != "It isn’t a machine provider: it has no workspace_stop tool." {
		t.Errorf("%v", err)
	}
}

func TestAListWithoutItsContractOrWithAnUnknownOneIsRefused(t *testing.T) {
	if _, err := fake(t, "no-contract", fiveSeconds).Check(); said(err) != "Its workspaces_list doesn’t say which contract it follows." {
		t.Errorf("%v", err)
	}
	if _, err := fake(t, "contract-2", fiveSeconds).Check(); said(err) != "It follows contract 2; this control plane reads 1." {
		t.Errorf("%v", err)
	}
}

func TestOddItemsAreLeftOutOrReadAsUnknown(t *testing.T) {
	listed, err := fake(t, "odd-items", fiveSeconds).List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range listed {
		names = append(names, w.Name)
	}
	// "twin" has devbox's id: a second workspace with an id already listed is left out.
	if !reflect.DeepEqual(names, []string{"devbox", "numbered"}) || listed[0].State != "unknown" || listed[1].ID != "7" {
		t.Errorf("%+v", listed)
	}
}

func TestTextThatIsNotJSONIsSaidSo(t *testing.T) {
	if _, err := fake(t, "not-json", fiveSeconds).List(); said(err) != "The provider’s answer isn’t JSON." {
		t.Errorf("%v", err)
	}
}

func TestALineThatIsNotJSONRPCIsPassedOver(t *testing.T) {
	if checked, err := fake(t, "noise", fiveSeconds).Check(); err != nil || len(checked.Workspaces) != 1 {
		t.Errorf("%v", err)
	}
}

func TestARefusalIsInItsOwnWordsCutAt1KB(t *testing.T) {
	_, err := fake(t, "refuse", fiveSeconds).Start("devbox")
	if !strings.HasPrefix(said(err), "Quota exceeded.") || len(said(err)) != 1024 {
		t.Errorf("%d %q", len(said(err)), said(err)[:20])
	}
	if _, err := fake(t, "silent-refusal", fiveSeconds).Start("devbox"); said(err) != "The provider said no, and not why." {
		t.Errorf("%v", err)
	}
}

func TestAStartWithoutItsWorkspaceIsNotAFailure(t *testing.T) {
	if w, err := fake(t, "bare-start", fiveSeconds).Start("devbox"); err != nil || w != nil {
		t.Errorf("%v %+v", err, w)
	}
}

func TestAHungStartRunsOutOfTimeAndTheNextCallStillWorks(t *testing.T) {
	c := fake(t, "hang", CallTiming{Quick: 5 * time.Second, Long: time.Second})
	if _, err := c.Start("devbox"); said(err) != "It didn’t answer in time." {
		t.Errorf("%v", err)
	}
	if listed, err := c.List(); err != nil || len(listed) != 1 {
		t.Errorf("%v", err)
	}
}

func TestACrashMidStartIsSaidAndTheNextCallStartsItAgain(t *testing.T) {
	c := fake(t, "crash", fiveSeconds)
	if _, err := c.Start("devbox"); said(err) == "" {
		t.Error("no failure")
	}
	if listed, err := c.List(); err != nil || len(listed) != 1 {
		t.Errorf("%v", err)
	}
}

func TestAToolDroppedAfterTheProviderWasAddedIsNamed(t *testing.T) {
	c := fake(t, "drops-stop", fiveSeconds)
	if _, err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stop("devbox"); said(err) != "Unknown tool: workspace_stop" {
		t.Errorf("%v", err)
	}
	if _, err := c.Check(); said(err) != "It isn’t a machine provider: it has no workspace_stop tool." {
		t.Errorf("%v", err)
	}
	if listed, err := c.List(); err != nil || len(listed) != 1 {
		t.Errorf("%v", err)
	}
}

func TestACommandThatIsNotThereCannotBeReached(t *testing.T) {
	c, _ := NewConnection(ProviderRecord{ID: "x", Name: "x", Kind: KindCommands, Commands: map[string]any{}}, nil, t.TempDir(),
		fiveSeconds, "no-such-provider-command", nil)
	defer c.End()
	if _, err := c.Check(); !strings.HasPrefix(said(err), "It can’t be reached: ") {
		t.Errorf("%v", err)
	}
}
