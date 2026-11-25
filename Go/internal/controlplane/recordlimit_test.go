package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
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

// A node that the limit took before #7193 stays, and the sweep writes its indented object again
// as it is measured, once.
func TestTheSweepStoresAnOldLineageObjectAsMeasured(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	host, now := p.host("OWNERS-MAC").member.Host, time.Now()
	many := make([]string, 15_000)
	for i := range many {
		many[i] = fmt.Sprint(i % 10)
	}
	old, err := records.Encode(storedLineage{Nodes: []wire.LineageCostNode{costNode("1", now, many...), costNode("2", now)}, At: records.At(now)})
	must(t, err)
	_, err = st.Put(t.Context(), lineageKey(host), old, store.Always)
	must(t, err)
	for _, want := range []int{1, 0} {
		n, err := sweepLineage(t.Context(), st, p.s.records, now, 10)
		if err != nil || n != want {
			t.Fatalf("wrote %d, not %d: %v", n, want, err)
		}
		kept, raw := keptLineage(t, st, host)
		if len(kept.Nodes) != 2 || len(kept.Nodes[0].Files) != len(many) || len(raw[0]) > lineageNodeBytes {
			t.Fatalf("%d nodes, the first of %d bytes", len(kept.Nodes), len(raw[0]))
		}
	}
}

// A request's answer keeps it at its largest size, whoever answers it.
func TestARequestsAnswerIsWithinItsLargest(t *testing.T) {
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
	if refused != nil {
		t.Fatalf("a request cannot be answered: %v", refused)
	}
	if len(object.Data) > governanceRecordBytes {
		t.Fatalf("%d bytes are stored, and the most is %d", len(object.Data), governanceRecordBytes)
	}
}

// A request at its largest can be approved for a group, and so can one that waits with it. The
// approval is within its largest too, and has the whole source.
func TestALargeRequestCanBeApproved(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	long := strings.Repeat("<", governanceRecordBytes)
	source, why := strings.Repeat("<", governanceSourceMost), strings.Repeat("<", governanceWhyMost)
	_, err := p.s.records.ChangeGroup(t.Context(), "g", func(*records.Group) (*records.Group, error) {
		return &records.Group{ID: "g", Name: long, Everyone: true}, nil
	})
	must(t, err)
	for _, id := range []string{"one", "two"} {
		keepJSON(t, st, governanceRequestKey(id), wire.CatalogRequest{ID: id, Kind: wire.CatalogKindMCP, Name: strings.Repeat("<", governanceNameMost),
			Source: &source, Why: &why, User: p.s.localOwner(), UserName: long[:300], State: wire.CatalogRequestStateWaiting, At: dateOf(time.Now())})
	}
	_, err = p.s.governanceDecide(t.Context(), wire.AuditActor{User: p.s.localOwner(), UserName: &long},
		wire.CatalogDecide{Request: "one", Approve: true, Group: new("g")}, time.Now())
	must(t, err)
	for _, id := range []string{"one", "two"} {
		object, err := st.Get(t.Context(), governanceRequestKey(id))
		must(t, err)
		var r wire.CatalogRequest
		must(t, json.Unmarshal(object.Data, &r))
		if r.State != wire.CatalogRequestStateApproved || len(object.Data) > governanceRecordBytes {
			t.Fatalf("%s: %s, %d bytes", id, r.State, len(object.Data))
		}
	}
	keys, err := st.List(t.Context(), governanceApprovalPrefix)
	must(t, err)
	approvals, err := p.s.governanceApprovals(t.Context())
	must(t, err)
	if len(approvals) != 1 || deref(approvals[0].Source) != source || keys[0].Size > governanceRecordBytes {
		t.Fatalf("%d approvals", len(approvals))
	}
}

// A name or a reason that is cut is whole characters, and says that it is cut.
func TestWhatGovernanceCutsSaysSo(t *testing.T) {
	t.Parallel()
	if got := cut(strings.Repeat("é", 10), 5); !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("%q", got)
	}
}
