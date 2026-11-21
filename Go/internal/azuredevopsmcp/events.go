package azuredevopsmcp

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

// The MCP events (#2829) of an Azure DevOps repository's pull requests and their Azure
// Pipelines builds, in the gitlab server's shape (Go/internal/gitlabmcp/events.go): the same
// names where they mean the same, and the same cursor, paging and trust filter (#728), polled
// only: `events/list` and `events/poll` of the events draft (experimental-ext-triggers-events),
// which the Agents daemon speaks. Work items are not here: they are the tracker's, as Linear's
// and Jira's issues are.

// event is one event as events/list gives it.
type event = mcpevents.Event

// repoPattern is a repository's organization/project/name. A project's name may have spaces.
var repoPattern = regexp.MustCompile(`^[^/\s][^/]*/[^/\s][^/]*/[^/\s][^/]*$`)

var (
	repoProperty = property{Name: "repo", Kind: "string",
		Description: "The repository on Azure DevOps, as organization/project/name (collection/project/name on Azure DevOps Server). Left out, the repository this folder pushes to.",
		Pattern:     repoPattern, LooksLike: "organization/project/name"}
	prProperty = property{Name: "pr", Kind: "integers", Description: "Only this pull request, or these, by number."}
	// anyoneProperty opts into anyone's comment or pull request (#728); left out, only the project's own people.
	anyoneProperty = property{Name: "anyone", Kind: "boolean",
		Description: "true: from anyone, bots and forks too. Left out, never a bot (a build service) and for a pull request never one from a fork. Only a project's members can write to it on Azure DevOps."}
	// mineProperty is only the token's owner's own pull requests (#2034), a fork's too.
	mineProperty = property{Name: "mine", Kind: "boolean",
		Description: "true: only pull requests by the Azure DevOps account whose token this server has, whatever anyone says."}
	// anyProperty is every one in the repository (#3797): a wait that names none sets it.
	anyProperty = property{Name: "any", Kind: "boolean",
		Description: "true: every one in the repository. A wait (not a workflow trigger) that names none, by the other arguments that say which, is refused without it. It is about which ones; anyone is about who."}
	// fixesProperty is the pull requests that close these work items (#3797): a lead knows a child's work item before its pull request.
	fixesProperty = property{Name: "fixes", Kind: "refs",
		Description: `Only pull requests whose description closes one of these work items, as "#12" (AB#12 in a description is #12).`,
		Pattern:     workItemRefPattern, LooksLike: `work items, as "#12"`}
)

// ownPRs is what every pull request event says of whose pull requests it raises.
const ownPRs = " Only pull requests from a branch of the repository itself, not a fork, and never by a bot, unless anyone is true."

func branchProperty(description string) property {
	return property{Name: "branch", Kind: "string", Description: description}
}

func object(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

var str, integer = map[string]any{"type": "string"}, map[string]any{"type": "integer"}

func prSchema(extra map[string]any) map[string]any {
	props := map[string]any{"number": integer, "title": str, "branch": str, "url": str, "fixes": fixesSchema}
	for k, v := range extra {
		props[k] = v
	}
	return object(props)
}

func prInput() schema {
	return schema{repoProperty, branchProperty("Only the pull request from this branch."), prProperty, anyoneProperty}
}

// namedInput is prInput for an event about one pull request: it names it by fixes or mine
// too, or takes any (#3797).
func namedInput() schema {
	return schema{repoProperty, branchProperty("Only the pull request from this branch."), prProperty, fixesProperty,
		anyoneProperty, mineProperty, anyProperty}
}

// fixesSchema is a pull request's fixes: the work items its description closes, "#12" (#2349).
var fixesSchema = map[string]any{"type": "array", "items": str}

// events are the events this server offers, in the gitlab server's order, and checks.passed,
// run.completed, deploy.waiting, pr.reviewed and pr.commented as the github server's (#3805, #3810, #3820, #6295); the gitlab server's issue events
// are the tracker's here.
var events = []event{
	{Name: "checks.failed", Title: "Checks failed",
		Description: "An open pull request's checks failed at its head commit: an Azure Pipelines build of it (its build validation, or any pipeline it triggers), a build of its branch at that commit (a CI trigger's), or the newest status that another service set on it (failed or error). One event per commit and failure time, so a build run again that fails again is a new one. failedJobs are the failed builds and statuses; run is the first failed build (get_build_log reads its failed steps), or the first failed status with id 0 when no build failed." + ownPRs,
		Input:       schema{repoProperty, branchProperty("Only checks of the pull request from this branch."), prProperty, fixesProperty, anyoneProperty, mineProperty, anyProperty},
		Payload: object(map[string]any{
			"pr": prSchema(nil), "headSha": str,
			"run":        object(map[string]any{"id": integer, "name": str, "url": str}),
			"failedJobs": map[string]any{"type": "array", "items": object(map[string]any{"name": str, "url": str})},
		})},
	{Name: "checks.passed", Title: "Checks passed",
		Description: "One of an open pull request's checks passed at its head commit: an Azure Pipelines build of it or of its branch at that commit, or the newest status of one context that another service set on it. One event per build or status, so a lane waiting on CI going green wakes without polling. run is the build (id 0 for a status)." + ownPRs,
		Input:       schema{repoProperty, branchProperty("Only checks of the pull request from this branch."), prProperty, fixesProperty, anyoneProperty, mineProperty, anyProperty},
		Payload: object(map[string]any{
			"pr": prSchema(nil), "headSha": str,
			"run": object(map[string]any{"id": integer, "name": str, "url": str}),
		})},
	// A build no pull request owns, as main's or a tag's, as the github server's (#3444, #3810).
	{Name: "run.completed", Title: "Pipeline build finished",
		Description: "An Azure Pipelines build of the repository finished, whatever started it (a push, a tag, a person, a schedule) and whatever its result (succeeded, failed, canceled, partiallySucceeded): one event per build and finish time, so a build run again is a new one. For a build no pull request owns, such as main's or a tag's release; a pull request's own builds are checks.failed and checks.passed. run.name is its pipeline's name, run.number its build number, run.event what started it (manual, individualCI, batchedCI, schedule, pullRequest…). Only builds asked for by a person, never a bot (a build service), and never a fork's pull request's, unless anyone is true. deploys is what its jobs say they deployed: the message of each warning with the code deploy, <target>:<id>/<version>, as testflight:<bundle id>/<build> from ##vso[task.logissue type=warning;code=deploy]testflight:com.example.app/42.",
		Input: schema{repoProperty,
			property{Name: "pipeline", Kind: "string", Description: "Only builds of this pipeline, by its name, as release."},
			branchProperty("Only builds of this branch or tag, as main or v0.16.0."),
			property{Name: "run", Kind: "integer", Description: "Only this build, by its id."}, anyoneProperty, anyProperty},
		Payload: object(map[string]any{
			"run": object(map[string]any{"id": integer, "name": str, "number": str, "event": str, "branch": str, "headSha": str,
				"url": str, "conclusion": str, "actor": str}),
			"completedAt": str,
			// What the build deployed, as its jobs say with ##vso[task.logissue type=warning;code=deploy]<target>:<id>/<version> (#3820).
			"deploys": map[string]any{"type": "array", "items": str},
		})},
	// A deploy held by an environment's approvals, as the github server's (#3587, #3820).
	{Name: "deploy.waiting", Title: "Deploy waiting for approval",
		Description: "An Azure Pipelines build is waiting for an approver of an environment's Approvals check, as a release pipeline's deployment job to production: one event per build and set of pending approvals, so the next stage it waits on is a new one. Each of environments is one pending approval, by its id, named by the stage it holds. reviewers are its approvers that have not answered; canApprove says whether the Azure DevOps account whose token this server has may answer every one. Answer it on Azure DevOps. Only builds asked for by a person, never a bot (a build service), and never a fork's pull request's, unless anyone is true.",
		Input: schema{repoProperty,
			property{Name: "pipeline", Kind: "string", Description: "Only builds of this pipeline, by its name, as release."},
			branchProperty("Only builds of this branch or tag, as main or v0.16.0."), anyoneProperty},
		Payload: object(map[string]any{
			"run": object(map[string]any{"id": integer, "name": str, "number": str, "event": str, "branch": str, "headSha": str,
				"url": str, "actor": str}),
			"environments": map[string]any{"type": "array", "items": object(map[string]any{"id": str, "name": str, "url": str})},
			"reviewers":    map[string]any{"type": "array", "items": str},
			"canApprove":   map[string]any{"type": "boolean"},
			"waitingSince": str,
		})},
	deployEvent("deploy.started", "Deploy started",
		"An Azure Pipelines deployment job began to deploy to an environment: its deployment record has a start. One event per record."),
	deployEvent("deploy.succeeded", "Deploy succeeded",
		"An Azure Pipelines deployment job deployed to an environment: its record's result is succeeded or succeededWithIssues. One event per record."),
	deployEvent("deploy.failed", "Deploy failed",
		"An Azure Pipelines deployment job did not deploy to an environment: its record's result is failed or canceled. One event per record. outcome is cancelled when it was canceled, else failed."),
	{Name: "pr.opened", Title: "Pull request opened",
		Description: "A pull request was opened, or a draft was published: a pull request active and not a draft now that was not at the last poll. Drafts never count. Reactivated, it is raised again." + ownPRs,
		Input:       prInput(),
		Payload:     object(map[string]any{"pr": prSchema(map[string]any{"author": str}), "openedAt": str})},
	{Name: "pr.merged", Title: "Pull request merged", Description: "A pull request was completed: merged into its target." + ownPRs,
		Input:   namedInput(),
		Payload: object(map[string]any{"pr": prSchema(nil), "mergedAt": str, "mergeSha": str})},
	{Name: "pr.closed", Title: "Pull request closed", Description: "A pull request was abandoned: closed without being merged." + ownPRs,
		Input:   namedInput(),
		Payload: object(map[string]any{"pr": prSchema(nil), "closedAt": str})},
	{Name: "pr.conflicted", Title: "Pull request in conflict",
		Description: "An active pull request began to conflict with its target branch, so it cannot merge until it is rebased. Once per conflict: it is raised again only after it merges cleanly and then conflicts anew." + ownPRs,
		Input:       namedInput(),
		Payload:     object(map[string]any{"pr": prSchema(nil), "conflictedAt": str})},
	{Name: "pr.blocked", Title: "Pull request blocked",
		Description: mcpevents.BlockedDescription("On Azure DevOps: conflict from mergeStatus; changes_requested from a reviewer's vote of waiting for author or rejected; and required_check_failed, the newest build of a pipeline at the head that failed, or the newest status of a context that failed or is error (Azure DevOps says which builds a branch policy requires only per pull request, so every one counts, as for checks.failed). Read for the 50 newest active pull requests. Auto-complete is not read, so never automerge_disabled or behind." + ownPRs),
		Input:       namedInput(),
		Payload:     mcpevents.BlockedPayload(prSchema(nil))},
	// The github server's pr.reviewed and pr.commented (#6295).
	{Name: "pr.reviewed", Title: "Pull request reviewed",
		Description: "A reviewer of an active pull request voted: approved or approved with suggestions (state approved), or waiting for author or rejected (changes_requested). Azure DevOps keeps only each reviewer's last vote, with no words: so body is always empty, url is the pull request's, commit is its head when the poll saw the vote, and reviewedAt is that poll's time. Once per vote: a vote reset by a new push and given again is a new one. Never a bot's vote, unless anyone is true." + ownPRs,
		Input:       namedInput(),
		Payload: object(map[string]any{"pr": prSchema(nil),
			"review": object(map[string]any{"author": str, "state": str, "body": str, "url": str, "commit": str}), "reviewedAt": str})},
	{Name: "pr.commented", Title: "Pull request commented on",
		Description: "A comment was left on a pull request itself, in a thread on no file (not on a line of its diff, which review.comment covers), or an answer in such a thread. Its body is the first 4000 characters; comment.threadId is the thread to answer in with reply_to_pull_request_comment. A comment whose body starts with <!-- agent --> is left out: an agent's, not a person's, where agents post as the person's account. The 50 newest active pull requests are read, and those closed since. Never a bot's comment, unless anyone is true; and only on the repository's own pull requests.",
		Input:       schema{repoProperty, prProperty, fixesProperty, anyoneProperty, mineProperty, anyProperty},
		Payload: object(map[string]any{"pr": prSchema(nil),
			"comment": object(map[string]any{"author": str, "url": str, "body": str, "threadId": integer}), "commentedAt": str})},
	// For the person, not the repository (#2034): what asks the token's owner for something.
	{Name: "review.requested", Title: "Review requested from me",
		Description: "The Azure DevOps account whose token this server has was added as a reviewer of an active pull request and has not voted: once per request, so a vote reset by a new push is a new one. Anyone's pull request, a fork's too.",
		Input:       schema{repoProperty, prProperty},
		Payload:     object(map[string]any{"pr": prSchema(map[string]any{"author": str}), "requestedAt": str})},
	{Name: "review.comment", Title: "Pull request line commented on",
		Description: "Someone left a comment on a line of a pull request's diff, or answered in its thread. Its body is the first 4000 characters; comment.threadId is the thread to answer in with reply_to_pull_request_comment. The 50 newest active pull requests are read, and those closed since. Never a bot's comment, unless anyone is true; and only on the repository's own pull requests.",
		Input:       namedInput(),
		Payload: object(map[string]any{"pr": prSchema(nil), "commentedAt": str,
			"comment": object(map[string]any{"id": integer, "threadId": integer, "author": str, "path": str, "line": integer, "body": str, "url": str})})},
}

func eventNamed(name string) (event, bool) { return mcpevents.Named(events, name) }

// EventNames is the events it declares, by name, in events/list's order.
func EventNames() []string { return mcpevents.Names(events) }

// eventsList is events/list's answer.
func eventsList() map[string]any { return mcpevents.List(events) }

// MARK: Polling

// candidate is something that may be raised: its id, its time (in mcpevents.TimeLayout), and its data,
// looked up only once it is taken; nil data skips it, though the cursor still moves past it.
type candidate = mcpevents.Candidate

const (
	maxEvents  = mcpevents.MaxEvents
	nextPollMs = 30000
)

type pollParams = mcpevents.Params

// polls is how it polls, with Azure DevOps's times and errors.
var polls = mcpevents.Poller{NextPollMs: nextPollMs, Normalize: mcpevents.Normalized, Fail: eventsError}

// Events answers events/list and events/poll over an Azure DevOps.
type Events struct {
	AzureDevOps AzureDevOps
	Now         func() time.Time
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
	if bad := s.defaultRepo(ctx, &a); bad != nil {
		return nil, bad
	}
	yours, bad := s.yoursFor(ctx, e.Name, a)
	if bad != nil {
		return nil, bad
	}
	if key, states := s.prStates(ctx, e.Name, a, p, yours); states != nil {
		return s.pollDiff(e.Name, key, a, p, states)
	}
	return s.poll(e, p, func(since string) ([]candidate, Listed, error) {
		return candidates(ctx, s.AzureDevOps, e.Name, a, yours, since)
	})
}

// defaultRepo sets a's repository to the default one when it names none.
func (s *Events) defaultRepo(ctx context.Context, a *args) *rpcError {
	if a.repo != "" {
		return nil
	}
	repo, err := s.AzureDevOps.DefaultRepo(ctx)
	if err != nil {
		var ae *AzureDevOpsError
		if errors.As(err, &ae) && ae.Kind == NotFound {
			return invalidParams("%s", ae.Message)
		}
		return eventsError(err)
	}
	a.repo = repo
	return nil
}

// yoursFor is the token's owner's own open pull requests, asked once a poll (#2034), when
// the event needs them (mine, or review.requested); else nil.
func (s *Events) yoursFor(ctx context.Context, name string, a args) (*Yours, *rpcError) {
	if !a.mine && name != "review.requested" {
		return nil, nil
	}
	y, err := s.AzureDevOps.Yours(ctx, a.repo)
	if err != nil {
		return nil, eventsError(err)
	}
	return &y, nil
}

// prStates is how an event that diffs pull requests' states reads them, with the name of
// the time its data has; nil for an event that does not.
func (s *Events) prStates(ctx context.Context, name string, a args, p pollParams, yours *Yours) (string, func() ([]state, error)) {
	switch name {
	case "review.requested":
		return "requestedAt", func() ([]state, error) { return requestedStates(yours), nil }
	case "pr.blocked":
		was := polls.CursorIDs(p)
		return "blockedAt", func() ([]state, error) { return s.blockedStates(ctx, name, a, yours, was) }
	case "pr.conflicted":
		return "conflictedAt", func() ([]state, error) { return s.conflictedStates(ctx, a, yours) }
	case "pr.opened":
		return "openedAt", func() ([]state, error) { return s.openedStates(ctx, a) }
	case "pr.reviewed":
		return "reviewedAt", func() ([]state, error) { return s.reviewedStates(ctx, a, yours) }
	}
	return "", nil
}

// requestedStates are the pull requests whose review the token's owner was asked for.
func requestedStates(yours *Yours) []state {
	var out []state
	for _, r := range yours.Reviews {
		j := r.PR.json()
		j["author"] = r.Author
		out = append(out, state{mr: r.PR, on: new(true), json: j})
	}
	return out
}

// blockedStates are the open pull requests and whether each is blocked; was are the ids
// the cursor had.
func (s *Events) blockedStates(ctx context.Context, name string, a args, yours *Yours, was []string) ([]state, error) {
	prs, err := s.AzureDevOps.OpenPRs(ctx, a.repo, true)
	var out []state
	for _, m := range prs {
		if m.SkipsBlocked || (a.mine && !a.byMe(m.PR, yours)) || (!a.mine && !a.anyone && !m.Trusted) {
			continue
		}
		bs := mcpevents.Blocked(fmt.Sprintf("%s:%s:%d", name, a.repo, m.PR.Number), m.blocking(), was)
		out = append(out, state{mr: m.PR, on: &bs.On, key: bs.Key, json: m.PR.json(), extra: bs.Data})
	}
	return out, err
}

// conflictedStates are the open pull requests and whether each is in conflict.
func (s *Events) conflictedStates(ctx context.Context, a args, yours *Yours) ([]state, error) {
	prs, err := s.AzureDevOps.OpenPRs(ctx, a.repo, false)
	var out []state
	for _, m := range prs {
		if a.mine {
			if a.byMe(m.PR, yours) {
				out = append(out, state{mr: m.PR, on: m.Conflicts, json: m.PR.json()})
			}
		} else if a.anyone || m.Trusted {
			out = append(out, state{mr: m.PR, on: m.Conflicts, json: m.PR.json()})
		}
	}
	return out, err
}

// openedStates are the open pull requests and whether each is ready (not a draft).
func (s *Events) openedStates(ctx context.Context, a args) ([]state, error) {
	prs, err := s.AzureDevOps.OpenPRs(ctx, a.repo, false)
	var out []state
	for _, m := range prs {
		if !a.anyone && !m.Trusted {
			continue
		}
		ready := !m.Draft
		j := m.PR.json()
		j["author"] = m.Author
		out = append(out, state{mr: m.PR, on: &ready, json: j})
	}
	return out, err
}

// reviewedStates are pr.reviewed's (#6295): each vote on an active pull request the poll
// wants, on while it stands, keyed by its reviewer and state.
func (s *Events) reviewedStates(ctx context.Context, a args, yours *Yours) ([]state, error) {
	prs, err := s.AzureDevOps.OpenPRs(ctx, a.repo, false)
	wanted := wantedPR(a, yours)
	var out []state
	for _, m := range prs {
		if !wanted(m.PR, m.Trusted) {
			continue
		}
		for _, v := range m.Votes {
			if !a.anyone && !v.Trusted {
				continue
			}
			out = append(out, state{mr: m.PR, on: new(true), key: v.Author + ":" + v.State, json: m.PR.json(),
				extra: map[string]any{"review": map[string]any{"author": v.Author, "state": v.State, "body": "", "url": m.PR.URL, "commit": m.HeadSHA}}})
		}
	}
	return out, err
}

func (s *Events) poll(e event, p pollParams, find func(since string) ([]candidate, Listed, error)) (map[string]any, *rpcError) {
	return polls.Poll(e.Name, p, func() string { return mcpevents.TimeString(s.now().Truncate(time.Second)) },
		func(since string) ([]candidate, mcpevents.Listed, error) {
			found, listed, err := find(since)
			return found, mcpevents.Listed{Truncated: listed.Truncated, Newest: listed.Newest}, err
		})
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }

// state is a pull request and whether its state (in conflict, or open and ready) is on now;
// nil keeps the one it had.
// key, when set, is part of its id and its envelope's key; extra is more of the event's data
// (pr.blocked's, #5321).
type state struct {
	mr    PR
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
			id := fmt.Sprintf("%s:%s:%d", name, a.repo, st.mr.Number)
			if st.key != "" {
				id += ":" + st.key
			}
			data := map[string]any{root: st.json}
			for k, v := range st.extra {
				data[k] = v
			}
			out = append(out, mcpevents.State{ID: id, Number: st.mr.Number,
				On: st.on, Data: data, Subject: diffRef(root, a.repo, st.mr.Number), Key: st.key})
		}
		return out, err
	})
}

// The refs of what azuredevops's events happen to (#4728, spec 4724-event-bus, "A ref").
func prRef(repo string, n int) wire.Ref {
	return mcpevents.Ref("azuredevops", "pr", repo, fmt.Sprint(n))
}
func runRef(repo string, id int) wire.Ref {
	return mcpevents.Ref("azuredevops", "run", repo, fmt.Sprint(id))
}

// diffRef is a state's subject: its pull request (no work item state is diffed here).
func diffRef(_ string, repo string, n int) wire.Ref { return prRef(repo, n) }

// candidates finds what an event may raise at or after since.
func candidates(ctx context.Context, f AzureDevOps, name string, a args, yours *Yours, since string) ([]candidate, Listed, error) {
	if mcpevents.IsDeployEvent(name) {
		list, err := deployCandidates(ctx, f, name, a)
		return list, Listed{}, err
	}
	switch name {
	case "run.completed":
		return runCandidates(ctx, f, a, since)
	case "deploy.waiting":
		list, err := waitingCandidates(ctx, f, a)
		return list, Listed{}, err
	case "review.comment", "pr.commented":
		return commentCandidates(ctx, f, name, a, yours, since)
	case "checks.failed":
		return failedCandidates(ctx, f, a, wantedPR(a, yours), since)
	case "checks.passed":
		return passedCandidates(ctx, f, a, wantedPR(a, yours), since)
	}
	return closedCandidates(ctx, f, name, a, wantedPR(a, yours), since)
}

// wantedPR says whether an event of a pull request is wanted. mine: the owner's own pull
// request, active or completed or abandoned, trusted or not; else the trust filter (#728,
// #3797). fixes narrows every one by the work items it closes.
func wantedPR(a args, yours *Yours) func(m PR, trusted bool) bool {
	return func(m PR, trusted bool) bool {
		return a.wantsPR(m.Number) && (a.branch == "" || m.Branch == a.branch) && a.closesOne(m) &&
			(a.mine && a.byMe(m, yours) || !a.mine && (a.anyone || trusted))
	}
}

// runCandidates are the builds that completed at or after since.
func runCandidates(ctx context.Context, f AzureDevOps, a args, since string) ([]candidate, Listed, error) {
	repo := a.repo
	builds, listed, err := f.CompletedBuilds(ctx, repo, a.pipeline, a.branch, a.run, since)
	if err != nil {
		return nil, Listed{}, err
	}
	// Its finish time in the id: a build run again (its failed jobs) is a new event.
	var list []candidate
	for _, b := range builds {
		if b.At != "" && (a.anyone || b.Trusted) {
			list = append(list, candidate{EventID: fmt.Sprintf("run.completed:%s:%d:%s", repo, b.ID, b.At), Time: b.At,
				Subject: runRef(repo, b.ID), Key: b.At,
				Data: func() (map[string]any, error) {
					deploys, err := f.BuildDeploys(ctx, repo, b.ID)
					if err != nil {
						return nil, err
					}
					return b.data(deploys), nil
				}})
		}
	}
	return list, listed, nil
}

// waitingCandidates are the builds that wait for an approval.
func waitingCandidates(ctx context.Context, f AzureDevOps, a args) ([]candidate, error) {
	repo := a.repo
	waiting, err := f.WaitingDeploys(ctx, repo, a.pipeline, a.branch)
	if err != nil {
		return nil, err
	}
	var list []candidate
	for _, w := range waiting {
		if w.Since != "" && (a.anyone || w.Build.Trusted) {
			list = append(list, candidate{EventID: "deploy.waiting:" + repo + ":" + w.key(), Time: w.Since,
				Data:    func() (map[string]any, error) { return w.data(), nil },
				Subject: runRef(repo, w.Build.ID), Key: strings.TrimPrefix(w.key(), fmt.Sprint(w.Build.ID)+":")})
		}
	}
	return list, nil
}

// commentCandidates are review.comment's comments on lines, or pr.commented's on the pull
// requests themselves (#6295), made at or after since.
func commentCandidates(ctx context.Context, f AzureDevOps, name string, a args, yours *Yours, since string) ([]candidate, Listed, error) {
	repo := a.repo
	read, data := f.ReviewComments, ReviewComment.data
	if name == "pr.commented" {
		read, data = f.PRComments, ReviewComment.prData
	}
	comments, listed, err := read(ctx, repo, since)
	if err != nil {
		return nil, Listed{}, err
	}
	var list []candidate
	for _, c := range comments {
		// Trust is the commenter's; mine only narrows the pull request (#3797).
		m := c.PR
		if at := mcpevents.Normalized(c.At); at != "" && a.wantsPR(m.Number) && (a.branch == "" || m.Branch == a.branch) &&
			a.closesOne(m) && a.byMe(m, yours) && (a.anyone || c.Trusted) &&
			(name != "pr.commented" || !mcpevents.ByAgent(c.Body)) {
			list = append(list, candidate{EventID: fmt.Sprintf("%s:%s:%d:%d:%d", name, repo, c.PR.Number, c.ThreadID, c.ID), Time: at,
				Data:    func() (map[string]any, error) { return data(c), nil },
				Subject: prRef(repo, c.PR.Number), Key: fmt.Sprintf("%d:%d", c.ThreadID, c.ID)})
		}
	}
	return list, listed, nil
}

// failedCandidates are the pull requests whose checks failed at or after since.
func failedCandidates(ctx context.Context, f AzureDevOps, a args, wanted func(PR, bool) bool, since string) ([]candidate, Listed, error) {
	repo := a.repo
	failed, listed, err := f.FailedChecks(ctx, repo, since)
	if err != nil {
		return nil, Listed{}, err
	}
	// The newest failed build's time is in its id: run again and failed again, it is new.
	var list []candidate
	for _, d := range failed {
		if at := mcpevents.Normalized(d.At); at != "" && wanted(d.PR, d.Trusted) {
			list = append(list, candidate{EventID: fmt.Sprintf("checks.failed:%s:%s:%s", repo, d.HeadSHA, at), Time: at,
				Data:    func() (map[string]any, error) { return d.json(), nil },
				Subject: prRef(repo, d.PR.Number), Key: d.HeadSHA + ":" + at})
		}
	}
	return list, listed, nil
}

// passedCandidates are the pull requests' builds and statuses that passed at or after since.
func passedCandidates(ctx context.Context, f AzureDevOps, a args, wanted func(PR, bool) bool, since string) ([]candidate, Listed, error) {
	repo := a.repo
	passed, listed, err := f.PassedChecks(ctx, repo, since)
	if err != nil {
		return nil, Listed{}, err
	}
	// One a build or a status, at its time: a build run again that passes again is a new one.
	var list []candidate
	for _, d := range passed {
		if at := mcpevents.Normalized(d.At); at != "" && wanted(d.PR, d.Trusted) {
			list = append(list, candidate{EventID: fmt.Sprintf("checks.passed:%s:%s:%s:%s", repo, d.HeadSHA, d.Key, at), Time: at,
				Data:    func() (map[string]any, error) { return d.json(), nil },
				Subject: prRef(repo, d.PR.Number), Key: d.HeadSHA + ":" + d.Key + ":" + at})
		}
	}
	return list, listed, nil
}

// closedCandidates are the pull requests merged (pr.merged) or else closed at or after since.
func closedCandidates(ctx context.Context, f AzureDevOps, name string, a args, wanted func(PR, bool) bool, since string) ([]candidate, Listed, error) {
	repo := a.repo
	merged, closed, listed, err := f.ClosedPRs(ctx, repo, since)
	if err != nil {
		return nil, Listed{}, err
	}
	var list []candidate
	if name == "pr.merged" {
		for _, m := range merged {
			if at := mcpevents.Normalized(m.MergedAt); wanted(m.PR, m.Trusted) && at != "" {
				list = append(list, candidate{EventID: fmt.Sprintf("pr.merged:%s:%d", repo, m.PR.Number), Time: at,
					Subject: prRef(repo, m.PR.Number),
					Data: func() (map[string]any, error) {
						return map[string]any{"pr": m.PR.json(), "mergedAt": m.MergedAt, "mergeSha": m.MergeSHA}, nil
					}})
			}
		}
		return list, listed, nil
	}
	// Its close time in the id: a pull request reopened and closed again is a new event.
	for _, c := range closed {
		if at := mcpevents.Normalized(c.ClosedAt); wanted(c.PR, c.Trusted) && at != "" {
			list = append(list, candidate{EventID: fmt.Sprintf("pr.closed:%s:%d:%s", repo, c.PR.Number, at), Time: at,
				Subject: prRef(repo, c.PR.Number), Key: at,
				Data: func() (map[string]any, error) {
					return map[string]any{"pr": c.PR.json(), "closedAt": c.ClosedAt}, nil
				}})
		}
	}
	return list, listed, nil
}

// eventsError is an Azure DevOps failure as the events draft's error codes.
func eventsError(err error) *rpcError {
	var rpc *rpcError
	if errors.As(err, &rpc) {
		return rpc
	}
	var fe *AzureDevOpsError
	if !errors.As(err, &fe) {
		return &rpcError{Code: -32603, Message: err.Error()}
	}
	switch fe.Kind {
	case NotSignedIn:
		return &rpcError{Code: -32012, Message: fe.Message, Data: map[string]any{"reason": "not connected"}}
	case RateLimited:
		return &rpcError{Code: -32013, Message: fe.Message, Data: map[string]any{"retryAfterMs": fe.RetryAfterMs}}
	case NotFound:
		return invalidParams("%s: check repo", fe.Message)
	}
	return &rpcError{Code: -32603, Message: fe.Message}
}
