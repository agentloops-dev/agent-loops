package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/citimings"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7248: the largest record of the copies, the mesh, the reports and the folder stores, at the
// limit and over it.

func storedSize(t *testing.T, st store.Store, key string) int {
	t.Helper()
	object, err := st.Get(t.Context(), key)
	must(t, err)
	if object == nil {
		return 0
	}
	return len(object.Data)
}

// A host's copy keeps as many of the agents it listed as fit, and none of the folders that come
// to more than the most.
func TestAHostsCopyIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	c := newHostCopies(st)
	agents := make([]map[string]string, 100)
	for i := range agents {
		agents[i] = map[string]string{"id": fmt.Sprint(i), "pad": strings.Repeat("x", 100<<10)}
	}
	reply, _ := json.Marshal(map[string]any{"result": agents})
	c.take("MAC", "agents/list", string(reply))
	size := storedSize(t, st, copyKey("MAC"))
	if size == 0 || size > hostCopyBytes {
		t.Fatalf("%d bytes are stored, and the most is %d", size, hostCopyBytes)
	}
	var kept hostCopy
	object, _ := st.Get(t.Context(), copyKey("MAC"))
	must(t, json.Unmarshal(object.Data, &kept))
	var listed []json.RawMessage
	must(t, json.Unmarshal(kept.Agents, &listed))
	if len(listed) == 0 || len(listed) >= len(agents) {
		t.Fatalf("%d of %d agents are kept", len(listed), len(agents))
	}
	// Folders over the most are not kept; the agents are.
	folders, _ := json.Marshal(map[string]any{"result": []string{strings.Repeat("f", hostCopyBytes)}})
	c.take("MAC", "folders/list", string(folders))
	object, _ = st.Get(t.Context(), copyKey("MAC"))
	kept = hostCopy{}
	must(t, json.Unmarshal(object.Data, &kept))
	if len(kept.Folders) != 0 || len(kept.Agents) == 0 || len(object.Data) > hostCopyBytes {
		t.Fatalf("%d bytes of folders, %d of agents", len(kept.Folders), len(kept.Agents))
	}
}

// A lease over its largest is refused, and one at it is taken.
func TestALeaseIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	l := &leases{store: store.NewMemory(), copy: "c", held: map[string]heldLease{}}
	if _, err := l.take(t.Context(), strings.Repeat("h", leaseBytes)); err == nil {
		t.Fatal("a lease over its largest was taken")
	}
	if _, err := l.take(t.Context(), strings.Repeat("h", 200)); err != nil {
		t.Fatal(err)
	}
}

// Past its count the oldest change events go, though they are of today.
func TestTheMeshEventsAreAtMostTheirCount(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	now := time.Now().UTC()
	for i := range 10 {
		_, err := st.Put(t.Context(), fmt.Sprintf("%s%s/%013d-a.json", meshEventsPrefix, now.Format(sweptDayLayout), i), []byte("{}"), store.Absent)
		must(t, err)
	}
	n, err := sweepMeshEventsTo(t.Context(), st, now, 100, 4)
	if err != nil || n != 6 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	left, _ := st.List(t.Context(), meshEventsPrefix)
	if len(left) != 4 || !strings.HasSuffix(left[0].Key, "0000000000006-a.json") {
		t.Fatalf("%v", left)
	}
}

// A usage period over its largest is not kept, and the other is. Past their counts the oldest
// periods go.
func TestAUsagePeriodIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	host, now := p.host("MAC").member.Host, time.Now().UTC()
	big := make([]wire.UsageRollupRow, 600)
	for i := range big {
		big[i] = wire.UsageRollupRow{Runtime: strings.Repeat("r", 2<<10), Cost: map[string]float64{}}
	}
	hour := func(ago int) string { return now.Add(-time.Duration(ago) * time.Hour).Format(usageHourLayout) }
	must(t, p.s.keepRollup(t.Context(), host, wire.UsageRollup{Hours: []wire.UsageRollupPeriod{{At: hour(1), Rows: big}, {At: hour(2), Rows: []wire.UsageRollupRow{}}, {At: hour(3), Rows: []wire.UsageRollupRow{}}}}, now))
	keys, _ := st.List(t.Context(), usageHoursPrefix)
	if len(keys) != 2 {
		t.Fatalf("%v", keys)
	}
	n, err := sweepUsageTo(t.Context(), st, now, 100, 1, 1)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	keys, _ = st.List(t.Context(), usageHoursPrefix)
	if len(keys) != 1 || !strings.Contains(keys[0].Key, hour(2)) {
		t.Fatalf("%v", keys)
	}
}

// A day's runs are cut to the largest, and what is cut is the runs sent last.
func TestADaysBuildTimingsAreAtMostTheirLargest(t *testing.T) {
	t.Parallel()
	runs := make([]citimings.Run, 10)
	for i := range runs {
		runs[i] = citimings.Run{Ref: fmt.Sprint(i), Attempt: 1, Workflow: strings.Repeat("w", 1<<20)}
	}
	data, err := encodeBuildsDay(runs, time.Now())
	if err != nil || len(data) > buildsDayBytes || len(data) < buildsDayBytes/2 {
		t.Fatalf("%d bytes: %v", len(data), err)
	}
	var kept storedBuildsDay
	must(t, json.Unmarshal(data, &kept))
	if len(kept.Runs) != 3 || kept.Runs[0].Ref != "0" {
		t.Fatalf("%d runs", len(kept.Runs))
	}
}

// A machine's deploys are cut to the largest.
func TestAMachinesDeploysAreAtMostTheirLargest(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	host, now := p.host("MAC").member.Host, time.Now().UTC()
	var sync wire.DeploysSync
	for i := range 6 {
		f := deployFact(fmt.Sprint(i), wire.DeployStateSucceeded, now)
		f.Component = strings.Repeat("c", 1<<20)
		sync.Deploys = append(sync.Deploys, f)
	}
	params, _ := json.Marshal(sync)
	_, err := p.s.methods.HostSaid(host, "deploys/sync", params)
	must(t, err)
	if size := storedSize(t, st, deploysKey(host)); size == 0 || size > deploysBytes {
		t.Fatalf("%d bytes are stored, and the most is %d", size, deploysBytes)
	}
}

// A machine's folders are cut to the largest.
func TestAMachinesProjectFoldersAreAtMostTheirLargest(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	host := p.host("MAC").member.Host
	var folders []wire.JoinedFolder
	for i := range 900 {
		folders = append(folders, wire.JoinedFolder{Folder: wire.URL("file:///" + strings.Repeat("d", 700) + fmt.Sprint(i)), Name: "n", Project: fmt.Sprint("p", i)})
	}
	joinFolders(t, p, host, folders...)
	size := storedSize(t, st, projectFoldersKey(host))
	if size == 0 || size > projectFoldersBytes {
		t.Fatalf("%d bytes are stored, and the most is %d", size, projectFoldersBytes)
	}
}

// A claim over its largest is refused, and one at it is kept.
func TestAWorkflowClaimIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	host, now := p.host("MAC").member.Host, time.Now()
	if _, err := p.s.claimRun(t.Context(), host, wire.WorkflowClaim{Workflow: "w", Trigger: strings.Repeat("t", runClaimBytes)}, now); err == nil {
		t.Fatal("a claim over its largest was kept")
	}
	if got, err := p.s.claimRun(t.Context(), host, wire.WorkflowClaim{Workflow: "w", Trigger: "t"}, now); err != nil || !got.Won {
		t.Fatalf("%+v: %v", got, err)
	}
}

// Past their count the oldest claims go, whatever their day.
func TestTheRunClaimsAreAtMostTheirCount(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	now := time.Now().UTC()
	for i := range 5 {
		_, err := st.Put(t.Context(), fmt.Sprintf("%s%s/%d.json", runsPrefix, now.Format(usageDayLayout), i), []byte("{}"), store.Absent)
		must(t, err)
	}
	if n, err := sweepRunsTo(t.Context(), st, now, 100, 2); err != nil || n != 3 {
		t.Fatalf("deleted %d: %v", n, err)
	}
}

// A link or a state over its largest is not kept, and the rest of the send is.
func TestALineageLinkIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	node := func(ref string) wire.LineageNode { return wire.LineageNode{Kind: "issue", Ref: ref} }
	sync := wire.LineageSync{Links: []wire.LineageLink{
		{From: node("github:issue:o/r#1"), To: node("github:issue:o/r#2"), Rel: "x", Source: "y"},
		{From: node("github:issue:o/r#" + strings.Repeat("1", lineageLinkBytes)), To: node("github:issue:o/r#2"), Rel: "x", Source: "y"},
	}}
	got := p.s.keepLineageWithinLargest("MAC", sync)
	if len(got.Links) != 1 || len(sync.Links) != 2 {
		t.Fatalf("%d links kept of %d", len(got.Links), len(sync.Links))
	}
}

// A machine's links and states are at most their largest as stored.
func TestALineageGraphObjectIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	big := storedLinks{Links: []wire.LineageLink{{Rel: strings.Repeat("r", lineageLinksBytes)}}}
	if err := putOver(context.Background(), st, "k", nil, big, lineageLinksBytes); err == nil {
		t.Fatal("an object over its largest was kept")
	}
	if err := putOver(context.Background(), st, "k", nil, storedLinks{}, lineageLinksBytes); err != nil {
		t.Fatal(err)
	}
}
