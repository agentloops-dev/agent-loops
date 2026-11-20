//go:build live

package bitbucketmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract's live mode (#6191) on a real Bitbucket Cloud repo (#3771), run by hand, never by CI:
//
//	AGENTS_LIVE_BITBUCKET_EMAIL=… AGENTS_LIVE_BITBUCKET_TOKEN=… AGENTS_LIVE_BITBUCKET_REPO=workspace/name go test -tags live -run ForgeContractLive -v ./internal/bitbucketmcp/
//
// It skips when a credential is not set. It reads only: each contract event bitbucket offers is
// polled twice, and must answer, not fail.

func TestForgeContractLive(t *testing.T) {
	env := forgecontract.Env(t, "AGENTS_LIVE_BITBUCKET_EMAIL", "AGENTS_LIVE_BITBUCKET_TOKEN", "AGENTS_LIVE_BITBUCKET_REPO")
	events := &Events{Bitbucket: NewRealBitbucket("", func() string { return env[1] }, func() string { return env[0] }, func(context.Context) (string, error) { return env[2], nil })}
	forgecontract.Live(t, forgecontract.Server{
		Name: "bitbucket", Kind: forgecontract.CodeForge, Offered: EventNames(), Missing: bitbucketMissing,
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
	})
}
