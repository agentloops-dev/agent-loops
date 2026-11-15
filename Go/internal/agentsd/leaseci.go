package agentsd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The ci lease (#5678, epic #4584). Alex, 2026-11-15: "keep it simple. If any jobs are
// queued, wait. Once no jobs are queued, continue." An agent leases ci, and releases it at
// once, before it starts work on an issue and before it pushes or opens a pull request. A
// grant goes in line while the repository the agent's folder pushes to has any CI job
// queued, and the whole line is granted when none is. Many may hold it at once: only the
// queue holds it.
//
// The agent says ci; the book keeps one entry per repository, ci:github:<owner>/<repo>, as
// one repository's queue says nothing about another's. Only GitHub is read. A folder on
// another forge, or with no forge, leases plain ci, which nothing holds; so does a
// repository whose CI cannot be read: CI must never stop work it cannot see.

const (
	ciResource    wire.ResourceName = "ci"
	ciLeasePrefix                   = "ci:"
	// ciQueueFresh is how old a read may be when an agent asks: older, and it reads again.
	ciQueueFresh = 30 * time.Second
	// ciQueueEvery is how often a waiting line's queue is read.
	ciQueueEvery = time.Minute
	// ciGhostAge is how long a queued run with no jobs is given before it is a ghost: a
	// run GitHub shows as queued for ever, and lets nobody cancel or delete (#5676).
	ciGhostAge = time.Hour
	// ciQueueTimeout bounds one read of the queue.
	ciQueueTimeout = 15 * time.Second
)

// ciQueueBook is the queue of each repository's CI, as last read.
type ciQueueBook struct {
	ciqMu    sync.Mutex
	ciq      map[wire.ResourceName]ciQueue
	ciGhosts map[int64]bool
}

// ciQueue is one read of a repository's queued jobs.
type ciQueue struct {
	jobs   int
	labels []string
	oldest time.Time
	readAt time.Time
	folder string
}

// isCI is whether name is the ci lease, of a repository or plain.
func isCI(name wire.ResourceName) bool {
	return name == ciResource || strings.HasPrefix(string(name), ciLeasePrefix)
}

// ciRepoOf is the repository of a ci:github:<path> name.
func ciRepoOf(name wire.ResourceName) (forgeRepo, bool) {
	rest, ok := strings.CutPrefix(string(name), ciLeasePrefix)
	if !ok {
		return forgeRepo{}, false
	}
	forge, path, ok := strings.Cut(rest, ":")
	if !ok || forge != "github" || path == "" {
		return forgeRepo{}, false
	}
	return forgeRepo{forge: forge, path: path}, true
}

// ciDisplay is the lease's name for a person: "CI of owner/repo", or "CI".
func ciDisplay(name wire.ResourceName) string {
	if r, ok := ciRepoOf(name); ok {
		return "CI of " + r.path
	}
	return "CI"
}

// ciLeaseName is the name the book keeps for an agent's ci: its folder's repository, with
// the folder noted for the read. Any other name is given back as it is.
func (d *Daemon) ciLeaseName(agent wire.UUID, given string) string {
	if n, _ := resourceName(given); n != ciResource {
		return given
	}
	folder, err := d.agentFolder(agent)
	if err != nil {
		return given
	}
	r := d.folderRepo(folder)
	if r.forge != "github" || r.path == "" {
		return given
	}
	name := wire.ResourceName(ciLeasePrefix + "github:" + strings.ToLower(r.path))
	d.ciqMu.Lock()
	if d.ciq == nil {
		d.ciq = map[wire.ResourceName]ciQueue{}
	}
	q := d.ciq[name]
	q.folder = folder
	d.ciq[name] = q
	d.ciqMu.Unlock()
	return string(name)
}

// ciQueueNow reads name's queue when the last read is older than ciQueueFresh. Not under
// leaseMu: it asks GitHub.
func (d *Daemon) ciQueueNow(name wire.ResourceName) {
	r, ok := ciRepoOf(name)
	if !ok {
		return
	}
	d.ciqMu.Lock()
	q := d.ciq[name]
	d.ciqMu.Unlock()
	if q.folder == "" || nowFunc().Sub(q.readAt) < ciQueueFresh {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ciQueueTimeout)
	defer cancel()
	d.readCIQueue(ctx, name, ciRepoHere{repo: r, folder: q.folder})
}

// ciJobs is the part of actions/runs/{id}/jobs that the queue reads.
type ciJobs struct {
	Jobs []struct {
		Status    string   `json:"status"`
		CreatedAt string   `json:"created_at"`
		Labels    []string `json:"labels"`
	} `json:"jobs"`
}

// readCIQueue counts the jobs queued in h's CI and keeps the count as name's. The runs that
// are queued or in progress are read, as a run in progress can have a job queued for a
// runner. A run with no queued job does not count. A read that fails keeps no jobs, so the
// line goes on.
func (d *Daemon) readCIQueue(ctx context.Context, name wire.ResourceName, h ciRepoHere) {
	at := nowFunc()
	q := ciQueue{readAt: at, folder: h.folder}
	labels := map[string]bool{}
	ok := true
	for _, status := range []string{"queued", "in_progress"} {
		raw, err := d.ciGet(ctx, h, fmt.Sprintf("repos/%s/actions/runs?status=%s&per_page=100", h.repo.path, status))
		var list struct {
			WorkflowRuns []struct {
				ID        int64  `json:"id"`
				CreatedAt string `json:"created_at"`
			} `json:"workflow_runs"`
		}
		if err != nil || json.Unmarshal(raw, &list) != nil {
			ok = false
			break
		}
		for _, run := range list.WorkflowRuns {
			d.ciqMu.Lock()
			ghost := d.ciGhosts[run.ID]
			d.ciqMu.Unlock()
			if ghost {
				continue
			}
			raw, err := d.ciGet(ctx, h, fmt.Sprintf("repos/%s/actions/runs/%d/jobs?per_page=100", h.repo.path, run.ID))
			var jobs ciJobs
			if err != nil || json.Unmarshal(raw, &jobs) != nil {
				ok = false
				break
			}
			if made, err := time.Parse(time.RFC3339, run.CreatedAt); err == nil && status == "queued" &&
				len(jobs.Jobs) == 0 && at.Sub(made) > ciGhostAge {
				d.ciqMu.Lock()
				if d.ciGhosts == nil {
					d.ciGhosts = map[int64]bool{}
				}
				d.ciGhosts[run.ID] = true
				d.ciqMu.Unlock()
			}
			for _, j := range jobs.Jobs {
				if j.Status != "queued" {
					continue
				}
				q.jobs++
				for _, l := range j.Labels {
					labels[l] = true
				}
				if made, err := time.Parse(time.RFC3339, j.CreatedAt); err == nil && (q.oldest.IsZero() || made.Before(q.oldest)) {
					q.oldest = made
				}
			}
		}
		if !ok {
			break
		}
	}
	if !ok {
		q = ciQueue{readAt: at, folder: h.folder}
	} else {
		for l := range labels {
			q.labels = append(q.labels, l)
		}
		sort.Strings(q.labels)
	}
	d.ciqMu.Lock()
	if d.ciq == nil {
		d.ciq = map[wire.ResourceName]ciQueue{}
	}
	d.ciq[name] = q
	d.ciqMu.Unlock()
}

// ciHeld is why a ci grant waits now, as a sentence with no full stop, or "" when it does
// not: "CI of owner/repo has 2 jobs queued (xcode-27), the oldest for 14 min".
func (d *Daemon) ciHeld(name wire.ResourceName) string {
	if !isCI(name) {
		return ""
	}
	d.ciqMu.Lock()
	q := d.ciq[name]
	d.ciqMu.Unlock()
	if q.jobs == 0 {
		return ""
	}
	words := ciDisplay(name) + " has 1 job queued"
	if q.jobs > 1 {
		words = fmt.Sprintf("%s has %d jobs queued", ciDisplay(name), q.jobs)
	}
	if len(q.labels) > 0 {
		words += " (" + strings.Join(q.labels, ", ") + ")"
	}
	if !q.oldest.IsZero() {
		words += fmt.Sprintf(", the oldest for %d min", max(0, int(q.readAt.Sub(q.oldest).Minutes())))
	}
	return words
}

// ciWait is why name's line waits, with no numbers, for its row in the lease snapshot.
func (d *Daemon) ciWait(name wire.ResourceName, e *leaseEntry) string {
	if e == nil || !isCI(name) || len(e.Line) == 0 || d.ciHeld(name) == "" {
		return ""
	}
	return ciDisplay(name) + " has jobs queued"
}

// ciQueueClock reads the queue of each repository whose ci line waits, and grants the line
// when no job is queued.
func (d *Daemon) ciQueueClock(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(ciQueueEvery):
		}
		d.serveCILines(ctx)
	}
}

// serveCILines is one turn of ciQueueClock.
func (d *Daemon) serveCILines(ctx context.Context) {
	d.leaseMu.Lock()
	var waiting []wire.ResourceName
	if d.book != nil {
		for name, e := range d.book.Entries {
			if isCI(name) && len(e.Line) > 0 {
				waiting = append(waiting, name)
			}
		}
	}
	d.leaseMu.Unlock()
	for _, name := range waiting {
		if r, ok := ciRepoOf(name); ok {
			d.ciqMu.Lock()
			folder := d.ciq[name].folder
			d.ciqMu.Unlock()
			if folder != "" {
				rctx, cancel := context.WithTimeout(ctx, ciQueueTimeout)
				d.readCIQueue(rctx, name, ciRepoHere{repo: r, folder: folder})
				cancel()
			}
		}
		d.leaseMu.Lock()
		e := d.book.Entries[name]
		var events []leaseEvent
		if e != nil {
			if events = d.handOn(name, e); len(events) > 0 {
				d.store(name, e)
				d.leasesChanged()
			}
		}
		d.leaseMu.Unlock()
		d.settle(events)
	}
}
