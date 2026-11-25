package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The scale fixture (#5282): 1,000 fake machines, and the budget for listing them
// (scripts/perf-budgets.json, "scale"). The count is held on every run; the time only off
// CI, whose shared runners measure the load (docs/how-to/keep-the-performance-budgets.md).
func TestScaleFixtureListsAThousandMachinesWithinBudget(t *testing.T) {
	t.Parallel()
	const n = 1000
	st := store.NewMemory()
	if err := SeedFakeMachines(context.Background(), st, n); err != nil {
		t.Fatal(err)
	}
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, window := p.client("Window")
	var times []time.Duration
	for range 7 {
		start := time.Now()
		raw := call(t, window, "", "machines/list", map[string]any{"limit": 50})
		times = append(times, time.Since(start))
		var page wire.MachineList
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Machines) != 50 || page.Total != n+1 || page.Next == nil {
			t.Fatalf("machines/list limit 50: %d rows, total %d, next %v", len(page.Machines), page.Total, page.Next)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	median := times[len(times)/2]
	// The budget of scripts/perf-budgets.json, "scale": a page of 50 within 50 ms. Under -race
	// on a loaded box the ceiling is looser; the time is only held off CI.
	const budget = 50 * time.Millisecond
	t.Logf("machines/list, limit 50 of %d machines: median %v (budget %v)", n, median, budget)
	if os.Getenv("CI") == "" && median > 4*budget {
		t.Fatalf("machines/list at %d machines: median %v is over %v", n, median, 4*budget)
	}
	// With no arguments it is the array, up to the cap, as older clients read it.
	var rows []wire.ControlMachine
	if err := json.Unmarshal(call(t, window, "", "machines/list", map[string]any{}), &rows); err != nil || len(rows) != machineListCap {
		t.Fatalf("machines/list with no arguments: %d rows (%v), want %d", len(rows), err, machineListCap)
	}
}

// Paging, filters, sorts and counts (#5283) over the fixture's machines.
func TestMachinesListFiltersSortsPagesAndCounts(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	if err := SeedFakeMachines(context.Background(), st, 1000); err != nil {
		t.Fatal(err)
	}
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, window := p.client("Window")
	list := func(q map[string]any) wire.MachineList {
		t.Helper()
		var page wire.MachineList
		if err := json.Unmarshal(call(t, window, "", "machines/list", q), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	// Every machine once, by name, through the cursors.
	if seen := machinesThroughThePages(t, list); len(seen) != 1001 {
		t.Fatalf("%d machines through the pages", len(seen))
	}

	// A filter, a query, and the counts leave out only their own filter.
	assertLinuxAtTenFilter(t, list(map[string]any{"os": "linux", "version": "0.10.0", "limit": 1000}))
	one := list(map[string]any{"query": "FAKE MACHINE 0042"})
	if one.Total != 1 || one.Machines[0].ID != "fake-0042" {
		t.Fatalf("query: %+v", one.Total)
	}
	if none := list(map[string]any{"state": "online"}); none.Total != 0 || none.Counts.State["offline"] != 1001 {
		t.Fatalf("online: total %d, counts %v", none.Total, none.Counts.State)
	}

	// Sorts: by version, the numbers in order (0.10.0 after 0.9.1).
	byVersion := list(map[string]any{"sort": "version", "limit": 200})
	// The control plane's own machine ("dev") sorts first (#6609).
	if byVersion.Machines[1].Version != "0.9.0" || list(map[string]any{"sort": "version", "limit": 200, "cursor": pageCursor(900)}).Machines[100].Version != "0.10.0" {
		t.Fatalf("version sort starts at %s", byVersion.Machines[1].Version)
	}
	if byName := list(map[string]any{"sort": "name", "limit": 2}); byName.Machines[0].ID != "fake-0001" {
		t.Fatalf("name sort: %v", byName.Machines[0].ID)
	}

	// A bad sort or cursor is refused.
	if _, err := window.Call(context.Background(), "", "machines/list", map[string]any{"sort": "size"}); err == nil {
		t.Fatal("a sort that is not one was taken")
	}
	if _, err := window.Call(context.Background(), "", "machines/list", map[string]any{"cursor": "!!"}); err == nil {
		t.Fatal("a cursor that is not one was taken")
	}
}

// machinesThroughThePages lists every machine, 200 a page, through the cursors; it checks no
// machine is on two pages and there are 6 pages (the 1,000 and the control plane's own machine), and returns the machines it saw.
func machinesThroughThePages(t *testing.T, list func(map[string]any) wire.MachineList) map[string]bool {
	t.Helper()
	seen, cursor := map[string]bool{}, ""
	for pages := 0; ; pages++ {
		q := map[string]any{"limit": 200}
		if cursor != "" {
			q["cursor"] = cursor
		}
		page := list(q)
		for _, m := range page.Machines {
			if seen[string(m.ID)] {
				t.Fatalf("%s on two pages", m.ID)
			}
			seen[string(m.ID)] = true
		}
		if page.Next == nil {
			if pages != 5 {
				t.Fatalf("%d pages, want 6", pages+1)
			}
			break
		}
		cursor = *page.Next
	}
	return seen
}

// assertLinuxAtTenFilter checks a page filtered by os=linux and version=0.10.0 has only those
// machines, all of them, and counts that leave out only their own filter.
func assertLinuxAtTenFilter(t *testing.T, page wire.MachineList) {
	t.Helper()
	for _, m := range page.Machines {
		if m.Version != "0.10.0" || len(m.Platform) < 5 || m.Platform[:5] != "linux" {
			t.Fatalf("%+v passed os=linux version=0.10.0", m)
		}
	}
	if int64(len(page.Machines)) != page.Total || page.Total == 0 || page.Total >= 1000 {
		t.Fatalf("total %d, rows %d", page.Total, len(page.Machines))
	}
	if page.Counts.Os["linux"] != page.Total || page.Counts.Os["darwin"] == 0 {
		t.Fatalf("os counts %v: linux is the filter's own, so darwin counts what picking it would show", page.Counts.Os)
	}
	if page.Counts.Version["0.10.0"] == 0 || page.Counts.Version["0.9.0"] == 0 {
		t.Fatalf("counts %+v", page.Counts)
	}
}

// Labels on machines (#5284): the provider's, a person's, set on one and on many, and what
// machines/list filters by.
func TestMachineLabelsAreSetByAPersonAndFiltered(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	if err := SeedFakeMachines(context.Background(), st, 30); err != nil {
		t.Fatal(err)
	}
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, window := p.client("Window")
	list := func(q map[string]any) wire.MachineList {
		t.Helper()
		var page wire.MachineList
		if err := json.Unmarshal(call(t, window, "", "machines/list", q), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	all := list(map[string]any{"limit": 30})
	var ids []string
	for _, row := range all.Machines {
		ids = append(ids, string(row.ID))
		if len(row.AutoLabels) == 0 || row.AutoLabels[0][:3] != "os=" {
			t.Fatalf("%s has no os label: %v", row.Name, row.AutoLabels)
		}
	}
	set := func(params map[string]any) wire.MachineLabelsResult {
		t.Helper()
		var out wire.MachineLabelsResult
		if err := json.Unmarshal(call(t, window, "", "machines/setLabels", params), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := set(map[string]any{"machines": ids[:10], "add": []string{"gpu", "team=a"}}); got.Changed != 10 {
		t.Fatalf("changed %d", got.Changed)
	}
	if got := set(map[string]any{"machines": ids[:10], "add": []string{"GPU"}}); got.Changed != 0 {
		t.Fatalf("adding a label it has changed %d", got.Changed)
	}
	if page := list(map[string]any{"labels": []string{"gpu", "team=a"}, "limit": 50}); page.Total != 10 {
		t.Fatalf("total %d", page.Total)
	}
	set(map[string]any{"machines": ids[:3], "remove": []string{"gpu"}})
	if page := list(map[string]any{"labels": []string{"gpu"}, "limit": 50}); page.Total != 7 {
		t.Fatalf("total %d counts %v", page.Total, page.Counts.Labels)
	}
	set(map[string]any{"machines": ids[:1], "set": []string{"solo"}})
	if page := list(map[string]any{"query": "solo"}); page.Total != 1 || len(page.Machines[0].Labels) < 2 {
		t.Fatalf("total %d", page.Total)
	}
	for _, bad := range []string{"os=linux", "has space", "", "-x"} {
		if bad == "" {
			continue
		}
		if e := callError(t, window, "", "machines/setLabels", map[string]any{"machines": ids[:1], "add": []string{bad}}); e == "" {
			t.Fatalf("%q was taken", bad)
		}
	}
}
