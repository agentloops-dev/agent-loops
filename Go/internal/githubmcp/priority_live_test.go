//go:build live

package githubmcp

import (
	"context"
	"testing"
	"time"
)

// The Priority issue field's write on a real GitHub (#6091), run by hand, never by CI:
//
//	AGENTS_LIVE_GITHUB_TOKEN=$(gh auth token) go test -tags live -run LivePriority -v ./internal/githubmcp/
//
// It opens an issue on the test repo (which has the org's Priority field), sets it to each
// priority and to none, reads it back each time, and closes the issue.
func TestLivePriorityField(t *testing.T) {
	g, repo := liveGitHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	n, url, err := g.CreateIssue(ctx, repo, "Live #6091: a priority ("+time.Now().UTC().Format("2006-01-02 15:04")+")", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		if err := g.SetState(context.Background(), repo, n, true); err != nil {
			t.Errorf("close #%d: %v", n, err)
		}
	})
	t.Logf("issue %s", url)
	for _, p := range []string{"high", "medium", "low", ""} {
		has, err := g.SetPriorityField(ctx, repo, n, p)
		if err != nil || !has {
			t.Fatalf("set %q: has %v, %v", p, has, err)
		}
		d, err := g.ItemPage(ctx, repo, n, "")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if d.Priority != p || !d.PriorityField {
			t.Fatalf("set %q, read %q (field %v)", p, d.Priority, d.PriorityField)
		}
	}
}
