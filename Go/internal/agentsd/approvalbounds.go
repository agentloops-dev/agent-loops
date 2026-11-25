package agentsd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/mcpcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The limits of the two approval files, mcp-approvals.json and plugin-approvals.json (#6749,
// #6750). They stay in the machine's data folder: an approval that came with a repo, a sync or
// another machine would approve what nobody here saw. An answer has no age while its connector
// or plugin is there. 90 days after that went, the answer goes, and it is asked again.

const (
	// approvalsMost bounds each map of an approvals file, and approvalMostBytes one entry: its
	// key and its value.
	approvalsMost     = 2000
	approvalMostBytes = 8 << 10
	approvalKeptGone  = store.MaxAge
)

// keepApproval puts one answer in a map of an approvals file, or says to the person why it is
// not kept. A full map lets go of the answer whose item went the longest ago, never of one whose
// item is there; gone has the marks of the map's keys, after prefix.
func keepApproval(m map[string]string, gone map[string]wire.Date, prefix, key, value string) error {
	if n := len(key) + len(value); n > approvalMostBytes {
		return Invalid("The answer was not kept: it is %d bytes, and the most is %d.", n, approvalMostBytes)
	}
	if _, had := m[key]; !had && len(m) >= approvalsMost {
		oldest := ""
		for k := range m {
			if at, ok := gone[prefix+k]; ok && (oldest == "" || at < gone[prefix+oldest]) {
				oldest = k
			}
		}
		if oldest == "" {
			return Invalid("This machine keeps %d answers of this kind at most. Remove a connector or a plugin that has one, and try again in an hour.", approvalsMost)
		}
		delete(m, oldest)
	}
	m[key] = value
	return nil
}

// approvalItems is what the answers of a map are about.
type approvalItems interface {
	folderGone(key string) bool
	itemThere(key string) bool
}

// ageApprovals drops the answers of a map whose folder is gone, and those whose item went 90
// days ago. was has the time each item went, by its key after prefix; the marks that stay go to
// marks. An item that is back has no mark.
func ageApprovals(m map[string]string, was, marks map[string]wire.Date, prefix string, items approvalItems, now time.Time) {
	for key := range m {
		at, marked := was[prefix+key]
		switch {
		case items.folderGone(key):
			delete(m, key)
		case items.itemThere(key):
		case !marked || at.Time().After(now):
			marks[prefix+key] = wire.DateOf(now)
		case now.Sub(at.Time()) >= approvalKeptGone:
			delete(m, key)
		default:
			marks[prefix+key] = at
		}
	}
}

// whyNotKept is what a person is told when an answer to mcp-approvals.json was not kept.
func whyNotKept(err error) string {
	if e := (*Error)(nil); errors.As(err, &e) {
		return e.Message
	}
	return "mcp-approvals.json could not be written."
}

func approvalNotKept(err error) error { return mcpRefusal(mcpcatalog.Failed(whyNotKept(err))) }

// keepUnreadable keeps a copy of an approvals file that cannot be read, before the person's own
// act writes a new one.
func keepUnreadable(path string) {
	b, err := os.ReadFile(path) //nolint:gosec // G703: a fixed name in the daemon's own data folder
	if err != nil {
		return
	}
	// store-bounds: kind=blob; size=100MiB a file; count=3; age=90d; applied=on write and hourly sweep
	if err := os.WriteFile(fmt.Sprintf("%s.unreadable-%d", path, time.Now().Unix()), b, 0o644); err != nil { //nolint:gosec // G306: the mode this file always had; only its error is new
		log.Printf("approvals: could not keep the unreadable %s aside: %v", filepath.Base(path), err) //nolint:gosec // G706: the same path
	}
	store.TrimSetAsides(path)
}

// mcpPartLocked is one map of mcp-approvals.json. With mcpMu held; a part that is missing or
// not a map is empty.
func (d *Daemon) mcpPartLocked(part string) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal(d.mcpApprovals[part], &m)
	if m == nil { // null
		m = map[string]string{}
	}
	return m
}

func (d *Daemon) mcpGoneLocked() map[string]wire.Date {
	gone := map[string]wire.Date{}
	_ = json.Unmarshal(d.mcpApprovals["gone"], &gone) // missing or not a map: none is marked
	return gone
}

// mcpGonePrefix is what a key of a part is marked under in "gone": the approvals, the denials
// and where each came from have the same keys, and a view has its own.
func mcpGonePrefix(part string) string {
	if part == "views" {
		return "view "
	}
	return ""
}

// sweepMCPApprovals drops the answers of folders that are gone, and those of a connector that
// left its file 90 days ago. A file not there, or one that could not be read, is left as it is.
func (d *Daemon) sweepMCPApprovals(gone func(string) bool, now time.Time) {
	if _, err := os.Stat(d.mcpApprovalsPath()); err != nil {
		return
	}
	d.mcpMu.Lock()
	defer d.mcpMu.Unlock()
	d.mcpApprovalsLocked()
	if d.mcpApprovals == nil {
		return
	}
	connectors := &connectorsThere{files: d.mcpFileNames(), worktrees: map[string][]string{}}
	was := d.mcpGoneLocked()
	marks := map[string]wire.Date{}
	changed := false
	for _, part := range []string{"approved", "denied", "from", "views", "synced"} {
		m := map[string]string{}
		if d.mcpApprovals[part] == nil || json.Unmarshal(d.mcpApprovals[part], &m) != nil {
			continue
		}
		n := len(m)
		ageApprovals(m, was, marks, mcpGonePrefix(part), connectorItems{connectors, part, gone}, now)
		if len(m) != n {
			d.mcpApprovals[part], _ = json.Marshal(m)
			changed = true
		}
	}
	if !maps.Equal(was, marks) {
		d.mcpApprovals["gone"], _ = json.Marshal(marks)
		changed = true
	}
	if changed {
		if err := d.saveMCPApprovalsLocked(false); err != nil {
			log.Printf("mcp-approvals.json: could not save: %v", err)
		}
	}
}

// connectorsThere says if the connector that a key of mcp-approvals.json names is in its
// file: the folder's, a worktree's of the folder (an approval is kept under the folder), or the
// person's own. A file that cannot be read, and a folder not on disk now, settle nothing.
type connectorsThere struct {
	files     *mcpFileNames
	worktrees map[string][]string
}

func (c *connectorsThere) has(part, key string) bool {
	scope, rest, ok := strings.Cut(key, "|")
	if !ok {
		return true
	}
	name := ""
	if part == "views" {
		name, _, _ = strings.Cut(rest, "|")
	} else if name, ok = strings.CutPrefix(rest, ".agents/mcp.json|"); !ok {
		return true
	}
	if scope == "personal" {
		return c.files.has(scope, name)
	}
	if !filepath.IsAbs(scope) {
		return true
	}
	if _, err := os.Stat(scope); err != nil { //nolint:gosec // G703: a folder the daemon wrote in its own file; only asked whether it is there
		return true
	}
	if c.files.has(scope, name) {
		return true
	}
	trees, seen := c.worktrees[scope]
	if !seen {
		dir := filepath.Join(scope, ".agents", "worktrees")
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return true
		}
		for _, e := range entries {
			trees = append(trees, filepath.Join(dir, e.Name()))
		}
		c.worktrees[scope] = trees
	}
	for _, tree := range trees {
		if c.files.has(tree, name) {
			return true
		}
	}
	return false
}

// connectorItems is the answers of one part of mcp-approvals.json.
type connectorItems struct {
	connectors *connectorsThere
	part       string
	gone       func(folder string) bool
}

func (c connectorItems) folderGone(key string) bool { return c.gone(approvalKeyFolder(key)) }
func (c connectorItems) itemThere(key string) bool  { return c.connectors.has(c.part, key) }

// pluginItems is the answers of plugin-approvals.json.
type pluginItems struct{ gone func(folder string) bool }

func (p pluginItems) folderGone(key string) bool {
	folder, _, ok := strings.Cut(key, "/.agents/plugins/")
	return ok && p.gone(folder)
}

// itemThere says if a plugin's folder is there. A project's folder not on disk now settles nothing.
func (p pluginItems) itemThere(key string) bool {
	folder, _, ok := strings.Cut(key, "/.agents/plugins/")
	if !ok {
		return true
	}
	if _, err := os.Stat(folder); err != nil { //nolint:gosec // G703: a folder the daemon wrote in its own file; only asked whether it is there
		return true
	}
	_, err := os.Lstat(key) //nolint:gosec // G703: as above
	return !errors.Is(err, os.ErrNotExist)
}

// sweepPluginApprovals drops the answers of plugins whose folder is gone, and those of a plugin
// that left its folder 90 days ago. A plugin's key is its folder, <folder>/.agents/plugins/<name>.
func (d *Daemon) sweepPluginApprovals(gone func(string) bool, now time.Time) {
	if _, err := os.Stat(d.pluginApprovalsPath()); err != nil {
		return
	}
	pluginMu.Lock()
	defer pluginMu.Unlock()
	records, unreadable := d.loadPluginApprovals()
	if unreadable {
		return
	}
	was, marks := records.Gone, map[string]wire.Date{}
	n := len(records.Approved) + len(records.Denied)
	for _, m := range []map[string]string{records.Approved, records.Denied} {
		ageApprovals(m, was, marks, "", pluginItems{gone}, now)
	}
	if len(records.Approved)+len(records.Denied) == n && maps.Equal(was, marks) {
		return
	}
	records.Gone = marks
	if err := d.savePluginApprovals(records); err != nil {
		log.Printf("plugin-approvals.json: could not save: %v", err)
	}
}
