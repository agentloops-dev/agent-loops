package agentsd

import (
	"errors"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/mcpcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Small stores that grew and never shrank (#3652): each is pruned by the hourly sweep.
// A workflow's state goes 30 days after its file went; the approvals (approvalbounds.go) and the
// review files let go of a folder that is gone; a device not seen for 90 days is forgotten; the record of a
// catalogue item goes 30 days after the item went.

const (
	workflowStateKeptGone = 30 * 24 * time.Hour
	deviceKeptUnseen      = 90 * 24 * time.Hour
	catalogItemKeptGone   = 30 * 24 * time.Hour
)

func (d *Daemon) sweepSmallStores(now time.Time) {
	d.sweepGoneWorkflowStates(now)
	gone := d.goneFolderTest()
	d.sweepMCPApprovals(gone, now)
	d.sweepPluginApprovals(gone, now)
	d.sweepAllowances(now)
	d.sweepReviewFolders(gone)
	d.sweepUnseenDevices(now)
	d.sweepConnectorSetup()
	d.sweepHostedLogs(now)
	d.sweepCatalogItems(now, gone)
	d.sweepAccountHomes(now)
	carryErr := d.carryRemovedConnectors()
	if carryErr != nil {
		log.Printf("retention: mcp.removed.json is not swept: its old copies could not be carried over: %v", carryErr)
	}
	for _, f := range d.smallFiles {
		// The sweep would write over the copies that could not be carried over.
		if carryErr != nil && f.Name() == d.removedConnectors.Name() {
			continue
		}
		if n, err := f.Sweep(); err != nil {
			log.Printf("retention: %s: %v", f.Name(), err)
		} else if n > 0 {
			log.Printf("retention: %d entries of %s past its limits", n, f.Name())
		}
	}
}

// smallFile opens a small file (store.JSONFile) that the hourly sweep holds to its limits. Only
// newWith opens one.
func smallFile[T any](d *Daemon, spec store.Spec, path func() string, at func(*T) *wire.Date) *store.JSONFile[T] {
	f := store.OpenJSONFile(spec, path, at, func() time.Time { return nowFunc() })
	d.smallFiles = append(d.smallFiles, f)
	return f
}

// goneFolderTest says whether a folder is gone: no folder of this host, and not on disk. A
// folder on a drive not plugged in now is still a folder of this host, so it is kept.
func (d *Daemon) goneFolderTest() func(folder string) bool {
	known := map[string]bool{}
	for _, f := range d.folderPaths() {
		known[filepath.Clean(f)] = true
	}
	if home := d.personalHome(); home != "" {
		known[filepath.Clean(home)] = true
	}
	return func(folder string) bool {
		if !filepath.IsAbs(folder) || known[filepath.Clean(folder)] {
			return false
		}
		_, err := os.Stat(folder)
		return errors.Is(err, os.ErrNotExist)
	}
}

// sweepGoneWorkflowStates marks each state whose workflow file is not there now with the
// time it went, clears that when the file is back, and drops a state gone for 30 days.
// Saving drops the copies no state names any more.
func (d *Daemon) sweepGoneWorkflowStates(now time.Time) {
	d.workflowMu.Lock()
	defer d.workflowMu.Unlock()
	// Before the first scan nothing is known to be gone.
	if d.workflowFiles == nil {
		return
	}
	r := d.recordsLocked()
	if d.workflowRecsBad {
		return
	}
	changed := false
	kept := r.States[:0]
	for _, st := range r.States {
		there := d.workflowFiles[store.FolderPath(st.Folder)][st.WorkflowID] != nil
		switch {
		case there:
			if st.GoneSince != nil {
				st.GoneSince, changed = nil, true
			}
			changed = st.ageDetails(d.workflowFiles[store.FolderPath(st.Folder)][st.WorkflowID], now) || changed
		case st.GoneSince == nil || st.GoneSince.Time().After(now):
			t := wire.DateOf(now)
			st.GoneSince, changed = &t, true
		case now.Sub(st.GoneSince.Time()) >= workflowStateKeptGone:
			log.Printf("workflows: let go of %s in %s: its file went 30 days ago", st.WorkflowID, st.Folder)
			changed = true
			continue
		}
		kept = append(kept, st)
	}
	r.States = kept
	changed = r.evictGone(0) || changed
	if changed {
		d.saveRecordsLocked(false)
	}
}

// approvalKeyFolder is the folder a key of the approvals files begins with: the part before
// the first "|". A scope that is not a path (the user's) is never a folder that is gone.
func approvalKeyFolder(key string) string {
	folder, _, _ := strings.Cut(key, "|")
	return folder
}

// sweepReviewFolders drops the dismissed and opened pull requests of folders that are gone.
func (d *Daemon) sweepReviewFolders(gone func(string) bool) {
	d.reviewMu.Lock()
	defer d.reviewMu.Unlock()
	for _, f := range []*store.JSONFile[reviewMark]{d.dismissed, d.opened} {
		if err := changeMarks(f, func(m map[string]reviewMark) {
			maps.DeleteFunc(m, func(_ string, r reviewMark) bool { return gone(r.Folder) })
		}); err != nil {
			log.Printf("for review: %s was not written: %v", f.Name(), err)
		}
	}
}

// sweepUnseenDevices forgets each device not seen for 90 days, as devices/forget does: it
// pairs again to come back.
func (d *Daemon) sweepUnseenDevices(now time.Time) {
	d.devMu.Lock()
	defer d.devMu.Unlock()
	d.loadDevices()
	var gone []wire.UUID
	kept := d.devRecords[:0]
	for _, r := range d.devRecords {
		seen := r.AnnouncedAt
		if r.LastSeenAt != nil {
			seen = *r.LastSeenAt
		}
		if now.Sub(seen.Time()) >= deviceKeptUnseen {
			gone = append(gone, r.ID)
			continue
		}
		kept = append(kept, r)
	}
	if len(gone) == 0 {
		return
	}
	d.devRecords = kept
	if err := d.writeDevices(); err != nil {
		log.Printf("devices.json: could not save: %v", err)
		return
	}
	for _, id := range gone {
		log.Printf("devices: forgot %s: not seen for 90 days", id)
		d.afterDeviceForgottenLocked(id)
	}
}

// sweepCatalogItems marks each catalogue record whose item is not there with the time it went,
// clears that when the item is back, and drops a record 30 days after. An item that is there
// keeps its record, whatever its age (gate 1 of #6669). A folder on a drive not plugged in now
// says nothing of its items.
func (d *Daemon) sweepCatalogItems(now time.Time, gone func(string) bool) {
	there := func(item func(scope, name string) bool) func(string) bool {
		return func(key string) bool {
			i := strings.LastIndex(key, "/")
			if i < 0 {
				return false
			}
			scope := key[:i]
			if scope != "personal" {
				if _, err := os.Stat(scope); errors.Is(err, os.ErrNotExist) { //nolint:gosec // G703: a folder the daemon wrote in its own file; only asked whether it is there
					return !gone(scope)
				}
			}
			return item(scope, key[i+1:])
		}
	}
	skill := there(func(scope, name string) bool {
		if scope == "personal" {
			if scope = d.personalHome(); scope == "" {
				return true
			}
		}
		_, err := os.Lstat(filepath.Join(scope, ".agents", "skills", name)) //nolint:gosec // G703: as above
		return !errors.Is(err, os.ErrNotExist)
	})
	inFile := d.mcpStillThere()
	server := there(func(scope, name string) bool { return inFile(scope + "/" + name) })
	if err := skillcatalog.UpdateSidecar(d.skillInstaller().Sidecar, func(m map[string]skillcatalog.SidecarRecord) {
		sweepGoneItems(m, skill, now, func(r *skillcatalog.SidecarRecord) **time.Time { return &r.GoneSince })
	}); err != nil {
		log.Printf("catalog-skills.json: could not save: %v", err)
	}
	if err := mcpcatalog.UpdateSidecar(d.mcpSidecarPath(), func(m map[string]mcpcatalog.Record) {
		sweepGoneItems(m, server, now, func(r *mcpcatalog.Record) **time.Time { return &r.GoneSince })
	}); err != nil {
		log.Printf("catalog-mcp.json: could not save: %v", err)
	}
}

func sweepGoneItems[R any](m map[string]R, there func(key string) bool, now time.Time, since func(*R) **time.Time) {
	for key, r := range m {
		at := since(&r)
		switch {
		case there(key):
			*at = nil
		case *at == nil || (*at).After(now):
			t := now.UTC().Truncate(time.Second)
			*at = &t
		case now.Sub(**at) >= catalogItemKeptGone:
			delete(m, key)
			continue
		}
		m[key] = r
	}
}
