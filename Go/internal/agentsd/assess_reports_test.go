package agentsd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func writeReport(t *testing.T, dir, name, content string, at time.Time) {
	t.Helper()
	must(t, os.MkdirAll(dir, 0o755)) //nolint:gosec // G301: as the daemon makes it
	p := filepath.Join(dir, name)
	must(t, os.WriteFile(p, []byte(content), 0o644)) //nolint:gosec // G306: a report, as an agent writes it
	must(t, os.Chtimes(p, at, at))
}

// The hourly sweep holds the reports of all folders to one count, whether or not an
// assessment's start is kept, and leaves what is not a report.
func TestTheNewestAssessmentReportsAreKept(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	one, two := reportsDir(folder(t, c, 1)), reportsDir(folder(t, c, 2))
	began := time.Now().Add(-time.Hour)
	for i := range assessReportsKept + 2 {
		// 00 and 01, the oldest, are one in each folder.
		writeReport(t, []string{one, two}[i%2], fmt.Sprintf("codex-2026-11-06-%02d.md", i), "report", began.Add(time.Duration(i)*time.Second))
	}
	ancient := began.Add(-365 * 24 * time.Hour)
	writeReport(t, one, "notes.md", "mine", ancient)

	d.sweepHourly()
	in := append(dirNames(t, one), dirNames(t, two)...)
	if len(in) != assessReportsKept+1 || !slices.Contains(in, "notes.md") ||
		slices.Contains(in, "codex-2026-11-06-00.md") || slices.Contains(in, "codex-2026-11-06-01.md") {
		t.Fatalf("%d: %v", len(in), in)
	}
}

// A report ages out at 90 days, and one that git tracks does not.
func TestAnOldAssessmentReportGoesUnlessCommitted(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	dir := reportsDir(repo)
	now := time.Now()
	past := now.Add(-assessReportMaxAge - time.Minute)
	writeReport(t, dir, "claude-2026-01-01.md", "committed", past)
	writeReport(t, dir, "claude-2026-01-02.md", "old", past)
	writeReport(t, dir, "claude-2026-01-03.md", "due tomorrow", past.Add(24*time.Hour))
	for _, args := range [][]string{{"init", "-q"}, {"add", ".agents/reviews/runtimes/claude-2026-01-01.md"}} {
		if _, err := git(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	sweepReports([]reportDocuments{reportFiles{dir}}, now)
	if got := dirNames(t, dir); !slices.Equal(got, []string{"claude-2026-01-01.md", "claude-2026-01-03.md"}) {
		t.Fatalf("%v", got)
	}

	// A repository git cannot read: nothing goes.
	must(t, os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("broken"), 0o600))
	sweepReports([]reportDocuments{reportFiles{dir}}, now.Add(assessReportMaxAge))
	if got := dirNames(t, dir); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

// A report at the largest size stays as it is; one over it is cut, between two characters,
// and keeps its age. A Put over the size, or on a report written since, is refused.
func TestAnAssessmentReportIsCutToSize(t *testing.T) {
	t.Parallel()

	dir := reportsDir(t.TempDir())
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	full := strings.Repeat("x", assessReportMaxBytes)
	writeReport(t, dir, "codex-2026-11-06.md", full, at)
	writeReport(t, dir, "grok-2026-11-06.md", full[1:]+"é and more", at)
	files := reportFiles{dir}
	sweepReports([]reportDocuments{files}, time.Now())

	docs, next, err := files.List("", 1)
	if err != nil || len(docs) != 1 || docs[0].size != assessReportMaxBytes || next != docs[0].id {
		t.Fatalf("%+v %q %v", docs, next, err)
	}
	docs, next, err = files.List(next, 1)
	if err != nil || len(docs) != 1 || docs[0].size != assessReportMaxBytes-1 || next != "" || !docs[0].at.Equal(at) {
		t.Fatalf("%+v %q %v", docs, next, err)
	}
	if b, err := files.Get("grok-2026-11-06.md"); err != nil || !utf8.Valid(b) {
		t.Fatal(err)
	}
	if files.Put(docs[0], []byte(full+"x")) == nil {
		t.Fatal("a report over the largest size was written")
	}
	docs[0].size--
	if err := files.Put(docs[0], nil); !errors.Is(err, errReportChanged) {
		t.Fatal(err)
	}
	if _, err := files.Get("../runtimes/codex-2026-11-06.md"); err == nil {
		t.Fatal("read a path outside the collection")
	}
}
