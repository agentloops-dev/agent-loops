//go:build live

package linearmcp

import (
	"context"
	"os"
	"testing"
	"time"
)

// An issue's priority on a real Linear workspace (#6091), run by hand, never by CI:
//
//	AGENTS_LIVE_LINEAR_KEY=lin_api_… AGENTS_LIVE_LINEAR_TEAM=AGE go test -tags live -run LivePriority -v ./internal/linearmcp/
//
// It opens an issue in the team, sets it to each priority and to none, reads it back each
// time, and closes it.
func TestLivePriority(t *testing.T) {
	key, team := os.Getenv("AGENTS_LIVE_LINEAR_KEY"), os.Getenv("AGENTS_LIVE_LINEAR_TEAM")
	if key == "" || team == "" {
		t.Skip("AGENTS_LIVE_LINEAR_KEY and AGENTS_LIVE_LINEAR_TEAM are not set")
	}
	l := NewRealLinear("", func() string { return key })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var teams struct {
		Teams struct{ Nodes []struct{ ID string } } `json:"teams"`
	}
	if err := l.query(ctx, `query($key: String!) { teams(filter: {key: {eq: $key}}) { nodes { id } } }`,
		map[string]any{"key": team}, &teams); err != nil || len(teams.Teams.Nodes) != 1 {
		t.Fatalf("team %s: %+v, %v", team, teams, err)
	}
	var made struct {
		IssueCreate struct {
			Issue struct{ Identifier, URL string } `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := l.query(ctx, `mutation($team: String!, $title: String!) { issueCreate(input: {teamId: $team, title: $title}) { issue { identifier url } } }`,
		map[string]any{"team": teams.Teams.Nodes[0].ID, "title": "Live #6091: a priority (" + time.Now().UTC().Format("2006-01-02 15:04") + ")"}, &made); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := made.IssueCreate.Issue.Identifier
	t.Cleanup(func() {
		if err := l.SetState(context.Background(), id, true); err != nil {
			t.Errorf("close %s: %v", id, err)
		}
	})
	t.Logf("issue %s", made.IssueCreate.Issue.URL)
	for _, p := range []string{"high", "medium", "low", ""} {
		if err := l.SetPriority(ctx, id, p); err != nil {
			t.Fatalf("set %q: %v", p, err)
		}
		d, err := l.ItemPage(ctx, id, "")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if d.Priority != p {
			t.Fatalf("set %q, read %q", p, d.Priority)
		}
	}
}
