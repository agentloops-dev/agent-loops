package blobstore

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// Keyed is a Store in a control plane's own store (#5953): each blob is the object at
// <prefix>blobs/sha256/<hex>, created with If-None-Match, so the control plane's folder or
// bucket, scoped to its organisation, holds the artifacts' blobs beside their records with
// no second place to set up. A create that loses to another of the same bytes is the dedupe.
//
// The control store keeps no time for an object, so Info.Stored is the time of the Put that
// made it, as this copy saw it, and zero for one read back.
type Keyed struct {
	s      store.Store
	prefix string
	now    func() time.Time
}

// NewKeyed is the blob store under prefix, which is "" or ends in "/".
func NewKeyed(s store.Store, prefix string) *Keyed {
	return &Keyed{s: s, prefix: prefix, now: time.Now}
}

func (k *Keyed) key(d Digest) string { return k.prefix + d.Path() }

func (k *Keyed) dir() string { return k.prefix + "blobs/sha256/" }

func (k *Keyed) Put(ctx context.Context, data []byte) (Info, bool, error) {
	return k.PutAs(ctx, Of(data), data)
}

func (k *Keyed) PutAs(ctx context.Context, d Digest, data []byte) (Info, bool, error) {
	if err := checkPut(d, data); err != nil {
		return Info{}, false, err
	}
	info := Info{Digest: d, Size: int64(len(data)), Stored: k.now()}
	// store-bounds: kind=blob; size=1GiB; age=until-deleted; applied=on write (refused), a blob goes only when its caller deletes it
	if _, err := k.s.Put(ctx, k.key(d), data, store.Absent); err != nil {
		if !store.IsConflict(err) {
			return Info{}, false, err
		}
		// There already: the same digest is the same bytes, so this is the dedupe.
		have, err := k.Head(ctx, d)
		return have, false, err
	}
	return info, true, nil
}

func (k *Keyed) Get(ctx context.Context, d Digest) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	o, err := k.s.Get(ctx, k.key(d))
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, ErrNotFound
	}
	if Of(o.Data) != d {
		return nil, ErrMismatch
	}
	return o.Data, nil
}

func (k *Keyed) Open(ctx context.Context, d Digest) (io.ReadCloser, error) {
	data, err := k.Get(ctx, d)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Head reads the object: the control store has no head of its own.
func (k *Keyed) Head(ctx context.Context, d Digest) (Info, error) {
	data, err := k.Get(ctx, d)
	if err != nil {
		return Info{}, err
	}
	return Info{Digest: d, Size: int64(len(data))}, nil
}

// List lists the blobs' folder, which the store returns whole and sorted; it skips any key
// that is not a digest.
func (k *Keyed) List(ctx context.Context, after Digest, limit int) (Page, error) {
	if err := checkAfter(after); err != nil {
		return Page{}, err
	}
	limit = pageLimit(limit)
	dir := k.dir()
	keys, err := k.s.List(ctx, dir)
	if err != nil {
		return Page{}, err
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })
	var p Page
	for _, key := range keys {
		d := Digest(prefix + strings.TrimPrefix(key.Key, dir))
		if !d.Valid() || (after != "" && d <= after) {
			continue
		}
		if len(p.Blobs) == limit {
			p.Next = p.Blobs[limit-1].Digest
			break
		}
		p.Blobs = append(p.Blobs, Info{Digest: d, Size: key.Size})
	}
	return p, nil
}

// Delete checks the blob is there first, because the store's delete of a missing key
// succeeds.
func (k *Keyed) Delete(ctx context.Context, d Digest) error {
	if err := d.Validate(); err != nil {
		return err
	}
	o, err := k.s.Get(ctx, k.key(d))
	if err != nil {
		return err
	}
	if o == nil {
		return ErrNotFound
	}
	return k.s.Delete(ctx, k.key(d))
}
