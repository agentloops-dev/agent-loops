package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Cost on the link (#2350): each machine's banked cost per lineage node (an issue, a PR, a
// session), as usage/rollup's work sends it, under v1/lineage/<host>.json. A node sent
// replaces that machine's copy of it, so the latest of each is kept, 90 days after it last
// changed and at most lineageNodesKept a machine. Only that machine writes its object.
// The machines' copies of one node add up, as each banked its own turns; reading them
// (lineage.Rollup) comes with the lineage view (#2351).

const (
	// store-bounds: kind=graph; count=5_000 a machine; size=32MiB a machine (64KiB a node); age=90d; applied=on write and hourly sweep
	lineagePrefix = "v1/lineage/"
	// lineageNodesKept bounds a machine's object, as the host bounds its own file.
	lineageNodesKept = 5_000
	// mostWorkNodes bounds one send.
	mostWorkNodes   = 1_000
	lineageNodeKept = 90 * 24 * time.Hour
	// lineageNodeBytes is the largest node as stored: one sent over it is not kept, so the
	// copy kept before it stays (#7193), and the rest of its send is kept (#6768).
	lineageNodeBytes = 64 << 10
	// lineageBytes is the most a machine's nodes come to: the oldest go past it.
	lineageBytes = 32 << 20
)

// storedLineage is one machine's nodes as the store keeps them.
type storedLineage struct {
	Nodes []wire.LineageCostNode `json:"nodes"`
	At    records.Stamp          `json:"at"`
}

func lineageKey(host string) string { return lineagePrefix + strings.ToLower(host) + ".json" }

// encodeLineage is a machine's object as stored: not indented, as records.Encode has it, so
// each node takes the bytes that its limits measure with json.Marshal (#7193).
func encodeLineage(kept storedLineage) ([]byte, error) { return json.Marshal(kept) }

// keepWork is usage/rollup's work: the nodes sent, over this machine's copies of them.
func (s *Service) keepWork(ctx context.Context, host string, work []wire.LineageCostNode, now time.Time) error {
	if len(work) == 0 {
		return nil
	}
	if len(work) > mostWorkNodes {
		return fail(invalidParams, "Send at most %d lineage nodes at a time.", mostWorkNodes)
	}
	var kept storedLineage
	object, err := s.cfg.Store.Get(ctx, lineageKey(host))
	if err != nil {
		return err
	}
	if object != nil {
		// What cannot be read starts again from what was sent.
		_ = json.Unmarshal(object.Data, &kept)
	}
	for _, n := range work {
		if n.Node.Kind == "" || n.Node.Ref == "" {
			return fail(invalidParams, "A lineage node needs a kind and a ref.")
		}
	}
	// In the full ref form (#4800): a machine on a release before it sends the old form, and
	// a copy kept from before it holds it. What is sent now names a session first.
	var names lineage.Names
	work = slices.Clone(work)
	for i, n := range work {
		work[i] = lineage.FullCostNode(n, nil)
		work[i].Node = names.Name(work[i].Node)
	}
	byKey := map[wire.LineageNode]wire.LineageCostNode{}
	for _, n := range kept.Nodes {
		n = lineage.FullCostNode(n, nil)
		n.Node = names.Name(n.Node)
		byKey[n.Node] = n
	}
	large := 0
	for _, n := range work {
		n.LastAt = min(n.LastAt, wire.DateOf(now))
		if data, err := json.Marshal(n); err != nil || len(data) > lineageNodeBytes {
			large++
			continue
		}
		byKey[n.Node] = n
	}
	nodes, dropped := boundLineage(slices.Collect(maps.Values(byKey)), now)
	if large += dropped; large > 0 {
		s.log(fmt.Sprintf("lineage: %d nodes of machine %s are over %d KiB and are not kept", large, host, lineageNodeBytes>>10))
	}
	data, err := encodeLineage(storedLineage{Nodes: nodes, At: records.At(now)})
	if err != nil {
		return err
	}
	_, err = s.cfg.Store.Put(ctx, lineageKey(host), data, store.Always)
	return err
}

// boundLineage is the nodes within every bound, newest first, so the oldest go past one,
// and how many were too large.
func boundLineage(nodes []wire.LineageCostNode, now time.Time) ([]wire.LineageCostNode, int) {
	// A machine whose clock is ahead keeps its nodes no longer.
	latest, oldest := wire.DateOf(now), wire.DateOf(now.Add(-lineageNodeKept))
	nodes = slices.Clone(nodes)
	for i := range nodes {
		nodes[i].LastAt = min(nodes[i].LastAt, latest)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].LastAt != nodes[j].LastAt {
			return nodes[i].LastAt > nodes[j].LastAt
		}
		return nodes[i].Node.Ref < nodes[j].Node.Ref
	})
	kept := make([]wire.LineageCostNode, 0, min(len(nodes), lineageNodesKept))
	size, large := 0, 0
	for _, n := range nodes {
		if n.LastAt < oldest || len(kept) == lineageNodesKept {
			break
		}
		data, err := json.Marshal(n)
		if err != nil || len(data) > lineageNodeBytes {
			large++
			continue
		}
		if size += len(data) + 1; size > lineageBytes {
			break
		}
		kept = append(kept, n)
	}
	return kept, large
}

// sweepLineage deletes the nodes of machines removed since, and bounds the rest again, so
// a machine that stopped sending loses its old nodes too (#6768); at most most deletions
// and writes.
func sweepLineage(ctx context.Context, st store.Store, rs *records.Records, now time.Time, most int) (int, error) {
	keys, err := st.List(ctx, lineagePrefix)
	if err != nil {
		return 0, err
	}
	known := map[string]bool{}
	for _, h := range rs.Hosts() {
		known[strings.ToLower(h.ID)] = true
	}
	n := 0
	for _, k := range keys {
		if n >= most {
			break
		}
		if known[strings.TrimSuffix(strings.TrimPrefix(k.Key, lineagePrefix), ".json")] {
			wrote, err := reboundLineage(ctx, st, k.Key, now)
			if err != nil {
				return n, err
			}
			if wrote {
				n++
			}
			continue
		}
		// A machine another copy has just added is not in these records yet: only an
		// object a day old goes.
		object, err := st.Get(ctx, k.Key)
		if err != nil {
			return n, err
		}
		var kept storedLineage
		if object == nil || json.Unmarshal(object.Data, &kept) == nil && kept.At.After(now.Add(-24*time.Hour)) {
			continue
		}
		if err := st.Delete(ctx, k.Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// reboundLineage writes one machine's nodes without those past a bound, over the object it
// read: a machine that wrote since keeps what it wrote. It says whether it wrote.
func reboundLineage(ctx context.Context, st store.Store, key string, now time.Time) (bool, error) {
	object, err := st.Get(ctx, key)
	if err != nil || object == nil {
		return false, err
	}
	var kept storedLineage
	if json.Unmarshal(object.Data, &kept) != nil {
		return false, nil
	}
	nodes, _ := boundLineage(kept.Nodes, now)
	// An object from a release before #7193 is indented, so larger than was measured: it is
	// written again as it is now stored.
	if len(nodes) == len(kept.Nodes) && !bytes.HasPrefix(object.Data, []byte("{\n")) {
		return false, nil
	}
	kept.Nodes = nodes
	data, err := encodeLineage(kept)
	if err != nil {
		return false, err
	}
	_, err = st.Put(ctx, key, data, store.Matching(object.ETag))
	if store.IsConflict(err) {
		return false, nil
	}
	return err == nil, err
}
