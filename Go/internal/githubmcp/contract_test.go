package githubmcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract (#6191) over the fake GitHub: pull request 1 is the story's, pull request
// 2 the one that closes.

// githubMissing are the contract's events this server does not give: none.
var githubMissing = map[string]string{}

// refusingGitHub is the fake, with every read it makes for the state events refused too when
// the fake's err is set, as GitHub refuses every call at a rate limit.
type refusingGitHub struct{ *fakeGitHub }

func (r refusingGitHub) refused() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r refusingGitHub) BlockedPRs(ctx context.Context, repo string) ([]BlockedPR, error) {
	if err := r.refused(); err != nil {
		return nil, err
	}
	return r.fakeGitHub.BlockedPRs(ctx, repo)
}

func (r refusingGitHub) ConflictPRs(ctx context.Context, repo string) ([]ConflictPR, error) {
	if err := r.refused(); err != nil {
		return nil, err
	}
	return r.fakeGitHub.ConflictPRs(ctx, repo)
}

func (r refusingGitHub) OpenPRs(ctx context.Context, repo string) ([]OpenPR, error) {
	if err := r.refused(); err != nil {
		return nil, err
	}
	return r.fakeGitHub.OpenPRs(ctx, repo)
}

func TestForgeContract(t *testing.T) {
	f := newFake()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	at := func() string { return mcpevents.TimeString(now.Add(-time.Minute)) }
	f.addPR(2, "other", nil)
	events := &Events{GitHub: refusingGitHub{f}, Now: func() time.Time { return now }}
	pr := func() PR { f.mu.Lock(); defer f.mu.Unlock(); return f.prs[1].pr }
	forgecontract.Run(t, forgecontract.Server{
		Name:    "github",
		Kind:    forgecontract.CodeForge,
		Offered: EventNames(),
		Missing: githubMissing,
		Args:    func(string) map[string]any { return map[string]any{"pr": []int{1, 2}} },
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			result, err := events.Poll(ctx, raw)
			if err != nil {
				return nil, err
			}
			// Through JSON, as a client sees it.
			data, _ := json.Marshal(result)
			var out map[string]any
			_ = json.Unmarshal(data, &out)
			return out, nil
		},
		Do: func(t *testing.T, step forgecontract.Step) {
			now = now.Add(2 * time.Minute)
			switch step.Name {
			case forgecontract.Open:
				f.addPR(1, "feature", func(p *fakePR) { p.conflicts = new(false) })
			case forgecontract.CheckFails:
				f.addRun(100, "feature", at(), 1, true)
				f.edit(1, func(p *fakePR) {
					p.blocking = mcpevents.Blocking{HeadSHA: "sha100", FailedChecks: []mcpevents.FailedCheck{{Name: "test", URL: "https://github.com/x/1"}}}
				})
			case forgecontract.CheckPasses:
				f.addPassedRun(101, "feature", at(), 1, true)
				f.edit(1, func(p *fakePR) { p.blocking = mcpevents.Blocking{HeadSHA: "sha101"} })
			case forgecontract.Conflict:
				f.edit(1, func(p *fakePR) {
					p.conflicts = new(true)
					p.blocking = mcpevents.Blocking{HeadSHA: "sha101", Conflict: true}
				})
			case forgecontract.ChangesAsked:
				f.edit(1, func(p *fakePR) {
					p.conflicts = new(false)
					p.blocking = mcpevents.Blocking{HeadSHA: "sha102", ChangesRequested: true, ReviewURL: "https://github.com/r/1"}
				})
				f.addPRReview(PRReview{ID: 1, PR: pr(), Author: "dev", State: "changes_requested", Body: "Please change this.",
					URL: "https://github.com/r/1", Commit: "sha102", At: at(), Trusted: true})
			case forgecontract.Comment:
				f.addPRComment(PRComment{ID: 1, PR: pr(), Author: "dev", Body: "Looks close.", URL: "https://github.com/c/1", At: at(), Trusted: true})
			case forgecontract.Merge:
				f.edit(1, func(p *fakePR) { p.state, p.mergedAt = "merged", at() })
			case forgecontract.Close:
				f.edit(2, func(p *fakePR) { p.state, p.closedAt = "closed", at() })
			case forgecontract.Refused:
				f.mu.Lock()
				f.err = &GitHubError{Kind: RateLimited, Message: "API rate limit exceeded", RetryAfterMs: 60_000}
				f.mu.Unlock()
			}
		},
	})
}
