//go:build live

package gitlabmcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract's live mode (#6191) on a real GitLab project, run by hand, never by CI:
//
//	AGENTS_LIVE_GITLAB_TOKEN=… AGENTS_LIVE_GITLAB_PROJECT=group/name [AGENTS_LIVE_GITLAB_URL=…] go test -tags live -run ForgeContractLive -v ./internal/gitlabmcp/
//
// It skips when a credential is not set. It reads only: each contract event gitlab offers is
// polled twice, and must answer, not fail.

func TestForgeContractLive(t *testing.T) {
	env := forgecontract.Env(t, "AGENTS_LIVE_GITLAB_TOKEN", "AGENTS_LIVE_GITLAB_PROJECT")
	events := &Events{GitLab: NewRealGitLab(os.Getenv("AGENTS_LIVE_GITLAB_URL"), func() string { return env[0] }, func(context.Context) (string, error) { return env[1], nil })}
	forgecontract.Live(t, forgecontract.Server{
		Name: "gitlab", Kind: forgecontract.CodeForge, Offered: EventNames(), Missing: gitlabMissing,
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
	})
}
