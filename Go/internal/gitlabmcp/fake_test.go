package gitlabmcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// A fake GitLab over HTTP (no account, #1629): the REST API v4 paths the server reads and
// writes, for the project group/app, with what a test puts in it.

type obj = map[string]any

const (
	project   = "group/app"
	projectID = 7
	token     = "glpat-test"
)

type fakeGitLab struct {
	t        *testing.T
	mu       sync.Mutex
	mrs      []obj
	pipes    []obj
	jobs     map[int][]obj
	commits  map[string][]obj
	issues   []obj
	activity []obj
	members  map[int]int // user id → access level
	// memberLists counts the calls that list members by user_ids[] (#2952).
	memberLists int
	requests    []string
	posted      []obj         // what was POSTed or PUT, with its path
	diffs       map[int][]obj // merge request → its files
	discuss     map[int][]obj // merge request → its discussions
	reviewers   map[int][]obj // merge request → its reviewers, with their states (#6296)
	approvers   map[int][]obj // merge request → who approved it (#6296)
	traces      map[int]string
	labelled    map[int][]obj // issue → its resource label events
	deploys     []obj         // the project's deployments (#4217)
	groupMember map[int]bool  // user id → a member of group 9
	status      int           // answer every request with this, when set
	header      http.Header
	sent        int // bytes of the bodies answered
	server      *httptest.Server
}

func newFake(t *testing.T) *fakeGitLab {
	f := &fakeGitLab{t: t, jobs: map[int][]obj{}, commits: map[string][]obj{}, members: map[int]int{}, diffs: map[int][]obj{},
		discuss: map[int][]obj{}, reviewers: map[int][]obj{}, approvers: map[int][]obj{}, traces: map[int]string{}, labelled: map[int][]obj{}, groupMember: map[int]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

// gitlab is a RealGitLab of the fake that shares nothing between polls.
func (f *fakeGitLab) gitlab() *RealGitLab {
	g := NewRealGitLab(f.server.URL, func() string { return token }, func(context.Context) (string, error) { return project, nil })
	g.sharing = 0
	return g
}

var (
	alex     = obj{"id": 1, "username": "alex"}
	dev      = obj{"id": 2, "username": "dev"}
	stranger = obj{"id": 3, "username": "stranger"}
	tokenBot = obj{"id": 4, "username": "project_7_bot_0a1b2c"}
	guest    = obj{"id": 5, "username": "guest"}
)

func (f *fakeGitLab) addMembers() {
	f.members[1], f.members[2], f.members[4], f.members[5] = 50, 30, 40, 10
}

func mrJSON(iid int, branch string, author obj, extra obj) obj {
	m := obj{"iid": iid, "title": "MR " + strconv.Itoa(iid), "state": "opened", "source_branch": branch, "target_branch": "main",
		"web_url": "https://gitlab.test/group/app/-/merge_requests/" + strconv.Itoa(iid), "author": author, "draft": false,
		"source_project_id": projectID, "target_project_id": projectID, "has_conflicts": false,
		"detailed_merge_status": "mergeable", "merge_status": "can_be_merged", "sha": "head" + strconv.Itoa(iid),
		"created_at": at(-60), "updated_at": at(-60), "labels": []string{}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func issueObj(iid int, author obj, extra obj) obj {
	i := obj{"iid": iid, "title": "Issue " + strconv.Itoa(iid), "state": "opened", "author": author,
		"web_url": "https://gitlab.test/group/app/-/issues/" + strconv.Itoa(iid), "labels": []string{}, "assignees": []obj{},
		"created_at": at(-60), "updated_at": at(-60)}
	for k, v := range extra {
		i[k] = v
	}
	return i
}

var (
	start = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	path  = regexp.MustCompile(`^/api/v4/projects/group%2Fapp(?:/(.*))?$`)
)

func at(minutes int) string {
	return mcpevents.TimeString(start.Add(time.Duration(minutes) * time.Minute))
}

func (f *fakeGitLab) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeGitLab) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.requests...)
}

func (f *fakeGitLab) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	rq := &fakeRequest{f: f, w: w, r: r, q: r.URL.Query()}
	if r.Header.Get("Authorization") != "Bearer "+token {
		rq.answer(401, obj{"message": "401 Unauthorized"})
		return
	}
	if f.status != 0 {
		rq.answer(f.status, obj{"message": "refused"})
		return
	}
	if r.URL.Path == "/api/v4/user" {
		rq.answer(200, alex)
		return
	}
	if id, ok := strings.CutPrefix(r.URL.Path, "/api/v4/groups/9/members/all/"); ok {
		rq.groupMember(id)
		return
	}
	m := path.FindStringSubmatch(r.URL.EscapedPath())
	if m == nil {
		rq.answer(404, obj{"message": "404 Project Not Found"})
		return
	}
	rq.parts = strings.Split(m[1], "/")
	if r.Method != http.MethodGet {
		body, _ := io.ReadAll(r.Body)
		var sent obj
		_ = json.Unmarshal(body, &sent)
		sent["path"] = m[1]
		f.posted = append(f.posted, sent)
	}
	// The routes in order: the first that matches answers.
	if !rq.routeProjectAndMergeRequests() {
		rq.routePipelinesIssuesAndMembers()
	}
}

// fakeRequest is one request to the fake GitLab, with the path's parts after the project.
type fakeRequest struct {
	f     *fakeGitLab
	w     http.ResponseWriter
	r     *http.Request
	q     url.Values
	parts []string
}

func (rq *fakeRequest) answer(status int, v any) {
	w := rq.w
	w.Header().Set("Content-Type", "application/json")
	for k, vs := range rq.f.header {
		w.Header()[k] = vs
	}
	body, _ := json.Marshal(v)
	// GitLab's Rack ETag: a weak one over the body, and 304 for a GET that sends it back.
	if rq.r.Method == http.MethodGet && status == 200 {
		etag := fmt.Sprintf(`W/"%x"`, sha256.Sum256(body))
		w.Header().Set("ETag", etag)
		if rq.r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.WriteHeader(status)
	rq.f.sent += len(body)
	_, _ = w.Write(body)
}

func (rq *fakeRequest) notFound() { rq.answer(404, obj{"message": "404 Not found"}) }

// one answers the item of list whose key is n, or 404.
func (rq *fakeRequest) one(list []obj, key, n string) {
	if o, ok := byIID(list, key, n); ok {
		rq.answer(200, o)
		return
	}
	rq.notFound()
}

func (rq *fakeRequest) groupMember(id string) {
	if n, _ := strconv.Atoi(id); rq.f.groupMember[n] {
		rq.answer(200, obj{"id": n, "access_level": 30})
		return
	}
	rq.notFound()
}

func byIID(list []obj, key string, n string) (obj, bool) {
	for _, o := range list {
		if strconv.Itoa(o[key].(int)) == n {
			return o, true
		}
	}
	return nil, false
}

// atHead is whether this request sent merge request iid's head sha.
func (rq *fakeRequest) atHead(iid string) bool {
	f := rq.f
	o, ok := byIID(f.mrs, "iid", iid)
	return ok && len(f.posted) > 0 && f.posted[len(f.posted)-1]["sha"] == o["sha"]
}

// stated is list's items in that state, in the order asked for and the page asked for, as
// GitLab answers.
func (rq *fakeRequest) stated(list []obj, key, want string) []obj {
	q := rq.q
	out := []obj{}
	for _, o := range list {
		if want == "" || want == "all" || o[key] == want {
			out = append(out, o)
		}
	}
	if by := q.Get("order_by"); strings.HasSuffix(by, "_at") && q.Get("sort") == "desc" {
		sort.SliceStable(out, func(i, j int) bool { s, _ := out[i][by].(string); t, _ := out[j][by].(string); return s > t })
	}
	if size, _ := strconv.Atoi(q.Get("per_page")); size > 0 {
		page, _ := strconv.Atoi(q.Get("page"))
		first := min(len(out), max(0, page-1)*size)
		out = out[first:min(len(out), first+size)]
	}
	return out
}

// is is whether the path has n parts and the first is first.
func (rq *fakeRequest) is(n int, first string) bool {
	return len(rq.parts) == n && rq.parts[0] == first
}

// routeProjectAndMergeRequests answers the project, its merge requests, writes of notes and
// a job's trace; false when none of them matches.
func (rq *fakeRequest) routeProjectAndMergeRequests() bool {
	f, parts, q, post := rq.f, rq.parts, rq.q, rq.r.Method == http.MethodPost
	switch {
	case len(parts) == 1 && parts[0] == "":
		rq.answer(200, obj{"id": projectID, "path_with_namespace": project, "default_branch": "main"})
	case rq.is(1, "merge_requests") && post:
		rq.answer(201, mrJSON(12, "agent/issue-2", alex, nil))
	case rq.is(5, "merge_requests") && parts[2] == "discussions" && parts[4] == "notes" && post:
		rq.answer(201, obj{"id": 901, "body": "x"})
	case rq.is(3, "issues") && parts[2] == "notes" && rq.r.Method == http.MethodGet:
		rq.answer(200, []obj{{"id": 50, "body": "added ~bug", "system": true, "author": alex},
			{"id": 51, "body": "Please also do this.", "author": stranger}, {"id": 52, "body": "Yes, do.", "author": dev}})
	case rq.is(3, "issues") && parts[2] == "resource_label_events":
		id, _ := strconv.Atoi(parts[1])
		rq.answer(200, orEmpty(f.labelled[id]))
	case rq.is(1, "merge_requests"):
		rq.answer(200, whose(rq.stated(f.mrs, "state", q.Get("state")), q))
	case rq.is(2, "merge_requests"):
		rq.one(f.mrs, "iid", parts[1])
	case rq.is(3, "merge_requests") && (parts[2] == "merge" || parts[2] == "approve") && !rq.atHead(parts[1]):
		// GitLab's answer to a sha that is not the head.
		rq.answer(409, obj{"message": "SHA does not match HEAD of source branch"})
	case rq.is(3, "merge_requests") && parts[2] == "merge" && rq.r.Method == http.MethodPut:
		rq.answer(200, obj{"iid": 1, "state": "merged", "merge_commit_sha": "merged1"})
	case rq.is(3, "merge_requests") && parts[2] == "diffs":
		id, _ := strconv.Atoi(parts[1])
		rq.answer(200, rq.stated(orEmpty(f.diffs[id]), "", ""))
	case rq.is(3, "merge_requests") && parts[2] == "reviewers":
		id, _ := strconv.Atoi(parts[1])
		rq.answer(200, orEmpty(f.reviewers[id]))
	case rq.is(3, "merge_requests") && parts[2] == "approvals":
		id, _ := strconv.Atoi(parts[1])
		rq.answer(200, obj{"approved_by": orEmpty(f.approvers[id])})
	case rq.is(3, "merge_requests") && parts[2] == "discussions":
		id, _ := strconv.Atoi(parts[1])
		rq.answer(200, orEmpty(f.discuss[id]))
	case rq.is(3, "merge_requests") && parts[2] == "approve" && post:
		rq.answer(201, obj{"id": 1})
	case rq.is(3, "pipelines") && parts[2] == "retry" && post:
		rq.answer(201, obj{"id": 100, "status": "pending", "web_url": "https://gitlab.test/p/100"})
	case rq.is(3, "jobs") && parts[2] == "trace":
		id, _ := strconv.Atoi(parts[1])
		rq.w.WriteHeader(200)
		_, _ = io.WriteString(rq.w, f.traces[id])
	case len(parts) == 3 && parts[2] == "notes" && post:
		rq.answer(201, obj{"id": 900, "body": "x"})
	default:
		return false
	}
	return true
}

// routePipelinesIssuesAndMembers answers pipelines, deployments, commits, issues, events and
// members, and 404 for the rest.
func (rq *fakeRequest) routePipelinesIssuesAndMembers() {
	f, parts, q := rq.f, rq.parts, rq.q
	switch {
	case rq.is(1, "pipelines") && q.Get("scope") == "finished":
		rq.finishedPipelines()
	case rq.is(1, "pipelines"):
		rq.answer(200, rq.stated(f.pipes, "status", q.Get("status")))
	case rq.is(1, "deployments") && q.Get("updated_after") != "":
		rq.deploymentsUpdated()
	case rq.is(1, "deployments"):
		rq.deploymentsFinished()
	case rq.is(2, "deployments"):
		rq.one(f.deploys, "id", parts[1])
	case rq.is(2, "pipelines"):
		rq.one(f.pipes, "id", parts[1])
	case rq.is(3, "pipelines") && parts[2] == "jobs":
		rq.pipelineJobs()
	case rq.is(4, "repository") && parts[1] == "commits" && parts[3] == "merge_requests":
		rq.answer(200, orEmpty(f.commits[parts[2]]))
	case rq.is(1, "issues"):
		rq.answer(200, whose(rq.stated(f.issues, "state", q.Get("state")), q))
	case rq.is(2, "issues"):
		rq.one(f.issues, "iid", parts[1])
	case rq.is(1, "events"):
		rq.answer(200, f.activity)
	case rq.is(2, "members") && parts[1] == "all":
		rq.membersListed()
	case rq.is(3, "members") && parts[1] == "all":
		rq.member(parts[2])
	default:
		rq.notFound()
	}
}

func (rq *fakeRequest) finishedPipelines() {
	done := []obj{}
	for _, p := range rq.f.pipes {
		if finished(fmt.Sprint(p["status"])) {
			done = append(done, p)
		}
	}
	rq.answer(200, rq.stated(done, "", ""))
}

// deploymentsUpdated is GitLab's updated_after and environment, newest updated first (#5244).
func (rq *fakeRequest) deploymentsUpdated() {
	q := rq.q
	out := []obj{}
	for _, d := range orEmpty(rq.f.deploys) {
		env, _ := d["environment"].(obj)
		if up, _ := d["updated_at"].(string); up >= q.Get("updated_after") && (q.Get("environment") == "" || env["name"] == q.Get("environment")) {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return fmt.Sprint(out[i]["updated_at"]) > fmt.Sprint(out[j]["updated_at"]) })
	rq.answer(200, out)
}

// deploymentsFinished is GitLab's finished_after and finished_before, by finished_at.
func (rq *fakeRequest) deploymentsFinished() {
	q := rq.q
	out := []obj{}
	for _, d := range rq.stated(orEmpty(rq.f.deploys), "status", q.Get("status")) {
		done, _ := d["finished_at"].(string)
		if (q.Get("finished_after") == "" || done > q.Get("finished_after")) && (q.Get("finished_before") == "" || done != "" && done < q.Get("finished_before")) {
			out = append(out, d)
		}
	}
	rq.answer(200, out)
}

func (rq *fakeRequest) pipelineJobs() {
	id, _ := strconv.Atoi(rq.parts[1])
	jobs := orEmpty(rq.f.jobs[id])
	if rq.q.Get("scope[]") == "failed" {
		jobs = rq.stated(jobs, "status", "failed")
	}
	rq.answer(200, jobs)
}

func (rq *fakeRequest) membersListed() {
	rq.f.memberLists++
	out := []obj{}
	for _, v := range rq.q["user_ids[]"] {
		id, _ := strconv.Atoi(v)
		if level, ok := rq.f.members[id]; ok {
			out = append(out, obj{"id": id, "access_level": level})
		}
	}
	rq.answer(200, out)
}

func (rq *fakeRequest) member(raw string) {
	id, _ := strconv.Atoi(raw)
	if level, ok := rq.f.members[id]; ok {
		rq.answer(200, obj{"id": id, "access_level": level})
		return
	}
	rq.notFound()
}

func orEmpty(list []obj) []obj {
	if list == nil {
		return []obj{}
	}
	return list
}

// whose narrows a list as GitLab's author_username, reviewer_username and assignee_username do (#2034).
func whose(list []obj, q url.Values) []obj {
	named := func(o obj, key string) []string {
		var out []string
		switch v := o[key].(type) {
		case obj:
			out = append(out, fmt.Sprint(v["username"]))
		case []obj:
			for _, u := range v {
				out = append(out, fmt.Sprint(u["username"]))
			}
		}
		return out
	}
	out := []obj{}
	for _, o := range list {
		keep := true
		for param, key := range map[string]string{"author_username": "author", "reviewer_username": "reviewers", "assignee_username": "assignees"} {
			if want := q.Get(param); want != "" && !slices.Contains(named(o, key), want) {
				keep = false
			}
		}
		if keep {
			out = append(out, o)
		}
	}
	return out
}
