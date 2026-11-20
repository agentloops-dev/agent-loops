package azureboardsmcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract (#6191) over the fake Azure DevOps: work item 1 is the story's.

// azureBoardsMissing are the contract's events this server does not give, with their bugs.
var azureBoardsMissing = map[string]string{}

func TestForgeContract(t *testing.T) {
	f := newFake(t)
	minute := 0
	events := &Events{Boards: f.boards(), Now: func() time.Time { return start.Add(time.Duration(minute) * time.Minute) }}
	forgecontract.Run(t, forgecontract.Server{
		Name:    "azureboards",
		Kind:    forgecontract.Tracker,
		Offered: EventNames(),
		Missing: azureBoardsMissing,
		Args: func(event string) map[string]any {
			if event == "issue.opened" {
				return nil
			}
			return map[string]any{"issue": "1"}
		},
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
		Do: func(t *testing.T, step forgecontract.Step) {
			minute += 2
			n := minute - 1
			switch step.Name {
			case forgecontract.IssueOpen:
				f.item(1, "Bug 1", "Bug", "New", dev, n, nil)
			case forgecontract.IssueComment:
				f.comment(1, 70, dev, n, "<p>Looks close.</p>")
			case forgecontract.IssueAssign:
				f.update(1, dev, n, map[string][2]any{fieldAssigned: {nil, alex}})
			case forgecontract.IssueLabel:
				f.update(1, dev, n, map[string][2]any{fieldTags: {nil, "bug"}})
			case forgecontract.IssueClose:
				f.update(1, dev, n, map[string][2]any{fieldState: {"New", "Closed"}})
			case forgecontract.IssueRefused:
				f.set(func() { f.status = 429 })
			}
		},
	})
}
