package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #6908: a choice too large to keep leaves the earlier one.
func TestAChoiceOverTheLargestKeepsTheEarlierOne(t *testing.T) {
	t.Skip("#7194")
	h := newHarness(t)
	id := h.add()
	old, _ := json.Marshal(map[string]any{"next": 2, "chosen": map[string]any{id: map[string]any{"size": "big"}}})
	if _, err := h.store.Put(t.Context(), machineNamesKey(""), old, store.Absent); err != nil {
		t.Fatal(err)
	}
	h.must(MethodProvidersCreate, map[string]any{"id": id, "options": map[string]any{"size": strings.Repeat("x", machineNamesMost)}})
	object, err := h.store.Get(t.Context(), machineNamesKey(""))
	if err != nil {
		t.Fatal(err)
	}
	var kept machineNames
	if err := json.Unmarshal(object.Data, &kept); err != nil || string(kept.Chosen[id]) != `{"size":"big"}` {
		t.Fatalf("the earlier choice is %s", kept.Chosen[id])
	}
}
