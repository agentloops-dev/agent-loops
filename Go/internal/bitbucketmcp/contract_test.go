package bitbucketmcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract (#6191) over the fake Bitbucket: pull request 1 is the story's, pull
// request 2 the one that closes.

func TestForgeContract(t *testing.T) {
	f := newFake(t)
	now := start
	// minute is the step's time, a minute before the clock, as at counts it.
	minute := func() int { return int(now.Sub(start)/time.Minute) - 1 }
	f.pulls = []obj{pull(2, "other", alex, nil)}
	events := &Events{Bitbucket: f.bitbucket(), Now: func() time.Time { return now }}
	pr := func() obj {
		for _, p := range f.pulls {
			if p["id"] == 1 {
				return p
			}
		}
		return nil
	}
	head := func(sha string) { pr()["source"].(obj)["commit"] = obj{"hash": sha} }
	forgecontract.Run(t, forgecontract.Server{
		Name:    "bitbucket",
		Kind:    forgecontract.CodeForge,
		Offered: EventNames(),
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
					f.pulls = append(f.pulls, pull(1, "feature", alex, obj{"created_on": at(n), "updated_on": at(n), "description": "Fixes #1"}))
					f.diffstat[1] = []obj{{"status": "modified"}}
				case forgecontract.CheckFails:
					f.statuses["sha1"] = []obj{{"key": "pipeline-1", "name": "Pipeline #12 for feature", "state": "FAILED",
						"url": "https://bitbucket.org/acme/app/pipelines/results/12", "updated_on": at(n)}}
					pr()["updated_on"] = at(n)
				case forgecontract.CheckPasses:
					head("sha1b")
					f.statuses["sha1b"] = []obj{{"key": "pipeline-1", "name": "Pipeline #13 for feature", "state": "SUCCESSFUL",
						"url": "https://bitbucket.org/acme/app/pipelines/results/13", "updated_on": at(n)}}
					pr()["updated_on"] = at(n)
				case forgecontract.Conflict:
					f.diffstat[1] = []obj{{"status": "modified"}, {"status": "merge conflict"}}
					pr()["updated_on"] = at(n)
				case forgecontract.ChangesAsked:
					head("sha1c")
					f.diffstat[1] = []obj{{"status": "modified"}}
					pr()["participants"] = []any{obj{"user": dev, "role": "REVIEWER", "approved": false, "state": "changes_requested"}}
					pr()["updated_on"] = at(n)
				case forgecontract.Comment:
					f.prComments[1] = append(f.prComments[1], comment(70, dev, "Looks close.", at(n), nil))
					pr()["updated_on"] = at(n)
				case forgecontract.Merge:
					pr()["state"], pr()["updated_on"], pr()["merge_commit"] = "MERGED", at(n), obj{"hash": "merged1"}
					f.activity[1] = []obj{{"update": obj{"state": "MERGED", "date": at(n)}}}
				case forgecontract.Close:
					for _, p := range f.pulls {
						if p["id"] == 2 {
							p["state"], p["updated_on"] = "DECLINED", at(n)
						}
					}
					f.activity[2] = []obj{{"update": obj{"state": "DECLINED", "date": at(n)}}}
				case forgecontract.Refused:
					f.status = 429
				}
			})
		},
	})
}
