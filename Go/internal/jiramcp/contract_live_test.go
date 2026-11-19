//go:build live

package jiramcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/forgecontract"
	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// The forge contract's live mode (#6191) on a real Jira Cloud site, run by hand, never by CI:
//
//	AGENTS_LIVE_JIRA_URL=https://….atlassian.net AGENTS_LIVE_JIRA_EMAIL=… AGENTS_LIVE_JIRA_TOKEN=… go test -tags live -run ForgeContractLive -v ./internal/jiramcp/
//
// It skips when a credential is not set. It reads only: each contract event jira offers is
// polled twice, and must answer, not fail.

func TestForgeContractLive(t *testing.T) {
	env := forgecontract.Env(t, "AGENTS_LIVE_JIRA_URL", "AGENTS_LIVE_JIRA_EMAIL", "AGENTS_LIVE_JIRA_TOKEN")
	auth := Auth{Site: env[0], Cloud: true, Header: "Basic " + base64.StdEncoding.EncodeToString([]byte(env[1]+":"+env[2]))}
	events := &Events{Jira: NewRealJira(func() Auth { return auth })}
	forgecontract.Live(t, forgecontract.Server{
		Name: "jira", Kind: forgecontract.Tracker, Offered: EventNames(), Missing: jiraMissing,
		Poll: func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error) {
			return events.Poll(ctx, raw)
		},
	})
}
