package controlplane

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// allHost is a host that says it has what fanall.go asks; boards says forge.boards/1 too.
func (p *plane) allHost(machine string, boards bool, answers map[string]func(json.RawMessage) any) *fakeHost {
	hello := fakeHello(machine)
	hello.Capabilities = append(hello.Capabilities, wire.CapForge1, wire.CapLineageForecast1, wire.CapFeatures1, wire.CapEvents1, wire.CapConnections1)
	if boards {
		hello.Capabilities = append(hello.Capabilities, wire.CapForgeBoards1)
	}
	h := p.hostSaying(hello, nil)
	h.mu.Lock()
	h.answers = answers
	h.mu.Unlock()
	return h
}

// #5592: forge/listAll asks each of the caller's machines once and gives each one's list; a
// next page asks only the machines it names, from their cursors; a board asks only the machines
// that know boards.
func TestForgeListAllGivesEachMachinesList(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	var mu sync.Mutex
	asked := map[string][]wire.ForgeListRequest{}
	list := func(name string) func(json.RawMessage) any {
		return func(params json.RawMessage) any {
			var r wire.ForgeListRequest
			json.Unmarshal(params, &r)
			mu.Lock()
			asked[name] = append(asked[name], r)
			mu.Unlock()
			out := map[string]any{"items": []any{map[string]any{"node": map[string]any{"ref": "github:o/r#" + name, "kind": "issue"}, "title": name}}}
			if r.Cursor == nil {
				out["cursor"] = name + "-2"
			}
			return out
		}
	}
	mac := p.allHost("MAC", true, map[string]func(json.RawMessage) any{"forge/issues": list("mac")})
	p.allHost("BOX", false, map[string]func(json.RawMessage) any{"forge/issues": list("box")})
	off := p.allHost("OFF", true, nil)
	off.stop()
	_, window := p.client("Window")

	var got wire.ForgeListAll
	json.Unmarshal(call(t, window, "", "forge/listAll", map[string]any{"kind": "issues", "state": "open"}), &got)
	if len(got.Lists) != 2 || len(got.Unreached) != 1 || got.Unreached[0].Machine != wire.MachineRef(off.member.Host) {
		t.Fatalf("%+v", got)
	}
	for _, l := range got.Lists {
		if l.List.Cursor == nil || l.MachineName == "" {
			t.Fatalf("%+v", l)
		}
	}
	if len(asked["mac"]) != 1 || len(asked["box"]) != 1 || deref(asked["mac"][0].State) != "open" {
		t.Fatalf("asked %+v", asked)
	}

	got = wire.ForgeListAll{}
	json.Unmarshal(call(t, window, "", "forge/listAll", map[string]any{"kind": "issues",
		"pages": []any{map[string]any{"machine": mac.member.Host, "cursor": "mac-2"}}}), &got)
	if len(got.Lists) != 1 || got.Lists[0].Machine != wire.MachineRef(mac.member.Host) || got.Lists[0].List.Cursor != nil {
		t.Fatalf("%+v", got)
	}
	if len(asked["box"]) != 1 || deref(asked["mac"][1].Cursor) != "mac-2" {
		t.Fatalf("asked %+v", asked)
	}

	got = wire.ForgeListAll{}
	json.Unmarshal(call(t, window, "", "forge/listAll", map[string]any{"kind": "issues", "board": "https://b"}), &got)
	if len(got.Lists) != 1 || got.Lists[0].Machine != wire.MachineRef(mac.member.Host) || len(asked["box"]) != 1 {
		t.Fatalf("%+v %+v", got, asked)
	}
	if e := callError(t, window, "", "forge/listAll", map[string]any{"kind": "epics"}); !strings.Contains(e, "issues or pulls") {
		t.Fatal(e)
	}
}

// #5592: boards, forecast, features, events and connections each come from every machine in one
// call, with the machine that gave them.
func TestEachMachineAnswersInOneCall(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	var mu sync.Mutex
	var eventLimits []int64
	answers := map[string]func(json.RawMessage) any{
		"forge/boards":     func(json.RawMessage) any { return map[string]any{"boards": []any{}} },
		"lineage/forecast": func(json.RawMessage) any { return map[string]any{"landings": 3} },
		"reports/features": func(json.RawMessage) any { return map[string]any{"features": []any{}, "days": 7} },
		"events/list": func(params json.RawMessage) any {
			var r wire.EventsListRequest
			json.Unmarshal(params, &r)
			mu.Lock()
			eventLimits = append(eventLimits, r.Limit)
			mu.Unlock()
			return map[string]any{"events": []any{}, "hasMore": false}
		},
		"connections/list": func(json.RawMessage) any { return map[string]any{"connections": []any{}} },
	}
	p.allHost("MAC", true, answers)
	p.allHost("BOX", true, answers)
	_, window := p.client("Window")

	count := func(method string, params any, field string) int {
		var got map[string]json.RawMessage
		json.Unmarshal(call(t, window, "", method, params), &got)
		var items []map[string]any
		json.Unmarshal(got[field], &items)
		for _, item := range items {
			if item["machine"] == "" || item["machineName"] == "" {
				t.Fatalf("%s: %v", method, item)
			}
		}
		return len(items)
	}
	for _, c := range []struct {
		method string
		params any
		field  string
	}{
		{"forge/boardsAll", map[string]any{}, "boards"},
		{"lineage/forecastAll", map[string]any{}, "forecasts"},
		{"reports/featuresAll", map[string]any{}, "usage"},
		{"events/listAll", map[string]any{"limit": 500}, "pages"},
		{"connections/listAll", map[string]any{}, "lists"},
	} {
		if n := count(c.method, c.params, c.field); n != 2 {
			t.Fatalf("%s: %d machines", c.method, n)
		}
	}
	// A limit past a page's most is the most.
	if len(eventLimits) != 2 || eventLimits[0] != mostEventsPage {
		t.Fatalf("events/list limits %v", eventLimits)
	}
}
