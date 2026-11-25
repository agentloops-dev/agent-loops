package githubmcp

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// The Timeline's read (#4172): a repo's epics, open and closed since a time, each with its
// sub-issues and their dates, and the repo's newest releases, in a few queries at once. The
// daemon puts the epics of a project's repos on one time axis and forecasts the open ones
// (lineage/forecast.go).

// TimelineChild is an epic's sub-issue: of the Item only the repo, number, state, dates (CreatedAt and
// ClosedAt) and labels (no colours), as no sub-issue is drawn (#7026); its StateReason (completed,
// not_planned or ""), and when each pull request that closes it opened.
type TimelineChild struct {
	Item
	StateReason string
	PROpenedAt  []string
}

// TimelineEpic is an epic with its dates and its sub-issues (at most 100), in GitHub's order.
type TimelineEpic struct {
	Item
	StateReason string
	Children    []TimelineChild
}

// Release is a published release: its tag, when, and its page.
type Release struct {
	Tag, PublishedAt, URL string
}

// Timeline is a repo's epics and its newest releases.
type Timeline struct {
	Epics    []TimelineEpic
	Releases []Release
	// Rest are the ids of the epics that a first read left for TimelineRest (#7027).
	Rest []string
}

// The read is in two steps (so a first open of Work ▸ Timeline or Features is not a dozen
// heavy pages one after another): a light list of the epics' ids, the open and the closed at
// the same time, then the epics with their sub-issues, timelineBatch at once by id, in
// timelineAtOnce queries at the same time. A page of epics with up to 100 sub-issues each takes
// GitHub about two seconds, whatever its size, so the time is in how many run one after another.
const (
	// timelineEpics bounds each of the two lists of epics.
	timelineEpics = 100
	// timelineBatch is how many epics one query reads with their sub-issues.
	timelineBatch = 10
	// timelineAtOnce is how many of those queries run at the same time: not one after another,
	// and not all at once against GitHub's secondary rate limit.
	timelineAtOnce = 4
	// timelineReleases is how many of the newest releases a read gives.
	timelineReleases = 50
)

const timelineListQuery = `query($owner: String!, $name: String!, $states: [IssueState!], $since: DateTime, $first: Int!, $releases: Int!) {
  repository(owner: $owner, name: $name) {
    issues(labels: ["epic"], states: $states, filterBy: {since: $since}, first: $first, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes { id number closedAt labels(first: 50) { nodes { name } } } }
    releases(first: $releases, orderBy: {field: CREATED_AT, direction: DESC}) { nodes { tagName publishedAt url isDraft } } } }`

// timelineChildFields are a sub-issue's: what its epic's block and forecast need, and no field of a list's row
// (#7026). Each one costs the read time, the priority and the list's counts the most.
const timelineChildFields = `issueState: state stateReason number createdAt closedAt repository { nameWithOwner } labels(first: 50) { nodes { name } }
      closedByPullRequestsReferences(first: 5, includeClosedPrs: true) { nodes { createdAt } }`

const timelineEpicsQuery = `query($ids: [ID!]!) {
  nodes(ids: $ids) { ... on Issue { issueState: state ` + itemFields + ` ` + issueFields + `
    subIssues(first: 100) { nodes { ` + timelineChildFields + ` } } } } }`

// timelineNode is an epic as timelineEpicsQuery gives it.
type timelineNode struct {
	gqlItem
	SubIssues struct {
		Nodes []struct {
			gqlItem
			ClosedBy struct {
				Nodes []struct {
					CreatedAt string `json:"createdAt"`
				} `json:"nodes"`
			} `json:"closedByPullRequestsReferences"`
		} `json:"nodes"`
	} `json:"subIssues"`
}

// Timeline is repo's open epics, and those closed since since (RFC 3339), with their
// sub-issues, and its newest releases (drafts left out). An open epic whose labels skip says yes to
// is left out, and its sub-issues are not read (#7026). The epics closed before from are only listed,
// in Rest, for TimelineRest to read (#7027); with "" every epic is read.
func (g *RealGitHub) Timeline(ctx context.Context, repo, since, from string, skip func(labels []string) bool) (Timeline, error) {
	owner, name, err := ownerName(repo)
	if err != nil {
		return Timeline{}, err
	}
	out := Timeline{Releases: []Release{}}
	lists, listErrs := g.timelineLists(ctx, owner, name, since)
	var ids []string
	seen := map[int]bool{}
	for n := range lists {
		if listErrs[n] != nil {
			return Timeline{}, listErrs[n]
		}
		if lists[n].Repository == nil {
			return Timeline{}, &GitHubError{Kind: NotFound, Message: "no repository " + repo}
		}
		for _, e := range lists[n].Repository.Issues.Nodes {
			if e.Number < 1 || e.ID == "" || seen[e.Number] {
				continue
			}
			seen[e.Number] = true
			// lists[0] is the open epics: a closed one is read for the forecast, whatever its labels.
			if labels, _ := labelsOf(e.Labels.Nodes); n == 0 && skip(labels) {
				continue
			}
			// Both are UTC in RFC 3339, so the strings are in the order of the times.
			if n == 1 && e.ClosedAt != "" && e.ClosedAt < from {
				out.Rest = append(out.Rest, e.ID)
				continue
			}
			ids = append(ids, e.ID)
		}
	}
	for _, r := range lists[0].Repository.Releases.Nodes {
		if !r.IsDraft && r.TagName != "" && r.PublishedAt != "" {
			out.Releases = append(out.Releases, Release{Tag: r.TagName, PublishedAt: r.PublishedAt, URL: r.URL})
		}
	}
	if out.Epics, err = g.timelineEpics(ctx, repo, ids); err != nil {
		return Timeline{}, err
	}
	return out, nil
}

// TimelineRest is t with the epics of its Rest read too (#7027). t is not changed.
func (g *RealGitHub) TimelineRest(ctx context.Context, repo string, t Timeline) (Timeline, error) {
	rest, err := g.timelineEpics(ctx, repo, t.Rest)
	if err != nil {
		return Timeline{}, err
	}
	return Timeline{Epics: slices.Concat(t.Epics, rest), Releases: t.Releases}, nil
}

// timelineEpics is the epics of ids with their sub-issues, in order.
func (g *RealGitHub) timelineEpics(ctx context.Context, repo string, ids []string) ([]TimelineEpic, error) {
	batches, err := g.timelineBatches(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := []TimelineEpic{}
	for _, batch := range batches {
		for _, e := range batch {
			// An id GitHub no longer gives (deleted, or moved out of reach) is null.
			if e == nil || e.Number < 1 {
				continue
			}
			out = append(out, timelineEpicOf(*e, repo))
		}
	}
	return out, nil
}

// timelineList is the ids of a state's epics, with the releases.
type timelineList struct {
	Repository *struct {
		Issues struct {
			Nodes []struct {
				ID       string    `json:"id"`
				Number   int       `json:"number"`
				ClosedAt string    `json:"closedAt"`
				Labels   gqlLabels `json:"labels"`
			} `json:"nodes"`
		} `json:"issues"`
		Releases struct {
			Nodes []struct {
				TagName     string `json:"tagName"`
				PublishedAt string `json:"publishedAt"`
				URL         string `json:"url"`
				IsDraft     bool   `json:"isDraft"`
			} `json:"nodes"`
		} `json:"releases"`
	} `json:"repository"`
}

// timelineLists asks for the ids: the open epics (with the releases) and those closed since
// since, at once, each with its error.
func (g *RealGitHub) timelineLists(ctx context.Context, owner, name, since string) ([2]timelineList, [2]error) {
	var lists [2]timelineList
	var listErrs [2]error
	var wg sync.WaitGroup
	for n, states := range [][]string{{"OPEN"}, {"CLOSED"}} {
		// The releases come with the open list; the other asks for one, the fewest GraphQL takes.
		vars := map[string]any{"owner": owner, "name": name, "states": states, "first": timelineEpics, "releases": 1}
		if n == 0 {
			vars["releases"] = timelineReleases
		} else {
			vars["since"] = since
		}
		wg.Go(func() { listErrs[n] = g.graphql(ctx, timelineListQuery, vars, &lists[n]) })
	}
	wg.Wait()
	return lists, listErrs
}

// timelineBatches asks for the epics with their sub-issues, a batch of ids a query, a few
// queries at once, in order. The first batch's error that failed is the one returned.
func (g *RealGitHub) timelineBatches(ctx context.Context, ids []string) ([][]*timelineNode, error) {
	batches := make([][]*timelineNode, (len(ids)+timelineBatch-1)/timelineBatch)
	batchErrs := make([]error, len(batches))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(timelineAtOnce, len(batches)) {
		wg.Go(func() {
			for b := range next {
				var data struct {
					Nodes []*timelineNode `json:"nodes"`
				}
				batch := ids[b*timelineBatch : min((b+1)*timelineBatch, len(ids))]
				batchErrs[b] = g.graphql(ctx, timelineEpicsQuery, map[string]any{"ids": batch}, &data)
				batches[b] = data.Nodes
			}
		})
	}
	for b := range batches {
		next <- b
	}
	close(next)
	wg.Wait()
	return batches, firstError(batchErrs)
}

// firstError is the first error in errs that is not nil, or nil.
func firstError(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// timelineEpicOf is an epic and its sub-issues from what timelineEpicsQuery gives.
func timelineEpicOf(e timelineNode, repo string) TimelineEpic {
	epic := TimelineEpic{Item: e.item(), StateReason: strings.ToLower(e.StateReason), Children: []TimelineChild{}}
	if epic.Repo == "" {
		epic.Repo = repo
	}
	for _, c := range e.SubIssues.Nodes {
		if c.Number < 1 || c.Repository.NameWithOwner == "" {
			continue
		}
		child := TimelineChild{Item: c.item(), StateReason: strings.ToLower(c.StateReason)}
		for _, pr := range c.ClosedBy.Nodes {
			if pr.CreatedAt != "" {
				child.PROpenedAt = append(child.PROpenedAt, pr.CreatedAt)
			}
		}
		epic.Children = append(epic.Children, child)
	}
	return epic
}
