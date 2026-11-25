package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7183: an entry over the limit is a notice in its place with its id and time, whether its
// text or its id comes first, and each other line stays as it was, a torn last one too.
func TestBoundTranscriptCutsOnlyWhatIsTooLarge(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "transcript.jsonl")
	big := strings.Repeat("x", transcriptRecord+1)
	at := `"2026-11-24T11:00:00.000Z"`
	head := `{"trimmed":{"lines":3,"bytes":99}}`
	small := `{"id":"A","at":` + at + `,"kind":{}}`
	exact := small + strings.Repeat(" ", transcriptRecord-len(small))
	torn := `{"id":"T","at`
	lines := []string{head, small, `{"id":"B","at":` + at + `,"kind":{"t":"` + big + `"}}`, exact,
		`{"kind":{"t":"` + big + `"},"id":"C"}`, torn}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if cut, err := BoundTranscript(file); err != nil || !cut {
		t.Fatal(cut, err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(string(b), "\n")
	if len(got) != len(lines) || got[0] != head || got[1] != small || got[3] != exact || got[5] != torn {
		t.Fatalf("%d lines, or another line is changed", len(got))
	}
	for _, i := range []int{2, 4} {
		var e wire.TranscriptEntry
		if err := diskjson.Unmarshal([]byte(got[i]), &e); err != nil || e.Kind.Notice == nil || e.ID == "" {
			t.Fatalf("line %d: %v %.300s", i, err, got[i])
		}
		if _, when, ok := RecordStamp([]byte(got[i])); !ok || wire.DateOf(when) != mustDate(t, "2026-11-24T11:00:00.000Z") {
			t.Fatalf("line %d is at %v", i, when)
		}
		if i == 2 && e.ID != "B" {
			t.Fatalf("id %s", e.ID)
		}
	}
	if cut, err := BoundTranscript(file); err != nil || cut {
		t.Fatal("a file within the limit is cut", err)
	}
}

func mustDate(t *testing.T, s string) wire.Date {
	t.Helper()
	d, err := diskjson.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
