package agentsd

import (
	"encoding/json"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/connections"
	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
)

// The closed epics that work/timeline read (#7028), kept in memory only, so that a read asks GitHub for its list of
// epics and then only for the open ones and those that changed. An epic's updatedAt does not move with each of its
// sub-issues, so one is read again after timelineKeptFor whatever its updatedAt.
const (
	timelineKeptFor = 24 * time.Hour
	// timelineKeptEpics bounds a repo's epics, and timelineKeptBytes all of them, by their JSON.
	timelineKeptEpics = 100
	timelineKeptBytes = 4 << 20
)

type timelineKeep struct {
	mu sync.Mutex
	// conn is the GitHub connection that read the epics: another one never sees them.
	conn  string
	repos map[string]map[int]keptEpic
	bytes int
}

type keptEpic struct {
	epic githubmcp.TimelineEpic
	at   time.Time
	size int
}

// timelineConn names a connection; a refresh of its token does not change it.
func timelineConn(c connections.Connection) string {
	return c.Login + " " + strconv.FormatInt(c.ConnectedAt.UnixNano(), 10)
}

// use says which connection reads now; the epics of another one are forgotten.
func (k *timelineKeep) use(conn string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn != conn {
		k.conn, k.repos, k.bytes = conn, nil, 0
	}
}

// get is repo's epic number as it was at updatedAt, when conn read it less than timelineKeptFor ago.
func (k *timelineKeep) get(conn, repo string, number int, updatedAt string, now time.Time) (githubmcp.TimelineEpic, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.repos[repo][number]
	if !ok || k.conn != conn || updatedAt == "" || e.epic.UpdatedAt != updatedAt || now.Sub(e.at) >= timelineKeptFor {
		return githubmcp.TimelineEpic{}, false
	}
	return e.epic, true
}

// put keeps the closed ones of the epics that conn read at now.
func (k *timelineKeep) put(conn, repo string, epics []githubmcp.TimelineEpic, now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn != conn {
		return
	}
	if k.repos == nil {
		k.repos = map[string]map[int]keptEpic{}
	}
	if k.repos[repo] == nil {
		k.repos[repo] = map[int]keptEpic{}
	}
	for _, e := range epics {
		k.drop(repo, e.Number)
		if e.ClosedAt == "" {
			continue
		}
		b, _ := json.Marshal(e)
		k.repos[repo][e.Number] = keptEpic{epic: e, at: now, size: len(b)}
		k.bytes += len(b)
	}
	for len(k.repos[repo]) > timelineKeptEpics {
		k.drop(repo, oldestKept(k.repos[repo]))
	}
	for k.bytes > timelineKeptBytes {
		var most string
		for r, kept := range k.repos {
			if len(kept) > 0 && (most == "" || len(kept) > len(k.repos[most])) {
				most = r
			}
		}
		k.drop(most, oldestKept(k.repos[most]))
	}
}

// trim forgets repo's epics that are not listed, and those closed longer ago than the forecast learns from.
func (k *timelineKeep) trim(conn, repo string, listed map[int]bool, now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn != conn {
		return
	}
	oldest := now.Add(-lineage.ForecastWindow).UTC().Format(time.RFC3339)
	for n, e := range k.repos[repo] {
		if !listed[n] || e.epic.ClosedAt < oldest {
			k.drop(repo, n)
		}
	}
}

// only forgets the repos that are not in repos.
func (k *timelineKeep) only(repos []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for repo, kept := range k.repos {
		if slices.Contains(repos, repo) {
			continue
		}
		for n := range kept {
			k.drop(repo, n)
		}
		delete(k.repos, repo)
	}
}

// drop forgets an epic. Holds mu.
func (k *timelineKeep) drop(repo string, number int) {
	k.bytes -= k.repos[repo][number].size
	delete(k.repos[repo], number)
}

// oldestKept is the number of the epic that closed first.
func oldestKept(kept map[int]keptEpic) int {
	first := 0
	for n, e := range kept {
		if first == 0 || e.epic.ClosedAt < kept[first].epic.ClosedAt {
			first = n
		}
	}
	return first
}

// timelineForgetGone forgets the epics of the repos that no folder is on.
func (d *Daemon) timelineForgetGone() {
	d.keptEpics.mu.Lock()
	none := len(d.keptEpics.repos) == 0
	d.keptEpics.mu.Unlock()
	if none {
		return
	}
	var repos []string
	for _, f := range d.folderPaths() {
		if r := d.folderRepo(f); r.forge == "github" {
			repos = append(repos, r.path)
		}
	}
	d.keptEpics.only(repos)
}
