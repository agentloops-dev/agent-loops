package agentsd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// reviewsIn is pins/list's For Review pull requests of the folder on repo.
func reviewsIn(t *testing.T, d *Daemon, c *conn, id int, repo string) []wire.ReviewPull {
	t.Helper()
	var listed []wire.FolderPins
	mustUnmarshal(t, c.call(t, id, "pins/list", wire.Empty{}), &listed)
	for _, pp := range listed {
		if d.folderRepo(store.FolderPath(pp.Folder)).path == repo && len(pp.Reviews) > 0 {
			return pp.Reviews
		}
	}
	return nil
}

// #3330: For Review lists each folder's open pull requests with a review requested from the
// person (#5803), newest first, but not a draft, whatever their labels; a dismissed one is kept
// out after the next read too, and the dismissal is kept on disk.
func TestForReviewListsPullRequestsWaitingForApproval(t *testing.T) {
	t.Parallel()

	d, c, gh, _ := forgeDaemon(t)
	pr := func(repo string, n, daysAgo int) githubmcp.Item {
		return githubmcp.Item{Repo: repo, Number: n, PR: true, State: "open", Title: fmt.Sprint("PR ", n),
			URL:    fmt.Sprintf("https://github.com/%s/pull/%d", repo, n),
			Author: "alexec", UpdatedAt: nowFunc().AddDate(0, 0, -daysAgo).UTC().Format(time.RFC3339)}
	}
	older, newer := pr("alexec/agents", 1, 3), pr("alexec/agents", 2, 2)
	draft := pr("alexec/agents", 5, 1)
	draft.Draft = true
	web := pr("alexec/web", 6, 3)
	gh.reviews = []githubmcp.Item{newer, older, draft, web}

	d.readReviews(d.ctx)
	if searches, _ := gh.asked(); len(searches) != 1 || !strings.Contains(searches[0], " user-review-requested:alexec ") ||
		strings.Contains(searches[0], "label:") {
		t.Fatalf("one search for the person's requested reviews: %q", searches)
	}
	got := reviewsIn(t, d, c, 1, "alexec/agents")
	if len(got) != 2 || got[0].Number != 2 || got[1].Number != 1 || got[0].Repo != "alexec/agents" ||
		string(got[0].URL) != newer.URL || deref(got[0].Author) != "alexec" || got[0].Title != newer.Title ||
		deref(got[0].Ref) != "github:pr:alexec/agents#2" {
		t.Fatalf("agents' reviews: %+v", got)
	}
	if web := reviewsIn(t, d, c, 2, "alexec/web"); len(web) != 1 || web[0].Number != 6 {
		t.Fatalf("web's reviews: %+v", web)
	}
	// The Remote reads them in its catch-up.
	var snapshot wire.CatchUpSnapshot
	mustUnmarshal(t, c.call(t, 20, "client/catchUp", wire.CatchUpRequest{Parts: []string{"pins"}}), &snapshot)
	caught := 0
	for _, pp := range snapshot.Pins {
		caught += len(pp.Reviews)
	}
	if caught != 3 {
		t.Fatalf("the catch-up's reviews: %d", caught)
	}

	var folder wire.URL
	var listed []wire.FolderPins
	mustUnmarshal(t, c.call(t, 3, "pins/list", wire.Empty{}), &listed)
	for _, pp := range listed {
		if len(pp.Reviews) == 2 {
			folder = pp.Folder
		}
	}
	c.call(t, 4, "reviews/dismiss", wire.ReviewDismissRequest{Folder: folder, URL: wire.URL(newer.URL)})
	if got := reviewsIn(t, d, c, 5, "alexec/agents"); len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("after Dismiss: %+v", got)
	}
	d.readReviews(d.ctx)
	if got := reviewsIn(t, d, c, 6, "alexec/agents"); len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("a dismissed pull request came back: %+v", got)
	}

	// #3464: one the person opened is said opened, after the next read too, and kept on disk.
	if got := reviewsIn(t, d, c, 30, "alexec/agents"); got[0].Opened != nil {
		t.Fatalf("not opened yet: %+v", got)
	}
	c.call(t, 31, "reviews/open", wire.ReviewOpenRequest{Folder: folder, URL: wire.URL(older.URL)})
	d.readReviews(d.ctx)
	if got := reviewsIn(t, d, c, 32, "alexec/agents"); len(got) != 1 || got[0].Opened == nil || !*got[0].Opened {
		t.Fatalf("after open: %+v", got)
	}
	if b, err := os.ReadFile(d.reviewOpenedPath()); err != nil || !strings.Contains(string(b), older.URL) {
		t.Fatalf("the opened pull request is kept on disk: %s %v", b, err)
	}
	b, err := os.ReadFile(d.reviewDismissalsPath())
	if err != nil || !strings.Contains(string(b), newer.URL) {
		t.Fatalf("the dismissal is kept on disk: %s %v", b, err)
	}

	// Gone from GitHub's list (approved, closed), it goes from For Review.
	gh.reviews = nil
	d.setReviews(map[string][]wire.ReviewPull{})
	if got := reviewsIn(t, d, c, 7, "alexec/agents"); got != nil {
		t.Fatalf("nothing waits: %+v", got)
	}
}

// #6728: a dismissal ages out 90 days after its pull request last changed, and one that has not
// changed for as long is not listed, so a dismissed pull request does not come back. A file from
// before the limits keeps its dismissals, and the count is bounded.
func TestReviewDismissalsAgeOut(t *testing.T) {
	clock := atTime(t, time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC))
	root, folder := t.TempDir(), t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "review-dismissals.json"), []byte(`{"`+folder+`":["https://x/1","https://x/2"]}`), 0o600))
	d, _ := start(t, root)
	keys := func() map[string]reviewDismissal {
		t.Helper()
		m, err := d.dismissed.Read()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	began, changed := wire.DateOf(nowFunc()), wire.DateOf(nowFunc())
	listed := func() string {
		d.setReviews(map[string][]wire.ReviewPull{folder: {
			{URL: "https://x/1", UpdatedAt: began}, {URL: "https://x/2", UpdatedAt: changed}, {URL: "https://x/3"}}})
		var urls []string
		for _, p := range d.reviewsFor(folder) {
			urls = append(urls, string(p.URL))
		}
		return strings.Join(urls, " ")
	}
	if got := listed(); got != "https://x/3" || len(keys()) != 2 {
		t.Fatalf("%q, %v", got, keys())
	}
	// 2 changes a day before the limit; its dismissal goes on from there.
	clock.add(reviewKeep - 24*time.Hour)
	changed = wire.DateOf(nowFunc())
	if got := listed(); got != "https://x/3" {
		t.Fatalf("changed: %q", got)
	}
	clock.add(24 * time.Hour)
	d.sweepSmallStores(nowFunc())
	if _, ok := keys()[reviewDismissalKey(folder, "https://x/2")]; !ok || len(keys()) != 1 {
		t.Fatalf("after 90 days: %v", keys())
	}
	if got := listed(); got != "https://x/3" {
		t.Fatalf("a dismissed pull request came back: %q", got)
	}

	must(t, d.dismissed.Update(func(m map[string]reviewDismissal) error {
		for n := range reviewDismissalsMost + 5 {
			m[fmt.Sprint(n)] = reviewDismissal{Folder: folder, At: wire.DateOf(nowFunc())}
		}
		return nil
	}))
	d.sweepSmallStores(nowFunc())
	if n := len(keys()); n != reviewDismissalsMost {
		t.Fatalf("%d kept", n)
	}
}
