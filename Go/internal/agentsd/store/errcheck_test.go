package store

import (
	"encoding/json"
	"testing"
)

// mustUnmarshal decodes raw into into, and ends the test when it cannot (#6318).
func mustUnmarshal(t testing.TB, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
}

// errOnly is the error of a call that also gives a value the test does not need, as
// must(t, errOnly(s.Append(id, e))) (#6318).
func errOnly[V any](_ V, err error) error { return err }
