package store

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// #2292: a file set aside again and again keeps its newest 3 copies of each kind.
func TestTheNewestSetAsidesAreKept(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "events.jsonl")
	other := filepath.Join(dir, "devices.json.corrupt-1")
	must(t, os.WriteFile(file, []byte("{}"), 0o644))  //nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, os.WriteFile(other, []byte("{}"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	// 9 and 10 sort below 8 as text: the time is a number.
	for _, at := range []int{1, 2, 8, 9, 10} {
		must(t, os.WriteFile(fmt.Sprintf("%s.unreadable-%d", file, at), []byte("{}"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
		must(t, os.WriteFile(fmt.Sprintf("%s.corrupt-%d", file, at), []byte("{}"), 0o644))    //nolint:gosec // G306: the mode this file always had; only its error is new
	}
	SweepSetAside(file, time.Unix(11, 0))
	entries, _ := os.ReadDir(dir)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{"devices.json.corrupt-1", "events.jsonl",
		"events.jsonl.corrupt-10", "events.jsonl.corrupt-8", "events.jsonl.corrupt-9",
		"events.jsonl.unreadable-10", "events.jsonl.unreadable-8", "events.jsonl.unreadable-9"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%v\nwant %v", got, want)
	}
}

// #6737: a copy goes at 90 days, also one that nothing writes beside again and one with a time
// to come; past the total size the older go first, and a name that is not a copy's stays.
func TestSetAsidesAgeOut(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	day := 24 * time.Hour
	name := func(file string, age time.Duration) string {
		return fmt.Sprintf("%s.corrupt-%d", file, now.Add(-age).Unix())
	}
	const mac = "hosts.json.corrupt-"
	stays := []string{name("a.json", 89*day), name("b.json", 0), mac + now.UTC().Format("2006-01-02T150405Z"), "c.json.corrupt-old", name("ahead.json", -400*day)}
	goes := []string{name("a.json", 90*day), name("b.json", day), mac + "2020-01-02T030405Z-2"}
	for _, f := range append(slices.Clone(stays), goes...) {
		must(t, os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o600))
	}
	for _, f := range []string{stays[1], goes[1]} { // the newest stays at any size
		must(t, os.Truncate(filepath.Join(dir, f), SetAsidesBytes+1))
	}
	if n := SweepSetAsides(dir, now); n != len(goes) {
		t.Fatal(n)
	}
	for _, f := range stays {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	if n := SweepSetAsides(dir, time.Now().Add(90*day)); n != 4 {
		t.Fatal(n)
	}
}
