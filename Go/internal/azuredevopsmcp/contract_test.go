package azuredevopsmcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract (#6191) over the fake Azure DevOps: pull request 1 is the story's, pull
// request 2 the one that closes.

// azureDevOpsMissing are the contract's events this server does not give, with their bugs.
var azureDevOpsMissing = map[string]string{
	"pr.reviewed":                      "#6295: no event for a review (a vote for or against)",
	"pr.commented":                     "#6295: no event for a comment on a pull request",
	"pr.blocked/required_check_failed": "#6295: OpenPRs reads no failed builds or statuses",
}

func TestForgeContract(t *testing.T) {
	f := newFake(t)
	now := start
	// minute is the step's time, a minute before the clock, as at counts it.
	minute := func() int { return int(now.Sub(start)/time.Minute) - 1 }
	f.pulls = []obj{pull(2, "other", dev, nil)}
	events := &Events{AzureDevOps: f.azureDevOps(), Now: func() time.Time { return now }}
	pr := func() obj {
		for _, p := range f.pulls {
			if p["pullRequestId"] == 1 {
				return p
			}
		}
		return nil
	}
	forgecontract.Run(t, forgecontract.Server{
		Name:    "azuredevops",
		Kind:    forgecontract.CodeForge,
		Offered: EventNames(),
		Missing: azureDevOpsMissing,
		Args:    func(string) map[string]any { return map[string]any{"pr": []int{1, 2}} },
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
		Do: func(t *testing.T, step forgecontract.Step) {
			now = now.Add(2 * time.Minute)
			n := minute()
			f.set(func() {
				switch step.Name {
				case forgecontract.Open:
					f.pulls = append(f.pulls, pull(1, "feature", dev, obj{"creationDate": at(n), "description": "Fixes #1"}))
				case forgecontract.CheckFails:
					f.builds = append(f.builds, build(11, 1, "sha1", "ci", "failed", n))
				case forgecontract.CheckPasses:
					pr()["lastMergeSourceCommit"] = obj{"commitId": "sha1b"}
					f.builds = append(f.builds, build(12, 1, "sha1b", "ci", "succeeded", n))
				case forgecontract.Conflict:
					pr()["mergeStatus"] = "conflicts"
				case forgecontract.ChangesAsked:
					pr()["lastMergeSourceCommit"], pr()["mergeStatus"] = obj{"commitId": "sha1c"}, "succeeded"
					pr()["reviewers"] = []any{obj{"id": alex["id"], "displayName": "Alex", "vote": -5}}
				case forgecontract.Comment:
					f.threads[1] = append(f.threads[1], obj{"id": 70, "publishedDate": at(n), "lastUpdatedDate": at(n),
						"comments": []any{obj{"id": 1, "content": "Looks close.", "author": dev, "publishedDate": at(n), "commentType": "text"}}})
				case forgecontract.Merge:
					pr()["status"], pr()["closedDate"], pr()["lastMergeCommit"] = "completed", at(n), obj{"commitId": "merged1"}
				case forgecontract.Close:
					for _, p := range f.pulls {
						if p["pullRequestId"] == 2 {
							p["status"], p["closedDate"] = "abandoned", at(n)
						}
					}
				case forgecontract.Refused:
					f.status = 429
				}
			})
		},
	})
}
