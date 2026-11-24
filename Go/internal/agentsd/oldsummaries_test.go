package agentsd

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// #6714: the hourly sweep lets go of an old daily summary 90 days after its last change and
// keeps the newest 3; the marks go with the last page, and the person's own files stay.
func TestTheHourlySweepLetsGoOfOldDailySummaries(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.PersonalHome = t.TempDir()
	dir := filepath.Join(d.PersonalHome, ".agents", "summaries")
	now := time.Now()
	past := now.Add(-oldSummaries.Bounds.MaxAge - time.Minute)
	const text = "# Daily summary, Fri 6 Nov\n"
	for i := range oldSummaries.Bounds.MaxCount + 1 {
		writeReport(t, dir, fmt.Sprintf("2026-11-0%d-0800.md", i+1), text, now.Add(time.Duration(i-5)*time.Hour))
	}
	writeReport(t, dir, "2026-08-01-0800.md", text, past)
	writeReport(t, dir, "pages.json", `{"2026-11-01-0800":{"writer":"E45D558E-D28B-4A81-8DD2-A3B38FCD3099","unread":true}}`, past)
	mine := []string{"2026-08-02-0800.md", "2026-08-03-0800.md", "2026-08-04-0800-copy.md", "notes.md"}
	writeReport(t, dir, mine[0], "# My day\n", past)
	writeReport(t, dir, mine[1], text+strings.Repeat("x", oldSummaries.Bounds.MaxRecordBytes), past)
	writeReport(t, dir, mine[2], text, past)
	writeReport(t, dir, mine[3], text, past)

	d.sweepHourly()
	want := append(slices.Clone(mine), "2026-11-02-0800.md", "2026-11-03-0800.md", "2026-11-04-0800.md", "pages.json")
	if got := dirNames(t, dir); !sameNames(got, want) {
		t.Fatalf("after the first sweep: %v", got)
	}

	d.sweepOldSummaries(now.Add(oldSummaries.Bounds.MaxAge))
	if got := dirNames(t, dir); !sameNames(got, mine) {
		t.Fatalf("after 90 days: %v", got)
	}
}

// A page git tracks and a pages.json of another shape are the person's; with no home the
// sweep touches nothing.
func TestTheSweepLeavesWhatIsNotAnOldDailySummary(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	home := t.TempDir()
	dir := filepath.Join(home, ".agents", "summaries")
	past := time.Now().Add(-oldSummaries.Bounds.MaxAge - time.Minute)
	writeReport(t, dir, "2026-08-01-0800.md", "# Daily summary\n", past)
	writeReport(t, dir, "pages.json", `{"home":{"title":"mine"}}`, past)
	for _, args := range [][]string{{"init", "-q"}, {"add", ".agents/summaries/2026-08-01-0800.md"}} {
		if _, err := git(home, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []string{"", home} {
		d.PersonalHome = h
		d.sweepOldSummaries(time.Now())
		if got := dirNames(t, dir); !sameNames(got, []string{"2026-08-01-0800.md", "pages.json"}) {
			t.Fatalf("home %q: %v", h, got)
		}
	}
}

func sameNames(got, want []string) bool {
	got, want = slices.Clone(got), slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
}
