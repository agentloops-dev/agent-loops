package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6908: the largest record of the control plane's stores, on the writes that the stores' own
// tests do not make.

func costNode(ref string, now time.Time, files ...string) wire.LineageCostNode {
	return wire.LineageCostNode{Node: wire.LineageNode{Kind: "issue", Ref: "github:issue:o/r#" + ref}, Files: files, LastAt: wire.DateOf(now)}
}

func keptLineage(t *testing.T, st store.Store, host string) (storedLineage, []json.RawMessage) {
	t.Helper()
	object, err := st.Get(t.Context(), lineageKey(host))
	must(t, err)
	var kept storedLineage
	must(t, json.Unmarshal(object.Data, &kept))
	var raw struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	must(t, json.Unmarshal(object.Data, &raw))
	return kept, raw.Nodes
}

// A node sent again over the largest size is not kept, and the one kept before it is.
func TestALineageNodeOverItsLargestKeepsTheEarlierOne(t *testing.T) {
	t.Skip("#7193")
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	host, now := p.host("OWNERS-MAC").member.Host, time.Now()
	must(t, p.s.keepWork(t.Context(), host, []wire.LineageCostNode{costNode("1", now, "a.go"), costNode("2", now)}, now))
	must(t, p.s.keepWork(t.Context(), host, []wire.LineageCostNode{costNode("1", now, strings.Repeat("x", lineageNodeBytes)), costNode("3", now)}, now))
	kept, _ := keptLineage(t, st, host)
	refs := map[string]int{}
	for _, n := range kept.Nodes {
		refs[n.Node.Ref] = len(n.Files)
	}
	if len(refs) != 3 || refs["github:issue:o/r#1"] != 1 {
		t.Fatalf("kept %v", refs)
	}
}

// The largest size is of the node as the store has it.
func TestALineageNodeIsAtMostItsLargestAsStored(t *testing.T) {
	t.Skip("#7193")
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	host, now := p.host("OWNERS-MAC").member.Host, time.Now()
	many := make([]string, 15_000)
	for i := range many {
		many[i] = fmt.Sprint(i % 10)
	}
	must(t, p.s.keepWork(t.Context(), host, []wire.LineageCostNode{costNode("1", now, many...)}, now))
	_, raw := keptLineage(t, st, host)
	for _, n := range raw {
		if len(n) > lineageNodeBytes {
			t.Fatalf("a node of %d bytes is stored, and the most is %d", len(n), lineageNodeBytes)
		}
	}
}

// A request's answer keeps it at its largest size, whoever answers it.
func TestARequestsAnswerIsWithinItsLargest(t *testing.T) {
	t.Skip("#7192")
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	r := wire.CatalogRequest{ID: "one", Kind: wire.CatalogKindMCP, Name: "server", User: p.s.localOwner(),
		State: wire.CatalogRequestStateWaiting, At: dateOf(time.Now())}
	keepJSON(t, st, governanceRequestKey(r.ID), r)
	name := strings.Repeat("n", governanceRecordBytes)
	_, refused := p.s.governanceDecide(t.Context(), wire.AuditActor{User: r.User, UserName: &name}, wire.CatalogDecide{Request: r.ID}, time.Now())
	object, err := st.Get(t.Context(), governanceRequestKey(r.ID))
	must(t, err)
	if refused != nil && len(object.Data) == 0 {
		t.Fatal("a refused answer lost the request")
	}
	if len(object.Data) > governanceRecordBytes {
		t.Fatalf("%d bytes are stored, and the most is %d", len(object.Data), governanceRecordBytes)
	}
}

// A name or a reason that is cut is whole characters, and says that it is cut.
func TestWhatGovernanceCutsSaysSo(t *testing.T) {
	t.Skip("#7192")
	t.Parallel()
	if got := cut(strings.Repeat("é", 10), 5); !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("%q", got)
	}
}
