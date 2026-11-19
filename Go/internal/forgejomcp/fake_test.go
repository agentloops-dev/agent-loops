package forgejomcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// A fake Forgejo over HTTP (no account, #1753): the REST API v1 paths the server reads and
// writes, for the repository acme/app, with what a test puts in it.

type obj = map[string]any

const (
	repo  = "acme/app"
	token = "forgejo-test"
)

type fakeForgejo struct {
	t        *testing.T
	mu       sync.Mutex
	pulls    []obj
	issues   []obj
	comments []obj
	timeline map[int][]obj
	reviews  map[int][]obj // pull request → its reviews
	lines    map[int][]obj // review → its comments
	statuses map[string]obj
	perms    map[string]string // login → permission
	me       string
	posted   []obj // what was POSTed, with its path
	// adminless answers 403 to a permission read, as Forgejo does for a token without admin.
	adminless     bool
	collaborators map[string]bool
	status        int // answer every request with this, when set
	// version is what /version says: Forgejo 17 when empty.
	version string
	// runs are the Actions runs, newest first; jobs a run's jobs by its id, logs a job's log by
	// its id, and with ?step=n, one step's. giteaJobs answers a run's jobs as Gitea does, {"jobs": …}.
	runs      []obj
	jobs      map[int][]obj
	logs      map[string]string
	giteaJobs bool
	ranges    []string // each log read's Range
	// rerunning are the jobs whose rerun Forgejo refuses as still running.
	rerunning map[int]bool
	// files are a pull request's changed files, diffs its git diff, thread an issue's or a
	// pull request's comments, by its number (the views, #3838).
	files  map[int][]obj
	diffs  map[int]string
	thread map[int][]obj
	server *httptest.Server
}

func newFake(t *testing.T) *fakeForgejo {
	f := &fakeForgejo{t: t, timeline: map[int][]obj{}, reviews: map[int][]obj{}, lines: map[int][]obj{}, statuses: map[string]obj{},
		jobs: map[int][]obj{}, logs: map[string]string{}, rerunning: map[int]bool{}, files: map[int][]obj{}, diffs: map[int]string{}, thread: map[int][]obj{},
		perms: map[string]string{"alex": "admin", "dev": "write", "reader": "read"}, me: "alex", collaborators: map[string]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

// forgejo is a RealForgejo of the fake that shares nothing between polls, on the clock now.
func (f *fakeForgejo) forgejo(now func() time.Time) *RealForgejo {
	fj := NewRealForgejo(f.server.URL, func() string { return token }, func(context.Context) (string, error) { return repo, nil })
	fj.sharing = 0
	fj.Now = now
	return fj
}

func (f *fakeForgejo) set(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change()
}

var (
	alex     = obj{"id": 1, "login": "alex", "avatar_url": "https://fj.test/avatars/a1"}
	dev      = obj{"id": 2, "login": "dev"}
	stranger = obj{"id": 3, "login": "stranger"}
	renovate = obj{"id": 4, "login": "renovate-bot"}
	reader   = obj{"id": 5, "login": "reader"}
)

var start = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

// at is start and n minutes, as Forgejo writes a time.
func at(n int) string { return start.Add(time.Duration(n) * time.Minute).Format(time.RFC3339) }

func pull(number int, branch string, author obj, extra obj) obj {
	p := obj{"number": number, "title": "PR " + strconv.Itoa(number), "body": "", "state": "open", "user": author,
		"html_url": "https://forgejo.test/acme/app/pulls/" + strconv.Itoa(number), "mergeable": true, "merged": false,
		"head": obj{"ref": branch, "sha": "sha" + strconv.Itoa(number), "repo_id": 7}, "base": obj{"ref": "main", "repo_id": 7},
		"created_at": at(0), "updated_at": at(0), "labels": []any{}, "requested_reviewers": []any{}}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func issue(number int, author obj, extra obj) obj {
	i := obj{"number": number, "title": "Issue " + strconv.Itoa(number), "body": "", "state": "open", "user": author,
		"html_url": "https://forgejo.test/acme/app/issues/" + strconv.Itoa(number), "labels": []any{}, "assignees": []any{},
		"created_at": at(0), "updated_at": at(0)}
	for k, v := range extra {
		i[k] = v
	}
	return i
}

var (
	pullsPath    = regexp.MustCompile(`^/api/v1/repos/acme/app/pulls$`)
	pullPath     = regexp.MustCompile(`^/api/v1/repos/acme/app/pulls/([0-9]+)$`)
	reviewsPath  = regexp.MustCompile(`^/api/v1/repos/acme/app/pulls/([0-9]+)/reviews$`)
	linesPath    = regexp.MustCompile(`^/api/v1/repos/acme/app/pulls/([0-9]+)/reviews/([0-9]+)/comments$`)
	mergePath    = regexp.MustCompile(`^/api/v1/repos/acme/app/pulls/([0-9]+)/merge$`)
	issuesPath   = regexp.MustCompile(`^/api/v1/repos/acme/app/issues$`)
	issuePath    = regexp.MustCompile(`^/api/v1/repos/acme/app/issues/([0-9]+)$`)
	timelinePath = regexp.MustCompile(`^/api/v1/repos/acme/app/issues/([0-9]+)/timeline$`)
	commentsPath = regexp.MustCompile(`^/api/v1/repos/acme/app/issues/([0-9]+)/comments$`)
	statusPath   = regexp.MustCompile(`^/api/v1/repos/acme/app/commits/([^/]+)/status$`)
	permPath     = regexp.MustCompile(`^/api/v1/repos/acme/app/collaborators/([^/]+)/permission$`)
	collabPath   = regexp.MustCompile(`^/api/v1/repos/acme/app/collaborators/([^/]+)$`)
	runJobsPath  = regexp.MustCompile(`^/api/v1/repos/acme/app/actions/runs/([0-9]+)/jobs$`)
	jobPath      = regexp.MustCompile(`^/api/v1/repos/acme/app/actions/jobs/([0-9]+)$`)
	jobLogPath   = regexp.MustCompile(`^/api/v1/repos/acme/app/actions/jobs/([0-9]+)/logs$`)
	filesPath    = regexp.MustCompile(`^/api/v1/repos/acme/app/pulls/([0-9]+)/files$`)
	diffPath     = regexp.MustCompile(`^/api/v1/repos/acme/app/pulls/([0-9]+)\.diff$`)
	jobRerunPath = regexp.MustCompile(`^/api/v1/repos/acme/app/actions/jobs/([0-9]+)/rerun$`)
	runRerunPath = regexp.MustCompile(`^/api/v1/repos/acme/app/actions/runs/([0-9]+)/rerun-failed-jobs$`)
)

func (f *fakeForgejo) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "token "+token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"message":"no"}`))
		return
	}
	q := r.URL.Query()
	if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
		reply(w, []any{})
		return
	}
	p := r.URL.Path
	if r.Method == http.MethodPost {
		f.servePost(w, r, p)
		return
	}
	if f.serveActions(w, r, p, q) || f.servePulls(w, r, p, q) || f.serveIssues(w, r, p, q) || f.serveAccess(w, r, p, q) {
		return
	}
	if strings.HasPrefix(p, "/api/v1/orgs/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.t.Errorf("fake Forgejo: no %s %s", r.Method, r.URL)
	w.WriteHeader(http.StatusNotFound)
}

// reply answers v as JSON.
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// num is the number a path's pattern matched first.
func num(m []string) int { i, _ := strconv.Atoi(m[1]); return i }

// servePost answers a POST at p, and keeps its body in posted.
func (f *fakeForgejo) servePost(w http.ResponseWriter, r *http.Request, p string) {
	body := obj{}
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	body["_path"] = p
	f.posted = append(f.posted, body)
	switch {
	case pullsPath.MatchString(p):
		reply(w, pull(9, body["head"].(string), alex, obj{"title": body["title"], "base": obj{"ref": body["base"], "repo_id": 7}}))
	case reviewsPath.MatchString(p):
		reply(w, obj{"id": 77, "html_url": "https://forgejo.test/acme/app/pulls/1#review-77"})
	case commentsPath.MatchString(p):
		reply(w, obj{"id": 55, "html_url": "https://forgejo.test/acme/app/issues/2#issuecomment-55"})
	case mergePath.MatchString(p):
		w.WriteHeader(http.StatusOK)
	case jobRerunPath.MatchString(p):
		// A job waiting again, after a job it needs was sent, is still running.
		if f.rerunning[num(jobRerunPath.FindStringSubmatch(p))] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"job is still running"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case runRerunPath.MatchString(p):
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveActions answers the version and the Actions runs, jobs and logs; false when p is none of them.
func (f *fakeForgejo) serveActions(w http.ResponseWriter, r *http.Request, p string, q url.Values) bool {
	switch {
	case p == "/api/v1/version":
		v := f.version
		if v == "" {
			v = "17.0.1+gitea-1.22.0"
		}
		reply(w, obj{"version": v})
	case p == "/api/v1/repos/acme/app/actions/runs":
		out := []obj{}
		for _, run := range f.runs {
			// Forgejo filters by run_number; Gitea has no such filter and lists them all.
			if f.giteaJobs || q.Get("run_number") == "" || strconv.Itoa(run["index_in_repo"].(int)) == q.Get("run_number") {
				out = append(out, run)
			}
		}
		reply(w, obj{"workflow_runs": out, "total_count": len(out)})
	case runJobsPath.MatchString(p):
		jobs := orEmpty(f.jobs[num(runJobsPath.FindStringSubmatch(p))])
		if f.giteaJobs {
			reply(w, obj{"jobs": jobs, "total_count": len(jobs)})
			return true
		}
		reply(w, jobs)
	case jobPath.MatchString(p):
		id := num(jobPath.FindStringSubmatch(p))
		for _, jobs := range f.jobs {
			for _, j := range jobs {
				if j["id"] == id {
					reply(w, j)
					return true
				}
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case jobLogPath.MatchString(p):
		key := jobLogPath.FindStringSubmatch(p)[1]
		if step := q.Get("step"); step != "" {
			key += "?step=" + step
		}
		text, ok := f.logs[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		f.ranges = append(f.ranges, r.Header.Get("Range"))
		// The end the Range asks for, as Forgejo's ServeContent answers it.
		if size, err := strconv.Atoi(strings.TrimPrefix(r.Header.Get("Range"), "bytes=-")); err == nil && size < len(text) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", len(text)-size, len(text)-1, len(text)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(text[len(text)-size:]))
			return true
		}
		_, _ = w.Write([]byte(text))
	default:
		return false
	}
	return true
}

// servePulls answers the user, the repository and its pull requests; false when p is none of them.
func (f *fakeForgejo) servePulls(w http.ResponseWriter, r *http.Request, p string, q url.Values) bool {
	switch {
	case p == "/api/v1/user":
		reply(w, obj{"id": 1, "login": f.me})
	case p == "/api/v1/repos/acme/app":
		reply(w, obj{"full_name": repo, "default_branch": "main", "allow_merge_commits": true, "allow_squash_merge": true,
			"default_merge_style": "squash"})
	case filesPath.MatchString(p):
		reply(w, orEmpty(f.files[num(filesPath.FindStringSubmatch(p))]))
	case diffPath.MatchString(p):
		_, _ = io.WriteString(w, f.diffs[num(diffPath.FindStringSubmatch(p))])
	case pullsPath.MatchString(p):
		out := []obj{}
		for _, pr := range f.pulls {
			if q.Get("state") == "all" || pr["state"] == q.Get("state") {
				out = append(out, pr)
			}
		}
		// recentupdate: newest updated first.
		for i := range out {
			for j := i + 1; j < len(out); j++ {
				if out[j]["updated_at"].(string) > out[i]["updated_at"].(string) {
					out[i], out[j] = out[j], out[i]
				}
			}
		}
		reply(w, out)
	case pullPath.MatchString(p):
		for _, pr := range f.pulls {
			if pr["number"] == num(pullPath.FindStringSubmatch(p)) {
				reply(w, pr)
				return true
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case reviewsPath.MatchString(p):
		reply(w, orEmpty(f.reviews[num(reviewsPath.FindStringSubmatch(p))]))
	case linesPath.MatchString(p):
		m := linesPath.FindStringSubmatch(p)
		id, _ := strconv.Atoi(m[2])
		reply(w, orEmpty(f.lines[id]))
	default:
		return false
	}
	return true
}

// serveIssues answers issues, comments, timelines and statuses; false when p is none of them.
func (f *fakeForgejo) serveIssues(w http.ResponseWriter, r *http.Request, p string, q url.Values) bool {
	switch {
	case issuesPath.MatchString(p):
		out := []obj{}
		for _, i := range f.issues {
			if (q.Get("state") == "all" || i["state"] == q.Get("state")) &&
				(q.Get("since") == "" || !mcpevents.Before(i["updated_at"].(string), q.Get("since"))) &&
				(q.Get("assigned_by") == "" || assigned(i, q.Get("assigned_by"))) &&
				(q.Get("labels") == "" || labelled(i, q.Get("labels"))) {
				out = append(out, i)
			}
		}
		reply(w, out)
	case issuePath.MatchString(p):
		for _, i := range f.issues {
			if i["number"] == num(issuePath.FindStringSubmatch(p)) {
				reply(w, i)
				return true
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case p == "/api/v1/repos/acme/app/issues/comments":
		out := []obj{}
		for _, c := range f.comments {
			if !mcpevents.Before(c["updated_at"].(string), q.Get("since")) {
				out = append(out, c)
			}
		}
		reply(w, out)
	case commentsPath.MatchString(p):
		reply(w, orEmpty(f.thread[num(commentsPath.FindStringSubmatch(p))]))
	case timelinePath.MatchString(p):
		reply(w, orEmpty(f.timeline[num(timelinePath.FindStringSubmatch(p))]))
	case statusPath.MatchString(p):
		if s, ok := f.statuses[statusPath.FindStringSubmatch(p)[1]]; ok {
			reply(w, s)
			return true
		}
		reply(w, obj{"state": "", "statuses": []any{}})
	default:
		return false
	}
	return true
}

// serveAccess answers permissions and collaborators; false when p is none of them.
func (f *fakeForgejo) serveAccess(w http.ResponseWriter, r *http.Request, p string, q url.Values) bool {
	switch {
	case permPath.MatchString(p):
		if f.adminless {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		perm, ok := f.perms[permPath.FindStringSubmatch(p)[1]]
		if !ok {
			perm = "read"
		}
		reply(w, obj{"permission": perm})
	case collabPath.MatchString(p):
		if f.collaborators[collabPath.FindStringSubmatch(p)[1]] {
			w.WriteHeader(http.StatusNoContent)
			return true
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		return false
	}
	return true
}

func orEmpty(list []obj) []obj {
	if list == nil {
		return []obj{}
	}
	return list
}

func assigned(i obj, login string) bool {
	for _, a := range i["assignees"].([]any) {
		if a.(obj)["login"] == login {
			return true
		}
	}
	return false
}

func labelled(i obj, name string) bool {
	for _, l := range i["labels"].([]any) {
		if l.(obj)["name"] == name {
			return true
		}
	}
	return false
}

// poller polls one event as the daemon does, keeping its cursor, on a clock a minute on each poll.
type poller struct {
	t      *testing.T
	events *Events
	name   string
	args   map[string]any
	cursor any
	clock  *time.Time
}

func newPoller(t *testing.T, f *fakeForgejo, name string, args map[string]any) *poller {
	clock := start
	p := &poller{t: t, name: name, args: args, clock: &clock}
	now := func() time.Time { return *p.clock }
	p.events = &Events{Forgejo: f.forgejo(now), Now: now}
	return p
}

func (p *poller) poll() ([]string, map[string]any) {
	p.t.Helper()
	params := map[string]any{"name": p.name, "arguments": p.args}
	if p.cursor != nil {
		params["cursor"] = p.cursor
	}
	raw, _ := json.Marshal(params)
	result, err := p.events.Poll(context.Background(), raw)
	if err != nil {
		p.t.Fatalf("%s: %v", p.name, err)
	}
	p.cursor = result["cursor"]
	*p.clock = p.clock.Add(time.Minute)
	var ids []string
	for _, e := range result["events"].([]any) {
		ids = append(ids, e.(map[string]any)["eventId"].(string))
	}
	return ids, result
}

func data(t *testing.T, result map[string]any, i int) map[string]any {
	t.Helper()
	return result["events"].([]any)[i].(map[string]any)["data"].(map[string]any)
}

func same(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
