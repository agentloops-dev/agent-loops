package agentsd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A folder's connector that waits for approval is in its summary's approvals, so Needs
// You lists it on every client and for a folder on any machine (#5717). Approve and Deny
// settle it from there, as for a workflow.
func TestAWaitingConnectorIsInTheFoldersApprovals(t *testing.T) {
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	folder := store.FolderURL(dir)
	// The approvals begin before the file is there, so its entries wait.
	if s, _ := d.summaryFor(dir); len(s.Approvals) != 0 {
		t.Fatalf("%+v", s.Approvals)
	}
	os.MkdirAll(filepath.Join(dir, ".agents"), 0o755)
	write := func(text string) {
		os.WriteFile(filepath.Join(dir, ".agents", "mcp.json"), []byte(text), 0o644)
	}
	write(`{"mcpServers":{"pulled":{"command":"echo"},"other":{"command":"true"}}}`)
	s, _ := d.summaryFor(dir)
	if len(s.Approvals) != 2 || s.Approvals[0].Kind != wire.ApprovalKindMcpServer || s.Approvals[0].Title != s.Approvals[0].Key ||
		s.Approvals[0].Line != "New with the last pull" || s.Approvals[0].Digest == nil || len(s.Approvals[0].Actions) != 2 {
		t.Fatalf("%+v", s.Approvals)
	}
	pulled := s.Approvals[0]
	if pulled.Key != "pulled" {
		pulled = s.Approvals[1]
	}
	var reading wire.GuardedChangeReading
	json.Unmarshal(c.call(t, 2, "approvals/read", map[string]any{"folder": folder, "kind": "mcpServer", "key": "pulled"}), &reading)
	if reading.Current == nil || *reading.Digest != *pulled.Digest {
		t.Fatalf("%+v", reading)
	}
	if e := c.callError(t, 3, "approvals/answer", map[string]any{"folder": folder, "kind": "mcpServer", "key": "pulled",
		"digest": "stale", "action": "approve"}); e.Code != wire.FailureCatalogRefused {
		t.Fatalf("%+v", e)
	}
	if e := c.callError(t, 4, "approvals/answer", map[string]any{"folder": folder, "kind": "mcpServer", "key": "pulled",
		"digest": *pulled.Digest, "action": "requestChanges", "note": "x"}); e.Code != CodeInvalidParams {
		t.Fatalf("%+v", e)
	}
	// Approve: mcp/approve's approval, and out of the list.
	c.call(t, 5, "approvals/answer", map[string]any{"folder": folder, "kind": "mcpServer", "key": "pulled",
		"digest": *pulled.Digest, "action": "approve"})
	var list wire.MCPListAnswer
	dest := map[string]any{"kind": "folder", "folder": dir}
	json.Unmarshal(c.call(t, 6, "mcp/list", map[string]any{"destination": dest}), &list)
	for _, row := range list.Servers {
		if row.Name == "pulled" && row.Approval.Approved == nil {
			t.Fatalf("%+v", row)
		}
	}
	s, _ = d.summaryFor(dir)
	if len(s.Approvals) != 1 || s.Approvals[0].Key != "other" {
		t.Fatalf("%+v", s.Approvals)
	}
	// Deny: off, and out of the list until its entry changes.
	c.call(t, 7, "approvals/answer", map[string]any{"folder": folder, "kind": "mcpServer", "key": "other",
		"digest": *s.Approvals[0].Digest, "action": "deny"})
	if s, _ := d.summaryFor(dir); len(s.Approvals) != 0 {
		t.Fatalf("%+v", s.Approvals)
	}
	json.Unmarshal(c.call(t, 8, "mcp/list", map[string]any{"destination": dest}), &list)
	for _, row := range list.Servers {
		if row.Name == "other" && row.Approval.Waiting == nil {
			t.Fatalf("a denied connector is started: %+v", row)
		}
	}
	// Changed: both wait again, the approved one as a change.
	write(`{"mcpServers":{"pulled":{"command":"echo","args":["hi"]},"other":{"command":"false"}}}`)
	s, _ = d.summaryFor(dir)
	if len(s.Approvals) != 2 {
		t.Fatalf("%+v", s.Approvals)
	}
	for _, a := range s.Approvals {
		if want := map[string]string{"pulled": "Changed since you approved it", "other": "New with the last pull"}[a.Key]; a.Line != want {
			t.Fatalf("%s: %q", a.Key, a.Line)
		}
	}
}
