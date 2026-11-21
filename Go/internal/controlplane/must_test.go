package controlplane

import "testing"

// must fails the test when a step of its set-up returns an error (#6319).
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fakeMust is must for a fake (a machine's handler or a notification it hears) that runs
// off the test's goroutine with no t: a step that fails panics, so the test fails at once
// at that line, not later on a zero value.
func fakeMust(err error) {
	if err != nil {
		panic(err)
	}
}
