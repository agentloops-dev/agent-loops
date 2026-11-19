//go:build live

package azureboardsmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract's live mode (#6191) on a real Azure Boards project (#3782), run by hand, never by CI:
//
//	AGENTS_LIVE_AZURE_DEVOPS_TOKEN=… AGENTS_LIVE_AZURE_BOARDS_PROJECT=org/project go test -tags live -run ForgeContractLive -v ./internal/azureboardsmcp/
//
// It skips when a credential is not set. It reads only: each contract event azureboards offers is
// polled twice, and must answer, not fail.

func TestForgeContractLive(t *testing.T) {
	env := forgecontract.Env(t, "AGENTS_LIVE_AZURE_DEVOPS_TOKEN", "AGENTS_LIVE_AZURE_BOARDS_PROJECT")
	events := &Events{Boards: NewRealBoards("", func() string { return env[0] }, func(context.Context) (string, error) { return env[1], nil })}
	forgecontract.Live(t, forgecontract.Server{
		Name: "azureboards", Kind: forgecontract.Tracker, Offered: EventNames(), Missing: azureBoardsMissing,
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
	})
}
