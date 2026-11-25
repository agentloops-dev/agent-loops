package citimings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #6908: a run over the largest line is refused or left out, and the others of its day stay.
func TestARunOverTheLargestLeavesTheOthers(t *testing.T) {
	s, r := &Store{Root: t.TempDir()}, Repo{"github", "o/r"}
	made := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC).Unix()
	run := func(ref, job string, at int64) Run {
		return Run{Ref: ref, Attempt: 1, Made: made + at, Jobs: []Job{{Name: job}}}
	}
	if err := s.Put(r, run("github:run:o/r#1", "a", 0), run("github:run:o/r#3", "c", 2)); err != nil {
		t.Fatal(err)
	}
	err := s.Put(r, run("github:run:o/r#2", strings.Repeat("x", mostLine), 1))
	t.Log(err)
	if err := s.Put(r, run("github:run:o/r#4", "d", 3)); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"1", "3", "4"} {
		if !s.Has(r, "github:run:o/r#"+ref, 1, time.Unix(made, 0)) {
			t.Errorf("run %s is gone", ref)
		}
	}
}

// #7190: a run over the largest line loses its steps, then its jobs, and stays in the store.
func TestARunOverTheLargestIsKeptWithLess(t *testing.T) {
	s, r := &Store{Root: t.TempDir()}, Repo{"github", "o/r"}
	made := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	big := strings.Repeat("x", mostLine)
	steps := Run{Ref: "github:run:o/r#1", Attempt: 1, Made: made.Unix(), Jobs: []Job{{Name: "a", Steps: []Step{{Name: big}}}}}
	jobs := Run{Ref: "github:run:o/r#2", Attempt: 1, Made: made.Unix() + 1, Ended: 9, Jobs: []Job{{Name: big}}}
	none := Run{Ref: "github:run:o/r#" + big, Attempt: 1, Made: made.Unix() + 2}
	if err := s.Put(r, steps, jobs, none); err != nil {
		t.Fatal(err)
	}
	got, err := s.Day(r, "2026-11-24")
	if err != nil || len(got) != 2 {
		t.Fatalf("%d runs, %v", len(got), err)
	}
	if len(got[0].Jobs) != 1 || got[0].hasSteps() {
		t.Errorf("run 1 is not its job without steps")
	}
	if got[1].Ref != jobs.Ref || got[1].Ended != 9 || len(got[1].Jobs) != 0 {
		t.Errorf("run 2 is not kept without its jobs")
	}
	if !steps.hasSteps() {
		t.Error("Put changed the caller's run")
	}
}

// #7190: a line over the largest, from before Put bounded one, does not end the day's read.
func TestAnOldLineOverTheLargestLeavesTheOthers(t *testing.T) {
	s, r := &Store{Root: t.TempDir()}, Repo{"github", "o/r"}
	made := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC).Unix()
	var file []byte
	for i, name := range []string{strings.Repeat("x", mostLine), "b"} {
		line, err := json.Marshal(Run{Ref: "github:run:o/r#" + name[:1], Attempt: 1, Made: made + int64(i), Jobs: []Job{{Name: name}}})
		if err != nil {
			t.Fatal(err)
		}
		file = append(append(file, line...), '\n')
	}
	path := filepath.Join(s.Root, "github", "o", "r", "2026-11-24.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(r, Run{Ref: "github:run:o/r#c", Attempt: 1, Made: made + 2}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Day(r, "2026-11-24")
	if err != nil || len(got) != 3 || len(got[0].Jobs) != 0 || len(got[1].Jobs) != 1 {
		t.Errorf("%d runs, %v", len(got), err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() > mostLine {
		t.Errorf("the old line is not bounded: %v", err)
	}
}
