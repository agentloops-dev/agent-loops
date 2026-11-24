package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// pageSize is the rows loops sessions list shows, and each page --all reads (bounds, Stage 3).
const pageSize = 20

const recentLimit = 9

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
func printList(out, errOut io.Writer, st style, p place, list []wire.Agent, more, all bool, now time.Time) {
	if len(list) == 0 {
		fmt.Fprintf(errOut, "No sessions in %s.\nStart one: loops \"what this folder needs\"\n", p.folderName())
		return
	}
	ids := shortIDs(list)
	headers := []string{"ID", "TITLE", "STATE", "RUNTIME", "FOLDER", "UPDATED"}
	styled := make([]string, len(headers))
	for i, h := range headers {
		styled[i] = st.bold(h)
	}
	plain := make([][]string, len(list))
	rows := make([][]string, len(list))
	for i, a := range list {
		updated := ago(now, a.LastActivityAt.Time())
		plain[i] = []string{ids[i], titleOf(a), listStateWord(a.State), runtimeOf(a), p.folderName(), updated}
		rows[i] = []string{st.paint(roleBrand, ids[i]), titleOf(a), st.paint(stateRole(a.State), listStateWord(a.State)), runtimeOf(a), p.folderName(), st.paint(roleQuiet, updated)}
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = visibleWidth(h)
	}
	for _, row := range plain {
		for i, cell := range row {
			if visibleWidth(cell) > widths[i] {
				widths[i] = visibleWidth(cell)
			}
		}
	}
	fmt.Fprintln(out, tableLine(styled, headers, widths))
	for i, row := range rows {
		fmt.Fprintln(out, tableLine(row, plain[i], widths))
	}
	count := fmt.Sprintf("%d sessions", len(list))
	if len(list) == 1 {
		count = "1 session"
	}
	if more {
		count = fmt.Sprintf("%d of many", len(list))
	}
	fmt.Fprintf(errOut, "%s · loops sessions list --all\n", count)
}

func recentSessions(ctx context.Context, c caller, p place) ([]wire.Agent, error) {
	limit := int64(recentLimit)
	req := wire.ListRequest{Folder: &p.folder, Limit: &limit, Lean: true}
	r, err := c.Call(ctx, string(p.machine.ID), "agents/list", req)
	if err != nil {
		return nil, unreachable(p.machineName(), err)
	}
	var list []wire.Agent
	if err := json.Unmarshal(r, &list); err != nil {
		return nil, err
	}
	if len(list) > recentLimit {
		list = list[:recentLimit]
	}
	return list, nil
}

func printRecent(out io.Writer, st style, p place, list []wire.Agent, now time.Time) {
	if len(list) == 0 {
		fmt.Fprintf(out, "No sessions in %s yet.\n", p.folderName())
		return
	}
	fmt.Fprintln(out, st.bold(fmt.Sprintf("Recent sessions in %s", p.folderName())))
	widths := []int{1, len("Untitled"), len("working"), len("opencode"), len("now")}
	plain := make([][]string, len(list))
	rows := make([][]string, len(list))
	for i, a := range list {
		number := strconv.Itoa(i + 1)
		updated := ago(now, a.LastActivityAt.Time())
		plain[i] = []string{number, titleOf(a), listStateWord(a.State), runtimeOf(a), updated}
		rows[i] = []string{st.paint(roleBrand, number), titleOf(a), st.paint(stateRole(a.State), listStateWord(a.State)), runtimeOf(a), st.paint(roleQuiet, updated)}
		for j, cell := range plain[i] {
			if visibleWidth(cell) > widths[j] {
				widths[j] = visibleWidth(cell)
			}
		}
	}
	for i := range rows {
		fmt.Fprintln(out, "  "+tableLine(rows[i], plain[i], widths))
	}
}

func tableLine(cells, plain []string, widths []int) string {
	var b strings.Builder
	for i, cell := range cells {
		b.WriteString(cell)
		if i < len(cells)-1 {
			b.WriteString(strings.Repeat(" ", widths[i]-visibleWidth(plain[i])+2))
		}
	}
	return b.String()
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

func listStateWord(s wire.AgentState) string {
	if s == wire.AgentStateWaitingOnUser {
		return "waiting"
	}
	return stateWord(s)
}

func stateRole(s wire.AgentState) role {
	switch s {
	case wire.AgentStateStarting, wire.AgentStateRunning:
		return roleBrand
	case wire.AgentStateWaitingOnUser:
		return roleAttention
	case wire.AgentStateStopped:
		return roleDone
	default:
		return roleQuiet
	}
}

func runtimeOf(a wire.Agent) string {
	if a.RuntimeID == "" {
		return "auto"
	}
	return a.RuntimeID
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

// ago is a time as "now", "12 s ago", "3 min ago", "2 h ago" or "4 days ago".
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d/time.Second))
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

// minID is the shortest ID loops sessions list shows, as git's short SHA.
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
