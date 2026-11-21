package githubmcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// A list in priority order (#6358, epic #6348): GitHub sorts by no label or field, so the list goes through
// each priority in turn, each newest first: High, then Medium, then Low, then the items with none. Each page is
// one request. A priority is its label, priority:high and so on. Its cursor says the priority and GitHub's
// cursor in it; a page that ends a priority is short, and the next page starts the next priority.
//
// A list of one repo with no other label, feature, words, board or reviewer (and, for pull requests, no
// assignee) reads GitHub's own list (repository.issues or pullRequests): no 1,000-result cap, and the main rate
// limit. Any other reads search, whose labels match all at once; GitHub's lists match labels any of.

// SortPriority is SearchFilter.Sort's value for a list in priority order.
const SortPriority = "priority"

// priorityLabels are the labels of each priority, in order; the last, none, is the items with none of them.
var priorityLabels = []string{"priority:high", "priority:medium", "priority:low"}

// priorityCursorPrefix starts a cursor of a list in priority order.
const priorityCursorPrefix = "prio:"

// priorityCursor is a cursor in a list in priority order: the priority (0 to 3) and GitHub's cursor in it.
func priorityCursor(bucket int, after string) string {
	return priorityCursorPrefix + strconv.Itoa(bucket) + ":" + after
}

// readPriorityCursor reads a cursor of priorityCursor; "" is the start of the list.
func readPriorityCursor(c string) (bucket int, after string, err error) {
	if c == "" {
		return 0, "", nil
	}
	rest, ok := strings.CutPrefix(c, priorityCursorPrefix)
	b, after, found := strings.Cut(rest, ":")
	n, convErr := strconv.Atoi(b)
	if !ok || !found || convErr != nil || n < 0 || n > len(priorityLabels) {
		return 0, "", fmt.Errorf("not a cursor of a list in priority order: %q", c)
	}
	return n, after, nil
}

// hasPriorityLabel says an item has a priority's label, so a list showed it before the items with none.
func hasPriorityLabel(i Item) bool {
	for _, l := range i.Labels {
		for _, p := range priorityLabels {
			if strings.EqualFold(l, p) {
				return true
			}
		}
	}
	return false
}

// readsList says a list in priority order reads GitHub's own list, not search.
func readsList(repos []string, f SearchFilter) bool {
	return len(repos) == 1 && f.Label == "" && f.Feature == "" && strings.TrimSpace(f.Query) == "" && f.Project == "" &&
		f.Reviewer == "" && (!f.PRs || f.Assignee == "")
}

// prioritySearch is one page of a list in priority order, at most first items, after the cursor after.
func (g *RealGitHub) prioritySearch(ctx context.Context, repos []string, f SearchFilter, after string, first int) (SearchPage, error) {
	bucket, at, err := readPriorityCursor(after)
	if err != nil {
		return SearchPage{}, err
	}
	first = max(1, min(first, perPage))
	if len(repos) == 0 {
		return SearchPage{Items: []Item{}, Cursors: []string{}}, nil
	}
	list := readsList(repos, f)
	// The items with no priority are read from the whole list, less those with one: a page of those only is
	// read again, at most a few times, so the list never stops on an empty page.
	for tries := 0; ; tries++ {
		var page bucketPage
		if list {
			page, err = g.priorityList(ctx, repos[0], f, bucket, at, first, after == "")
		} else {
			page, err = g.priorityBySearch(ctx, repos, f, bucket, at, first, after == "")
		}
		if err != nil {
			return SearchPage{}, err
		}
		p := SearchPage{Items: []Item{}, Cursors: []string{}, Total: page.total}
		for i, item := range page.items {
			if bucket == len(priorityLabels) && list && hasPriorityLabel(item) {
				continue
			}
			p.Items = append(p.Items, item)
			p.Cursors = append(p.Cursors, priorityCursor(bucket, page.cursors[i]))
		}
		switch {
		case page.next != "":
			p.Next = priorityCursor(bucket, page.next)
		case bucket < len(priorityLabels):
			p.Next = priorityCursor(bucket+1, "")
		}
		if len(p.Items) > 0 || p.Next == "" || tries >= 4 {
			return p, nil
		}
		bucket, at, _ = readPriorityCursor(p.Next)
		after = p.Next
	}
}

// bucketPage is a page of one priority: its items, each one's cursor, the next page's cursor ("" at the end of
// the priority), and the whole list's count on the list's first page.
type bucketPage struct {
	items   []Item
	cursors []string
	next    string
	total   *int
}

// gqlEdges is a GraphQL connection's page.
type gqlEdges struct {
	TotalCount int `json:"totalCount"`
	IssueCount int `json:"issueCount"`
	PageInfo   struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Edges []struct {
		Cursor string  `json:"cursor"`
		Node   gqlItem `json:"node"`
	} `json:"edges"`
}

func (e gqlEdges) page() bucketPage {
	p := bucketPage{items: []Item{}, cursors: []string{}}
	for _, edge := range e.Edges {
		if n := edge.Node; n.Number > 0 && n.Repository.NameWithOwner != "" {
			p.items = append(p.items, n.item())
			p.cursors = append(p.cursors, edge.Cursor)
		}
	}
	if e.PageInfo.HasNextPage {
		p.next = e.PageInfo.EndCursor
	}
	return p
}

// listStates are GitHub's list states for a filter's state: open (for ""), closed, or all (none).
func listStates(f SearchFilter) string {
	switch f.State {
	case "", "open":
		return "[OPEN]"
	case "closed":
		if f.PRs {
			return "[CLOSED, MERGED]"
		}
		return "[CLOSED]"
	}
	return ""
}

// priorityList reads one page of one priority from the repo's own list, newest first, with the list's count on
// its first page in the same request.
func (g *RealGitHub) priorityList(ctx context.Context, repo string, f SearchFilter, bucket int, after string, first int,
	withTotal bool) (bucketPage, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return bucketPage{}, fmt.Errorf("not a repo: %q", repo)
	}
	conn, fields := "issues", `__typename issueState: state `+itemFields+` `+issueFields
	if f.PRs {
		conn, fields = "pullRequests", `__typename pullState: state `+itemFields+` `+pullFields
	}
	args := []string{"first: $first", "after: $after", "orderBy: {field: UPDATED_AT, direction: DESC}"}
	common := []string{}
	if s := listStates(f); s != "" {
		common = append(common, "states: "+s)
	}
	if !f.PRs && f.Assignee != "" {
		common = append(common, "filterBy: {assignee: $assignee}")
	}
	if bucket < len(priorityLabels) {
		args = append(args, fmt.Sprintf("labels: [%q]", priorityLabels[bucket]))
	}
	total := ""
	if withTotal {
		total = fmt.Sprintf("total: %s(%s) { totalCount }", conn, strings.Join(append([]string{"first: 0"}, common...), ", "))
	}
	query := fmt.Sprintf(`query($owner: String!, $name: String!, $first: Int!, $after: String%s) {
  repository(owner: $owner, name: $name) {
    page: %s(%s) { pageInfo { hasNextPage endCursor } edges { cursor node { %s } } }
    %s } }`, map[bool]string{true: ", $assignee: String", false: ""}[!f.PRs && f.Assignee != ""],
		conn, strings.Join(append(args, common...), ", "), fields, total)
	vars := map[string]any{"owner": owner, "name": name, "first": first}
	if after != "" {
		vars["after"] = after
	}
	if !f.PRs && f.Assignee != "" {
		vars["assignee"] = f.Assignee
	}
	var data struct {
		Repository struct {
			Page  gqlEdges  `json:"page"`
			Total *gqlEdges `json:"total"`
		} `json:"repository"`
	}
	if err := g.graphqlFields(ctx, query, vars, &data); err != nil {
		return bucketPage{}, err
	}
	p := data.Repository.Page.page()
	if data.Repository.Total != nil {
		p.total = &data.Repository.Total.TotalCount
	}
	return p, nil
}

// priorityBySearch reads one page of one priority by search (its labels match all at once), with the list's
// count on its first page in the same request.
func (g *RealGitHub) priorityBySearch(ctx context.Context, repos []string, f SearchFilter, bucket int, after string, first int,
	withTotal bool) (bucketPage, error) {
	base := SearchQuery(repos, SearchFilter{PRs: f.PRs, State: f.State, Label: f.Label, Assignee: f.Assignee, Query: f.Query,
		Feature: f.Feature, Project: f.Project, Reviewer: f.Reviewer})
	words := strings.TrimSuffix(base, " sort:updated-desc")
	if bucket < len(priorityLabels) {
		words += fmt.Sprintf(" label:%q", priorityLabels[bucket])
	} else {
		for _, l := range priorityLabels {
			words += fmt.Sprintf(" -label:%q", l)
		}
	}
	total := ""
	if withTotal {
		total = `total: search(query: $all, type: ISSUE, first: 0) { issueCount }`
	}
	query := `query($q: String!, $first: Int!, $after: String` + map[bool]string{true: `, $all: String!`, false: ``}[withTotal] + `) {
  page: search(query: $q, type: ISSUE, first: $first, after: $after) { pageInfo { hasNextPage endCursor } edges { cursor node {
    __typename ... on Issue { issueState: state ` + itemFields + ` ` + issueFields + ` }
    ... on PullRequest { pullState: state ` + itemFields + ` ` + pullFields + ` } } } }
  ` + total + ` }`
	vars := map[string]any{"q": words + " sort:updated-desc", "first": first}
	if after != "" {
		vars["after"] = after
	}
	if withTotal {
		vars["all"] = base
	}
	var data struct {
		Page  gqlEdges  `json:"page"`
		Total *gqlEdges `json:"total"`
	}
	if err := g.graphqlFields(ctx, query, vars, &data); err != nil {
		return bucketPage{}, err
	}
	p := data.Page.page()
	if data.Total != nil {
		p.total = &data.Total.IssueCount
	}
	return p, nil
}
