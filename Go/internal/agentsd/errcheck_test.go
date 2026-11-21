package agentsd

import "testing"

// must ends the test when err is not nil: a set-up step that did not work (#6318).
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// check marks the test failed when err is not nil, and goes on: for a handler or a
// goroutine, where t.Fatal must not run (#6318).
func check(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Error(err)
	}
}

// errOnly is the error of a call that also gives a value the test does not need, as
// must(t, errOnly(w.Write(b))) (#6318).
func errOnly[V any](_ V, err error) error { return err }
