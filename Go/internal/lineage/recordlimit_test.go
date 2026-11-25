package lineage

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/repoalias"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6908: a node at its largest is taken and read again, and one a byte over it is refused.
func TestRecordLimitOfANode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lineage.jsonl")
	f := openAt(t, path, t0)
	ref := "github:issue:o/r#"
	at := issue("1")
	at.Ref = ref + strings.Repeat("1", maxRef-len(ref))
	over := at
	over.Ref += "1"
	link(t, f, at, issue("2"), RelPartOf, t0)
	if _, err := f.Link(wire.LineageLinkRequest{From: over, To: issue("2"), Rel: RelPartOf, Source: SourcePerson}, t0); err == nil {
		t.Fatal("a ref over the largest was taken")
	}
	if _, err := f.SetState(over, wire.LineageSessionStateNameWorking, t0); err == nil {
		t.Fatal("a ref over the largest was taken")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := openAt(t, path, t0).Links(at); len(got.Links) != 1 {
		t.Fatalf("%d links read again", len(got.Links))
	}
}

// A link that was taken is read again, also when its repo's move makes its ref longer.
func TestALinkWhoseRefAMoveMakesLongerIsReadAgain(t *testing.T) {
	was := repoalias.All()
	t.Cleanup(func() { repoalias.Set(was) })
	repoalias.Set(nil)
	if !repoalias.Add("github", "o/r", "a-much-longer-owner/a-longer-name", time.Now()) {
		t.Fatal("no alias")
	}
	path := filepath.Join(t.TempDir(), "lineage.jsonl")
	f := openAt(t, path, t0)
	ref := "github:issue:o/r#"
	at := issue("1")
	at.Ref = ref + strings.Repeat("1", maxRef-len(ref))
	_, err := f.Link(wire.LineageLinkRequest{From: at, To: issue("2"), Rel: RelPartOf, Source: SourcePerson}, t0)
	if err != nil {
		return // refused, so nothing is lost
	}
	link(t, f, issue("3"), issue("2"), RelPartOf, t0)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := openAt(t, path, t0).Links(issue("2")); len(got.Links) != 2 {
		t.Fatalf("%d of 2 links read again", len(got.Links))
	}
}

// #7247: the largest change is inside the largest line, a line at it is read, and one over it is
// refused, or set aside when it is in the file.
func TestTheLargestLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lineage.jsonl")
	f := openAt(t, path, t0)
	word := strings.Repeat("w", 40)
	large := func(c string) wire.LineageNode {
		ref := "github:issue:o/r#"
		return wire.LineageNode{Kind: word, Ref: ref + strings.Repeat(c, maxRef-len(ref))}
	}
	if _, err := f.Link(wire.LineageLinkRequest{From: large("1"), To: large("2"), Rel: word, Source: word}, t0); err != nil {
		t.Fatal(err)
	}
	_, err := f.Link(wire.LineageLinkRequest{From: large("\x01"), To: large("\x02"), Rel: word, Source: word}, t0)
	if !errors.Is(err, store.ErrLimit) || len(f.Links(large("\x01")).Links) != 0 {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pad := func(n int) string {
		line := `{"pad":"","state":{"node":{"kind":"session","ref":"agents:session:o/r#` + strconv.Itoa(n) + `"},"state":"working"},"at":1}`
		return strings.Replace(line, `"pad":"`, `"pad":"`+strings.Repeat("p", n-len(line)), 1) + "\n"
	}
	data = append(data, pad(lineMost)+pad(lineMost+1)+pad(lineMost-1)...)
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // G703: a path in the test's own folder
		t.Fatal(err)
	}
	if _, states := openAt(t, path, t0).All(); len(states) != 2 {
		t.Fatalf("%d states", len(states))
	}
	if aside, _ := filepath.Glob(path + ".unreadable-*"); len(aside) != 1 {
		t.Fatalf("%d set aside", len(aside))
	}
}
