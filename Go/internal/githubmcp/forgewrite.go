package githubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The daemon's forge writes (#3130): a person's own clicks on the Work pages, as the signed-in
// person, next to the views' merge, review and comment (pages.go), which they reuse.

// mergeSettings are the merge methods a repo allows, and the person's default there.
type mergeSettings struct {
	Default string `json:"viewerDefaultMergeMethod"`
	Squash  bool   `json:"squashMergeAllowed"`
	Merge   bool   `json:"mergeCommitAllowed"`
	Rebase  bool   `json:"rebaseMergeAllowed"`
}

// methods are merge, squash or rebase, as the repo allows them, the default first.
func (m mergeSettings) methods() []string {
	out := []string{}
	if d := strings.ToLower(m.Default); d != "" && m.allows(d) {
		out = append(out, d)
	}
	for _, method := range []string{"squash", "merge", "rebase"} {
		if m.allows(method) && (len(out) == 0 || out[0] != method) {
			out = append(out, method)
		}
	}
	return out
}

func (m mergeSettings) allows(method string) bool {
	return map[string]bool{"squash": m.Squash, "merge": m.Merge, "rebase": m.Rebase}[method]
}

// MergeBy merges a pull request at sha by method (merge, squash or rebase) and deletes its
// branch on GitHub; with auto, GitHub does that once its checks pass. When the head is another
// commit, GitHub merges nothing.
func (g *RealGitHub) MergeBy(ctx context.Context, repo string, number int, sha, method string, auto bool) (map[string]any, error) {
	if _, _, err := ownerName(repo); err != nil {
		return nil, err
	}
	if err := checkSha(sha); err != nil {
		return nil, err
	}
	if method != "merge" && method != "squash" && method != "rebase" {
		return nil, &GitHubError{Kind: Failed, Message: "method is merge, squash or rebase"}
	}
	var p struct {
		NodeID string `json:"node_id"`
		Head   ref    `json:"head"`
	}
	if err := g.get(ctx, fmt.Sprintf("repos/%s/pulls/%d", repo, number), false, &p); err != nil {
		return nil, err
	}
	if p.Head.Sha != sha {
		return nil, headChanged(number)
	}
	if auto {
		const enable = `mutation($id: ID!, $sha: GitObjectID!, $method: PullRequestMergeMethod!) { enablePullRequestAutoMerge(input: {
  pullRequestId: $id, mergeMethod: $method, expectedHeadOid: $sha}) { pullRequest { number } } }`
		var out struct{}
		if err := g.graphql(ctx, enable, map[string]any{"id": p.NodeID, "sha": sha, "method": strings.ToUpper(method)}, &out); err != nil {
			return nil, err
		}
		return map[string]any{"merged": false, "auto": true, "url": prURL(repo, number)}, nil
	}
	body, _ := json.Marshal(map[string]string{"merge_method": method, "sha": sha})
	if _, err := g.do(ctx, http.MethodPut, fmt.Sprintf("repos/%s/pulls/%d/merge", repo, number), body, false); err != nil {
		return nil, err
	}
	// Its branch goes when it is this repo's own, as --delete-branch does.
	if p.Head.Repo != nil && p.Head.Repo.FullName == repo && p.Head.Ref != "" {
		_, _ = g.do(ctx, http.MethodDelete, fmt.Sprintf("repos/%s/git/refs/heads/%s", repo, p.Head.Ref), nil, false)
	}
	return map[string]any{"merged": true, "auto": false, "url": prURL(repo, number)}, nil
}

// DisableAutoMerge turns off a pull request's auto-merge (#4410): it merges only when a person
// merges it.
func (g *RealGitHub) DisableAutoMerge(ctx context.Context, repo string, number int) error {
	const disable = `mutation($id: ID!) { disablePullRequestAutoMerge(input: {pullRequestId: $id}) { pullRequest { number } } }`
	return g.pullMutation(ctx, repo, number, disable)
}

// MarkReady marks a draft pull request ready for review (#4410).
func (g *RealGitHub) MarkReady(ctx context.Context, repo string, number int) error {
	const ready = `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { number } } }`
	return g.pullMutation(ctx, repo, number, ready)
}

// pullMutation runs a GraphQL mutation whose one variable is the pull request's node id.
func (g *RealGitHub) pullMutation(ctx context.Context, repo string, number int, mutation string) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	var p struct {
		NodeID string `json:"node_id"`
	}
	if err := g.get(ctx, fmt.Sprintf("repos/%s/pulls/%d", repo, number), false, &p); err != nil {
		return err
	}
	var out struct{}
	return g.graphql(ctx, mutation, map[string]any{"id": p.NodeID}, &out)
}

// SetLabels adds labels to an issue or a pull request and takes others off it, by name. A label
// to take off that it does not have is not an error.
func (g *RealGitHub) SetLabels(ctx context.Context, repo string, number int, add, remove []string) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	if len(add) > 0 {
		body, _ := json.Marshal(map[string][]string{"labels": add})
		if _, err := g.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues/%d/labels", repo, number), body, false); err != nil {
			return err
		}
	}
	for _, name := range remove {
		_, err := g.do(ctx, http.MethodDelete, fmt.Sprintf("repos/%s/issues/%d/labels/%s", repo, number, url.PathEscape(name)), nil, false)
		var ge *GitHubError
		if err != nil && (!errors.As(err, &ge) || ge.Kind != NotFound) {
			return err
		}
	}
	return nil
}

// AddLabels adds labels to an issue or a pull request and gives all its labels after, from the
// write's own answer (#6644): the priority by labels needs no read before it.
func (g *RealGitHub) AddLabels(ctx context.Context, repo string, number int, add []string) ([]string, error) {
	if _, _, err := ownerName(repo); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string][]string{"labels": add})
	answer, err := g.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues/%d/labels", repo, number), body, false)
	if err != nil {
		return nil, err
	}
	var labels []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(answer, &labels); err != nil {
		return nil, fmt.Errorf("GitHub's labels of %s#%d: %w", repo, number, err)
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, l.Name)
	}
	return out, nil
}

// SetState closes an issue or a pull request, or opens it again.
func (g *RealGitHub) SetState(ctx context.Context, repo string, number int, closed bool) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"state": map[bool]string{true: "closed", false: "open"}[closed]})
	_, err := g.do(ctx, http.MethodPatch, fmt.Sprintf("repos/%s/issues/%d", repo, number), body, false)
	return err
}

// CloseAs closes an issue with GitHub's state_reason: completed or not_planned (#4404).
func (g *RealGitHub) CloseAs(ctx context.Context, repo string, number int, reason string) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"state": "closed", "state_reason": reason})
	_, err := g.do(ctx, http.MethodPatch, fmt.Sprintf("repos/%s/issues/%d", repo, number), body, false)
	return err
}

// SetBody writes an issue's or a pull request's body: a forge request's ticked line (#3865).
func (g *RealGitHub) SetBody(ctx context.Context, repo string, number int, text string) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"body": text})
	_, err := g.do(ctx, http.MethodPatch, fmt.Sprintf("repos/%s/issues/%d", repo, number), body, false)
	return err
}

// Unassign takes people off an issue or a pull request: a forge request done (#3865).
func (g *RealGitHub) Unassign(ctx context.Context, repo string, number int, logins []string) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string][]string{"assignees": logins})
	_, err := g.do(ctx, http.MethodDelete, fmt.Sprintf("repos/%s/issues/%d/assignees", repo, number), body, false)
	return err
}

// Assign adds people to an issue or a pull request, by login (#4403). GitHub leaves out, with no
// error, a login that cannot be assigned.
func (g *RealGitHub) Assign(ctx context.Context, repo string, number int, logins []string) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string][]string{"assignees": logins})
	_, err := g.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues/%d/assignees", repo, number), body, false)
	return err
}

// RepoLabels are a repo's labels, at most 100, with their colours.
func (g *RealGitHub) RepoLabels(ctx context.Context, repo string) ([]string, map[string]string, error) {
	if _, _, err := ownerName(repo); err != nil {
		return nil, nil, err
	}
	var ls []label
	if err := g.get(ctx, fmt.Sprintf("repos/%s/labels?per_page=100", repo), false, &ls); err != nil {
		return nil, nil, err
	}
	names, colors := labelsOf(ls)
	return names, colors, nil
}

// The plan's writes (#3271): a child added to an epic, new or one that is there, and a child's
// "blocked by" links. GitHub's sub-issue and dependency APIs take an issue's id, not its number.

// issueID is the id of issue number of repo.
func (g *RealGitHub) issueID(ctx context.Context, repo string, number int) (int64, error) {
	if _, _, err := ownerName(repo); err != nil {
		return 0, err
	}
	var i struct {
		ID int64 `json:"id"`
	}
	if err := g.get(ctx, fmt.Sprintf("repos/%s/issues/%d", repo, number), false, &i); err != nil {
		return 0, err
	}
	if i.ID == 0 {
		return 0, &GitHubError{Kind: NotFound, Message: fmt.Sprintf("no issue #%d in %s", number, repo)}
	}
	return i.ID, nil
}

// CreateIssue opens an issue in repo with labels (GitHub makes a label that the repo does not
// have), and is its number and URL.
func (g *RealGitHub) CreateIssue(ctx context.Context, repo, title string, labels []string) (int, string, error) {
	if _, _, err := ownerName(repo); err != nil {
		return 0, "", err
	}
	body, _ := json.Marshal(map[string]any{"title": title, "labels": append([]string{}, labels...)})
	data, err := g.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues", repo), body, false)
	if err != nil {
		return 0, "", err
	}
	var i struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if json.Unmarshal(data, &i) != nil || i.Number < 1 {
		return 0, "", &GitHubError{Kind: Failed, Message: "GitHub's answer is not an issue"}
	}
	return i.Number, i.HTMLURL, nil
}

// AddSubIssue makes issue childNumber of childRepo a sub-issue of issue number of repo; it leaves
// the parent it had, as a person who adds it to this epic means.
func (g *RealGitHub) AddSubIssue(ctx context.Context, repo string, number int, childRepo string, childNumber int) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	id, err := g.issueID(ctx, childRepo, childNumber)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"sub_issue_id": id, "replace_parent": true})
	_, err = g.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues/%d/sub_issues", repo, number), body, false)
	return err
}

// SetBlockedBy says that issue number of repo is blocked by issue blockerNumber of blockerRepo,
// or with blocked false that it is not. A link to take off that is not there is not an error.
func (g *RealGitHub) SetBlockedBy(ctx context.Context, repo string, number int, blockerRepo string, blockerNumber int, blocked bool) error {
	if _, _, err := ownerName(repo); err != nil {
		return err
	}
	id, err := g.issueID(ctx, blockerRepo, blockerNumber)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("repos/%s/issues/%d/dependencies/blocked_by", repo, number)
	if blocked {
		body, _ := json.Marshal(map[string]int64{"issue_id": id})
		_, err = g.do(ctx, http.MethodPost, path, body, false)
		return err
	}
	_, err = g.do(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", path, id), nil, false)
	var ge *GitHubError
	if err != nil && errors.As(err, &ge) && ge.Kind == NotFound {
		return nil
	}
	return err
}

// priorityOptions are the names of the Priority issue field's options that each priority
// writes, the first that the field has (#6091): high is High (Urgent is above it), and the read
// takes Urgent and Highest as high too (#5747).
var priorityOptions = map[string][]string{
	"high":   {"High", "Urgent", "Highest"},
	"medium": {"Medium"},
	"low":    {"Low", "Lowest"},
}

// SetPriorityField sets issue number of repo's Priority issue field to priority (high, medium or
// low), or clears it for "" (#6091). has is false, with no write, when the repo has no Priority
// field, or the GitHub has no issue fields (GitHub Enterprise Server): the caller writes the
// labels then. The repo's field and the issue's id are kept (#6644), so a change in a repo with
// no field asks nothing here, and one with the field asks only the write.
func (g *RealGitHub) SetPriorityField(ctx context.Context, repo string, number int, priority string) (has bool, err error) {
	if _, _, err := ownerName(repo); err != nil {
		return false, err
	}
	if g.noIssueFields.Load() {
		return false, nil
	}
	f, issue, err := g.priorityFieldOf(ctx, repo, number)
	if err != nil || f == nil {
		return false, err
	}
	defer func() {
		if err != nil {
			g.fields.forget(repo)
		}
	}()
	if priority == "" {
		const clear = `mutation($issue: ID!, $field: ID!) { deleteIssueFieldValue(input: {issueId: $issue, fieldId: $field}) { success } }`
		var out map[string]any
		return true, g.graphql(ctx, clear, map[string]any{"issue": issue, "field": f.ID}, &out)
	}
	for _, want := range priorityOptions[priority] {
		for _, o := range f.Options {
			if !strings.EqualFold(strings.TrimSpace(o.Name), want) {
				continue
			}
			const set = `mutation($issue: ID!, $field: ID!, $option: ID!) { setIssueFieldValue(input: {issueId: $issue,
  issueFields: [{fieldId: $field, singleSelectOptionId: $option}]}) { issue { number } } }`
			var out map[string]any
			return true, g.graphql(ctx, set, map[string]any{"issue": issue, "field": f.ID, "option": o.ID}, &out)
		}
	}
	return true, &GitHubError{Kind: Failed, Message: fmt.Sprintf("the Priority field of %s has no option for %s", repo, priority)}
}

// priorityFieldOf is repo's Priority issue field (nil where it has none, or the GitHub has no
// issue fields) and, where it has one, issue number's id: from the cache when it has them, else
// by one read, which it keeps.
func (g *RealGitHub) priorityFieldOf(ctx context.Context, repo string, number int) (*issueField, string, error) {
	owner, name, _ := ownerName(repo)
	f, known := g.fields.field(repo)
	if known && f == nil {
		return nil, "", nil
	}
	if issue, ok := g.fields.issue(repo, number); known && ok {
		return f, issue, nil
	}
	vars := map[string]any{"owner": owner, "name": name, "number": number}
	var data struct {
		Repository *struct {
			Issue *struct {
				ID string `json:"id"`
			} `json:"issue"`
			IssueFields *struct {
				Nodes []issueField `json:"nodes"`
			} `json:"issueFields"`
		} `json:"repository"`
	}
	query := `query($owner: String!, $name: String!, $number: Int!) { repository(owner: $owner, name: $name) {
  issue(number: $number) { id }
  issueFields(first: 25) { nodes { ... on IssueFieldSingleSelect { id name options { id name } } } } } }`
	if known {
		query = `query($owner: String!, $name: String!, $number: Int!) { repository(owner: $owner, name: $name) { issue(number: $number) { id } } }`
	}
	if err := g.graphql(ctx, query, vars, &data); err != nil {
		var ge *GitHubError
		if !known && errors.As(err, &ge) && ge.issueFields {
			if strings.Contains(ge.Message, "doesn't exist on type") {
				g.noIssueFields.Store(true)
			}
			return nil, "", nil
		}
		return nil, "", err
	}
	if data.Repository == nil || data.Repository.Issue == nil {
		return nil, "", &GitHubError{Kind: NotFound, Message: fmt.Sprintf("%s#%d is not an issue on GitHub", repo, number)}
	}
	if !known {
		f = nil
		if data.Repository.IssueFields != nil {
			for _, n := range data.Repository.IssueFields.Nodes {
				if n.ID != "" && strings.EqualFold(n.Name, priorityField) {
					f = &n
					break
				}
			}
		}
		g.fields.keep(repo, f)
	}
	if f == nil {
		return nil, "", nil
	}
	g.fields.keepIssue(repo, number, data.Repository.Issue.ID)
	return f, data.Repository.Issue.ID, nil
}
