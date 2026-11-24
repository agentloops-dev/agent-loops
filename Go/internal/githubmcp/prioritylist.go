package githubmcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// A list in priority order (#6538, epic #6348): GitHub sorts by no label or field, so the list goes through each
// priority in turn, each newest first: High, then Medium, then Low, then the items with none. A priority is its
// label, priority:high and so on; an item with two is in the higher one only.
//
// Each page is one GraphQL request. The priorities are aliased connections of one query, each with its count, so
// a page that ends one priority goes on into the next in the same request. The cursor holds the priority, GitHub's
// cursor in it, how many of it were read, and the counts, so a later page asks only the priorities it reaches, and
// leaves out a priority whose count is 0.
//
// A list of one repo with no other label, feature, words, board, reviewer or time bound (and, for pull requests,
// no assignee) reads High, Medium and Low from GitHub's own list (repository.issues or pullRequests): no
// 1,000-result cap, and the main rate limit. Any other reads search, because GitHub's lists match labels any of
// and search matches all. The items with no priority read search while they are at most searchCap: a list cannot
// leave out labels. When they are more (#6637), search would stop at its cap, so they read the whole list and
// drop the ones with a priority: a page can then cost more than one request, but the list reaches its end.

// SortPriority is SearchFilter.Sort's value for a list in priority order.
const SortPriority = "priority"

// priorityLabels are the labels of each priority, in order; bucket len(priorityLabels) is the items with none.
var priorityLabels = []string{"priority:high", "priority:medium", "priority:low"}

// listSpare is how many more items than its page a priority asks for from GitHub's list, for the items that a
// higher priority has too.
const listSpare = 10

// searchCap is how many results GitHub search gives for one query at most.
const searchCap = 1000

// noPriority is the bucket of the items with no priority label.
var noPriority = len(priorityLabels)

// priorityCursorPrefix starts a cursor of a list in priority order.
const priorityCursorPrefix = "prio:"

// priorityPlace is a place in a list in priority order: the bucket (0 to noPriority), GitHub's cursor in it ("" at
// its start), how many of it were read before it, and each bucket's count (nil before the first page answered).
// List says After is a cursor of GitHub's whole list, not of search: the items with no priority, when search
// cannot reach them all (#6637).
type priorityPlace struct {
	Bucket int    `json:"b"`
	After  string `json:"a,omitempty"`
	Read   int    `json:"r,omitempty"`
	Counts []int  `json:"c,omitempty"`
	List   bool   `json:"l,omitempty"`
}

// text is the place as a cursor.
func (p priorityPlace) text() string {
	b, _ := json.Marshal(p)
	return priorityCursorPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// readPriorityCursor reads a cursor of priorityPlace.text; "" is the start of the list.
func readPriorityCursor(c string) (priorityPlace, error) {
	if c == "" {
		return priorityPlace{}, nil
	}
	var p priorityPlace
	rest, ok := strings.CutPrefix(c, priorityCursorPrefix)
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if !ok || err != nil || json.Unmarshal(b, &p) != nil || p.Bucket < 0 || p.Bucket > noPriority ||
		p.Counts != nil && len(p.Counts) != noPriority+1 {
		return priorityPlace{}, fmt.Errorf("not a cursor of a list in priority order: %q", c)
	}
	return p, nil
}

// priorityBucket is the bucket an item's labels put it in: its highest priority, else noPriority.
func priorityBucket(i Item) int {
	for n, p := range priorityLabels {
		for _, l := range i.Labels {
			if strings.EqualFold(l, p) {
				return n
			}
		}
	}
	return noPriority
}

// readsList says a list in priority order reads its priorities from GitHub's own list, not search.
func readsList(repos []string, f SearchFilter) bool {
	return len(repos) == 1 && f.Label == "" && f.Feature == "" && strings.TrimSpace(f.Query) == "" && f.Project == "" &&
		f.Reviewer == "" && f.UpdatedSince == "" && f.UpdatedBefore == "" && (!f.PRs || f.Assignee == "")
}

// nextBucket is the first bucket after b whose count is not 0, and false when none is left. With no counts, it is
// b+1.
func nextBucket(b int, counts []int) (int, bool) {
	for k := b + 1; k <= noPriority; k++ {
		if counts == nil || counts[k] > 0 {
			return k, true
		}
	}
	return 0, false
}

// bucketAsk is one bucket in a page's request: where it starts and how many it asks for. whole says the bucket
// (the items with no priority) reads the repo's whole list where the request reads lists, not search.
type bucketAsk struct {
	bucket int
	after  string
	read   int
	first  int
	whole  bool
}

// readsWhole says the items with no priority, from at's place, read the whole list: they go on in it, or they
// start and are more than search gives.
func (at priorityPlace) readsWhole(b int) bool {
	if b != noPriority {
		return false
	}
	if at.Bucket == noPriority && at.After != "" {
		return at.List
	}
	return at.Counts != nil && at.Counts[noPriority] > searchCap
}

// plan is the buckets a page from at asks for, in order: at's own, then each next one whose count is not 0 while
// the ones before it may not fill the page. Before the counts are known, every bucket from at's.
func (at priorityPlace) plan(first int) []bucketAsk {
	asks := []bucketAsk{{bucket: at.Bucket, after: at.After, read: at.Read, first: first, whole: at.readsWhole(at.Bucket)}}
	if at.Counts == nil {
		for k := at.Bucket + 1; k <= noPriority; k++ {
			asks = append(asks, bucketAsk{bucket: k, first: first})
		}
		return asks
	}
	left := at.Counts[at.Bucket] - at.Read
	for k, ok := nextBucket(at.Bucket, at.Counts); ok && left < first; k, ok = nextBucket(k, at.Counts) {
		asks = append(asks, bucketAsk{bucket: k, first: min(first, max(1, first-left)), whole: at.readsWhole(k)})
		left += at.Counts[k]
	}
	return asks
}

// fillRequests is how many requests a page of a list in priority order makes at most to fill itself.
const fillRequests = 10

// prioritySearch is one page of a list in priority order, at most first items, after the cursor after: one
// request, and more only while a page that goes on is not full (the items with no priority read from the whole
// list, which drops those with a priority, or the first page finds them more than search gives).
func (g *RealGitHub) prioritySearch(ctx context.Context, repos []string, f SearchFilter, after string, first int) (SearchPage, error) {
	first = max(1, min(first, perPage))
	p, err := g.priorityPage(ctx, repos, f, after, first)
	for n := 1; err == nil && p.Next != "" && len(p.Items) < first && n < fillRequests; n++ {
		var more SearchPage
		if more, err = g.priorityPage(ctx, repos, f, p.Next, first-len(p.Items)); err == nil {
			p.Items, p.Cursors, p.Next = append(p.Items, more.Items...), append(p.Cursors, more.Cursors...), more.Next
		}
	}
	return p, err
}

// priorityPage is one request of a list in priority order: at most first items after the cursor after.
func (g *RealGitHub) priorityPage(ctx context.Context, repos []string, f SearchFilter, after string, first int) (SearchPage, error) {
	at, err := readPriorityCursor(after)
	if err != nil {
		return SearchPage{}, &GitHubError{Kind: Failed, Message: err.Error()}
	}
	if len(repos) == 0 {
		return SearchPage{Items: []Item{}, Cursors: []string{}}, nil
	}
	asks := at.plan(first)
	q := priorityQuery(repos, f, asks, after == "")
	var data map[string]json.RawMessage
	if err := g.searchFields(ctx, q.text, q.vars, &data); err != nil {
		return SearchPage{}, err
	}
	conns := map[int]gqlEdges{}
	counts := append([]int(nil), at.Counts...)
	if counts == nil {
		counts = make([]int, noPriority+1)
	}
	for _, a := range asks {
		e, err := q.edges(data, a.bucket, a.whole)
		if err != nil {
			return SearchPage{}, err
		}
		conns[a.bucket] = e
		// The whole list counts the items with a priority too: their count is search's, from the first page.
		if !q.list || !a.whole {
			counts[a.bucket] = max(e.TotalCount, e.IssueCount)
		}
	}
	// The first page found more with no priority than search gives: they are left for the next request, which
	// reads them from the whole list (#6637), so the list does not stop at search's cap.
	if n := len(asks) - 1; q.list && n > 0 && asks[n].bucket == noPriority && !asks[n].whole && counts[noPriority] > searchCap {
		asks = asks[:n]
	}
	return pageOfBuckets(asks, conns, counts, first, q.total(data)), nil
}

// pageOfBuckets keeps at most first items of the buckets' answers, in the buckets' order, and the cursor of each.
// A bucket that goes on past its answer ends the page there, so no item of a later bucket comes before it.
func pageOfBuckets(asks []bucketAsk, conns map[int]gqlEdges, counts []int, first int, total *int) SearchPage {
	p := SearchPage{Items: []Item{}, Cursors: []string{}, Total: total}
	// placeAfter is the place after an edge of bucket b: in b, or at the next bucket's start when b ends there.
	whole := map[int]bool{}
	for _, a := range asks {
		whole[a.bucket] = a.whole
	}
	placeAfter := func(b int, cursor string, read int, ends bool) string {
		if !ends {
			return priorityPlace{Bucket: b, After: cursor, Read: read, Counts: counts, List: whole[b]}.text()
		}
		if k, ok := nextBucket(b, counts); ok {
			return priorityPlace{Bucket: k, Counts: counts}.text()
		}
		return ""
	}
	last := asks[len(asks)-1].bucket
	for _, a := range asks {
		e := conns[a.bucket]
		read := a.read
		for n, edge := range e.Edges {
			read++
			ends := n == len(e.Edges)-1 && !e.PageInfo.HasNextPage
			node := edge.Node
			if node.Number <= 0 || node.Repository.NameWithOwner == "" {
				continue
			}
			item := node.item()
			// GitHub's list matches one label, so an item with a higher priority as well is in that one.
			if priorityBucket(item) != a.bucket {
				continue
			}
			p.Items = append(p.Items, item)
			p.Cursors = append(p.Cursors, placeAfter(a.bucket, edge.Cursor, read, ends))
			if len(p.Items) == first {
				p.Next = p.Cursors[len(p.Cursors)-1]
				return p
			}
		}
		if e.PageInfo.HasNextPage {
			p.Next = priorityPlace{Bucket: a.bucket, After: e.PageInfo.EndCursor, Read: read, Counts: counts, List: a.whole}.text()
			return p
		}
		if a.bucket == last {
			p.Next = placeAfter(a.bucket, "", read, true)
		}
	}
	return p
}

// priorityRequest is a page's one GraphQL request: its text and variables, and where its answer says each bucket.
type priorityRequest struct {
	text string
	vars map[string]any
	list bool
}

// bucketAlias is a bucket's connection in the request.
func bucketAlias(b int) string { return fmt.Sprintf("b%d", b) }

// edges is bucket b's connection in the answer; whole says it is the whole list's.
func (q priorityRequest) edges(data map[string]json.RawMessage, b int, whole bool) (gqlEdges, error) {
	raw := data[bucketAlias(b)]
	if q.list && (b != noPriority || whole) {
		var repo map[string]json.RawMessage
		if json.Unmarshal(data["repository"], &repo) != nil {
			return gqlEdges{}, &GitHubError{Kind: NotFound, Message: "not found on GitHub"}
		}
		raw = repo[bucketAlias(b)]
	}
	var e gqlEdges
	if json.Unmarshal(raw, &e) != nil {
		return gqlEdges{}, &GitHubError{Kind: Failed, Message: "GitHub's answer is not JSON"}
	}
	return e, nil
}

// total is the whole list's count, when the request asked for it (a list's first page).
func (q priorityRequest) total(data map[string]json.RawMessage) *int {
	raw, ok := data["total"]
	if q.list {
		var repo map[string]json.RawMessage
		if json.Unmarshal(data["repository"], &repo) != nil {
			return nil
		}
		raw, ok = repo["total"]
	}
	var e gqlEdges
	if !ok || json.Unmarshal(raw, &e) != nil {
		return nil
	}
	n := max(e.TotalCount, e.IssueCount)
	return &n
}

// The fragments of a list's items: an issue's and a pull request's own fields.
const (
	issueFragment = `fragment I on Issue { issueState: state ` + itemFields + ` ` + issueFields + ` }`
	pullFragment  = `fragment P on PullRequest { pullState: state ` + itemFields + ` ` + pullFields + ` }`
	edgesOf       = `pageInfo { hasNextPage endCursor } edges { cursor node { __typename %s } }`
)

// priorityQuery is the one request of a page that asks for asks: each bucket as an aliased connection with its
// count, and, with withTotal, the whole list's count.
func priorityQuery(repos []string, f SearchFilter, asks []bucketAsk, withTotal bool) priorityRequest {
	q := priorityRequest{vars: map[string]any{}, list: readsList(repos, f)}
	base := SearchQuery(repos, SearchFilter{PRs: f.PRs, State: f.State, Label: f.Label, Assignee: f.Assignee, Query: f.Query,
		Feature: f.Feature, Project: f.Project, Reviewer: f.Reviewer, UpdatedSince: f.UpdatedSince, UpdatedBefore: f.UpdatedBefore})
	words := strings.TrimSuffix(base, " sort:updated-desc")
	params := []string{}
	var root, inRepo []string
	searchNode := "...I ...P"
	usesIssue, usesPull := true, true
	listNode := "...I"
	if f.PRs {
		listNode = "...P"
	}
	if q.list {
		usesIssue, usesPull = !f.PRs, f.PRs
	}
	for _, a := range asks {
		name := bucketAlias(a.bucket)
		after := ""
		if a.after != "" {
			params = append(params, "$a"+name+": String")
			q.vars["a"+name] = a.after
			after = ", after: $a" + name
		}
		if q.list && (a.bucket != noPriority || a.whole) {
			// An item with two priorities' labels is in both lists and kept in the higher only: a few more than the
			// page keep such a page full. The whole list drops every item with a priority, so it asks for a full
			// page of GitHub's.
			n := min(perPage, a.first+listSpare)
			if a.whole {
				n = perPage
			}
			args := append([]string{fmt.Sprintf("first: %d", n)}, listArgs(f)...)
			if a.bucket != noPriority {
				args = append(args, fmt.Sprintf("labels: [%q]", priorityLabels[a.bucket]))
			}
			inRepo = append(inRepo, fmt.Sprintf("%s: %s(%s%s) { totalCount "+edgesOf+" }", name, listConnection(f),
				strings.Join(args, ", "), after, listNode))
			continue
		}
		params = append(params, "$q"+name+": String!")
		q.vars["q"+name] = words + bucketWords(a.bucket) + " sort:updated-desc"
		node := searchNode
		if q.list {
			node = listNode
		}
		root = append(root, fmt.Sprintf("%s: search(query: $q%s, type: ISSUE, first: %d%s) { issueCount "+edgesOf+" }",
			name, name, a.first, after, node))
	}
	if withTotal {
		if q.list {
			inRepo = append(inRepo, fmt.Sprintf("total: %s(%s) { totalCount }", listConnection(f),
				strings.Join(append([]string{"first: 0"}, listArgs(f)...), ", ")))
		} else {
			params = append(params, "$all: String!")
			q.vars["all"] = base
			root = append(root, "total: search(query: $all, type: ISSUE, first: 0) { issueCount }")
		}
	}
	if q.list && len(inRepo) > 0 {
		owner, name, _ := strings.Cut(repos[0], "/")
		params = append(params, "$owner: String!", "$name: String!")
		q.vars["owner"], q.vars["name"] = owner, name
		if !f.PRs && f.Assignee != "" {
			params = append(params, "$assignee: String")
			q.vars["assignee"] = f.Assignee
		}
		root = append(root, "repository(owner: $owner, name: $name) { "+strings.Join(inRepo, "\n    ")+" }")
	}
	head := "query"
	if len(params) > 0 {
		head += "(" + strings.Join(params, ", ") + ")"
	}
	var frags []string
	if usesIssue {
		frags = append(frags, issueFragment)
	}
	if usesPull {
		frags = append(frags, pullFragment)
	}
	q.text = head + " {\n  " + strings.Join(root, "\n  ") + "\n}\n" + strings.Join(frags, "\n")
	return q
}

// bucketWords are search's words for a bucket: its label, and not a higher one's; for none, no priority label.
func bucketWords(b int) string {
	var w strings.Builder
	for n, l := range priorityLabels {
		switch {
		case n == b:
			fmt.Fprintf(&w, " label:%q", l)
		case n < b:
			fmt.Fprintf(&w, " -label:%q", l)
		}
	}
	return w.String()
}

// listConnection is the repo's list: issues or pullRequests.
func listConnection(f SearchFilter) string {
	if f.PRs {
		return "pullRequests"
	}
	return "issues"
}

// listArgs are a list's arguments but first, after and labels: the last updated first, its states, and an
// issue's assignee.
func listArgs(f SearchFilter) []string {
	args := []string{"orderBy: {field: UPDATED_AT, direction: DESC}"}
	if s := listStates(f); s != "" {
		args = append(args, "states: "+s)
	}
	if !f.PRs && f.Assignee != "" {
		args = append(args, "filterBy: {assignee: $assignee}")
	}
	return args
}

// gqlEdges is a GraphQL connection's page: a list's totalCount or a search's issueCount.
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

// listCursorPrefix starts a cursor of a list, last updated first, read from GitHub's own list, not search.
const listCursorPrefix = "list:"

// readsPlainList says a list, last updated first, reads GitHub's own list (repository.issues or pullRequests), not
// search, which stops at searchCap (#6637): one repo, at most one label (a list matches labels any of), and no
// words, board, reviewer or time bound; for pull requests, no assignee, which their list cannot ask.
func readsPlainList(repos []string, f SearchFilter) bool {
	return len(repos) == 1 && (f.Label == "" || f.Feature == "") && strings.TrimSpace(f.Query) == "" && f.Project == "" &&
		f.Reviewer == "" && f.UpdatedSince == "" && f.UpdatedBefore == "" && (!f.PRs || f.Assignee == "")
}

// listPage is one page of repo's own list, last updated first, at most first items after GitHub's cursor after.
func (g *RealGitHub) listPage(ctx context.Context, repo string, f SearchFilter, after string, first int) (SearchPage, error) {
	owner, name, err := ownerName(repo)
	if err != nil {
		return SearchPage{}, err
	}
	vars := map[string]any{"owner": owner, "name": name}
	params := []string{"$owner: String!", "$name: String!"}
	args := append([]string{fmt.Sprintf("first: %d", max(1, min(first, perPage)))}, listArgs(f)...)
	if after != "" {
		params, vars["after"] = append(params, "$after: String"), after
		args = append(args, "after: $after")
	}
	if !f.PRs && f.Assignee != "" {
		params, vars["assignee"] = append(params, "$assignee: String"), f.Assignee
	}
	label := f.Label
	if f.Feature != "" {
		label = "feature:" + f.Feature
	}
	if label != "" {
		args = append(args, fmt.Sprintf("labels: [%q]", label))
	}
	node, frag := "...I", issueFragment
	if f.PRs {
		node, frag = "...P", pullFragment
	}
	q := fmt.Sprintf("query(%s) {\n  repository(owner: $owner, name: $name) { l: %s(%s) { totalCount "+edgesOf+" } }\n}\n%s",
		strings.Join(params, ", "), listConnection(f), strings.Join(args, ", "), node, frag)
	var data struct {
		Repository *struct {
			L gqlEdges `json:"l"`
		} `json:"repository"`
	}
	if err := g.searchFields(ctx, q, vars, &data); err != nil {
		return SearchPage{}, err
	}
	if data.Repository == nil {
		return SearchPage{}, &GitHubError{Kind: NotFound, Message: "not found on GitHub"}
	}
	e := data.Repository.L
	p := SearchPage{Items: []Item{}, Cursors: []string{}, Total: &e.TotalCount}
	for _, edge := range e.Edges {
		if n := edge.Node; n.Number > 0 && n.Repository.NameWithOwner != "" {
			p.Items = append(p.Items, n.item())
			p.Cursors = append(p.Cursors, listCursorPrefix+edge.Cursor)
		}
	}
	if e.PageInfo.HasNextPage {
		p.Next = listCursorPrefix + e.PageInfo.EndCursor
	}
	return p, nil
}
