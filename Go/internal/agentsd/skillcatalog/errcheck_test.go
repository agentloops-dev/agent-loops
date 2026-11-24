package skillcatalog

import (
	"encoding/json"
	"testing"
)

// must ends the test when err is not nil: a set-up step that did not work (#6318).
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// mustUnmarshal decodes raw into into, and ends the test when it cannot (#6318).
func mustUnmarshal(t testing.TB, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
}
