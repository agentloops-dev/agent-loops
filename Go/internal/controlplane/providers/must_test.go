package providers

import "testing"

// must fails the test when a step of its set-up returns an error (#6319).
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// mustKeep fails the test unless Keep kept what it was given: its answer is CodeKept.
func mustKeep(t testing.TB, f *Failure) {
	t.Helper()
	if f == nil || f.Code != CodeKept {
		t.Fatalf("not kept: %+v", f)
	}
}

// mustParse is JSON text that a test writes, parsed; it panics on text that is not JSON.
func mustParse(text string) any {
	v, err := parseJSON([]byte(text))
	if err != nil {
		panic(err)
	}
	return v
}
