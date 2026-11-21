package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// pageSize is the rows loops ls shows, and each page --all reads (bounds, Stage 3).
const pageSize = 20

// listSessions is the folder's sessions, newest activity first, as agents/list answers
// them: one page of pageSize, or with all every page, archived ones too. more says a
// page with no all was full, so there may be others.
func listSessions(ctx context.Context, c caller, p place, all bool) (list []wire.Agent, more bool, err error) {
	limit := int64(pageSize)
	req := wire.ListRequest{Folder: &p.folder, Limit: &limit, Lean: true, IncludeArchived: all}
	for {
		r, err := c.Call(ctx, string(p.machine.ID), "agents/list", req)
		if err != nil {
			return nil, false, unreachable(p.machineName(), err)
		}
		var page []wire.Agent
		if err := json.Unmarshal(r, &page); err != nil {
			return nil, false, err
		}
		list = append(list, page...)
		if len(page) < pageSize {
			return list, false, nil
		}
		if !all {
			return list, true, nil
		}
		last := page[len(page)-1]
		req.After = &wire.ListCursor{LastActivityAt: last.LastActivityAt, ID: last.ID}
	}
}

// printList is state 4 of the wireframes, or state 5 when the folder has no session.
func printList(w io.Writer, p place, list []wire.Agent, more, all bool, now time.Time) {
	if len(list) == 0 {
		fmt.Fprintf(w, "%s · %s · no sessions yet\n\nStart one: loops \"what this folder needs\"\n", p.folderName(), p.machineName())
		return
	}
	count := fmt.Sprintf("%d sessions", len(list))
	if len(list) == 1 {
		count = "1 session"
	}
	if more {
		count = fmt.Sprintf("the %d most recent sessions", len(list))
	}
	fmt.Fprintf(w, "%s · %s · %s\n\n", p.folderName(), p.machineName(), count)
	ids := shortIDs(list)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  STATE\tLAST\tTITLE\tID")
	for i, a := range list {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", stateWord(a.State), ago(now, a.LastActivityAt.Time()), titleOf(a), ids[i])
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	switch {
	case all:
		fmt.Fprintln(w, "loops -r <id> to continue one")
	default:
		fmt.Fprintln(w, "loops -r <id> to continue one · --all for archived")
	}
}

// stateWord is a session's state as the wireframes say it.
func stateWord(s wire.AgentState) string {
	switch s {
	case wire.AgentStateStarting, wire.AgentStateRunning:
		return "working"
	case wire.AgentStateWaitingOnUser:
		return "needs you"
	case wire.AgentStateFinished:
		return "idle"
	case wire.AgentStateStopped:
		return "done"
	case wire.AgentStateArchived:
		return "archived"
	default:
		return string(s)
	}
}

// titleOf is the session's title on one line of at most 48 characters.
func titleOf(a wire.Agent) string {
	t := "Untitled"
	if a.Title != nil && strings.TrimSpace(*a.Title) != "" {
		t = strings.Join(strings.Fields(*a.Title), " ")
	}
	if r := []rune(t); len(r) > 48 {
		t = string(r[:47]) + "…"
	}
	return t
}

// ago is a time as "now", "12 min ago", "3 h ago" or "4 days ago".
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	case d < 48*time.Hour:
		return "1 day ago"
	default:
		return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
	}
}

// minID is the shortest ID loops ls shows, as git's short SHA.
const minID = 6

// shortIDs is each session's id in lower case, at the shortest length (at least minID)
// that no other session in the list shares.
func shortIDs(list []wire.Agent) []string {
	full := make([]string, len(list))
	for i, a := range list {
		full[i] = strings.ToLower(strings.ReplaceAll(string(a.ID), "-", ""))
	}
	n := minID
	for ; ; n++ {
		seen := map[string]bool{}
		clash := false
		for _, id := range full {
			p := id[:min(n, len(id))]
			if seen[p] {
				clash = true
				break
			}
			seen[p] = true
		}
		if !clash || n >= 32 {
			break
		}
	}
	out := make([]string, len(full))
	for i, id := range full {
		out[i] = id[:min(n, len(id))]
	}
	return out
}
