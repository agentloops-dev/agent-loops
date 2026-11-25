package citimings

import (
	"strings"
	"testing"
	"time"
)

// #6908: a run over the largest line is refused or left out, and the others of its day stay.
func TestARunOverTheLargestLeavesTheOthers(t *testing.T) {
	t.Skip("#7190")
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
