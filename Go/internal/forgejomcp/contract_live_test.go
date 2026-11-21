//go:build live

package forgejomcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract's live mode (#6191) on a real Forgejo or Codeberg repo (#3739), run by hand, never by CI:
//
//	AGENTS_LIVE_FORGEJO_URL=https://codeberg.org AGENTS_LIVE_FORGEJO_TOKEN=… AGENTS_LIVE_FORGEJO_REPO=owner/name go test -tags live -run ForgeContractLive -v ./internal/forgejomcp/
//
// It skips when a credential is not set. It reads only: each contract event forgejo offers is
// polled twice, and must answer, not fail.

func TestForgeContractLive(t *testing.T) {
	env := forgecontract.Env(t, "AGENTS_LIVE_FORGEJO_URL", "AGENTS_LIVE_FORGEJO_TOKEN", "AGENTS_LIVE_FORGEJO_REPO")
	events := &Events{Forgejo: NewRealForgejo(env[0], func() string { return env[1] }, func(context.Context) (string, error) { return env[2], nil })}
	forgecontract.Live(t, forgecontract.Server{
		Name: "forgejo", Kind: forgecontract.CodeForge, Offered: EventNames(), Missing: forgejoMissing,
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
	})
}
