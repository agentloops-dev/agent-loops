package githubmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// A deploy that waits for an approval in Agent Loops (#5564), on any GitHub plan: GitHub Free
// has an environment's required reviewers on public repositories only, so a private starter
// cannot wait on them. Instead, a tag's release.yml makes a GitHub Deployment with the task
// deploy:approval, the payload {"workflow": "deploy.yml", "actor": <who pushed the tag>}, and a
// status pending. Approve sets the status queued and dispatches the payload's workflow at the
// Deployment's ref with the input deployment, whose job checks that status, then writes
// in_progress and success or failure as it deploys. Reject sets the status inactive. The
// Deployments API and workflow_dispatch are on every plan.

const (
	// ApprovalTask is the task of a Deployment that waits for an approval in Agent Loops.
	ApprovalTask = "deploy:approval"
	// mostApprovals bounds the approval Deployments one read lists: the newest made.
	mostApprovals = 10
	// approvalDays is how long an approval waits: an older one is not listed any more.
	approvalDays = 30
)

// DeployApproval is a Deployment that waits for an approval in Agent Loops, and the workflow
// that Approve dispatches.
type DeployApproval struct {
	Deployment Deployment
	// Workflow is the workflow file Approve dispatches, as deploy.yml.
	Workflow string
}

type approvalJSON struct {
	deploymentJSON
	Task    string          `json:"task"`
	Payload json.RawMessage `json:"payload"`
	Creator user            `json:"creator"`
}

// approvalPayload is what release.yml says in the Deployment's payload: GitHub keeps an object,
// or the text of one.
type approvalPayload struct {
	Workflow string `json:"workflow"`
	Actor    string `json:"actor"`
}

func (a approvalJSON) payload() approvalPayload {
	var p approvalPayload
	raw := a.Payload
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = json.RawMessage(text)
	}
	_ = json.Unmarshal(raw, &p)
	return p
}

// approvalWorkflow is a workflow file's name, which a dispatch puts in its path.
func approvalWorkflow(name string) bool {
	if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
		return false
	}
	for _, r := range name {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)
		if !allowed {
			return false
		}
	}
	return true
}

// approvalDeploys are the repo's Deployments that wait for an approval in Agent Loops: of the
// task deploy:approval, made in the last approvalDays, with a workflow to dispatch, and whose
// newest status is pending. Only workflow's (the file Approve dispatches) and branch's (the
// Deployment's ref) when they are set.
func (g *RealGitHub) approvalDeploys(ctx context.Context, repo, workflow, branch string) ([]WaitingDeploy, error) {
	q := url.Values{"task": {ApprovalTask}, "per_page": {strconv.Itoa(mostApprovals)}}
	if branch != "" {
		q.Set("ref", branch)
	}
	var page []approvalJSON
	if err := g.get(ctx, fmt.Sprintf("repos/%s/deployments?%s", repo, q.Encode()), true, &page); err != nil {
		return nil, err
	}
	oldest := mcpevents.Normalized(time.Now().Add(-approvalDays * 24 * time.Hour).UTC().Format(time.RFC3339))
	var out []WaitingDeploy
	for _, a := range page {
		p := a.payload()
		if a.Task != ApprovalTask || !approvalWorkflow(p.Workflow) || (workflow != "" && p.Workflow != workflow) ||
			mcpevents.Normalized(a.CreatedAt) < oldest {
			continue
		}
		var statuses []deploymentStatusJSON
		if err := g.get(ctx, fmt.Sprintf("repos/%s/deployments/%d/statuses?per_page=%d", repo, a.ID, mostStatuses), true, &statuses); err != nil {
			return nil, err
		}
		// GitHub lists the newest first.
		if len(statuses) == 0 || statuses[0].State != "pending" {
			continue
		}
		dep := Deployment{ID: a.ID, SHA: a.SHA, Ref: a.Ref, Environment: a.Environment, Production: a.Production,
			Transient: a.Transient, Created: a.CreatedAt, Updated: a.UpdatedAt}
		for i := len(statuses) - 1; i >= 0; i-- {
			s := statuses[i]
			u := s.TargetURL
			if u == "" {
				u = s.LogURL
			}
			dep.Statuses = append(dep.Statuses, DeploymentStatus{ID: s.ID, State: s.State, At: s.CreatedAt, URL: u, EnvironmentURL: s.EnvironmentURL})
		}
		pending := dep.Statuses[len(dep.Statuses)-1]
		bot := strings.HasSuffix(p.Actor, "[bot]")
		run := CompletedRun{Run: RunRef{Name: p.Workflow, URL: pending.URL, HeadSHA: a.SHA, Branch: a.Ref,
			UpdatedAt: pending.At, CreatedAt: a.CreatedAt}, Workflow: p.Workflow, Event: "deployment", Actor: p.Actor,
			Trusted: p.Actor != "" && !bot}
		out = append(out, WaitingDeploy{Run: run,
			Environments: []Environment{{Name: a.Environment, URL: fmt.Sprintf("https://github.com/%s/deployments/%s", repo, url.PathEscape(a.Environment))}},
			CanApprove:   true, Approval: &DeployApproval{Deployment: dep, Workflow: p.Workflow}})
	}
	return out, nil
}

// AnswerApproval is Approve or Reject of a Deployment that waits for an approval in Agent Loops,
// as the person, with comment as its status's description. Approve sets the status queued and
// dispatches the approval's workflow at the Deployment's ref; when GitHub does not start it, the
// status is pending again, so the approval waits as before.
func (g *RealGitHub) AnswerApproval(ctx context.Context, repo string, a DeployApproval, approve bool, comment string) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	if !approvalWorkflow(a.Workflow) || a.Deployment.ID == 0 || a.Deployment.Ref == "" {
		return &GitHubError{Kind: NotFound, Message: "name the Deployment and its workflow to answer"}
	}
	status := func(state, description string) error {
		body, _ := json.Marshal(map[string]any{"state": state, "description": description, "auto_inactive": false})
		_, err := g.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/deployments/%d/statuses", repo, a.Deployment.ID), body, false)
		return err
	}
	if !approve {
		return status("inactive", comment)
	}
	if err := status("queued", comment); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"ref": a.Deployment.Ref,
		"inputs": map[string]string{"deployment": strconv.FormatInt(a.Deployment.ID, 10)}})
	if _, err := g.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/actions/workflows/%s/dispatches", repo, a.Workflow), body, false); err != nil {
		_ = status("pending", "Not started: "+a.Workflow+" could not be dispatched")
		return err
	}
	return nil
}
