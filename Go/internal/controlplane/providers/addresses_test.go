package providers

import (
	"reflect"
	"strings"
	"testing"
)

// A contract provider's addresses reach the machine's record, its text items only, at most
// eight, each at most 64 runes (#7466).
func TestContractWorkspaceKeepsItsPublicAddresses(t *testing.T) {
	item := map[string]any{"id": "d1", "name": "devbox", "state": "running",
		"addresses": []any{"203.0.113.7", " 2001:db8::7 ", "", 42, strings.Repeat("a", 65)}}
	w := ContractMapping.Workspace(item)
	if w == nil || !reflect.DeepEqual(w.Addresses, []string{"203.0.113.7", "2001:db8::7"}) {
		t.Fatalf("%+v", w)
	}
	many := make([]any, 10)
	for i := range many {
		many[i] = "203.0.113.1"
	}
	if w := ContractMapping.Workspace(map[string]any{"id": "d1", "name": "devbox", "addresses": many}); len(w.Addresses) != 8 {
		t.Errorf("%d addresses", len(w.Addresses))
	}
	if w := ContractMapping.Workspace(map[string]any{"id": "d1", "name": "devbox", "addresses": "203.0.113.7"}); w.Addresses != nil {
		t.Errorf("%v", w.Addresses)
	}
}
