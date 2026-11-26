package agentsd

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/mcpcatalog"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The approval of a connector in the person's own settings that came in machine/userSettings.
// It uses what a folder's connectors use: an entry runs on this machine only while its digest
// is the one in mcp-approvals.json's "approved", under the scope "personal". "synced" names each
// connector that a delivery brought; one with no such name is this machine's own and needs no
// approval. A connector that was in use here when a delivery first names it is approved as it
// stands, so an update of the app stops nothing.
//
// The approval is of this machine only: it is not shared (personapprovals.go). It is taken from
// a client's checked channel or from a socket of this machine.
//
// In Needs You it is an approval of one folder's summary (the first by path), with the key
// "~|<name>", so every client draws and answers it as it does a folder's connector.

const (
	personalScope       = "personal"
	personalApprovalKey = "~|"
)

func personalMCPKey(name string) string { return mcpApprovalKey(personalScope, name) }

// callerKey is a request's context value: it came on a checked channel or a socket of this machine.
type callerKey struct{}

func withCaller(ctx context.Context, vouched bool) context.Context {
	return context.WithValue(ctx, callerKey{}, vouched)
}

func callerVouched(ctx context.Context) bool {
	ok, _ := ctx.Value(callerKey{}).(bool)
	return ok
}

// personalGate says which entries of the person's own mcp.json wait here.
type personalGate struct {
	all              bool
	synced, approved map[string]string
}

// personalGate is read after the file that its entries come from: a delivery writes its marks
// before the file.
func (d *Daemon) personalGate() personalGate {
	d.mcpMu.Lock()
	defer d.mcpMu.Unlock()
	return d.personalGateLocked()
}

func (d *Daemon) personalGateLocked() personalGate {
	approved, _, unreadable := d.mcpApprovalsLocked()
	if unreadable {
		return personalGate{all: true}
	}
	return personalGate{synced: d.mcpPartLocked("synced"), approved: approved}
}

func (g personalGate) waits(e mcpEntry) bool {
	if g.all {
		return true
	}
	key := personalMCPKey(e.name)
	if _, ok := g.synced[key]; !ok {
		return false
	}
	return g.approved[key] != entryDigest(e.raw)
}

// rawServers is every server of a "mcpServers" object as it is written, whether it is one or not.
func rawServers(servers json.RawMessage) (map[string]json.RawMessage, error) {
	var each map[string]json.RawMessage
	if len(servers) == 0 {
		return each, nil
	}
	if json.Unmarshal(servers, &each) != nil {
		return nil, errors.New("not an object")
	}
	return each, nil
}

// writeSyncedServers notes each server of a delivery and then writes it, under one lock, so a
// sweep of the marks does not come between. held is what the file has now: one of its entries
// that was this machine's own is approved as it stands when the delivery first names it.
func (d *Daemon) writeSyncedServers(held, coming json.RawMessage, write func() error) error {
	d.mcpMu.Lock()
	defer d.mcpMu.Unlock()
	if err := d.markSyncedLocked(held, coming); err != nil {
		return err
	}
	return write()
}

func (d *Daemon) markSyncedLocked(held, coming json.RawMessage) error {
	was, _ := rawServers(held) // one that cannot be read is in use by nothing
	now, err := rawServers(coming)
	if err != nil {
		return err
	}
	if len(now) == 0 {
		return nil
	}
	approved, _, unreadable := d.mcpApprovalsLocked()
	if unreadable {
		// Nothing of the person's starts until an approval begins a new file.
		return nil
	}
	synced, gone := d.mcpPartLocked("synced"), d.mcpGoneLocked()
	changed := false
	for name := range now {
		key := personalMCPKey(name)
		if _, had := synced[key]; had {
			continue
		}
		// No mark is evicted to make room: a delivery of too many is refused whole.
		if len(synced) >= approvalsMost || len(key)+1 > approvalMostBytes {
			return Invalid("This machine keeps at most %d connectors from your settings, so the new ones were not added. Remove one in Settings ▸ You ▸ Connectors, then try again.", approvalsMost)
		}
		synced[key], changed = "1", true
		if raw, ok := was[name]; ok {
			if err := keepApproval(approved, gone, "", key, entryDigest(raw)); err != nil {
				log.Printf("mcp: %s waits for approval: %v", name, err)
			}
		}
	}
	if !changed {
		return nil
	}
	// In memory only once on disk: a mark that is not kept must not let the file be written.
	old := [2]json.RawMessage{d.mcpApprovals["synced"], d.mcpApprovals["approved"]}
	d.mcpApprovals["synced"], _ = json.Marshal(synced)
	d.mcpApprovals["approved"], _ = json.Marshal(approved)
	if err := d.saveMCPApprovalsLocked(false); err != nil {
		d.mcpApprovals["synced"], d.mcpApprovals["approved"] = old[0], old[1]
		return err
	}
	return nil
}

// syncedFromFile is the marks a new approvals file begins with when the old one could not be
// read: every connector of the person's own file, so that each waits.
func (d *Daemon) syncedFromFile() (map[string]string, error) {
	out := map[string]string{}
	home := d.personalHome()
	if home == "" {
		return out, nil
	}
	b, err := os.ReadFile(mcpcatalog.PersonalFile(home))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, err
	}
	each, err := rawServers(file["mcpServers"])
	if err != nil {
		return nil, err
	}
	for name := range each {
		out[personalMCPKey(name)] = "1"
	}
	return out, nil
}

// personalApprovalFolder is the folder whose summary lists the person's waiting connectors.
func (d *Daemon) personalApprovalFolder() string {
	paths := d.folderPaths()
	slices.Sort(paths)
	for _, f := range paths {
		if isDir(f) {
			return f
		}
	}
	if len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// personalConnectorApprovals is the person's waiting connectors, for the summary of folder.
func (d *Daemon) personalConnectorApprovals(folder string) []wire.Approval {
	home := d.personalHome()
	if home == "" {
		return nil
	}
	entries, _ := readMCPFile(mcpcatalog.PersonalFile(home))
	if len(entries) == 0 {
		return nil
	}
	d.mcpMu.Lock()
	gate, denied := d.personalGateLocked(), d.mcpDeniedLocked()
	d.mcpMu.Unlock()
	var out []wire.Approval
	for _, e := range entries {
		key, digest := personalMCPKey(e.name), entryDigest(e.raw)
		if !gate.waits(e) || denied[key] == digest {
			continue
		}
		line := "New in your settings"
		if _, had := gate.approved[key]; had {
			line = "Changed in your settings since you approved it"
		}
		out = append(out, wire.Approval{Kind: wire.ApprovalKindMcpServer, Key: personalApprovalKey + e.name, Title: e.name,
			Digest: &digest, Line: line, Actions: []wire.ApprovalAction{wire.ApprovalActionApprove, wire.ApprovalActionDeny}})
	}
	if len(out) == 0 || d.personalApprovalFolder() != folder {
		return nil
	}
	return out
}

// tellPersonalConnectors has the checks look again and sends the summary that lists them.
func (d *Daemon) tellPersonalConnectors() {
	d.mcpChanged()
	if folder := d.personalApprovalFolder(); folder != "" {
		if _, err := d.sendFolder(folder); err != nil {
			log.Printf("mcp: folder summary: %v", err)
		}
	}
}

// personalEntry is the person's own entry called name.
func (d *Daemon) personalEntry(name string) (mcpEntry, error) {
	home := d.personalHome()
	if home == "" {
		return mcpEntry{}, mcpRefusal(mcpcatalog.NoPersonalHome())
	}
	path := filepath.Join(home, ".agents", "mcp.json")
	entries, problem := readMCPFile(path)
	if problem != "" {
		return mcpEntry{}, mcpRefusal(mcpcatalog.Unreadable(path))
	}
	for _, e := range entries {
		if e.name == name {
			return e, nil
		}
	}
	return mcpEntry{}, Invalid("%s is not in your connectors any more.", name)
}

// answerPersonalConnector approves or denies the entry of that digest on this machine.
func (d *Daemon) answerPersonalConnector(ctx context.Context, name, digest string, approve bool) error {
	if !callerVouched(ctx) {
		return notInLock("approve this connector in Agent Loops on the machine itself, or on a device that the machine trusts")
	}
	e, err := d.personalEntry(name)
	if err != nil {
		return err
	}
	if entryDigest(e.raw) != digest {
		return mcpRefusal(mcpcatalog.StaleDigest())
	}
	key := personalMCPKey(name)
	if approve {
		if err := d.changeMCPApprovals(true, func(approved, _ map[string]string, gone map[string]wire.Date) error {
			return keepApproval(approved, gone, "", key, digest)
		}); err != nil {
			return approvalNotKept(err)
		}
		log.Printf("mcp: approve %s of the person's", name)
	} else {
		if err := d.keepDenied(key, digest); err != nil {
			return err
		}
		log.Printf("mcp: deny %s of the person's", name)
	}
	d.tellPersonalConnectors()
	return nil
}

// recordPersonalApproval approves an entry that a client just wrote to the person's own file.
func (d *Daemon) recordPersonalApproval(ctx context.Context, name string, raw json.RawMessage) {
	if !callerVouched(ctx) {
		return
	}
	if err := d.changeMCPApprovals(false, func(approved, _ map[string]string, gone map[string]wire.Date) error {
		return keepApproval(approved, gone, "", personalMCPKey(name), entryDigest(raw))
	}); err != nil {
		log.Printf("mcp: could not record the approval of %s: %v", name, err)
	}
}
