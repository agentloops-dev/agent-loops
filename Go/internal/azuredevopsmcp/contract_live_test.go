//go:build live

package azuredevopsmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract's live mode (#6191) on a real Azure DevOps repo (#3782), run by hand, never by CI:
//
//	AGENTS_LIVE_AZURE_DEVOPS_TOKEN=… AGENTS_LIVE_AZURE_DEVOPS_REPO=org/project/repo go test -tags live -run ForgeContractLive -v ./internal/azuredevopsmcp/
//
// It skips when a credential is not set. It reads only: each contract event azuredevops offers is
// polled twice, and must answer, not fail.

func TestForgeContractLive(t *testing.T) {
	env := forgecontract.Env(t, "AGENTS_LIVE_AZURE_DEVOPS_TOKEN", "AGENTS_LIVE_AZURE_DEVOPS_REPO")
	events := &Events{AzureDevOps: NewRealAzureDevOps("", func() string { return env[0] }, func(context.Context) (string, error) { return env[1], nil })}
	forgecontract.Live(t, forgecontract.Server{
		Name: "azuredevops", Kind: forgecontract.CodeForge, Offered: EventNames(), Missing: azureDevOpsMissing,
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
	})
}
