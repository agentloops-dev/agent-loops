package agentsd

import (
	"encoding/json"
	"log"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/mcpcatalog"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A folder's connector that waits for approval is in Needs You (#5717), on every client
// and for a folder on any machine: its summary carries it, as a waiting workflow's does.
// Approve is mcp/approve; Deny keeps the entry's digest in mcp-approvals.json's "denied",
// so it stays off and out of the list until its entry in .agents/mcp.json changes again.

// connectorApprovals is a folder's waiting connectors as Needs You lists them, in the
// file's order.
func (d *Daemon) connectorApprovals(folder string) []wire.Approval {
	entries, problem := readMCPFile(mcpcatalog.FolderFile(folder))
	if problem != "" || len(entries) == 0 {
		return nil
	}
	d.mcpMu.Lock()
	approved, _, _ := d.mcpApprovalsLocked()
	denied := d.mcpDeniedLocked()
	d.mcpMu.Unlock()
	var out []wire.Approval
	for _, e := range entries {
		key, digest := mcpApprovalKey(folder, e.name), entryDigest(e.raw)
		stored, had := approved[key]
		if (approved != nil && stored == digest) || denied[key] == digest {
			continue
		}
		line := "New with the last pull"
		if had {
			line = "Changed since you approved it"
		}
		out = append(out, wire.Approval{Kind: wire.ApprovalKindMcpServer, Key: e.name, Title: e.name, Digest: &digest,
			Line: line, Actions: []wire.ApprovalAction{wire.ApprovalActionApprove, wire.ApprovalActionDeny}})
	}
	return out
}

// mcpDeniedLocked is mcp-approvals.json's "denied": a folder's entry and the digest denied.
func (d *Daemon) mcpDeniedLocked() map[string]string {
	return d.mcpPartLocked("denied")
}

// readConnectorApproval is the entry that waits, as approvals/read shows it.
func (d *Daemon) readConnectorApproval(folder, name string) (wire.GuardedChangeReading, error) {
	if _, ok := d.summaryFor(folder); !ok {
		return wire.GuardedChangeReading{}, noSuchFolder(folder + " is not a folder.")
	}
	entries, problem := readMCPFile(mcpcatalog.FolderFile(folder))
	if problem != "" {
		return wire.GuardedChangeReading{}, mcpRefusal(mcpcatalog.Unreadable(mcpcatalog.FolderFile(folder)))
	}
	for _, e := range entries {
		if e.name == name {
			digest, current := entryDigest(e.raw), string(e.raw)
			return wire.GuardedChangeReading{Path: ".agents/mcp.json", Digest: &digest, Current: &current,
				Lines: []wire.GuardedDiffLine{}}, nil
		}
	}
	return wire.GuardedChangeReading{}, Invalid("%s is not in this folder's .agents/mcp.json any more.", name)
}

// answerConnector is approvals/answer for a waiting connector.
func (d *Daemon) answerConnector(folder, name string, digest *string, approve bool) (wire.FolderSummary, error) {
	if digest == nil {
		return wire.FolderSummary{}, Invalid("Say which version you were shown.")
	}
	if _, ok := d.summaryFor(folder); !ok {
		return wire.FolderSummary{}, noSuchFolder(folder + " is not a folder.")
	}
	if approve {
		if _, err := d.mcpApprove(wire.MCPApproveRequest{Destination: wire.SkillDestination{
			Kind: wire.SkillDestinationKindFolder, Folder: &folder}, Name: name, Digest: *digest}); err != nil {
			return wire.FolderSummary{}, err
		}
		// mcp/approve has sent the folder's summary already.
		s, _ := d.summaryFor(folder)
		return s, nil
	}
	if err := d.denyConnector(folder, name, *digest); err != nil {
		return wire.FolderSummary{}, err
	}
	return d.sendFolder(folder)
}

// denyConnector keeps the shown entry off until it changes again.
func (d *Daemon) denyConnector(folder, name, digest string) error {
	var raw json.RawMessage
	entries, _ := readMCPFile(mcpcatalog.FolderFile(folder))
	for _, e := range entries {
		if e.name == name {
			raw = e.raw
		}
	}
	if raw == nil || entryDigest(raw) != digest {
		return mcpRefusal(mcpcatalog.StaleDigest())
	}
	d.mcpMu.Lock()
	defer d.mcpMu.Unlock()
	d.mcpApprovalsLocked()
	if d.mcpApprovals == nil {
		// Unreadable or missing: begin a new file, as Approve does, so the map is there.
		if err := d.saveMCPApprovalsLocked(true); err != nil {
			return approvalNotKept(err)
		}
	}
	denied := d.mcpDeniedLocked()
	if err := keepApproval(denied, d.mcpGoneLocked(), "", mcpApprovalKey(folder, name), digest); err != nil {
		return approvalNotKept(err)
	}
	d.mcpApprovals["denied"], _ = json.Marshal(denied)
	if err := d.saveMCPApprovalsLocked(true); err != nil {
		return approvalNotKept(err)
	}
	log.Printf("mcp: deny %s in %s", name, filepath.Base(folder))
	return nil
}
