package linearmcp

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract (#6191) over the fake Linear: issue ENG-1 is the story's.

// linearMissing are the contract's events this server does not give, with their bugs.
var linearMissing = map[string]string{}

func TestForgeContract(t *testing.T) {
	f := &fakeLinear{}
	now := start
	at := func() string { return mcpevents.TimeString(now.Add(-time.Minute)) }
	events := &Events{Linear: f, Now: func() time.Time { return now }}
	one := issue("ENG-1", "ENG")
	n := 0
	add := func(kind Kind, extra func(*Activity)) {
		n++
		a := Activity{ID: "h" + strconv.Itoa(n), At: at(), Issue: one, Kind: kind, By: member}
		if kind == Opened {
			a.ID = one.ID
		}
		if extra != nil {
			extra(&a)
		}
		f.add(a)
	}
	forgecontract.Run(t, forgecontract.Server{
		Name:    "linear",
		Kind:    forgecontract.Tracker,
		Offered: EventNames(),
		Missing: linearMissing,
		Args: func(event string) map[string]any {
			if event == "issue.opened" {
				return map[string]any{"team": "ENG"}
			}
			return map[string]any{"team": "ENG", "issue": "ENG-1"}
		},
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
		Do: func(t *testing.T, step forgecontract.Step) {
			now = now.Add(2 * time.Minute)
			switch step.Name {
			case forgecontract.IssueOpen:
				add(Opened, nil)
			case forgecontract.IssueComment:
				add(Commented, func(a *Activity) { a.CommentBody, a.CommentURL = "Looks close.", one.URL+"#comment-1" })
			case forgecontract.IssueAssign:
				add(Assigned, nil)
			case forgecontract.IssueLabel:
				one.Labels = []string{"bug"}
				add(Labelled, func(a *Activity) { a.Label = "bug" })
			case forgecontract.IssueClose:
				one.State, one.StateType = "Done", "completed"
				add(Closed, func(a *Activity) { a.Reason = "completed" })
			case forgecontract.IssueRefused:
				f.mu.Lock()
				f.err = &LinearError{Kind: RateLimited, Message: "Linear's rate limit is reached", RetryAfterMs: 60000}
				f.mu.Unlock()
			}
		},
	})
}
