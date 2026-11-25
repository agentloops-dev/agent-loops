package agentsd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// deliverServers is machine/userSettings with these servers, as the uplink gives it.
func deliverServers(d *Daemon, servers string) {
	d.Told(wire.NotifyMachineUserSettings.Name, mustJSON(wire.MachineUserSettings{User: "sam",
		Settings: wire.UserSettings{McpServers: json.RawMessage(servers)}}))
}

// startsMark is a server that writes a file when it starts.
func startsMark(path string) string {
	return string(mustJSON(map[string]any{"command": "/bin/sh", "args": []string{"-c", `: > "$0"`, path}}))
}

func exists(path string) bool { return fileExists(path) }

// checksDone runs the health checks and waits for those they started.
func checksDone(t *testing.T, d *Daemon) {
	t.Helper()
	d.mcpHealthCheck(nil)
	waitFor(t, "the checks ended", func() bool {
		d.reconcileMCPHealth()
		d.healthMu.Lock()
		defer d.healthMu.Unlock()
		return len(d.running) == 0 && !d.reconciling
	})
}

// syncedMachine is a locked machine with a personal home and one folder.
func syncedMachine(t *testing.T) (d *Daemon, home, dir string, phone *conn, channel *machinelock.Channel) {
	t.Helper()
	d, key, _, state := lockedMachine(t)
	home, dir = t.TempDir(), t.TempDir()
	d.PersonalHome = home
	must(t, os.MkdirAll(filepath.Join(home, ".agents"), 0o750))
	must(t, d.editRecord(dir, func(*wire.Folder) {}))
	phone = channelTo(t, d)
	return d, home, dir, phone, hello(t, phone, state, key)
}

// checked is a call on the checked channel: its result, or the error's line.
func checked(t *testing.T, c *conn, ch *machinelock.Channel, id int, method string, params any) string {
	t.Helper()
	c.send(ch.Seal(requestLine(id, method, params)))
	return c.answer(t, id)
}

func gatedNames(d *Daemon) (started []string) {
	servers, _, _ := d.personalMCP(map[string]string{}, nil)
	for _, s := range servers {
		started = append(started, s.name)
	}
	return started
}

// A server that a delivery brings is started by nothing before the person approves it here.
func TestADeliveredServerWaitsForApproval(t *testing.T) {
	d, home, dir, _, _ := syncedMachine(t)
	marks := t.TempDir()
	plain, hosted := filepath.Join(marks, "plain"), filepath.Join(marks, "hosted")
	hostedEntry := strings.TrimSuffix(startsMark(hosted), "}") + `,"hosted":true}`
	deliverServers(d, `{"plain":`+startsMark(plain)+`,"shared":`+hostedEntry+`,"web":{"url":"http://127.0.0.1:1/"}}`)

	if b, _ := os.ReadFile(filepath.Join(home, ".agents", "mcp.json")); !strings.Contains(string(b), "plain") {
		t.Fatal("the delivery was not written")
	}
	checksDone(t, d)
	if exists(plain) || exists(hosted) {
		t.Fatal("a check started a waiting server")
	}
	if got := gatedNames(d); len(got) != 0 {
		t.Fatalf("a session would be given %v", got)
	}
	if got := d.sessionEntries(dir, ""); len(got) != 0 {
		t.Fatalf("a session would be given %+v", got)
	}
	for _, name := range []string{"plain", "shared", "web"} {
		if _, problem := d.eventServerNamed(name, dir, nil); problem != "waiting" {
			t.Fatalf("events of %s: %q", name, problem)
		}
		if _, trouble := d.viewServerFor(name, dir); trouble != viewWaiting {
			t.Fatalf("views of %s: %v", name, trouble)
		}
		if _, _, err := d.docsServerFor(name, dir, nil); err == nil {
			t.Fatalf("a read of %s would start it", name)
		}
	}
	if _, _, err := d.mcpSignInServer(wire.MCPSignInTarget{Entry: &wire.MCPSignInTargetEntry{Name: "web",
		Destination: wire.SkillDestination{Kind: wire.SkillDestinationKindPersonal}}}); err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("a sign-in would reach it: %v", err)
	}
	for _, h := range d.mcpHealthSnapshot().Servers {
		if !h.Hosted && h.Check != wire.MCPCheckStateWaiting {
			t.Fatalf("%+v", h)
		}
	}

}

// The person answers a waiting server from Needs You or with mcp/approve, on a device of theirs.
func TestADeliveredServerIsApprovedOnADevice(t *testing.T) {
	d, _, dir, phone, ch := syncedMachine(t)
	marks := t.TempDir()
	plain, hosted := filepath.Join(marks, "plain"), filepath.Join(marks, "hosted")
	hostedEntry := strings.TrimSuffix(startsMark(hosted), "}") + `,"hosted":true}`
	deliverServers(d, `{"plain":`+startsMark(plain)+`,"shared":`+hostedEntry+`,"web":{"url":"http://127.0.0.1:1/"}}`)

	// It is in Needs You, with its entry to read.
	s, _ := d.summaryFor(dir)
	byKey := map[string]wire.Approval{}
	for _, a := range s.Approvals {
		byKey[a.Key] = a
	}
	a := byKey["~|plain"]
	if len(byKey) != 3 || a.Kind != wire.ApprovalKindMcpServer || a.Title != "plain" || a.Digest == nil {
		t.Fatalf("%+v", s.Approvals)
	}
	folder := store.FolderURL(dir)
	if line := checked(t, phone, ch, 1, "approvals/read", map[string]any{"folder": folder, "kind": "mcpServer", "key": "~|plain"}); !strings.Contains(line, "/bin/sh") {
		t.Fatal(line)
	}

	// An answer with no device behind it is refused, on each method.
	answer := map[string]any{"folder": folder, "kind": "mcpServer", "key": "~|plain", "digest": *a.Digest, "action": "approve"}
	direct := map[string]any{"destination": map[string]any{"kind": "personal"}, "name": "plain", "digest": *a.Digest}
	cp := channelTo(t, d)
	cp.send(requestLine(2, "approvals/answer", answer))
	if line := cp.answer(t, 2); !notInLockAnswer(line) {
		t.Fatal(line)
	}
	cp.send(requestLine(3, "mcp/approve", direct))
	if line := cp.answer(t, 3); !notInLockAnswer(line) {
		t.Fatal(line)
	}
	// Another machine's signed approval of the same entry, as a folder's takes, is not enough.
	d.personApprovals.taken = map[string]wire.PersonApproval{
		approvalTakenKey(approvalKindConnector, *a.Digest): {Kind: approvalKindConnector, Digest: *a.Digest, Host: "h-2"}}
	d.adoptSharedMCPAll()
	checksDone(t, d)
	if exists(plain) || len(gatedNames(d)) != 0 {
		t.Fatal("it started with no approval here")
	}

	// A device of the person's approves the one it read; the others still wait.
	if line := checked(t, phone, ch, 4, "approvals/answer", map[string]any{"folder": folder, "kind": "mcpServer", "key": "~|plain", "digest": "stale", "action": "approve"}); !strings.Contains(line, `"error"`) {
		t.Fatal(line)
	}
	if line := checked(t, phone, ch, 5, "approvals/answer", answer); strings.Contains(line, `"error"`) {
		t.Fatal(line)
	}
	checksDone(t, d)
	if !exists(plain) || exists(hosted) {
		t.Fatalf("plain %v, hosted %v", exists(plain), exists(hosted))
	}
	if got := gatedNames(d); len(got) != 1 || got[0] != "plain" {
		t.Fatalf("%v", got)
	}

	// Deny leaves one off and out of the list.
	deny := map[string]any{"folder": folder, "kind": "mcpServer", "key": "~|web", "digest": *byKey["~|web"].Digest, "action": "deny"}
	if line := checked(t, phone, ch, 6, "approvals/answer", deny); strings.Contains(line, `"error"`) {
		t.Fatal(line)
	}
	if s, _ := d.summaryFor(dir); len(s.Approvals) != 1 || s.Approvals[0].Key != "~|shared" {
		t.Fatalf("%+v", s.Approvals)
	}

	// A changed entry waits again, and so does one that comes back under the same name.
	changed := filepath.Join(marks, "changed")
	deliverServers(d, `{"plain":`+startsMark(changed)+`}`)
	checksDone(t, d)
	if exists(changed) || len(gatedNames(d)) != 0 {
		t.Fatal("a changed server started")
	}
	deliverServers(d, `{}`)
	deliverServers(d, `{"plain":`+startsMark(changed)+`}`)
	checksDone(t, d)
	if exists(changed) {
		t.Fatal("a server that came back started")
	}
	// mcp/approve takes the person's own too.
	direct["digest"] = entryDigest(json.RawMessage(startsMark(changed)))
	if line := checked(t, phone, ch, 7, "mcp/approve", direct); strings.Contains(line, `"error"`) {
		t.Fatal(line)
	}
	checksDone(t, d)
	if !exists(changed) {
		t.Fatal("an approved server did not start")
	}
}

// What was in use when the first delivery names it goes on as it stands; a change waits.
func TestAServerInUseGoesOnAfterTheUpdate(t *testing.T) {
	d, home, dir, _, _ := syncedMachine(t)
	must(t, os.WriteFile(filepath.Join(home, ".agents", "mcp.json"),
		[]byte(`{"mcpServers":{"same":{"command":"same"},"other":{"command":"other"},"mine":{"command":"mine"}}}`), 0o600))
	if got := gatedNames(d); len(got) != 3 {
		t.Fatalf("%v", got)
	}
	deliverServers(d, `{"same":{ "command" : "same" },"other":{"command":"new"},"added":{"command":"added"}}`)
	if got := gatedNames(d); len(got) != 1 || got[0] != "same" {
		t.Fatalf("%v", got)
	}
	lines := map[string]string{}
	s, _ := d.summaryFor(dir)
	for _, a := range s.Approvals {
		lines[a.Title] = a.Line
	}
	if len(lines) != 2 || lines["added"] != "New in your settings" || lines["other"] != "Changed in your settings since you approved it" {
		t.Fatalf("%v", lines)
	}
	// The one in use before the change is the one approved.
	deliverServers(d, `{"same":{"command":"same"},"other":{"command":"other"}}`)
	if got := gatedNames(d); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	// A second delivery, the same as the file, approves nothing that waits.
	deliverServers(d, `{"same":{"command":"same"},"other":{"command":"new"}}`)
	deliverServers(d, `{"same":{"command":"same"},"other":{"command":"new"}}`)
	if got := gatedNames(d); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

// With no lock, an approval that the control plane carries is refused; the machine's own socket's is taken.
func TestWithNoLockOnlyTheMachinesSocketApproves(t *testing.T) {
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	home := t.TempDir()
	d.PersonalHome = home
	deliverServers(d, `{"x":{"command":"x"}}`)
	digest := entryDigest(json.RawMessage(`{"command":"x"}`))
	direct := map[string]any{"destination": map[string]any{"kind": "personal"}, "name": "x", "digest": digest}
	if e := c.callError(t, 1, "mcp/approve", direct); e.Code != wire.FailureNotInLock {
		t.Fatalf("%+v", e)
	}
	if len(gatedNames(d)) != 0 {
		t.Fatal("it starts")
	}
	if err := d.answerPersonalConnector(withCaller(context.Background(), true), "x", digest, true); err != nil {
		t.Fatal(err)
	}
	if len(gatedNames(d)) != 1 {
		t.Fatal("it does not start")
	}
}

// With approvals that cannot be read, nothing of the person's starts, and the file an approval
// then begins holds back the rest.
func TestUnreadableApprovalsHoldThePersonsServers(t *testing.T) {
	d, home, _, _, _ := syncedMachine(t)
	must(t, os.WriteFile(filepath.Join(home, ".agents", "mcp.json"), []byte(`{"mcpServers":{"a":{"command":"a"},"b":{"command":"b"}}}`), 0o600))
	must(t, os.WriteFile(d.mcpApprovalsPath(), []byte("{"), 0o600))
	d.mcpMu.Lock()
	d.mcpLoaded = false
	d.mcpMu.Unlock()
	if got := gatedNames(d); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	deliverServers(d, `{"a":{"command":"a"},"b":{"command":"b"},"c":{"command":"c"}}`)
	digest := entryDigest(json.RawMessage(`{"command":"a"}`))
	if err := d.answerPersonalConnector(withCaller(context.Background(), true), "a", digest, true); err != nil {
		t.Fatal(err)
	}
	if got := gatedNames(d); len(got) != 1 || got[0] != "a" {
		t.Fatalf("%v", got)
	}
}

// More servers than the marks hold are refused whole.
func TestTooManyDeliveredServersAreRefused(t *testing.T) {
	d, home, _, _, _ := syncedMachine(t)
	many := map[string]any{}
	for i := range approvalsMost + 1 {
		many["s"+string(mustJSON(i))] = map[string]any{"command": "x"}
	}
	deliverServers(d, string(mustJSON(many)))
	if exists(filepath.Join(home, ".agents", "mcp.json")) || len(gatedNames(d)) != 0 {
		t.Fatal("the delivery was written")
	}
}

// The first read of the approvals makes no summary, which asks for the same lock.
func TestApprovalsBeginWithAFolderThatHasConnectors(t *testing.T) {
	d, home, dir, _, _ := syncedMachine(t)
	must(t, os.MkdirAll(filepath.Join(dir, ".agents"), 0o750))
	must(t, os.WriteFile(filepath.Join(dir, ".agents", "mcp.json"), []byte(`{"mcpServers":{"f":{"command":"f"}}}`), 0o600))
	must(t, os.WriteFile(filepath.Join(home, ".agents", "mcp.json"), []byte(`{"mcpServers":{"a":{"command":"a"}}}`), 0o600))
	d.mcpMu.Lock()
	d.mcpLoaded = false
	_ = os.Remove(d.mcpApprovalsPath())
	d.mcpMu.Unlock()
	done := make(chan struct{})
	go func() { d.beginMCPApprovals(); close(done) }()
	select {
	case <-done:
	case <-afterLimit(t):
		t.Fatal("it did not begin")
	}
}
