package agentsd

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #3865: the forge items that ask the person for something are requests.

func (f *fakeForgeGitHub) SetBody(_ context.Context, repo string, number int, body string) error {
	f.note("body %s#%d %s", repo, number, body)
	f.mu.Lock()
	f.bodies[number] = body
	f.mu.Unlock()
	return nil
}

func (f *fakeForgeGitHub) Unassign(_ context.Context, repo string, number int, logins []string) error {
	f.note("unassign %s#%d %v", repo, number, logins)
	return nil
}

func (f *fakeForgeGitHub) Assign(_ context.Context, repo string, number int, logins []string) error {
	f.note("assign %s#%d %v", repo, number, logins)
	return nil
}

const todoBody = "Why.\n\n## What Alex does\n\n- [ ] Sign in to gitlab.com\n- [x] Make a token\n- [ ] Paste it\n\n## Notes\n\n- [ ] not a step\n"

func TestChecklistIsReadAndTicked(t *testing.T) {
	t.Parallel()

	lines := checklistOf(todoBody)
	if len(lines) != 3 || lines[0] != (wire.RequestChecklistLine{Text: "Sign in to gitlab.com"}) || !lines[1].Done {
		t.Fatalf("%+v", lines)
	}
	ticked := tickChecklist(todoBody, []int64{0})
	if got := checklistOf(ticked); !got[0].Done || got[2].Done || allDone(got) {
		t.Fatalf("%+v", got)
	}
	if got := checklistOf(tickChecklist(ticked, []int64{2})); !allDone(got) {
		t.Fatalf("%+v", got)
	}
	if shots := screenshotsOf("## Screenshots\n\n![mac](https://x/mac.png) <img src=\"https://x/web.png\">\n## Other\n![no](https://x/no.png)"); !slices.Equal(shots, []string{"https://x/mac.png", "https://x/web.png"}) {
		t.Fatalf("%q", shots)
	}
}

func forgeRequestsDaemon(t *testing.T) (*Daemon, *conn, *fakeForgeGitHub) {
	d, c, gh, _ := forgeDaemon(t)
	gh.mu.Lock()
	gh.todos = []githubmcp.Item{{Repo: "alexec/agents", Number: 5, State: "open", Title: "Make a GitLab account",
		URL: "https://github.com/alexec/agents/issues/5", Labels: []string{"alex-todo"}, Assignees: []string{"alexec"}, UpdatedAt: "2026-11-01T10:00:00Z"}}
	gh.reviews = []githubmcp.Item{{Repo: "alexec/agents", Number: 12, PR: true, State: "open", Title: "A new button",
		URL: "https://github.com/alexec/agents/pull/12", Author: "alexec-agents[bot]", UpdatedAt: "2026-11-01T10:00:00Z"},
		{Repo: "alexec/agents", Number: 13, PR: true, State: "open", Title: "A draft", URL: "https://github.com/alexec/agents/pull/13", Draft: true}}
	gh.bodies = map[int]string{5: todoBody, 12: "## Screenshots\n\n![mac](https://x/mac.png)\n"}
	gh.authors = map[int]string{12: "alexec-agents[bot]"}
	gh.states = map[int]string{}
	gh.mu.Unlock()
	return d, c, gh
}

// sweepAgain sweeps with the forge reads' cache emptied, as the next sweep after a minute.
func sweepAgain(d *Daemon) {
	d.forgeMu.Lock()
	d.forgeCache = nil
	d.forgeMu.Unlock()
	d.sweepForgeRequests(context.Background())
}

func TestForgeItemsBecomeRequestsAndTheChecklistGoesBack(t *testing.T) {
	t.Parallel()

	d, c, gh := forgeRequestsDaemon(t)
	sweepAgain(d)
	sweepAgain(d)
	open := listedRequests(t, c, 1, "open")
	if len(open) != 2 {
		t.Fatalf("one request each: %+v", open)
	}
	var todo, review wire.Request
	for _, r := range open {
		if r.Forge.Item == "issue" {
			todo = r
		} else {
			review = r
		}
	}
	if todo.Kind != wire.RequestKindStep || todo.Source != wire.RequestSourceForge || todo.Title != "Make a GitLab account" ||
		len(todo.Forge.Checklist) != 3 || todo.Forge.Number != 5 || todo.Forge.Label != "alex-todo" || todo.Folder == "" {
		t.Fatalf("todo: %+v %+v", todo, todo.Forge)
	}
	if review.Kind != wire.RequestKindApprove || !slices.Equal(review.Forge.Screenshots, []string{"https://x/mac.png"}) {
		t.Fatalf("review: %+v %+v", review, review.Forge)
	}
	// One line ticked: the issue's line is ticked, and the request stays open.
	var u wire.RequestUpdate
	mustUnmarshal(t, c.call(t, 2, "requests/answer", map[string]any{"id": todo.ID, "answer": map[string]any{"checklist": map[string]any{"done": []int{0}}}}), &u)
	if u.Request.State != wire.RequestStateOpen || !u.Request.Forge.Checklist[0].Done || u.Request.Forge.Checklist[2].Done {
		t.Fatalf("%+v", u.Request.Forge)
	}
	// The last line: the label and the person come off, a comment says it, and it is answered.
	mustUnmarshal(t, c.call(t, 3, "requests/answer", map[string]any{"id": todo.ID, "answer": map[string]any{"checklist": map[string]any{"done": []int{2}}}}), &u)
	if u.Request.State != wire.RequestStateAnswered {
		t.Fatalf("%+v", u.Request)
	}
	w := gh.writes()
	if len(w) != 5 || !slices.Contains(w, "labels alexec/agents#5 + -alex-todo") || !slices.Contains(w, "unassign alexec/agents#5 [alexec]") ||
		!slices.Contains(w, "comment alexec/agents#5 Done in Agent Loops: every step is ticked.") {
		t.Fatalf("%q", w)
	}
	// The approval (#5803): only the person's review of the head; no comment, no label.
	mustUnmarshal(t, c.call(t, 4, "requests/answer", map[string]any{"id": review.ID, "answer": map[string]any{"approval": map[string]any{"action": "approve"}}}), &u)
	if u.Request.State != wire.RequestStateAnswered || deref(u.Request.Answer) != "approve: Approved in Agent Loops." {
		t.Fatalf("%+v", u.Request)
	}
	if w := gh.writes(); len(w) != 6 || !slices.Contains(w, "review alexec/agents#12 h12 approve=true") {
		t.Fatalf("%q", w)
	}
	var answered *wire.Event
	for _, e := range logged(d) {
		if e.Name == "request.answered" && e.Details["pr"] == "12" && answered == nil {
			answered = &e
		}
	}
	if answered == nil || answered.Details["answer"] != "approve: Approved in Agent Loops." {
		t.Fatalf("no request.answered with pr 12: %v", names(d))
	}
}

// The person's own pull request takes no review from them: the answer is refused, and nothing
// is written (#5803).
func TestTheirOwnPullRequestCannotBeApproved(t *testing.T) {
	t.Parallel()

	d, c, gh := forgeRequestsDaemon(t)
	gh.mu.Lock()
	gh.authors[12] = "alexec"
	gh.mu.Unlock()
	sweepAgain(d)
	var review wire.Request
	for _, r := range listedRequests(t, c, 1, "open") {
		if r.Forge.Item == "pr" {
			review = r
		}
	}
	msg := c.callErr(t, 2, "requests/answer", map[string]any{"id": review.ID, "answer": map[string]any{"approval": map[string]any{"action": "approve"}}})
	if !strings.Contains(msg, "You opened this pull request, so you cannot approve it.") {
		t.Fatalf("%s", msg)
	}
	if w := gh.writes(); len(w) != 0 {
		t.Fatalf("%q", w)
	}
}

// A label taken off on the forge answers the request at the next sweep, and so does the
// person's review of a pull request, which drops the request from GitHub's list; an issue
// closed with steps left drops it.
func TestTheForgeEndsARequest(t *testing.T) {
	t.Parallel()

	d, c, gh := forgeRequestsDaemon(t)
	sweepAgain(d)
	if len(listedRequests(t, c, 1, "open")) != 2 {
		t.Fatal("not opened")
	}
	gh.mu.Lock()
	gh.todos, gh.reviews = nil, nil
	gh.states[5] = "closed"
	gh.reviewers = map[int][]githubmcp.Reviewer{12: {{Login: "someone", State: "approved"}, {Login: "alexec", State: "changes_requested"}}}
	gh.mu.Unlock()
	sweepAgain(d)
	if open := listedRequests(t, c, 2, "open"); len(open) != 0 {
		t.Fatalf("%+v", open)
	}
	for _, r := range listedRequests(t, c, 3, "ended") {
		switch r.Forge.Item {
		case "issue":
			if r.State != wire.RequestStateDropped || r.DroppedWhy == nil || *r.DroppedWhy != wire.RequestEndedWhyForgeClosed {
				t.Fatalf("%+v", r)
			}
		case "pr":
			if r.State != wire.RequestStateAnswered || deref(r.Answer) != "changes" {
				t.Fatalf("%+v", r)
			}
		}
	}
}

// #3860: on the agents' bot's PR, Approve and Request changes in Agents are the person's real
// review of the head commit, not a comment, and no label (#5803).
func TestAnApprovalOfTheBotsPRIsARealReview(t *testing.T) {
	t.Parallel()

	d, c, gh := forgeRequestsDaemon(t)
	gh.mu.Lock()
	gh.authors = map[int]string{12: "alexec-agents"}
	gh.mu.Unlock()
	sweepAgain(d)
	var review wire.Request
	for _, r := range listedRequests(t, c, 1, "open") {
		if r.Forge.Item == "pr" {
			review = r
		}
	}
	var u wire.RequestUpdate
	mustUnmarshal(t, c.call(t, 2, "requests/answer", map[string]any{"id": review.ID, "answer": map[string]any{"approval": map[string]any{"action": "changes", "note": "Move it left"}}}), &u)
	if u.Request.State != wire.RequestStateAnswered {
		t.Fatalf("%+v", u.Request)
	}
	w := gh.writes()
	if !slices.Equal(w, []string{"review alexec/agents#12 h12 approve=false Move it left"}) {
		t.Fatalf("%q", w)
	}
	for _, line := range w {
		if strings.HasPrefix(line, "comment alexec/agents#12") {
			t.Fatalf("a comment as well: %q", w)
		}
	}
}
