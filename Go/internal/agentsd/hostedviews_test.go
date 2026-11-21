package agentsd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
)

// A hosted server's views come through its one copy, and a session is never offered, nor
// can call, a tool only a view may call (#1341).
func TestAHostedServersViewsComeThroughItsOneCopy(t *testing.T) {
	self, _ := os.Executable()
	t.Setenv("AGENTS_FAKE_MCP", "1")
	t.Setenv("AGENTS_FAKE_MCP_VIEWS", "1")
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	home := t.TempDir()
	d.PersonalHome = home
	must(t, os.MkdirAll(filepath.Join(home, ".agents"), 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	entry, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"gh": map[string]any{"command": self, "hosted": true}}})
	must(t, os.WriteFile(filepath.Join(home, ".agents", "mcp.json"), entry, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new

	s, problem := d.viewServerFor("gh", t.TempDir())
	if problem != 0 || s.connect == nil {
		t.Fatalf("a hosted server shows no views: %d", problem)
	}
	catalog := d.loadViewCatalog(context.Background(), s)
	if catalog == nil {
		t.Fatalf("no catalog\n%s", hostedCopies(d))
	}
	if catalog.tool("board") == nil || !catalog.tool("board").feedsPins() || !catalog.appTool("merge") {
		t.Fatalf("catalog: %+v", catalog)
	}
	var called json.RawMessage
	if err := withViewSession(context.Background(), s, func(m *viewSession) error {
		var err error
		called, err = m.call("tools/call", map[string]any{"name": "merge", "arguments": map[string]any{}})
		return err
	}); err != nil || !strings.Contains(string(called), "called") {
		t.Fatalf("a view's tools/call: %s, %v", called, err)
	}

	// A session on the same copy: one process, and no merge.
	entries := d.sessionEntries(t.TempDir(), "")
	got := d.hostedSwapped(entries, 4242, "tok", "claude")
	if len(got) != 1 || got[0].Http == nil {
		t.Fatalf("the session is not handed the copy: %+v", got)
	}
	path := got[0].Http.Url[len("http://127.0.0.1:4242"):]
	d.hostedMu.Lock()
	n := len(d.hostedByKey)
	h := d.hostedByPath(path)
	d.hostedMu.Unlock()
	if n != 1 || h == nil {
		t.Fatalf("%d copies of one server", n)
	}
	list, err := d.hostedSessionCall(h, "tools/list", json.RawMessage(`{}`))
	if err != nil || strings.Contains(string(list), `"merge"`) || !strings.Contains(string(list), `"board"`) {
		t.Fatalf("a session's tools/list: %s, %v", list, err)
	}
	if _, err := d.hostedSessionCall(h, "tools/call", json.RawMessage(`{"name":"merge","arguments":{}}`)); err == nil {
		t.Fatal("a session called a tool only a view may call")
	}
	if _, err := d.hostedSessionCall(h, "tools/call", json.RawMessage(`{"name":"board","arguments":{}}`)); err != nil {
		t.Fatalf("a session's own tool: %v", err)
	}
}

// A changed entry is a new catalog, and the old one goes; so does one too old to be read
// (#3128).
func TestAChangedEntrysOldViewCatalogIsRemoved(t *testing.T) {
	self, _ := os.Executable()
	t.Setenv("AGENTS_FAKE_MCP", "1")
	t.Setenv("AGENTS_FAKE_MCP_VIEWS", "1")
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	home := t.TempDir()
	d.PersonalHome = home
	must(t, os.MkdirAll(filepath.Join(home, ".agents"), 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	write := func(args ...string) {
		entry, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
			"gh": map[string]any{"command": self, "args": args, "hosted": true}}})
		must(t, os.WriteFile(filepath.Join(home, ".agents", "mcp.json"), entry, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	}
	dir := filepath.Dir(d.viewCatalogPath("x"))
	old, _ := json.Marshal(viewCatalog{Version: viewCatalogVersion, Server: "gone", MadeAt: nowFunc().Add(-2 * viewCatalogFresh)})
	must(t, os.MkdirAll(dir, 0o755))                                            //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(dir, "removed-entry.json"), old, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new

	var last viewServer
	for _, args := range [][]string{nil, {"-test.run=^$"}} {
		write(args...)
		s, problem := d.viewServerFor("gh", t.TempDir())
		if problem != 0 {
			t.Fatalf("no views: %d", problem)
		}
		if d.loadViewCatalog(context.Background(), s) == nil {
			t.Fatalf("no catalog\n%s", hostedCopies(d))
		}
		last = s
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 || files[0].Name() != last.entry+".json" {
		var names []string
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Fatalf("mcp-views/ holds %v, not only %s.json", names, last.entry)
	}
}

// hostedCopies says what each hosted copy of d is doing, for a test that waited on one in
// vain (#5988): its state, its process (on Linux, the kernel's state of it and where it
// waits), what it still owes an answer to, its last stderr and the end of its log.
func hostedCopies(d *Daemon) string {
	var b strings.Builder
	d.hostedMu.Lock()
	for k, h := range d.hostedByKey {
		pid := 0
		if h.cmd != nil && h.cmd.Process != nil {
			pid = h.cmd.Process.Pid
		}
		var waiting []string
		for id := range h.waiting {
			waiting = append(waiting, id)
		}
		fmt.Fprintf(&b, "%s %s: state %s, generation %d, pid %d, initialized %v, starting %v, users %d, waiting %v, last error %q, last stderr %q\n",
			k.scope, k.name, h.state, h.generation, pid, h.initialized != nil, h.starting != nil, len(h.users), waiting, h.lastError, h.lastStderr)
		if pid != 0 {
			for _, f := range []string{"status", "wchan", "stack"} {
				if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", pid, f)); err == nil {
					fmt.Fprintf(&b, "  /proc/%d/%s:\n%s\n", pid, f, raw)
				}
			}
		}
		if h.log != nil {
			if raw, err := os.ReadFile(h.log.path); err == nil {
				fmt.Fprintf(&b, "  its log, the last 4 KB:\n%s\n", raw[max(0, len(raw)-4096):])
			}
		}
	}
	d.hostedMu.Unlock()
	var stacks strings.Builder
	_ = pprof.Lookup("goroutine").WriteTo(&stacks, 1)
	b.WriteString("goroutines:\n" + stacks.String())
	return b.String()
}
