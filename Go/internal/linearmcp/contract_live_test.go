//go:build live

package linearmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract's live mode (#6191) on a real Linear workspace, run by hand, never by CI:
//
//	AGENTS_LIVE_LINEAR_KEY=… go test -tags live -run ForgeContractLive -v ./internal/linearmcp/
//
// It skips when a credential is not set. It reads only: each contract event linear offers is
// polled twice, and must answer, not fail.

func TestForgeContractLive(t *testing.T) {
	env := forgecontract.Env(t, "AGENTS_LIVE_LINEAR_KEY")
	events := &Events{Linear: NewRealLinear("", func() string { return env[0] })}
	forgecontract.Live(t, forgecontract.Server{
		Name: "linear", Kind: forgecontract.Tracker, Offered: EventNames(), Missing: linearMissing,
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
	})
}
