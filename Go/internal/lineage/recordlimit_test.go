package lineage

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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
