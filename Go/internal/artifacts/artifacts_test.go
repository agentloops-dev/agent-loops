package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/agentloops-dev/agent-loops/Go/internal/blobstore"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

func newStore(t *testing.T) (*Store, *blobstore.Keyed, *store.Memory) {
	t.Helper()
	records := store.NewMemory()
	blobs := blobstore.NewKeyed(records, "v1/artifacts/")
	return New(blobs, records, "v1/artifacts/"), blobs, records
}

func doc(text string) []Layer {
	return []Layer{{MediaType: "text/markdown", Title: "plan.md", Data: []byte(text)}}
}

func mustPut(t *testing.T, s *Store, p Put) (Record, Version, bool) {
	t.Helper()
	rec, v, diverged, err := s.Put(context.Background(), p)
	if err != nil {
		t.Fatalf("put %+v: %v", p, err)
	}
	return rec, v, diverged
}

func TestFirstVersionMakesTheRecordAndAnOCIManifest(t *testing.T) {
	t.Parallel()
	s, blobs, _ := newStore(t)
	ctx := context.Background()
	rec, v, diverged := mustPut(t, s, Put{Topic: "agent-loops", ID: "plan-5713", Kind: "document", Author: "person:alex",
		Labels: map[string]string{"epic": "5713"},
		Layers: []Layer{
			{MediaType: "text/markdown", Title: "plan.md", Data: []byte("# Plan")},
			{MediaType: "image/svg+xml", Title: "diagram.svg", Data: []byte("<svg/>")},
		}})
	if diverged || rec.Versions != 1 || rec.Head != v.Digest || rec.Name != "plan-5713" || len(rec.Heads) != 0 || v.Number != 1 || v.Capture != "manual" {
		t.Fatalf("%+v %+v %v", rec, v, diverged)
	}
	data, err := blobs.Get(ctx, v.Digest)
	if err != nil {
		t.Fatal(err)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 2 || m.MediaType != ocispec.MediaTypeImageManifest || m.ArtifactType != "application/vnd.agentloops.document.v1" ||
		m.Config.Digest != ocispec.DescriptorEmptyJSON.Digest || len(m.Layers) != 2 ||
		m.Layers[1].Annotations[ocispec.AnnotationTitle] != "diagram.svg" || m.Layers[0].Digest.String() != string(blobstore.Of([]byte("# Plan"))) ||
		m.Annotations["dev.agentloops.author"] != "person:alex" || m.Annotations["dev.agentloops.label.epic"] != "5713" ||
		m.Annotations["dev.agentloops.parent"] != "" || m.Annotations[ocispec.AnnotationCreated] == "" {
		t.Fatalf("manifest: %s", data)
	}
	// The config blob is there too, so the store is an OCI layout's blobs.
	if _, err := blobs.Get(ctx, blobstore.Digest(ocispec.DescriptorEmptyJSON.Digest)); err != nil {
		t.Fatal(err)
	}
	got, gv, err := s.Get(ctx, NameRef("agent-loops", "plan-5713"))
	if err != nil || got.Head != v.Digest || gv.Digest != v.Digest || len(gv.Layers) != 2 || gv.Author != "person:alex" {
		t.Fatalf("get: %+v %+v %v", got, gv, err)
	}
	content, err := s.Content(ctx, gv)
	if err != nil || string(content[0]) != "# Plan" || string(content[1]) != "<svg/>" {
		t.Fatalf("content: %q %v", content, err)
	}
}

func TestHeadMovesOnlyFromTheHeadTheWriterSaw(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, v1, _ := mustPut(t, s, Put{Topic: "p", ID: "a", Kind: "document", Author: "person:alex", Layers: doc("one")})
	rec, v2, diverged := mustPut(t, s, Put{Topic: "p", ID: "a", Author: "session:abc", Capture: "settled", Parents: []blobstore.Digest{v1.Digest}, Layers: doc("two")})
	if diverged || rec.Head != v2.Digest || rec.Versions != 2 || len(rec.Heads) != 0 || v2.Parents[0] != v1.Digest {
		t.Fatalf("%+v %v", rec, diverged)
	}
	// A writer that saw v1, not v2, makes a second head: v2 is not overwritten.
	rec, v3, diverged := mustPut(t, s, Put{Topic: "p", ID: "a", Author: "person:alex", Parents: []blobstore.Digest{v1.Digest}, Layers: doc("three")})
	if !diverged || rec.Head != v3.Digest || len(rec.Heads) != 2 || rec.Heads[0] != v2.Digest || rec.Heads[1] != v3.Digest {
		t.Fatalf("diverged: %+v %v", rec, diverged)
	}
	// A version whose parents are both heads settles it.
	rec, v4, diverged := mustPut(t, s, Put{Topic: "p", ID: "a", Author: "person:alex", Parents: []blobstore.Digest{v2.Digest, v3.Digest}, Layers: doc("four")})
	if diverged || rec.Head != v4.Digest || len(rec.Heads) != 0 || rec.Versions != 4 {
		t.Fatalf("settled: %+v %v", rec, diverged)
	}
	// Every version is still there, by its digest.
	for i, v := range []Version{v1, v2, v3, v4} {
		_, got, err := s.Get(ctx, VersionRef("p", v.Digest))
		if err != nil || got.Number != i+1 || got.Digest != v.Digest {
			t.Fatalf("version %d: %+v %v", i+1, got, err)
		}
	}
	_, got, _ := s.Get(ctx, VersionRef("p", v4.Digest))
	if len(got.Parents) != 2 || got.Parents[0] != v2.Digest || got.Parents[1] != v3.Digest {
		t.Fatalf("parents: %+v", got.Parents)
	}
}

func TestRacingWritersLoseNothing(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, v1, _ := mustPut(t, s, Put{Topic: "p", ID: "a", Kind: "document", Author: "person:alex", Layers: doc("one")})
	const writers = 6
	var wg sync.WaitGroup
	made := make(chan blobstore.Digest, writers)
	for i := range writers {
		wg.Go(func() {
			_, v, _, err := s.Put(ctx, Put{Topic: "p", ID: "a", Author: "person:alex", Parents: []blobstore.Digest{v1.Digest}, Layers: doc(fmt.Sprint("w", i))})
			if err != nil {
				t.Error(err)
				return
			}
			made <- v.Digest
		})
	}
	wg.Wait()
	close(made)
	rec, err := s.Record(ctx, "p", "a")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Versions != 1+writers || len(rec.Heads) != writers {
		t.Fatalf("%d versions, heads %v", rec.Versions, rec.Heads)
	}
	for d := range made {
		found := false
		for _, h := range rec.Heads {
			found = found || h == d
		}
		if !found {
			t.Fatalf("%s is not a head", d)
		}
	}
	entries, _, err := s.Versions(ctx, "p", "a", 0, 0)
	if err != nil || len(entries) != 1+writers {
		t.Fatalf("log: %d %v", len(entries), err)
	}
	seen := map[int]bool{}
	for _, e := range entries {
		seen[e.Number] = true
	}
	if len(seen) != 1+writers {
		t.Fatalf("numbers: %v", seen)
	}
}

func TestVersionsArePagedNewestFirst(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()
	var parent []blobstore.Digest
	for i := range 7 {
		_, v, _ := mustPut(t, s, Put{Topic: "p", ID: "a", Kind: "document", Author: "person:alex", Parents: parent, Layers: doc(fmt.Sprint(i))})
		parent = []blobstore.Digest{v.Digest}
	}
	var numbers []int
	before, pages := 0, 0
	for {
		page, next, err := s.Versions(ctx, "p", "a", before, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page {
			numbers = append(numbers, e.Number)
		}
		pages++
		if next == 0 {
			break
		}
		before = next
	}
	if fmt.Sprint(numbers) != "[7 6 5 4 3 2 1]" || pages != 3 {
		t.Fatalf("%v in %d pages", numbers, pages)
	}
}

func TestALostLogLineIsRepaired(t *testing.T) {
	t.Parallel()
	s, _, records := newStore(t)
	ctx := context.Background()
	_, v1, _ := mustPut(t, s, Put{Topic: "p", ID: "a", Kind: "document", Author: "person:alex", Layers: doc("one")})
	// As if the line's write was lost after the record moved.
	if err := records.Delete(ctx, s.entryKey("a", 1)); err != nil {
		t.Fatal(err)
	}
	if page, _, err := s.Versions(ctx, "p", "a", 0, 0); err != nil || len(page) != 1 || page[0].Digest != v1.Digest {
		t.Fatalf("read from the record: %+v %v", page, err)
	}
	mustPut(t, s, Put{Topic: "p", ID: "a", Author: "person:alex", Parents: []blobstore.Digest{v1.Digest}, Layers: doc("two")})
	if o, err := records.Get(ctx, s.entryKey("a", 1)); err != nil || o == nil {
		t.Fatalf("line 1 not written again: %v", err)
	}
}

func TestTombstoneIsAVersion(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, v1, _ := mustPut(t, s, Put{Topic: "p", ID: "a", Kind: "image", Author: "person:alex",
		Layers: []Layer{{MediaType: "image/png", Title: "a.png", Data: []byte{0x89, 'P', 'N', 'G'}}}})
	rec, v2, _ := mustPut(t, s, Put{Topic: "p", ID: "a", Author: "person:alex", Parents: []blobstore.Digest{v1.Digest}, Tombstone: true})
	if !rec.Deleted || !v2.Tombstone || len(v2.Layers) != 0 || rec.Versions != 2 {
		t.Fatalf("%+v %+v", rec, v2)
	}
	_, got, err := s.Get(ctx, NameRef("p", "a"))
	if err != nil || !got.Tombstone || len(got.Layers) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	_, old, err := s.Get(ctx, VersionRef("p", v1.Digest))
	if err != nil || len(old.Layers) != 1 {
		t.Fatalf("the old version: %+v %v", old, err)
	}
	// A new version brings it back.
	rec, _, _ = mustPut(t, s, Put{Topic: "p", ID: "a", Author: "person:alex", Parents: []blobstore.Digest{v2.Digest},
		Layers: []Layer{{MediaType: "image/png", Title: "a.png", Data: []byte("again")}}})
	if rec.Deleted {
		t.Fatalf("%+v", rec)
	}
}

func TestRefusals(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, v1, _ := mustPut(t, s, Put{Topic: "p", ID: "a", Kind: "document", Author: "person:alex", Layers: doc("one")})
	big := bytes.Repeat([]byte("x"), MaxPutBytes+1)
	many := make([]Layer, MaxLayers+1)
	for i := range many {
		many[i] = Layer{MediaType: "text/plain", Title: fmt.Sprint(i), Data: []byte("x")}
	}
	for name, c := range map[string]struct {
		p    Put
		want error
	}{
		"no kind":       {Put{Topic: "p", ID: "b", Author: "person:alex", Layers: doc("x")}, ErrInvalid},
		"bad kind":      {Put{Topic: "p", ID: "b", Kind: "pdf", Author: "person:alex", Layers: doc("x")}, ErrInvalid},
		"kind changes":  {Put{Topic: "p", ID: "a", Kind: "image", Author: "person:alex", Parents: []blobstore.Digest{v1.Digest}, Layers: doc("x")}, ErrInvalid},
		"bad id":        {Put{Topic: "p", ID: "../a", Kind: "document", Author: "person:alex", Layers: doc("x")}, ErrInvalid},
		"bad topic":     {Put{Topic: "a/b", ID: "b", Kind: "document", Author: "person:alex", Layers: doc("x")}, ErrInvalid},
		"no author":     {Put{Topic: "p", ID: "b", Kind: "document", Layers: doc("x")}, ErrInvalid},
		"no files":      {Put{Topic: "p", ID: "b", Kind: "document", Author: "person:alex"}, ErrInvalid},
		"slash title":   {Put{Topic: "p", ID: "b", Kind: "document", Author: "person:alex", Layers: []Layer{{MediaType: "text/plain", Title: "a/b"}}}, ErrInvalid},
		"same title":    {Put{Topic: "p", ID: "b", Kind: "document", Author: "person:alex", Layers: append(doc("x"), doc("y")...)}, ErrInvalid},
		"too big":       {Put{Topic: "p", ID: "b", Kind: "binary", Author: "person:alex", Layers: []Layer{{MediaType: "application/octet-stream", Title: "b", Data: big}}}, ErrLimit},
		"too many":      {Put{Topic: "p", ID: "b", Kind: "build", Author: "person:alex", Layers: many}, ErrInvalid},
		"other topic":   {Put{Topic: "q", ID: "a", Author: "person:alex", Parents: []blobstore.Digest{v1.Digest}, Layers: doc("x")}, ErrNotFound},
		"new, a parent": {Put{Topic: "p", ID: "b", Kind: "document", Author: "person:alex", Parents: []blobstore.Digest{v1.Digest}, Layers: doc("x")}, ErrNotFound},
		"new, deleted":  {Put{Topic: "p", ID: "b", Kind: "document", Author: "person:alex", Tombstone: true}, ErrNotFound},
	} {
		if _, _, _, err := s.Put(ctx, c.p); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// A ref in another topic, or a digest that is not a version, is not found.
	for _, ref := range []Ref{NameRef("q", "a"), VersionRef("q", v1.Digest), VersionRef("p", blobstore.Of([]byte("one"))), NameRef("p", "nope")} {
		if _, _, err := s.Get(ctx, ref); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", ref, err)
		}
	}
	if _, _, err := s.Get(ctx, Ref{System: "oci", Topic: "ghcr.io/alexec/agents-control", ID: string(v1.Digest)}); !errors.Is(err, ErrNotFound) {
		t.Errorf("referenced: %v", err)
	}
}

func TestTooManyHeadsMustBeSettled(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	mustPut(t, s, Put{Topic: "p", ID: "a", Kind: "document", Author: "person:alex", Layers: doc("0")})
	var err error
	for i := 1; i <= MaxHeads; i++ {
		// No parents: each writer saw nothing, so each is one more head.
		_, _, _, err = s.Put(context.Background(), Put{Topic: "p", ID: "a", Author: "person:alex", Layers: doc(fmt.Sprint(i))})
	}
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	rec, _ := s.Record(context.Background(), "p", "a")
	if len(rec.Heads) != MaxHeads {
		t.Fatalf("%d heads", len(rec.Heads))
	}
	if _, _, diverged, err := s.Put(context.Background(), Put{Topic: "p", ID: "a", Author: "person:alex", Parents: rec.Heads, Layers: doc("settled")}); err != nil || diverged {
		t.Fatalf("settle: %v %v", diverged, err)
	}
}

func TestRefs(t *testing.T) {
	t.Parallel()
	d := blobstore.Of([]byte("x"))
	for text, want := range map[string]Ref{
		"agents:artifact:agent-loops#plan-5713":                   {"agents", "agent-loops", "plan-5713"},
		"agents:artifact:agent-loops#" + string(d):                {"agents", "agent-loops", string(d)},
		"oci:artifact:ghcr.io/alexec/agents-control#" + string(d): {"oci", "ghcr.io/alexec/agents-control", string(d)},
		"testflight:artifact:agent-loops#812":                     {"testflight", "agent-loops", "812"},
	} {
		got, err := ParseRef(text)
		if err != nil || got != want || got.String() != text {
			t.Errorf("%s: %+v %v", text, got, err)
		}
	}
	for _, bad := range []string{"", "agents:plan", "agents:artifact:p", "agents:artifact:#a", "agents:artifact:p#", "agents:artifact:p#../x",
		"agents:artifact:a/b#x", "Agents:artifact:p#a", "agents:artifact:p#sha256:nothex"} {
		if _, err := ParseRef(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if r, _ := ParseRef("agents:artifact:p#" + string(d)); !r.Stored() {
		t.Error("stored")
	} else if got, ok := r.Digest(); !ok || got != d {
		t.Error("digest")
	}
}

func TestTheStoreIsBoundedInArtifacts(t *testing.T) {
	t.Parallel()
	s, _, records := newStore(t)
	ctx := context.Background()
	// Fill the folders cheaply: a record each, as the store keeps them.
	for i := range MaxArtifacts {
		if _, err := records.Put(ctx, s.recordKey(fmt.Sprint("x", i)), []byte("{}"), store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := s.Put(ctx, Put{Topic: "p", ID: "one-more", Kind: "document", Author: "person:alex", Layers: doc("x")}); !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
}
