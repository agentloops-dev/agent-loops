package gitlabmcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract (#6191) over the fake GitLab: merge request 1 is the story's, merge
// request 2 the one that closes.

// gitlabMissing are the contract's events this server does not give: none (#6296).
var gitlabMissing = map[string]string{}

func TestForgeContract(t *testing.T) {
	f := newFake(t)
	f.addMembers()
	now := start
	at := func() string { return mcpevents.TimeString(now.Add(-time.Minute)) }
	f.mrs = []obj{mrJSON(2, "other", alex, nil)}
	events := &Events{GitLab: f.gitlab(), Now: func() time.Time { return now }}
	mr := func() obj { o, _ := byIID(f.mrs, "iid", "1"); return o }
	forgecontract.Run(t, forgecontract.Server{
		Name:    "gitlab",
		Kind:    forgecontract.CodeForge,
		Offered: EventNames(),
		Missing: gitlabMissing,
		Args:    func(string) map[string]any { return map[string]any{"pr": []int{1, 2}} },
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
		Do: func(t *testing.T, step forgecontract.Step) {
			now = now.Add(2 * time.Minute)
			f.set(func() {
				switch step.Name {
				case forgecontract.Open:
					f.mrs = append(f.mrs, mrJSON(1, "feature", alex, obj{"created_at": at(), "updated_at": at(), "description": "Closes #1"}))
				case forgecontract.CheckFails:
					f.pipes = append(f.pipes, obj{"id": 100, "status": "failed", "ref": "refs/merge-requests/1/head", "sha": "head1",
						"web_url": "https://gitlab.test/p/100", "updated_at": at(), "source": "merge_request_event"})
					f.jobs[100] = []obj{{"id": 1, "name": "test", "status": "failed", "web_url": "https://gitlab.test/j/1"}}
					mr()["detailed_merge_status"], mr()["updated_at"] = "ci_must_pass", at()
				case forgecontract.CheckPasses:
					f.pipes = append(f.pipes, obj{"id": 101, "status": "success", "ref": "refs/merge-requests/1/head", "sha": "head1b",
						"web_url": "https://gitlab.test/p/101", "updated_at": at(), "source": "merge_request_event"})
					mr()["sha"], mr()["detailed_merge_status"], mr()["updated_at"] = "head1b", "mergeable", at()
				case forgecontract.Conflict:
					mr()["has_conflicts"], mr()["detailed_merge_status"], mr()["merge_status"], mr()["updated_at"] = true, "conflict", "cannot_be_merged", at()
				case forgecontract.ChangesAsked:
					mr()["sha"], mr()["has_conflicts"], mr()["merge_status"] = "head1c", false, "can_be_merged"
					mr()["detailed_merge_status"], mr()["updated_at"] = "requested_changes", at()
					f.reviewers[1] = []obj{{"user": dev, "state": "requested_changes"}}
				case forgecontract.Comment:
					note := obj{"id": 70, "body": "Looks close.", "author": dev, "created_at": at(), "system": false, "noteable_type": "MergeRequest", "noteable_iid": 1}
					f.discuss[1] = append(f.discuss[1], obj{"id": "d1", "notes": []obj{note}})
					f.activity = append([]obj{{"created_at": at(), "note": note}}, f.activity...)
					mr()["updated_at"] = at()
				case forgecontract.Merge:
					mr()["state"], mr()["merged_at"], mr()["updated_at"], mr()["merge_commit_sha"] = "merged", at(), at(), "merged1"
				case forgecontract.Close:
					o, _ := byIID(f.mrs, "iid", "2")
					o["state"], o["closed_at"], o["updated_at"] = "closed", at(), at()
				case forgecontract.Refused:
					f.status = 429
				}
			})
		},
	})
}
