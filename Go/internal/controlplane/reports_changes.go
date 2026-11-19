package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"
	"sort"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// reports/changes (#6350, epic #6348): the rows of reports/nodes whose figures changed since
// a client's cursor, for the client's local cache. The client keeps the cursor; the control
// plane keeps none (principles.md, "Sync by a changed-since cursor").
//
// The cursor is the entity tag of each machine's lineage object in the scope, as the store's
// listing says it. A read whose listing has the cursor's tags reads, decodes and merges no
// lineage: it answers from the listing alone. When some tags moved, it reads the scope's
// lineage as reports/nodes does (the read cache keeps the objects that did not move), and
// sends only the rows with a node that a changed object holds. A machine gone from the
// scope, or a cursor of another question, sends every row again (reset).
//
// A node that a machine drops from its object (its retention) is not in the new object, so
// its row is not sent; the client's daily read with no cursor (a reset) corrects it.

const changesPageMost = 100

// changesState is what a cursor saw: the question's hash, and a short hash of each lineage
// key in the scope with a short hash of its entity tag. One entry per machine in the scope.
type changesState struct {
	Q    string            `json:"q"`
	Tags map[string]string `json:"t"`
}

// changesPage is a page cursor: the change that page 1 found (the keys whose tags moved, or
// a reset), the state to send as the cursor on the last page, and the last row's ref.
type changesPage struct {
	Q       string       `json:"q"`
	Reset   bool         `json:"r,omitempty"`
	Changed []string     `json:"c,omitempty"`
	To      changesState `json:"to"`
	After   string       `json:"a"`
}

func encodeCursor(v any) string {
	data, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(s string, v any) bool {
	data, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && json.Unmarshal(data, v) == nil
}

// shortHash is 6 bytes of a string's SHA-256, so a cursor stays small.
func shortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:6])
}

// changesQuestion is the hash of what a cursor answers: who asks, the kind, repo and scope.
func changesQuestion(user string, q wire.ReportChangesQuery, scope *wire.ReportScope) string {
	sc, _ := json.Marshal(scope)
	repo := ""
	if q.Repo != nil {
		repo = *q.Repo
	}
	return shortHash(user, q.Kind, repo, string(sc))
}

func lineageTagsOf(read []store.Key) map[string]string {
	tags := make(map[string]string, len(read))
	for _, k := range read {
		tags[shortHash(k.Key)] = shortHash(k.ETag)
	}
	return tags
}

// changesReport is reports/changes for the scope the caller asks for.
func (s *Service) changesReport(ctx context.Context, q wire.ReportChangesQuery, user string) (wire.ReportChanges, error) {
	switch q.Kind {
	case lineage.KindIssue, lineage.KindPR, lineage.KindRelease:
	default:
		return wire.ReportChanges{}, fail(invalidParams, "Say a kind: issue, pr or release.")
	}
	limit := changesPageMost
	if q.Limit != nil {
		if *q.Limit < 1 || *q.Limit > changesPageMost {
			return wire.ReportChanges{}, fail(invalidParams, "Ask for 1 to %d rows a page.", changesPageMost)
		}
		limit = int(*q.Limit)
	}
	scope, keep, err := s.reportScope(ctx, q.Scope, user, s.isAdmin(user))
	if err != nil {
		return wire.ReportChanges{}, err
	}
	question := changesQuestion(user, q, scope)
	keys, read, err := s.scopeLineageKeys(ctx, keep)
	if err != nil {
		return wire.ReportChanges{}, err
	}
	now := changesState{Q: question, Tags: lineageTagsOf(read)}

	page, err := changesPageOf(q, now)
	if err != nil {
		return wire.ReportChanges{}, err
	}
	if !page.Reset && len(page.Changed) == 0 {
		// Nothing moved: no read of the lineage at all.
		cursor := encodeCursor(page.To)
		return wire.ReportChanges{Nodes: []wire.ReportNode{}, Cursor: &cursor}, nil
	}

	var changed func(store.Key) bool
	if !page.Reset {
		set := map[string]bool{}
		for _, c := range page.Changed {
			set[c] = true
		}
		changed = func(k store.Key) bool { return set[shortHash(k.Key)] }
	}
	nodes, touched, err := s.readLineageTouched(ctx, keys, read, changed)
	if err != nil {
		return wire.ReportChanges{}, err
	}
	nodes = s.projectNodes(ctx, nodes, scope)
	rows := nodeRowsTouched(nodes, q.Kind, touched)
	return changesPageCut(rows, q, page, limit), nil
}

// changesPageOf is what this read sends: from the page cursor, else what moved between the
// cursor and now, else everything.
func changesPageOf(q wire.ReportChangesQuery, now changesState) (changesPage, error) {
	if q.PageCursor != nil && *q.PageCursor != "" {
		var p changesPage
		if !decodeCursor(*q.PageCursor, &p) || p.Q != now.Q || p.After == "" {
			return changesPage{}, fail(invalidParams, "The page cursor is not from this question: ask for the first page again.")
		}
		return p, nil
	}
	p := changesPage{Q: now.Q, To: now}
	if q.Since == nil || *q.Since == "" {
		p.Reset = true
		return p, nil
	}
	var since changesState
	if !decodeCursor(*q.Since, &since) {
		return changesPage{}, fail(invalidParams, "The cursor is not from reports/changes: ask with no cursor.")
	}
	if since.Q != now.Q {
		// Another person, kind, repo or scope: everything again.
		p.Reset = true
		return p, nil
	}
	for key := range since.Tags {
		if _, ok := now.Tags[key]; !ok {
			// A machine left the scope: its nodes' rows changed, and nothing says which.
			p.Reset = true
			return p, nil
		}
	}
	for _, key := range slices.Sorted(maps.Keys(now.Tags)) {
		if since.Tags[key] != now.Tags[key] {
			p.Changed = append(p.Changed, key)
		}
	}
	return p, nil
}

// changesPageCut filters rows by repo, orders them by ref, and cuts the page after the page
// cursor's ref. The last page carries the cursor for next time.
func changesPageCut(rows []wire.ReportNode, q wire.ReportChangesQuery, page changesPage, limit int) wire.ReportChanges {
	kept := rows[:0]
	for _, r := range rows {
		if q.Repo != nil && *q.Repo != "" && !inRepo(r.Node, *q.Repo) {
			continue
		}
		if page.After != "" && r.Node.Ref <= page.After {
			continue
		}
		kept = append(kept, r)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Node.Ref < kept[j].Node.Ref })
	out := wire.ReportChanges{Nodes: []wire.ReportNode{}, Reset: page.Reset}
	if len(kept) > limit {
		kept = kept[:limit]
		next := page
		next.After = kept[len(kept)-1].Node.Ref
		cursor := encodeCursor(next)
		out.NextPageCursor = &cursor
	} else {
		cursor := encodeCursor(page.To)
		out.Cursor = &cursor
	}
	out.Nodes = append(out.Nodes, kept...)
	return out
}
