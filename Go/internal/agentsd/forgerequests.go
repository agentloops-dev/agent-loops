package agentsd

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Forge requests (#3865, spec 3862 §4): a GitHub issue labelled alex-todo or needs-account,
// with its "## What Alex does" checklist, or an open, non-draft pull request with a review
// requested from the person (#5803), with its screenshots, is a request like an agent's
// question. A sweep every forgeRequestEvery lists the open items of each source (forge/issues
// and forge/pulls, so one search for every folder) and opens, updates or ends their records.
// Answered in the app, the answer goes back to GitHub as the person: a ticked line ticks the
// issue's line, the last one takes the label and the person off and says so in a comment; an
// approval is only the person's review of the head (Approve, or Request changes with the note),
// the one way a pull request is approved (epic #5802). A pull request's request ends when
// GitHub says the person reviewed it, or the request was taken off, at the next sweep.

// forgeRequestLabel is what makes a request: a label on an issue, or a review requested from
// the person on a pull request (label is then reviewRequested, which is no forge label).
type forgeRequestLabel struct {
	label string
	pr    bool
	kind  wire.RequestKind
}

// reviewRequested is a pull request request's source (#5803): a review requested from the person.
const reviewRequested = "review-requested"

// forgeRequestLabels are the two labels and the requested review (spec 3862 §4: a fixed set,
// not a setting).
var forgeRequestLabels = []forgeRequestLabel{
	{"alex-todo", false, wire.RequestKindStep},
	{"needs-account", false, wire.RequestKindAccount},
	{reviewRequested, true, wire.RequestKindApprove},
}

// of says a request is of this source. Every pull request's request is of the requested review,
// also one opened before #5803 by the needs-approval label, so it ends by the review too.
func (l forgeRequestLabel) of(f *wire.RequestForge) bool {
	return f != nil && (f.Label == l.label || l.pr && f.Item == lineage.KindPR)
}

var (
	// forgeRequestEvery is the sweep; forgeRequestFirst is how long after the start the first is.
	forgeRequestEvery = 5 * time.Minute
	forgeRequestFirst = 20 * time.Second
)

// checklistHeading starts the checklist: "## What Alex does", whoever's name it says.
var (
	checklistHeading = regexp.MustCompile(`^##\s+What\s+.+\s+does\s*$`)
	checklistLine    = regexp.MustCompile(`^(\s*[-*]\s+\[)([ xX])(\]\s+)(.*)$`)
	screenshotsHead  = regexp.MustCompile(`^##\s+Screenshots\s*$`)
	imageLink        = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)|<img[^>]+src="([^"]+)"`)
)

// checklistOf is the lines under the body's first "## What <name> does" heading, up to the
// next heading.
func checklistOf(body string) []wire.RequestChecklistLine {
	out := []wire.RequestChecklistLine{}
	in := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case !in && checklistHeading.MatchString(line):
			in = true
		case in && strings.HasPrefix(line, "#"):
			return out
		case in:
			if m := checklistLine.FindStringSubmatch(line); m != nil {
				out = append(out, wire.RequestChecklistLine{Text: strings.TrimSpace(m[4]), Done: m[2] != " "})
			}
		}
	}
	return out
}

// tickChecklist ticks the checklist's lines at done (by index) in the body; the others stay
// as they are, so a line ticked on the forge meanwhile is not taken back.
func tickChecklist(body string, done []int64) string {
	lines := strings.Split(body, "\n")
	in, i := false, 0
	for n, line := range lines {
		switch {
		case !in && checklistHeading.MatchString(strings.TrimRight(line, "\r")):
			in = true
		case in && strings.HasPrefix(line, "#"):
			return strings.Join(lines, "\n")
		case in:
			if m := checklistLine.FindStringSubmatchIndex(line); m != nil {
				if slices.Contains(done, int64(i)) {
					lines[n] = line[:m[4]] + "x" + line[m[5]:]
				}
				i++
			}
		}
	}
	return strings.Join(lines, "\n")
}

// screenshotsOf is the image links under the body's "## Screenshots" heading.
func screenshotsOf(body string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		switch {
		case !in && screenshotsHead.MatchString(strings.TrimRight(line, "\r")):
			in = true
		case in && strings.HasPrefix(line, "## "):
			return out
		case in:
			for _, m := range imageLink.FindAllStringSubmatch(line, -1) {
				out = append(out, m[1]+m[2])
			}
		}
	}
	return out
}

func allDone(lines []wire.RequestChecklistLine) bool {
	for _, l := range lines {
		if !l.Done {
			return false
		}
	}
	return true
}

// sweepForgeRequests lists each label's open items once and brings the records in step.
func (d *Daemon) sweepForgeRequests(ctx context.Context) {
	for _, l := range forgeRequestLabels {
		var list wire.ForgeList
		var err error
		if l.pr {
			login := d.reviewer()
			if login == "" {
				continue
			}
			list, err = d.forgeListAsked(ctx, true, wire.ForgeListRequest{State: new("open"), Limit: new(int64(forgeListMost))}, login)
		} else {
			list, err = d.forgeList(ctx, false, wire.ForgeListRequest{State: new("open"), Label: new(l.label), Limit: new(int64(forgeListMost))})
		}
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, i := range list.Items {
			repo, number, ok := nodeOfRepo(i.Node)
			if !ok || repo.forge != "github" || i.URL == nil || i.Folder == nil || i.State != "open" || i.Draft != nil && *i.Draft {
				continue
			}
			seen[*i.URL] = true
			d.forgeRequestSeen(ctx, l, i, repo, number)
		}
		// Only a whole list says an item lost its label or its requested review: a forge that
		// could not be read, or a list cut at its page, ends nothing.
		if len(list.Errors) > 0 || list.Cursor != nil {
			continue
		}
		for _, r := range d.listRequests(wire.RequestsListStateOpen, "") {
			if l.of(r.Forge) && !seen[r.Forge.URL] {
				d.forgeRequestGone(ctx, r)
			}
		}
	}
}

// openForgeRequest is the open request for an item and source, or nil.
func (d *Daemon) openForgeRequest(url string, l forgeRequestLabel) *wire.Request {
	d.requests.mu.Lock()
	defer d.requests.mu.Unlock()
	for _, r := range d.requests.byID {
		if r.State == wire.RequestStateOpen && l.of(r.Forge) && r.Forge.URL == url {
			c := copyRequest(r)
			return &c
		}
	}
	return nil
}

// forgeRequestSeen opens the request for an item the sweep saw, or reads it again when the
// item changed since.
func (d *Daemon) forgeRequestSeen(ctx context.Context, l forgeRequestLabel, i wire.ForgeItem, repo forgeRepo, number int) {
	at := now()
	old := d.openForgeRequest(*i.URL, l)
	if old != nil && old.Forge.UpdatedAt != nil && *old.Forge.UpdatedAt == i.UpdatedAt {
		d.requests.mu.Lock()
		if r := d.requests.byID[old.ID]; r != nil && r.Forge != nil {
			r.Forge.SeenAt = at
			d.saveRequestLocked(r)
		}
		d.requests.mu.Unlock()
		return
	}
	if old == nil && len(d.listRequests(wire.RequestsListStateOpen, "")) >= requestFileLimit {
		log.Printf("requests: %d open already, so %s is not a request yet", requestFileLimit, *i.URL)
		return
	}
	kind := lineage.KindIssue
	if l.pr {
		kind = lineage.KindPR
	}
	detail, err := d.forgeItem(ctx, kind, wire.ForgeItemRequest{Node: i.Node})
	if err != nil {
		log.Printf("requests: %s: %v", *i.URL, err)
		return
	}
	updated := i.UpdatedAt
	f := &wire.RequestForge{Provider: repo.forge, Repo: repo.path, Item: kind, Number: int64(number), URL: *i.URL,
		Label: l.label, Checklist: []wire.RequestChecklistLine{}, Author: i.Author, UpdatedAt: &updated, SeenAt: at}
	if l.pr {
		f.Screenshots = screenshotsOf(detail.Body)
	} else {
		f.Checklist = checklistOf(detail.Body)
	}
	d.requests.mu.Lock()
	if d.requests.byID == nil {
		d.requests.byID = map[wire.UUID]*wire.Request{}
	}
	r := (*wire.Request)(nil)
	if old != nil {
		r = d.requests.byID[old.ID]
	}
	opened := r == nil
	if opened {
		r = &wire.Request{ID: wire.UUID(store.NewUUID()), Kind: l.kind, Source: wire.RequestSourceForge, State: wire.RequestStateOpen,
			Folder: string(store.FolderURL(*i.Folder)), Waiters: []wire.RequestWaiter{}, AskedAt: at}
		r.Title, r.Forge = i.Title, f
		if err := d.requestRefusedLocked(r); err != nil {
			d.requests.mu.Unlock()
			log.Printf("requests: %s is not a request: %v", *i.URL, err)
			return
		}
		d.requests.byID[r.ID] = r
	}
	r.Title, r.Forge = i.Title, f
	d.saveRequestLocked(r)
	c := copyRequest(r)
	d.requests.mu.Unlock()
	Broadcast(d, wire.NotifyRequestsChanged, wire.RequestUpdate{Request: c})
	if opened {
		d.raiseRequestEvent("request.opened", c, nil, nil)
	}
}

// forgeRequestGone ends the request for an item the sweep no longer lists with its label or its
// requested review: closed, it is answered when every line is ticked and else dropped; open,
// the label came off, or the person reviewed the pull request (the review is the answer) or the
// request was taken off, and it is answered.
func (d *Daemon) forgeRequestGone(ctx context.Context, r wire.Request) {
	kind := map[string]string{"issue": lineage.KindIssue, "pr": lineage.KindPR}[r.Forge.Item]
	repo := forgeRepo{r.Forge.Provider, r.Forge.Repo}
	node := repo.issue(int(r.Forge.Number))
	if kind == lineage.KindPR {
		node = repo.pr(int(r.Forge.Number))
	}
	detail, err := d.forgeItem(ctx, kind, wire.ForgeItemRequest{Node: node})
	if err != nil {
		return
	}
	switch {
	case detail.Item.State != "open" && (kind == lineage.KindPR || !allDone(checklistOf(detail.Body))):
		d.endRequest(r.ID, wire.RequestStateDropped, nil, wire.RequestDroppedWhyForgeClosed, nil)
	case detail.Item.State != "open":
		d.endRequest(r.ID, wire.RequestStateAnswered, new("Closed with every step done"), "", nil)
	case kind == lineage.KindPR:
		answer, ok := reviewAnswer(detail.Reviewers, d.reviewer())
		if !ok {
			return
		}
		d.endRequest(r.ID, wire.RequestStateAnswered, &answer, "", nil)
	default:
		answer := "The label " + r.Forge.Label + " was taken off"
		d.endRequest(r.ID, wire.RequestStateAnswered, &answer, "", nil)
	}
}

// reviewAnswer is a pull request request's answer from its reviewers (#5803): the person's
// newest review, approve, changes or commented, or the request taken off with none. Not ok while
// the person is still asked, or with no person to read.
func reviewAnswer(reviewers []wire.ForgeReviewer, login string) (string, bool) {
	if login == "" {
		return "", false
	}
	for _, rv := range reviewers {
		if !strings.EqualFold(rv.Login, login) {
			continue
		}
		switch rv.State {
		case wire.ForgeReviewerStateApproved:
			return "approve", true
		case wire.ForgeReviewerStateChangesRequested:
			return "changes", true
		case wire.ForgeReviewerStateCommented:
			return "commented", true
		default:
			return "", false
		}
	}
	return "The review request was taken off", true
}

// answerForgeRequest is requests/answer for a forge request: a checklist or an approval, sent
// to GitHub as the person.
func (d *Daemon) answerForgeRequest(ctx context.Context, rec wire.Request, a wire.RequestAnswer, by *wire.AuditActor) (wire.RequestUpdate, error) {
	f := rec.Forge
	if f.Provider != "github" {
		return wire.RequestUpdate{}, Invalid("Only a GitHub request is answered here; answer it on %s.", f.URL)
	}
	gh, err := d.forgeGitHub()
	if err != nil {
		return wire.RequestUpdate{}, err
	}
	var answer string
	switch {
	case a.Checklist != nil && f.Item == lineage.KindIssue:
		var open *wire.RequestUpdate
		answer, open, err = d.answerForgeChecklist(ctx, gh, rec, a)
		if err != nil {
			return wire.RequestUpdate{}, err
		}
		if open != nil {
			return *open, nil
		}
	case a.Approval != nil && f.Item == lineage.KindPR:
		answer, err = d.answerForgeApproval(ctx, gh, f, a)
		if err != nil {
			return wire.RequestUpdate{}, err
		}
	default:
		return wire.RequestUpdate{}, Invalid("This request takes a %s answer.", map[string]string{"issue": "checklist", "pr": "approval"}[f.Item])
	}
	if !d.endRequest(rec.ID, wire.RequestStateAnswered, &answer, "", by) {
		return wire.RequestUpdate{}, &Error{Code: wire.FailureNoSuchAgent, Message: "That request has ended already."}
	}
	after := d.request(rec.ID)
	return wire.RequestUpdate{Request: *after}, nil
}

// answerForgeChecklist ticks the steps of an issue's checklist on GitHub. When
// a step is still open, it keeps the request open and gives its update as open.
// When every step is done, it takes the label off, unassigns the person, comments,
// and gives the answer.
func (d *Daemon) answerForgeChecklist(ctx context.Context, gh forgeGitHub, rec wire.Request, a wire.RequestAnswer) (string, *wire.RequestUpdate, error) {
	f := rec.Forge
	number := int(f.Number)
	page, err := gh.ItemPage(ctx, f.Repo, number, "")
	if err != nil {
		return "", nil, err
	}
	body := tickChecklist(page.Body, a.Checklist.Done)
	if body != page.Body {
		if err := gh.SetBody(ctx, f.Repo, number, body); err != nil {
			return "", nil, err
		}
	}
	lines := checklistOf(body)
	if !allDone(lines) {
		d.requests.mu.Lock()
		r := d.requests.byID[rec.ID]
		if r != nil && r.Forge != nil {
			r.Forge.Checklist = lines
			d.saveRequestLocked(r)
			rec = copyRequest(r)
		}
		d.requests.mu.Unlock()
		Broadcast(d, wire.NotifyRequestsChanged, wire.RequestUpdate{Request: rec})
		return "", &wire.RequestUpdate{Request: rec}, nil
	}
	if err := gh.SetLabels(ctx, f.Repo, number, nil, []string{f.Label}); err != nil {
		return "", nil, err
	}
	if c, ok := d.githubConnection(); ok && c.Login != "" {
		if err := gh.Unassign(ctx, f.Repo, number, []string{c.Login}); err != nil {
			log.Printf("requests: %s: unassign: %v", f.URL, err)
		}
	}
	if _, err := gh.CommentOnPR(ctx, f.Repo, number, "Done in Agent Loops: every step is ticked."); err != nil {
		log.Printf("requests: %s: comment: %v", f.URL, err)
	}
	d.requests.mu.Lock()
	if r := d.requests.byID[rec.ID]; r != nil && r.Forge != nil {
		r.Forge.Checklist = lines
	}
	d.requests.mu.Unlock()
	return "Every step is done", nil, nil
}

// answerForgeApproval sends the person's review of a pull request to GitHub:
// approve, or changes with a note. It gives the answer.
func (d *Daemon) answerForgeApproval(ctx context.Context, gh forgeGitHub, f *wire.RequestForge, a wire.RequestAnswer) (string, error) {
	number := int(f.Number)
	action := a.Approval.Action
	note := strings.TrimSpace(deref(a.Approval.Note))
	switch action {
	case "approve":
		if note == "" {
			note = "Approved in Agent Loops."
		}
	case "changes":
		if note == "" {
			return "", Invalid("Say what to change in the note.")
		}
	default:
		return "", Invalid("An approval is approve or changes, not %q.", action)
	}
	page, err := gh.ItemPage(ctx, f.Repo, number, "")
	if err != nil {
		return "", err
	}
	login := d.reviewer()
	if login == "" || page.Head == "" {
		return "", Invalid("Connect GitHub as yourself to review %s.", f.URL)
	}
	// The one way a pull request is approved (epic #5802): the person's own review of the head
	// commit, which GitHub's approval rule and a required review count (#3860). GitHub takes
	// no review of a pull request from its author.
	if strings.EqualFold(page.Author, login) {
		return "", Invalid("You opened this pull request, so you cannot approve it.")
	}
	if _, err := gh.ReviewPR(ctx, f.Repo, number, page.Head, action == "approve", note); err != nil {
		return "", err
	}
	return action + ": " + note, nil
}

// addForgeRequestWaiter is a session that waits on request.* with where: {pr: <n>} or
// {issue: <n>}: a waiter of each open forge request on that item.
func (d *Daemon) addForgeRequestWaiter(item, number string, agent wire.UUID) {
	n, err := strconv.ParseInt(number, 10, 64)
	if err != nil {
		return
	}
	for _, r := range d.listRequests(wire.RequestsListStateOpen, "") {
		if r.Forge != nil && r.Forge.Item == item && r.Forge.Number == n {
			d.addRequestWaiter(r.ID, agent)
		}
	}
}

// forgeRequestDetails are a forge request's event details: pr or issue, and url.
func forgeRequestDetails(r wire.Request, details map[string]string) {
	if r.Forge == nil {
		return
	}
	details[r.Forge.Item] = fmt.Sprint(r.Forge.Number)
	details["url"] = r.Forge.URL
}

// sweepForgeRequestsEvery sweeps until ctx ends, at Shutdown.
func (d *Daemon) sweepForgeRequestsEvery(ctx context.Context) {
	t := time.NewTimer(forgeRequestFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.sweepForgeRequests(ctx)
		t.Reset(forgeRequestEvery)
	}
}

func (d *Daemon) startForgeRequests() { d.goLoop("forge requests", d.sweepForgeRequestsEvery) }
