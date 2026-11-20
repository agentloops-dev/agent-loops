package gitlabmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The MCP events (#1629) of a GitLab project's merge requests, pipelines and issues, in the
// github server's shape (Go/internal/githubmcp/events.go): the same names where they mean the
// same, and the same cursor, paging and trust filter (#728), polled only: `events/list` and
// `events/poll` of the events draft (experimental-ext-triggers-events), which the Agents
// daemon speaks. A merge request is a "pr" in names and data, as on GitHub.

// event is one event as events/list gives it.
type event = mcpevents.Event

// projectPattern is a project's path (group/name, with subgroups) or its numeric id.
var projectPattern = regexp.MustCompile(`^([0-9]+|[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+)$`)

var (
	projectProperty = property{Name: "project", Kind: "string",
		Description: "The GitLab project, by its path (group/name, subgroups too) or its id. Left out, the project this folder pushes to.",
		Pattern:     projectPattern, LooksLike: "group/name or a project id"}
	prProperty = property{Name: "pr", Kind: "integers", Description: "Only this merge request, or these, by number (its iid, as !12)."}
	// anyoneProperty opts into anyone's issue, comment or merge request (#728); left out, only the project's own people.
	anyoneProperty = property{Name: "anyone", Kind: "boolean",
		Description: "true: from anyone, bots, forks and people outside the project too. Left out, only the project's members with Reporter access or more, never a bot, and for a merge request never one from a fork."}
	// mineProperty is only the connection's owner's own merge requests (#2034), a fork's too.
	mineProperty = property{Name: "mine", Kind: "boolean",
		Description: "true: only merge requests by the GitLab account this server is connected as, whatever anyone says."}
	// anyProperty is every one in the project (#3797): a wait that names none sets it.
	anyProperty = property{Name: "any", Kind: "boolean",
		Description: "true: every one in the project. A wait (not a workflow trigger) that names none, by the other arguments that say which, is refused without it. It is about which ones; anyone is about who."}
	// fixesProperty is the merge requests that close these issues (#3797): a lead knows a child's issue before its merge request.
	fixesProperty = property{Name: "fixes", Kind: "refs",
		Description: `Only merge requests whose description closes one of these issues, as "#12" (this project's) or "group/project#12".`,
		Pattern:     issueRefPattern, LooksLike: `issues, as "#12" or "group/project#12"`}
)

// ownMRs is what every merge request event says of whose merge requests it raises.
const ownMRs = " Only merge requests from a branch of the project itself, not a fork, by its members with Reporter access or more, never a bot, unless anyone is true."

func branchProperty(description string) property {
	return property{Name: "branch", Kind: "string", Description: description}
}

func object(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

var str, integer = map[string]any{"type": "string"}, map[string]any{"type": "integer"}

func mrSchema(extra map[string]any) map[string]any {
	props := map[string]any{"number": integer, "title": str, "branch": str, "url": str, "fixes": fixesSchema}
	for k, v := range extra {
		props[k] = v
	}
	return object(props)
}

func mrInput() schema {
	return schema{projectProperty, branchProperty("Only the merge request from this branch."), prProperty, anyoneProperty}
}

// namedInput is mrInput for an event about one merge request: it names it by fixes or mine
// too, or takes any (#3797).
func namedInput() schema {
	return schema{projectProperty, branchProperty("Only the merge request from this branch."), prProperty, fixesProperty,
		anyoneProperty, mineProperty, anyProperty}
}

// events are the events this server offers, in the github server's order, with run.completed
// and deploy.waiting after checks.passed as there (#4217), and pr.reviewed and pr.commented
// after pr.blocked (#6296).
// fixesSchema is a merge request's fixes: the issues its description closes, "#12" or
// "group/project#12" (#2349).
var fixesSchema = map[string]any{"type": "array", "items": str}

var events = append([]event{
	{Name: "checks.failed", Title: "Pipeline failed",
		Description: "A merge request's pipeline finished failed: one event per pipeline and failure, so a pipeline retried that fails again is a new one. Jobs allowed to fail are not in failedJobs." + ownMRs,
		Input:       schema{projectProperty, branchProperty("Only pipelines of the merge request from this branch."), prProperty, fixesProperty, anyoneProperty, mineProperty, anyProperty},
		Payload: object(map[string]any{
			"pr": mrSchema(nil), "headSha": str,
			"run":        object(map[string]any{"id": integer, "name": str, "url": str}),
			"failedJobs": map[string]any{"type": "array", "items": object(map[string]any{"name": str, "url": str})},
		})},
	// The github server's checks.passed (#6296): one event per pipeline that passed.
	{Name: "checks.passed", Title: "Pipeline passed",
		Description: "A merge request's pipeline finished with success: one event per pipeline and update time, so a lane waiting on CI going green wakes without polling, and a pipeline retried that passes again is a new one. run is the pipeline; completedAt is when GitLab last updated it, as it finished." + ownMRs,
		Input:       schema{projectProperty, branchProperty("Only pipelines of the merge request from this branch."), prProperty, fixesProperty, anyoneProperty, mineProperty, anyProperty},
		Payload: object(map[string]any{
			"pr": mrSchema(nil), "headSha": str,
			"run":         object(map[string]any{"id": integer, "name": str, "url": str}),
			"completedAt": str,
		})},
	// A pipeline no merge request owns, as main's or a tag's, as the github server's (#3810, #4217).
	{Name: "run.completed", Title: "Pipeline finished",
		Description: "A GitLab CI/CD pipeline of the project finished, whatever started it (a push, a tag, a person, a schedule, the API) and whatever its status (success, failed, canceled, skipped): one event per pipeline and update time, so a pipeline retried is a new one. For a pipeline no merge request owns, such as main's or a tag's release; a merge request's failed pipelines are checks.failed. run.name is its name (workflow:name, or Pipeline #<id>), run.number its iid, run.event its source (push, web, schedule, api, trigger, merge_request_event…), run.branch its ref. Only pipelines started by the project's members with Reporter access or more, never a bot, unless anyone is true. deploys is what it deployed: each of its jobs with an environment: that deployed, as gitlab:<project>/<environment>/<deployment iid>.",
		Input: schema{projectProperty,
			property{Name: "pipeline", Kind: "string", Description: "Only pipelines of this name (workflow:name), as release."},
			branchProperty("Only pipelines of this branch or tag, as main or v0.16.0."),
			property{Name: "run", Kind: "integer", Description: "Only this pipeline, by its id."}, anyoneProperty, anyProperty},
		Payload: object(map[string]any{
			"run": object(map[string]any{"id": integer, "name": str, "number": integer, "event": str, "branch": str, "headSha": str,
				"url": str, "conclusion": str, "actor": str}),
			"completedAt": str,
			// What the pipeline deployed: its successful deployments (#4217).
			"deploys": map[string]any{"type": "array", "items": str},
		})},
	// A deploy held by a protected environment's approvals, as the github server's (#3587, #4217).
	{Name: "deploy.waiting", Title: "Deploy waiting for approval",
		Description: "A GitLab pipeline's job is waiting for an approver of a protected environment's deployment approvals (GitLab Premium), as a release pipeline's deploy job to production: one event per pipeline and set of blocked deployments, so the next environment it waits on is a new one. Each of environments is one blocked deployment, by its id, named by its environment, with the job's page as its url. reviewers are the approval rules still open, by GitLab's names for them; canApprove says whether the GitLab account this server is connected as may answer every one. Answer it on GitLab. Only pipelines started by the project's members with Reporter access or more, never a bot, unless anyone is true.",
		Input: schema{projectProperty,
			property{Name: "pipeline", Kind: "string", Description: "Only pipelines of this name (workflow:name), as release."},
			branchProperty("Only pipelines of this branch or tag, as main or v0.16.0."), anyoneProperty},
		Payload: object(map[string]any{
			"run": object(map[string]any{"id": integer, "name": str, "number": integer, "event": str, "branch": str, "headSha": str,
				"url": str, "actor": str}),
			"environments": map[string]any{"type": "array", "items": object(map[string]any{"id": integer, "name": str, "url": str})},
			"reviewers":    map[string]any{"type": "array", "items": str},
			"canApprove":   map[string]any{"type": "boolean"},
			"waitingSince": str,
		})},
	deployEvent("deploy.started", "Deploy started",
		"A GitLab deployment went running: a job with an environment: began to deploy, whatever started its pipeline. One event per deployment."),
	deployEvent("deploy.succeeded", "Deploy succeeded",
		"A GitLab deployment went success: a job with an environment: deployed. One event per deployment."),
	deployEvent("deploy.failed", "Deploy failed",
		"A GitLab deployment went failed or canceled: a job with an environment: did not deploy. One event per deployment. outcome is cancelled when GitLab says canceled, else failed."),
	{Name: "pr.opened", Title: "Merge request opened",
		Description: "A merge request was opened, or a draft was marked ready: a merge request open and not a draft now that was not at the last poll. Drafts never count. Reopened, it is raised again." + ownMRs,
		Input:       mrInput(),
		Payload:     object(map[string]any{"pr": mrSchema(map[string]any{"author": str}), "openedAt": str})},
	{Name: "pr.merged", Title: "Merge request merged", Description: "A merge request was merged." + ownMRs,
		Input:   namedInput(),
		Payload: object(map[string]any{"pr": mrSchema(nil), "mergedAt": str, "mergeSha": str})},
	{Name: "pr.closed", Title: "Merge request closed", Description: "A merge request was closed without being merged." + ownMRs,
		Input:   namedInput(),
		Payload: object(map[string]any{"pr": mrSchema(nil), "closedAt": str})},
	{Name: "pr.conflicted", Title: "Merge request in conflict",
		Description: "An open merge request began to conflict with its target branch, so it cannot merge until it is rebased. Once per conflict: it is raised again only after it merges cleanly and then conflicts anew." + ownMRs,
		Input:       namedInput(),
		Payload:     object(map[string]any{"pr": mrSchema(nil), "conflictedAt": str})},
	{Name: "pr.blocked", Title: "Merge request blocked",
		Description: mcpevents.BlockedDescription("On GitLab: detailed_merge_status, one reason at a time (conflict, need_rebase as behind, ci_must_pass as required_check_failed with the head pipeline, requested_changes); automerge_disabled is never read, as GitLab does not say who turned auto-merge off." + ownMRs),
		Input:       namedInput(),
		Payload:     mcpevents.BlockedPayload(mrSchema(nil))},
	// The github server's pr.reviewed and pr.commented (#6296).
	{Name: "pr.reviewed", Title: "Merge request reviewed",
		Description: "Someone approved an open merge request (state approved), or a reviewer asked for changes (changes_requested, GitLab 17.2 or later). GitLab keeps only each person's last answer, with no words: so body is always empty, url is the merge request's, commit is its head when the poll saw the answer, and reviewedAt is that poll's time. Once per answer: an approval taken back and given again is a new one. Read for the 50 newest open merge requests. Never a bot's answer, nor a person's who is not one of the project's members with Reporter access or more, unless anyone is true." + ownMRs,
		Input:       namedInput(),
		Payload: object(map[string]any{"pr": mrSchema(nil),
			"review": object(map[string]any{"author": str, "state": str, "body": str, "url": str, "commit": str}), "reviewedAt": str})},
	{Name: "pr.commented", Title: "Merge request commented on",
		Description: "A comment was left on a merge request itself, in its overview (not on a line of its diff, which review.comment covers), or an answer in such a thread. Its body is the first 4000 characters. GitLab's own notes (a push, an approval) are not comments. A comment whose body starts with <!-- agent --> is left out: an agent's, not a person's, where agents post as the person's account. Only comments by the project's members with Reporter access or more, never a bot, unless anyone is true; and only on the project's own merge requests.",
		Input:       schema{projectProperty, prProperty, fixesProperty, anyoneProperty, mineProperty, anyProperty},
		Payload: object(map[string]any{"pr": mrSchema(nil),
			"comment": object(map[string]any{"id": integer, "author": str, "url": str, "body": str}), "commentedAt": str})},
	// For the person, not the project (#2034): what asks the connection's owner for something.
	{Name: "review.requested", Title: "Review requested from me",
		Description: "The GitLab account this server is connected as was made a reviewer of an open merge request: once per request, so one taken off and put back is a new one. Anyone's merge request, a fork's too.",
		Input:       schema{projectProperty, prProperty},
		Payload:     object(map[string]any{"pr": mrSchema(map[string]any{"author": str}), "requestedAt": str})},
	{Name: "review.comment", Title: "Merge request line commented on",
		Description: "Someone left a comment on a line of a merge request's diff, or answered in its thread. Its body is the first 4000 characters. Only comments by the project's members with Reporter access or more, never a bot, unless anyone is true; and only on the project's own merge requests.",
		Input:       namedInput(),
		Payload: object(map[string]any{"pr": mrSchema(nil), "commentedAt": str,
			"comment": object(map[string]any{"id": integer, "author": str, "path": str, "line": integer, "body": str, "url": str})})},
}, append(issueEvents(), assignedEvent)...)

// assignedEvent is for the person, not the project (#2034): an issue given to the connection's owner.
var assignedEvent = event{Name: "issue.assigned", Title: "Issue assigned to me",
	Description: "An open issue was assigned to the GitLab account this server is connected as: once per assignment, so one unassigned and assigned again is a new one. Anyone's issue.",
	Input:       schema{projectProperty, property{Name: "issue", Kind: "integer", Description: "Only this issue, by number (its iid, as #12)."}},
	Payload: object(map[string]any{"issue": object(map[string]any{
		"number": integer, "title": str, "author": str, "labels": map[string]any{"type": "array", "items": str}, "url": str,
		"assignees": map[string]any{"type": "array", "items": str},
	}), "assignedAt": str})}

func issueEvents() []event {
	issueSchema := object(map[string]any{
		"number": integer, "title": str, "author": str, "labels": map[string]any{"type": "array", "items": str}, "url": str,
		"assignees": map[string]any{"type": "array", "items": str},
	})
	issue := property{Name: "issue", Kind: "integers", Description: "Only this issue, or these, by number (its iid, as #12)."}
	kinds := []struct {
		kind                      IssueKind
		title, description, label string
		payload                   map[string]any
		who                       string
	}{
		{IssueOpened, "Issue opened", "An issue was opened.",
			"Only issues opened with this label.", map[string]any{"openedAt": str}, "Only issues opened by"},
		{IssueLabelled, "Issue labelled", "A label was added to an issue: one event per label added.",
			"Only when this label is the one added.", map[string]any{"label": str, "by": str, "labelledAt": str}, "Only labels added by"},
		{IssueCommented, "Issue commented on",
			"A comment was left on an issue. Its body is the first 4000 characters. GitLab's own notes (a label added, a close) are not comments.",
			"Only issues with this label.",
			map[string]any{"comment": object(map[string]any{"author": str, "url": str, "body": str}), "commentedAt": str},
			"Only comments by"},
		{IssueClosed, "Issue closed", "An issue was closed. Reopened and closed again, it is a new event.", "Only issues with this label.",
			map[string]any{"by": str, "closedAt": str}, "Only issues closed by"},
	}
	out := make([]event, len(kinds))
	for i, k := range kinds {
		label := property{Name: "label", Kind: "string", Description: k.label}
		input := schema{projectProperty, label, issue, anyoneProperty, anyProperty}
		if k.kind == IssueOpened {
			input = schema{projectProperty, label, anyoneProperty}
		}
		description := k.description + " " + k.who + " the project's members with Reporter access or more, never a bot, unless anyone is true."
		payload := map[string]any{"issue": issueSchema}
		for name, v := range k.payload {
			payload[name] = v
		}
		out[i] = event{Name: "issue." + string(k.kind), Title: k.title, Description: description, Input: input, Payload: object(payload)}
	}
	return out
}

func eventNamed(name string) (event, bool) { return mcpevents.Named(events, name) }

// EventNames is the events it declares, by name, in events/list's order.
func EventNames() []string { return mcpevents.Names(events) }

// eventsList is events/list's answer.
func eventsList() map[string]any { return mcpevents.List(events) }

// MARK: The cursor

// cursor is where a poll has got to: a high-water time, and the ids already raised at exactly
// that time, so events that share one time are neither lost nor raised twice. A client keeps
// it as an opaque string (base64url of its JSON).
type cursor = mcpevents.Cursor

func decodeCursor(raw json.RawMessage) (cursor, *rpcError) { return polls.Decode(raw) }

// MARK: Polling

// candidate is something that may be raised: its id, its time (in mcpevents.TimeLayout), and its data,
// looked up only once it is taken; nil data skips it, though the cursor still moves past it.
type candidate = mcpevents.Candidate

const (
	maxEvents  = mcpevents.MaxEvents
	nextPollMs = 30000
)

type pollParams = mcpevents.Params

// polls is how it polls, with GitLab's times and errors.
var polls = mcpevents.Poller{NextPollMs: nextPollMs, Normalize: mcpevents.Normalized, Fail: eventsError}

// Events answers events/list and events/poll over a GitLab.
type Events struct {
	GitLab GitLab
	Now    func() time.Time
}

func (s *Events) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Poll answers events/poll's params.
func (s *Events) Poll(ctx context.Context, raw json.RawMessage) (map[string]any, *rpcError) {
	p, bad := mcpevents.ParseParams(raw)
	if bad != nil {
		return nil, bad
	}
	e, ok := eventNamed(p.Name)
	if !ok {
		return nil, mcpevents.NoEvent(p.Name, EventNames())
	}
	a, bad := check(e.Input, p.Arguments)
	if bad != nil {
		return nil, bad
	}
	if bad := s.defaultProject(ctx, &a); bad != nil {
		return nil, bad
	}
	yours, bad := s.yoursFor(ctx, e.Name, a)
	if bad != nil {
		return nil, bad
	}
	if key, states := s.diffStates(ctx, e.Name, a, p, yours); states != nil {
		return s.pollDiff(e.Name, key, a, p, states)
	}
	return s.poll(e, p, func(since string) ([]candidate, Listed, error) {
		return candidates(ctx, s.GitLab, e.Name, a, yours, since)
	})
}

// defaultProject sets a's project to the default project when a names none.
func (s *Events) defaultProject(ctx context.Context, a *args) *rpcError {
	if a.project != "" {
		return nil
	}
	project, err := s.GitLab.DefaultProject(ctx)
	if err != nil {
		var gl *GitLabError
		if errors.As(err, &gl) && gl.Kind == NotFound {
			return invalidParams("%s", gl.Message)
		}
		return eventsError(err)
	}
	a.project = project
	return nil
}

// yoursFor is the connection's owner's own open merge requests, asked once a poll (#2034),
// for mine and for the events that are about the owner; else nil.
func (s *Events) yoursFor(ctx context.Context, name string, a args) (*Yours, *rpcError) {
	if !a.mine && name != "review.requested" && name != "issue.assigned" {
		return nil, nil
	}
	y, err := s.GitLab.Yours(ctx, a.project)
	if err != nil {
		return nil, eventsError(err)
	}
	return &y, nil
}

// diffStates is, for an event raised when a state turns on (pollDiff), its time key and how
// to read the states now; nil states for an event that polls candidates instead.
func (s *Events) diffStates(ctx context.Context, name string, a args, p pollParams, yours *Yours) (string, func() ([]state, error)) {
	switch name {
	case "review.requested":
		return "requestedAt", func() ([]state, error) { return requestedStates(yours), nil }
	case "issue.assigned":
		return "assignedAt", func() ([]state, error) { return assignedStates(a, yours), nil }
	case "pr.conflicted":
		return "conflictedAt", func() ([]state, error) {
			mrs, err := s.GitLab.OpenMRs(ctx, a.project)
			return conflictedStates(a, yours, mrs), err
		}
	case "pr.blocked":
		was := polls.CursorIDs(p)
		return "blockedAt", func() ([]state, error) {
			mrs, err := s.GitLab.OpenMRs(ctx, a.project)
			return blockedStates(name, a, yours, mrs, was), err
		}
	case "pr.opened":
		return "openedAt", func() ([]state, error) {
			mrs, err := s.GitLab.OpenMRs(ctx, a.project)
			return openedStates(a, mrs), err
		}
	case "pr.reviewed":
		return "reviewedAt", func() ([]state, error) {
			reviews, err := s.GitLab.Reviews(ctx, a.project)
			return reviewedStates(a, yours, reviews), err
		}
	}
	return "", nil
}

// requestedStates is each merge request the owner is asked to review, with its author.
func requestedStates(yours *Yours) []state {
	var out []state
	for _, r := range yours.Reviews {
		j := r.MR.json()
		j["author"] = r.Author
		out = append(out, state{mr: r.MR, on: new(true), json: j})
	}
	return out
}

// assignedStates is each issue assigned to the owner that a wants.
func assignedStates(a args, yours *Yours) []state {
	var out []state
	for _, i := range yours.Assigned {
		if a.wantsIssue(i.Number) {
			out = append(out, state{mr: MR{Number: i.Number, Title: i.Title, URL: i.URL}, on: new(true), json: i.json()})
		}
	}
	return out
}

// conflictedStates is whether each open merge request a wants is in conflict: with mine, the
// owner's own; else the trusted ones, or all with anyone.
func conflictedStates(a args, yours *Yours, mrs []OpenMR) []state {
	var out []state
	for _, m := range mrs {
		if a.mine {
			if a.byMe(m.MR, yours) {
				out = append(out, state{mr: m.MR, on: m.Conflicts, json: m.MR.json()})
			}
		} else if a.anyone || m.Trusted {
			out = append(out, state{mr: m.MR, on: m.Conflicts, json: m.MR.json()})
		}
	}
	return out
}

// blockedStates is whether each open merge request a wants is blocked, and by what (#5321);
// was is the ids the cursor had.
func blockedStates(name string, a args, yours *Yours, mrs []OpenMR, was []string) []state {
	var out []state
	for _, m := range mrs {
		if m.SkipsBlocked || (a.mine && !a.byMe(m.MR, yours)) || (!a.mine && !a.anyone && !m.Trusted) {
			continue
		}
		bs := mcpevents.Blocked(fmt.Sprintf("%s:%s:%d", name, a.project, m.MR.Number), m.Blocking, was)
		out = append(out, state{mr: m.MR, on: &bs.On, key: bs.Key, json: m.MR.json(), extra: bs.Data})
	}
	return out
}

// openedStates is whether each open merge request a wants is ready (not a draft), with its author.
func openedStates(a args, mrs []OpenMR) []state {
	var out []state
	for _, m := range mrs {
		if !a.anyone && !m.Trusted {
			continue
		}
		ready := !m.Draft
		j := m.MR.json()
		j["author"] = m.Author
		out = append(out, state{mr: m.MR, on: &ready, json: j})
	}
	return out
}

// reviewedStates is pr.reviewed's (#6296): each answer on an open merge request a wants, on
// while it stands, keyed by its author and state. Trust is the merge request's and the
// reviewer's; mine narrows the merge request.
func reviewedStates(a args, yours *Yours, reviews []Review) []state {
	var out []state
	for _, r := range reviews {
		if (a.mine && !a.byMe(r.MR, yours)) || (!a.mine && !a.anyone && !r.MRTrusted) || (!a.anyone && !r.Trusted) {
			continue
		}
		out = append(out, state{mr: r.MR, on: new(true), key: r.Author + ":" + r.State, json: r.MR.json(),
			extra: map[string]any{"review": map[string]any{"author": r.Author, "state": r.State, "body": "", "url": r.MR.URL, "commit": r.HeadSHA}}})
	}
	return out
}

// poll is events/poll over a cursor (mcpevents.Poller.Poll), from now on the first poll.
func (s *Events) poll(e event, p pollParams, find func(since string) ([]candidate, Listed, error)) (map[string]any, *rpcError) {
	return polls.Poll(e.Name, p, func() string { return mcpevents.TimeString(s.now()) },
		func(since string) ([]candidate, mcpevents.Listed, error) {
			found, listed, err := find(since)
			return found, mcpevents.Listed{Truncated: listed.Truncated, Newest: listed.Newest}, err
		})
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }

// state is a merge request and whether its state (in conflict, or open and ready) is on now;
// nil keeps the one it had.
// key, when set, is part of its id and its envelope's key; extra is more of the event's data
// (pr.blocked's, #5321).
type state struct {
	mr    MR
	on    *bool
	json  map[string]any
	key   string
	extra map[string]any
}

func (s *Events) pollDiff(name, key string, a args, p pollParams, states func() ([]state, error)) (map[string]any, *rpcError) {
	root := "pr"
	if strings.HasPrefix(name, "issue.") {
		root = "issue" // issue.assigned (#2034)
	}
	return polls.Diff(name, key, p, func() string { return mcpevents.TimeString(s.now()) }, func() ([]mcpevents.State, error) {
		all, err := states()
		var out []mcpevents.State
		for _, st := range all {
			if !a.wantsPR(st.mr.Number) || (a.branch != "" && st.mr.Branch != a.branch) || !a.closesOne(st.mr) {
				continue
			}
			id := fmt.Sprintf("%s:%s:%d", name, a.project, st.mr.Number)
			if st.key != "" {
				id += ":" + st.key
			}
			data := map[string]any{root: st.json}
			for k, v := range st.extra {
				data[k] = v
			}
			out = append(out, mcpevents.State{ID: id, Number: st.mr.Number,
				On: st.on, Data: data, Subject: diffRef(root, a.project, st.mr.Number), Key: st.key})
		}
		return out, err
	})
}

// The refs of what gitlab's events happen to (#4728, spec 4724-event-bus, "A ref").
func prRef(project string, n int) wire.Ref {
	return mcpevents.Ref("gitlab", "mr", project, fmt.Sprint(n))
}
func pipelineRef(project string, id int) wire.Ref {
	return mcpevents.Ref("gitlab", "pipeline", project, fmt.Sprint(id))
}
func issueRef(project string, n int) wire.Ref {
	return mcpevents.Ref("gitlab", "issue", project, fmt.Sprint(n))
}

// diffRef is a state's subject: its issue for an issue's event, else its mr.
func diffRef(root, project string, n int) wire.Ref {
	if root == "issue" {
		return issueRef(project, n)
	}
	return prRef(project, n)
}

// candidates finds what an event may raise at or after since.
func candidates(ctx context.Context, gl GitLab, name string, a args, yours *Yours, since string) ([]candidate, Listed, error) {
	f := finder{ctx: ctx, gl: gl, name: name, a: a, yours: yours, project: a.project, since: since}
	if kind, ok := strings.CutPrefix(name, "issue."); ok {
		return f.issueActivity(IssueKind(kind))
	}
	if mcpevents.IsDeployEvent(name) {
		deploys, listed, err := gl.Deployments(ctx, f.project, a.deploy.Environment, since)
		if err != nil {
			return nil, Listed{}, err
		}
		return mcpevents.DeployCandidates(name, a.deploy, deploys), listed, nil
	}
	switch name {
	case "review.comment", "pr.commented":
		return f.mrComments()
	case "run.completed":
		return f.completedRuns()
	case "deploy.waiting":
		return f.waitingDeploys()
	case "checks.failed", "checks.passed":
		return f.pipelineChecks()
	}
	return f.closedMRs()
}

// finder is one poll's search for candidates: the event, its arguments and the cursor's time.
type finder struct {
	ctx     context.Context
	gl      GitLab
	name    string
	a       args
	yours   *Yours
	project string
	since   string
}

// wanted is whether a merge request is one the arguments want. mine: the owner's own merge
// request, open or merged or closed, trusted or not; else the trust filter (#728, #3797).
// fixes narrows every one by the issues it closes.
func (f finder) wanted(m MR, trusted bool) bool {
	a := f.a
	return a.wantsPR(m.Number) && (a.branch == "" || m.Branch == a.branch) && a.closesOne(m) &&
		(a.mine && a.byMe(m, f.yours) || !a.mine && (a.anyone || trusted))
}

// wantsActivity is whether an issue's activity is one the arguments want.
func (f finder) wantsActivity(it IssueActivity) bool {
	a := f.a
	if !a.wantsIssue(it.Issue.Number) || (!a.anyone && !it.Trusted) {
		return false
	}
	return a.label == "" || (contains(it.Issue.Labels, a.label) && (it.Kind != IssueLabelled || it.Label == a.label))
}

func (f finder) issueActivity(kind IssueKind) ([]candidate, Listed, error) {
	items, listed, err := f.gl.IssueActivity(f.ctx, f.project, kind, f.since)
	if err != nil {
		return nil, Listed{}, err
	}
	var list []candidate
	for _, it := range items {
		if !f.wantsActivity(it) {
			continue
		}
		at := mcpevents.Normalized(it.At)
		if at == "" {
			continue
		}
		list = append(list, candidate{EventID: f.name + ":" + f.project + ":" + it.ID, Time: at,
			Data:    func() (map[string]any, error) { return it.data(), nil },
			Subject: issueRef(f.project, it.Issue.Number), Key: it.ID})
	}
	return list, listed, nil
}

// mrComments are review.comment's comments on lines, or pr.commented's on the merge requests
// themselves (#6296), but an agent's.
func (f finder) mrComments() ([]candidate, Listed, error) {
	a := f.a
	read, data := f.gl.ReviewComments, ReviewComment.data
	if f.name == "pr.commented" {
		read, data = f.gl.MRComments, ReviewComment.prData
	}
	comments, listed, err := read(f.ctx, f.project, f.since)
	if err != nil {
		return nil, Listed{}, err
	}
	var list []candidate
	for _, c := range comments {
		// Trust is the commenter's; mine only narrows the merge request (#3797).
		m := c.MR
		if at := mcpevents.Normalized(c.At); at != "" && a.wantsPR(m.Number) && (a.branch == "" || m.Branch == a.branch) &&
			a.closesOne(m) && a.byMe(m, f.yours) && (a.anyone || c.Trusted) && (f.name != "pr.commented" || !mcpevents.ByAgent(c.Body)) {
			list = append(list, candidate{EventID: fmt.Sprintf("%s:%s:%d", f.name, f.project, c.ID), Time: at,
				Data:    func() (map[string]any, error) { return data(c), nil },
				Subject: prRef(f.project, m.Number), Key: fmt.Sprint(c.ID)})
		}
	}
	return list, listed, nil
}

// listedOtherPipeline is whether a pipeline's listed name says it is not the one asked for.
func (f finder) listedOtherPipeline(listed string) bool {
	return f.a.pipeline != "" && listed != "" && !strings.EqualFold(listed, f.a.pipeline)
}

// completedPipeline is a pipeline's details, or nil when it is not one the arguments want.
func (f finder) completedPipeline(id int) (*CompletedPipeline, error) {
	c, err := f.gl.CompletedPipeline(f.ctx, f.project, id)
	if err != nil || c == nil || (f.a.pipeline != "" && !strings.EqualFold(c.Name, f.a.pipeline)) || (!f.a.anyone && !c.Trusted) {
		return nil, err
	}
	return c, nil
}

func (f finder) completedRuns() ([]candidate, Listed, error) {
	pipelines, listed, err := f.gl.FinishedPipelines(f.ctx, f.project, f.a.branch, f.a.run, f.since)
	if err != nil {
		return nil, Listed{}, err
	}
	// Its update time in the id: a pipeline retried that finishes again is a new event. Who
	// started it, its name and its deploys are read only for one that is taken.
	var list []candidate
	for _, pl := range pipelines {
		at := mcpevents.Normalized(pl.UpdatedAt)
		if at == "" || f.listedOtherPipeline(pl.Listed) {
			continue
		}
		list = append(list, candidate{EventID: fmt.Sprintf("run.completed:%s:%d:%s", f.project, pl.ID, at), Time: at,
			Subject: pipelineRef(f.project, pl.ID), Key: at,
			Data: func() (map[string]any, error) {
				c, err := f.completedPipeline(pl.ID)
				if c == nil {
					return nil, err
				}
				deploys, err := f.gl.PipelineDeploys(f.ctx, f.project, *c)
				if err != nil {
					return nil, err
				}
				return c.data(deploys), nil
			}})
	}
	return list, listed, nil
}

func (f finder) waitingDeploys() ([]candidate, Listed, error) {
	waiting, err := f.gl.WaitingDeploys(f.ctx, f.project, f.a.branch)
	if err != nil {
		return nil, Listed{}, err
	}
	var list []candidate
	for _, w := range waiting {
		if w.Since == "" || f.listedOtherPipeline(w.Pipeline.Listed) {
			continue
		}
		list = append(list, candidate{EventID: fmt.Sprintf("deploy.waiting:%s:%d:%s", f.project, w.Pipeline.ID, w.key()), Time: w.Since,
			Subject: pipelineRef(f.project, w.Pipeline.ID), Key: w.key(),
			Data: func() (map[string]any, error) { return f.waitingDeployData(w) }})
	}
	return list, Listed{}, nil
}

// waitingDeployData is deploy.waiting's data for w, or nil when its pipeline is not one the
// arguments want.
func (f finder) waitingDeployData(w WaitingDeploy) (map[string]any, error) {
	c, err := f.completedPipeline(w.Pipeline.ID)
	if c == nil {
		return nil, err
	}
	reviewers, canApprove, err := f.gl.Approvers(f.ctx, f.project, w)
	if err != nil {
		return nil, err
	}
	envs := make([]any, len(w.Environments))
	for i, e := range w.Environments {
		envs[i] = map[string]any{"id": e.ID, "name": e.Name, "url": e.URL}
	}
	return map[string]any{"run": c.run(), "environments": envs, "reviewers": reviewers,
		"canApprove": canApprove, "waitingSince": w.Since}, nil
}

// pipelineChecks are checks.failed's failed pipelines, or checks.passed's that passed (#6296).
func (f finder) pipelineChecks() ([]candidate, Listed, error) {
	read, data := f.gl.FailedPipelines, FailedPipelineData.json
	if f.name == "checks.passed" {
		read, data = f.gl.PassedPipelines, FailedPipelineData.passedJSON
	}
	pipelines, listed, err := read(f.ctx, f.project, f.since)
	if err != nil {
		return nil, Listed{}, err
	}
	// A pipeline that names another merge request is skipped before its details are
	// fetched; one that names none is checked against the one its details find. The cursor
	// still moves past both. Its update time is in its id: retried and failed again, it is new.
	var list []candidate
	for _, pl := range pipelines {
		at := mcpevents.Normalized(pl.UpdatedAt)
		if at == "" {
			continue
		}
		list = append(list, candidate{EventID: fmt.Sprintf("%s:%s:%d:%s", f.name, f.project, pl.ID, at), Time: at,
			Subject: pipelineRef(f.project, pl.ID), Key: at,
			Data: func() (map[string]any, error) {
				if pl.MRNumber != 0 && !f.a.wantsPR(pl.MRNumber) {
					return nil, nil
				}
				details, err := f.gl.PipelineDetails(f.ctx, f.project, pl)
				if err != nil || details == nil {
					return nil, err
				}
				if !f.wanted(details.MR, details.Trusted) {
					return nil, nil
				}
				return data(*details), nil
			}})
	}
	return list, listed, nil
}

// closedMRs is pr.merged's or pr.closed's candidates.
func (f finder) closedMRs() ([]candidate, Listed, error) {
	merged, closed, listed, err := f.gl.ClosedMRs(f.ctx, f.project, f.since)
	if err != nil {
		return nil, Listed{}, err
	}
	if f.name == "pr.merged" {
		return f.mergedCandidates(merged), listed, nil
	}
	return f.closedCandidates(closed), listed, nil
}

func (f finder) mergedCandidates(merged []MergedMR) []candidate {
	var list []candidate
	for _, m := range merged {
		if at := mcpevents.Normalized(m.MergedAt); f.wanted(m.MR, m.Trusted) && at != "" {
			list = append(list, candidate{EventID: fmt.Sprintf("pr.merged:%s:%d", f.project, m.MR.Number), Time: at,
				Subject: prRef(f.project, m.MR.Number),
				Data: func() (map[string]any, error) {
					return map[string]any{"pr": m.MR.json(), "mergedAt": m.MergedAt, "mergeSha": m.MergeSHA}, nil
				}})
		}
	}
	return list
}

// closedCandidates has its close time in the id: a merge request reopened and closed again is
// a new event.
func (f finder) closedCandidates(closed []ClosedMR) []candidate {
	var list []candidate
	for _, c := range closed {
		if at := mcpevents.Normalized(c.ClosedAt); f.wanted(c.MR, c.Trusted) && at != "" {
			list = append(list, candidate{EventID: fmt.Sprintf("pr.closed:%s:%d:%s", f.project, c.MR.Number, at), Time: at,
				Subject: prRef(f.project, c.MR.Number), Key: at,
				Data: func() (map[string]any, error) {
					return map[string]any{"pr": c.MR.json(), "closedAt": c.ClosedAt}, nil
				}})
		}
	}
	return list
}

// eventsError is a GitLab failure as the events draft's error codes.
func eventsError(err error) *rpcError {
	var rpc *rpcError
	if errors.As(err, &rpc) {
		return rpc
	}
	var gl *GitLabError
	if !errors.As(err, &gl) {
		return &rpcError{Code: -32603, Message: err.Error()}
	}
	switch gl.Kind {
	case NotSignedIn:
		return &rpcError{Code: -32012, Message: gl.Message, Data: map[string]any{"reason": "not connected"}}
	case RateLimited:
		return &rpcError{Code: -32013, Message: gl.Message, Data: map[string]any{"retryAfterMs": gl.RetryAfterMs}}
	case NotFound:
		return invalidParams("%s: check project", gl.Message)
	}
	return &rpcError{Code: -32603, Message: gl.Message}
}
