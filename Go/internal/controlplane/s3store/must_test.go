package s3store

import "testing"

// must fails the test when a step of its set-up returns an error (#6319).
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
