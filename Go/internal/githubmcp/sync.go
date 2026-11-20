package githubmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The reads a client cache syncs by (#6349, epic #6348): what changed since a time, and the
// checks of many pull requests at once, which do not move a pull request's updatedAt.

// ChecksMost bounds the pull requests of one PullChecks call.
const ChecksMost = 100

// pullChecksFields are a pull request's state and its checks summary, as pullFields says them.
const pullChecksFields = `number pullState: state isDraft mergeable reviewDecision autoMergeRequest { enabledAt }
  checkState: commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }`

// PullChecks are the state and the checks summary of each of repo's pull requests numbers (at
// most ChecksMost), in one GraphQL call, by alias: one call however many there are, not one for
// each. A number that is not a pull request of repo is left out.
func (g *RealGitHub) PullChecks(ctx context.Context, repo string, numbers []int) ([]Item, error) {
	owner, name, err := ownerName(repo)
	if err != nil {
		return nil, err
	}
	if len(numbers) == 0 {
		return []Item{}, nil
	}
	if len(numbers) > ChecksMost {
		return nil, &GitHubError{Kind: Failed, Message: fmt.Sprintf("GitHub: at most %d pull requests a call", ChecksMost)}
	}
	var b strings.Builder
	b.WriteString(`query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) {`)
	for _, n := range numbers {
		fmt.Fprintf(&b, " p%d: pullRequest(number: %d) { %s }", n, n, pullChecksFields)
	}
	b.WriteString(" } }")
	var data struct {
		Repository map[string]*gqlItem `json:"repository"`
	}
	// A number that is not a pull request is an error on its alias, and null in the data: the
	// others still come, so the data is read with what GitHub says of them.
	if err := g.graphqlPartial(ctx, b.String(), map[string]any{"owner": owner, "name": name}, &data); err != nil {
		return nil, err
	}
	if data.Repository == nil {
		return nil, &GitHubError{Kind: NotFound, Message: "not found on GitHub"}
	}
	out := []Item{}
	for _, n := range numbers {
		node := data.Repository[fmt.Sprintf("p%d", n)]
		if node == nil || node.Number == 0 {
			continue
		}
		node.Typename = "PullRequest"
		node.Repository.NameWithOwner = repo
		out = append(out, node.item())
	}
	return out, nil
}

// Newest is a mark of repo's newest issue or pull request updated at or after since (RFC 3339):
// the same mark says nothing in repo changed since then, as an edit makes the item edited the
// newest. It is read by REST with its ETag kept, so while nothing changes GitHub answers 304,
// which costs no rate limit (#6349). The mark is a hash of GitHub's answer; "" when nothing was
// updated since.
func (g *RealGitHub) Newest(ctx context.Context, repo, since string) (string, error) {
	if _, _, err := ownerName(repo); err != nil {
		return "", err
	}
	q := url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"1"}, "since": {since}}
	body, err := g.do(ctx, http.MethodGet, "repos/"+repo+"/issues?"+q.Encode(), nil, true)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(body)) == "[]" {
		return "", nil
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// graphqlPartial is graphql for a query of many aliases: an alias GitHub does not find (an
// error of type NOT_FOUND) is null in the data and the rest is read; any other error is one.
func (g *RealGitHub) graphqlPartial(ctx context.Context, query string, vars map[string]any, into any) error {
	answer, err := g.graphqlAnswer(ctx, query, vars)
	if err != nil {
		return err
	}
	for _, e := range answer.Errors {
		if e.Type != "NOT_FOUND" {
			return &GitHubError{Kind: Failed, Message: "GitHub: " + e.Message}
		}
	}
	if len(answer.Data) == 0 || string(answer.Data) == "null" || json.Unmarshal(answer.Data, into) != nil {
		return &GitHubError{Kind: Failed, Message: "GitHub's answer is not JSON"}
	}
	return nil
}
