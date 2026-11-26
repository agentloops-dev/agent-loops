package blobstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SyncWrites is whether Folder syncs a blob before it renames it into place. Only tests turn
// it off, as store.SyncWrites does (#4709).
var SyncWrites = true

// Folder is a Store in a folder: <root>/blobs/sha256/<hex>, one plain file a blob, the layout
// of OCI's image layout. A put writes a temporary file beside the blob, syncs it and links it
// into place, so a crash leaves the blob whole or not there, and a blob that exists is never
// overwritten. Temporary files (".<hex>.tmp") are not blobs: List skips them, and Sweep
// removes the ones a crash left.
type Folder struct{ root string }

// NewFolder returns the store under root; the folder is made on the first put.
func NewFolder(root string) *Folder { return &Folder{root: root} }

func (f *Folder) dir() string { return filepath.Join(f.root, "blobs", "sha256") }

func (f *Folder) path(d Digest) string { return filepath.Join(f.dir(), d.Hex()) }

func infoOf(d Digest, fi fs.FileInfo) Info { return Info{d, fi.Size(), fi.ModTime()} }

func (f *Folder) Put(ctx context.Context, data []byte) (Info, bool, error) {
	return f.PutAs(ctx, Of(data), data)
}

func (f *Folder) PutAs(ctx context.Context, d Digest, data []byte) (Info, bool, error) {
	if err := d.Validate(); err != nil {
		return Info{}, false, err
	}
	if len(data) > MaxBlobBytes {
		return Info{}, false, ErrTooLarge
	}
	if Of(data) != d {
		return Info{}, false, ErrMismatch
	}
	if err := ctx.Err(); err != nil {
		return Info{}, false, err
	}
	if fi, err := os.Stat(f.path(d)); err == nil && fi.Size() == int64(len(data)) {
		return infoOf(d, fi), false, nil
	}
	dir := f.dir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Info{}, false, err
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	tmp := filepath.Join(dir, "."+hex.EncodeToString(b)+".tmp")
	w, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return Info{}, false, err
	}
	defer os.Remove(tmp)
	_, err = w.Write(data)
	if err == nil && SyncWrites {
		err = w.Sync()
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Info{}, false, err
	}
	// Link, not rename: a blob that a concurrent put made first stays as it is.
	created := true
	if err := os.Link(tmp, f.path(d)); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return Info{}, false, err
		}
		created = false
	}
	if SyncWrites {
		if dh, err := os.Open(dir); err == nil {
			_ = dh.Sync()
			_ = dh.Close()
		}
	}
	fi, err := os.Stat(f.path(d))
	if err != nil {
		return Info{}, false, err
	}
	return infoOf(d, fi), created, nil
}

func (f *Folder) Get(ctx context.Context, d Digest) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(f.path(d))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if Of(data) != d {
		return nil, ErrMismatch
	}
	return data, nil
}

func (f *Folder) Open(ctx context.Context, d Digest) (io.ReadCloser, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	r, err := os.Open(f.path(d))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return r, err
}

func (f *Folder) Head(ctx context.Context, d Digest) (Info, error) {
	if err := d.Validate(); err != nil {
		return Info{}, err
	}
	fi, err := os.Stat(f.path(d))
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, ErrNotFound
	}
	if err != nil {
		return Info{}, err
	}
	return infoOf(d, fi), nil
}

// List reads the folder's names, which is bounded by the number of blobs, then stats only the
// page it returns.
func (f *Folder) List(ctx context.Context, after Digest, limit int) (Page, error) {
	if err := checkAfter(after); err != nil {
		return Page{}, err
	}
	limit = pageLimit(limit)
	ents, err := os.ReadDir(f.dir())
	if errors.Is(err, fs.ErrNotExist) {
		return Page{}, nil
	}
	if err != nil {
		return Page{}, err
	}
	var ds []Digest
	for _, e := range ents {
		d := Digest(prefix + e.Name())
		if e.Type().IsRegular() && d.Valid() && d > after {
			ds = append(ds, d)
		}
	}
	slices.Sort(ds)
	var p Page
	for i, d := range ds {
		if i == limit {
			p.Next = p.Blobs[len(p.Blobs)-1].Digest
			break
		}
		fi, err := os.Stat(f.path(d))
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted since the listing
		}
		if err != nil {
			return Page{}, err
		}
		p.Blobs = append(p.Blobs, infoOf(d, fi))
	}
	return p, nil
}

func (f *Folder) Delete(ctx context.Context, d Digest) error {
	if err := d.Validate(); err != nil {
		return err
	}
	err := os.Remove(f.path(d))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// Sweep removes temporary files older than minAge, which a crash in a put left. It returns
// how many it removed. A put in flight is younger than minAge.
func (f *Folder) Sweep(minAge time.Duration) (int, error) {
	ents, err := os.ReadDir(f.dir())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		fi, err := e.Info()
		if err != nil || time.Since(fi.ModTime()) < minAge {
			continue
		}
		if os.Remove(filepath.Join(f.dir(), e.Name())) == nil {
			n++
		}
	}
	return n, nil
}
