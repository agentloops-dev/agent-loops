//go:build live

package githubmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract's live mode (#6191) on a real GitHub repo, run by hand, never by CI:
//
//	AGENTS_LIVE_GITHUB_TOKEN=… AGENTS_LIVE_GITHUB_REPO=owner/name go test -tags live -run ForgeContractLive -v ./internal/githubmcp/
//
// It skips when a credential is not set. It reads only: each contract event github offers is
// polled twice, and must answer, not fail.

func TestForgeContractLive(t *testing.T) {
	env := forgecontract.Env(t, "AGENTS_LIVE_GITHUB_TOKEN", "AGENTS_LIVE_GITHUB_REPO")
	events := &Events{GitHub: NewRealGitHub("", func() string { return env[0] }, func(context.Context) (string, error) { return env[1], nil })}
	forgecontract.Live(t, forgecontract.Server{
		Name: "github", Kind: forgecontract.CodeForge, Offered: EventNames(), Missing: githubMissing,
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
	})
}
