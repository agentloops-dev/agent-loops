package forgejomcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract (#6191) over the fake Forgejo: pull request 1 is the story's, pull
// request 2 the one that closes.

// forgejoMissing are the contract's events this server does not give, with their bugs.
var forgejoMissing = map[string]string{ //nolint:gosec // G101: event names and bugs, not credentials
	"checks.passed":                    "#6294: no event for a pull request whose checks passed",
	"pr.reviewed":                      "#6294: no event for a review (an approval or a request for changes)",
	"pr.commented":                     "#6294: no event for a comment on a pull request",
	"pr.blocked/required_check_failed": "#6294: the open pull requests' read takes no combined status",
	"pr.blocked/changes_requested":     "#6294: the open pull requests' read takes no REQUEST_CHANGES review",
}

func TestForgeContract(t *testing.T) {
	f := newFake(t)
	now := start
	clock := func() time.Time { return now }
	at := func() string { return now.Add(-time.Minute).Format(time.RFC3339) }
	f.pulls = []obj{pull(2, "other", dev, nil)}
	events := &Events{Forgejo: f.forgejo(clock), Now: clock}
	pr := func(n int) obj {
		for _, p := range f.pulls {
			if p["number"] == n {
				return p
			}
		}
		t.Fatalf("no pull request %d", n)
		return nil
	}
	forgecontract.Run(t, forgecontract.Server{
		Name:    "forgejo",
		Kind:    forgecontract.CodeForge,
		Offered: EventNames(),
		Missing: forgejoMissing,
		Args:    func(string) map[string]any { return map[string]any{"pr": []int{1, 2}} },
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
		// Forgejo trusts a not-mergeable pull request as a conflict only a while later (conflictAfter).
		Settle: func() { now = now.Add(conflictAfter + time.Second) },
		Do: func(t *testing.T, step forgecontract.Step) {
			now = now.Add(2 * time.Minute)
			f.set(func() {
				switch step.Name {
				case forgecontract.Open:
					f.pulls = append(f.pulls, pull(1, "feature", dev, obj{"body": "Closes #1", "created_at": at(), "updated_at": at()}))
				case forgecontract.CheckFails:
					f.statuses["sha1"] = obj{"state": "failure", "sha": "sha1", "statuses": []any{
						obj{"context": "ci / test (pull_request)", "status": "failure", "target_url": "https://forgejo.test/acme/app/actions/runs/12/jobs/0", "updated_at": at()},
					}}
					pr(1)["updated_at"] = at()
				case forgecontract.CheckPasses:
					pr(1)["head"].(obj)["sha"], pr(1)["updated_at"] = "sha1b", at()
					f.statuses["sha1b"] = obj{"state": "success", "sha": "sha1b", "statuses": []any{
						obj{"context": "ci / test (pull_request)", "status": "success", "target_url": "https://forgejo.test/acme/app/actions/runs/13/jobs/0", "updated_at": at()},
					}}
				case forgecontract.Conflict:
					pr(1)["mergeable"], pr(1)["updated_at"] = false, at()
				case forgecontract.ChangesAsked:
					pr(1)["head"].(obj)["sha"], pr(1)["mergeable"], pr(1)["updated_at"] = "sha1c", true, at()
					f.statuses["sha1c"] = obj{"state": "success", "sha": "sha1c", "statuses": []any{}}
					f.reviews[1] = []obj{{"id": 40, "user": dev, "state": "REQUEST_CHANGES", "body": "Please change this.", "comments_count": 0,
						"commit_id": "sha1c", "submitted_at": at(), "updated_at": at()}}
				case forgecontract.Comment:
					f.comments = append(f.comments, obj{"id": 70, "body": "Looks close.", "user": dev,
						"issue_url": "https://forgejo.test/api/v1/repos/acme/app/issues/1", "pull_request_url": "https://forgejo.test/acme/app/pulls/1",
						"html_url": "https://forgejo.test/acme/app/pulls/1#issuecomment-70", "created_at": at(), "updated_at": at()})
					pr(1)["updated_at"] = at()
				case forgecontract.Merge:
					p := pr(1)
					p["state"], p["merged"], p["merged_at"], p["updated_at"], p["merge_commit_sha"] = "closed", true, at(), at(), "m1"
				case forgecontract.Close:
					p := pr(2)
					p["state"], p["closed_at"], p["updated_at"] = "closed", at(), at()
				case forgecontract.Refused:
					f.status = 429
				}
			})
		},
	})
}
