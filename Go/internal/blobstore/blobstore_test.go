package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

func init() { SyncWrites = false }

func each(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Run("memory", func(t *testing.T) { fn(t, NewMemory()) })
	t.Run("folder", func(t *testing.T) { fn(t, NewFolder(t.TempDir())) })
	t.Run("bucket", func(t *testing.T) { fn(t, fakeBucket(t, "org/a")) })
	t.Run("keyed", func(t *testing.T) { fn(t, NewKeyed(store.NewMemory(), "v1/artifacts/")) })
}

func TestPutGetHeadDedupeDelete(t *testing.T) {
	ctx := context.Background()
	each(t, func(t *testing.T, s Store) {
		data := []byte("hello")
		in, created, err := s.Put(ctx, data)
		if err != nil || !created || in.Digest != Of(data) || in.Size != 5 {
			t.Fatalf("put: %+v %v %v", in, created, err)
		}
		in2, created, err := s.Put(ctx, data)
		if err != nil || created || in2.Digest != in.Digest {
			t.Fatalf("dedupe: %+v %v %v", in2, created, err)
		}
		got, err := s.Get(ctx, in.Digest)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("get: %q %v", got, err)
		}
		r, err := s.Open(ctx, in.Digest)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
		if h, err := s.Head(ctx, in.Digest); err != nil || h.Size != 5 {
			t.Fatalf("head: %+v %v", h, err)
		}
		if err := s.Delete(ctx, in.Digest); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, in.Digest); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get after delete: %v", err)
		}
		if _, err := s.Head(ctx, in.Digest); !errors.Is(err, ErrNotFound) {
			t.Fatalf("head after delete: %v", err)
		}
		if err := s.Delete(ctx, in.Digest); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete: %v", err)
		}
		// An empty blob is a blob.
		if _, created, err := s.Put(ctx, nil); err != nil || !created {
			t.Fatalf("empty: %v %v", created, err)
		}
	})
}

func TestBadDigestsAndMismatch(t *testing.T) {
	ctx := context.Background()
	each(t, func(t *testing.T, s Store) {
		for _, d := range []Digest{"", "sha256:", "sha256:../../etc/passwd", Digest("sha256:" + string(make([]byte, 64))),
			Digest("sha256:" + fmt.Sprintf("%064s", "G")), "md5:abc"} {
			if _, err := s.Get(ctx, d); !errors.Is(err, ErrBadDigest) {
				t.Errorf("get %q: %v", d, err)
			}
			if err := s.Delete(ctx, d); !errors.Is(err, ErrBadDigest) {
				t.Errorf("delete %q: %v", d, err)
			}
		}
		if _, _, err := s.PutAs(ctx, Of([]byte("a")), []byte("b")); !errors.Is(err, ErrMismatch) {
			t.Errorf("mismatch: %v", err)
		}
		if _, err := s.List(ctx, "nope", 1); !errors.Is(err, ErrBadDigest) {
			t.Errorf("list cursor: %v", err)
		}
	})
}

func TestListIsBoundedAndPaged(t *testing.T) {
	ctx := context.Background()
	each(t, func(t *testing.T, s Store) {
		const n = 25
		want := map[Digest]bool{}
		for i := range n {
			in, _, err := s.Put(ctx, []byte(fmt.Sprint("blob", i)))
			if err != nil {
				t.Fatal(err)
			}
			want[in.Digest] = true
		}
		var after Digest
		var prev Digest
		pages := 0
		for {
			p, err := s.List(ctx, after, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Blobs) > 10 {
				t.Fatalf("page of %d", len(p.Blobs))
			}
			for _, b := range p.Blobs {
				if b.Digest <= prev {
					t.Fatalf("not in order: %s after %s", b.Digest, prev)
				}
				prev = b.Digest
				delete(want, b.Digest)
			}
			pages++
			if p.Next == "" {
				break
			}
			after = p.Next
		}
		if len(want) != 0 || pages != 3 {
			t.Fatalf("missing %d, pages %d", len(want), pages)
		}
		if p, _ := s.List(ctx, "", 1<<30); len(p.Blobs) != n {
			t.Fatalf("big limit: %d", len(p.Blobs))
		}
	})
}

func TestFolderEmptyListAndLimit(t *testing.T) {
	f := NewFolder(filepath.Join(t.TempDir(), "none"))
	if p, err := f.List(context.Background(), "", 0); err != nil || len(p.Blobs) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	if got := pageLimit(0); got != DefaultListLimit {
		t.Fatal(got)
	}
	if got := pageLimit(MaxListLimit + 1); got != MaxListLimit {
		t.Fatal(got)
	}
}

// A crash in a put leaves a temporary file: it is not a blob, and a sweep removes it. The
// blob itself appears whole or not at all.
func TestFolderCrashSafeWrite(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	f := NewFolder(root)
	dir := f.dir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	torn := filepath.Join(dir, ".deadbeef.tmp")
	if err := os.WriteFile(torn, []byte("par"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.List(ctx, "", 0); len(p.Blobs) != 0 {
		t.Fatalf("temp listed: %+v", p)
	}
	if n, _ := f.Sweep(time.Hour); n != 0 {
		t.Fatal("swept a young temp file")
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(torn, old, old)
	if n, _ := f.Sweep(time.Hour); n != 1 {
		t.Fatalf("sweep: %d", n)
	}
	// A put leaves no temp file.
	if _, _, err := f.Put(ctx, []byte("x")); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("%d files", len(ents))
	}
}

func TestFolderDetectsCorruptBlob(t *testing.T) {
	ctx := context.Background()
	f := NewFolder(t.TempDir())
	in, _, _ := f.Put(ctx, []byte("good"))
	p := f.path(in.Digest)
	_ = os.Chmod(p, 0o600)
	if err := os.WriteFile(p, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get(ctx, in.Digest); !errors.Is(err, ErrMismatch) {
		t.Fatalf("%v", err)
	}
}

func TestConcurrentPutsOfOneBlob(t *testing.T) {
	ctx := context.Background()
	each(t, func(t *testing.T, s Store) {
		created := make(chan bool, 8)
		for range 8 {
			go func() {
				_, c, err := s.Put(ctx, []byte("same"))
				if err != nil {
					t.Error(err)
				}
				created <- c
			}()
		}
		n := 0
		for range 8 {
			if <-created {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d creators", n)
		}
	})
}

func TestWire(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	for i := range 3 {
		_, _, _ = m.Put(ctx, []byte(fmt.Sprint(i)))
	}
	p, _ := m.List(ctx, "", 2)
	w := p.Wire()
	if len(w.Blobs) != 2 || w.Next == nil || *w.Next != string(p.Blobs[1].Digest) || w.Blobs[0].Digest != string(p.Blobs[0].Digest) {
		t.Fatalf("%+v", w)
	}
	if _, err := time.Parse(time.RFC3339, w.Blobs[0].Stored); err != nil {
		t.Fatal(err)
	}
}
