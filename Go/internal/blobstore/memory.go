package blobstore

import (
	"bytes"
	"context"
	"io"
	"slices"
	"sync"
	"time"
)

// Memory is a Store in memory, for tests. It follows the same bounds as Folder.
type Memory struct {
	mu    sync.Mutex
	blobs map[Digest]memBlob
	now   func() time.Time
}

type memBlob struct {
	data   []byte
	stored time.Time
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{blobs: map[Digest]memBlob{}, now: time.Now} }

func (m *Memory) Put(ctx context.Context, data []byte) (Info, bool, error) {
	return m.PutAs(ctx, Of(data), data)
}

func (m *Memory) PutAs(ctx context.Context, d Digest, data []byte) (Info, bool, error) {
	if err := d.Validate(); err != nil {
		return Info{}, false, err
	}
	if len(data) > MaxBlobBytes {
		return Info{}, false, ErrTooLarge
	}
	if Of(data) != d {
		return Info{}, false, ErrMismatch
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.blobs[d]; ok {
		return Info{d, int64(len(b.data)), b.stored}, false, nil
	}
	b := memBlob{slices.Clone(data), m.now()}
	m.blobs[d] = b
	return Info{d, int64(len(data)), b.stored}, true, nil
}

func (m *Memory) Get(ctx context.Context, d Digest) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[d]
	if !ok {
		return nil, ErrNotFound
	}
	return slices.Clone(b.data), nil
}

func (m *Memory) Open(ctx context.Context, d Digest) (io.ReadCloser, error) {
	data, err := m.Get(ctx, d)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *Memory) Head(ctx context.Context, d Digest) (Info, error) {
	if err := d.Validate(); err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[d]
	if !ok {
		return Info{}, ErrNotFound
	}
	return Info{d, int64(len(b.data)), b.stored}, nil
}

func (m *Memory) List(ctx context.Context, after Digest, limit int) (Page, error) {
	if err := checkAfter(after); err != nil {
		return Page{}, err
	}
	limit = pageLimit(limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]Digest, 0, len(m.blobs))
	for d := range m.blobs {
		if d > after {
			keys = append(keys, d)
		}
	}
	slices.Sort(keys)
	var p Page
	for i, d := range keys {
		if i == limit {
			p.Next = p.Blobs[len(p.Blobs)-1].Digest
			break
		}
		b := m.blobs[d]
		p.Blobs = append(p.Blobs, Info{d, int64(len(b.data)), b.stored})
	}
	return p, nil
}

func (m *Memory) Delete(ctx context.Context, d Digest) error {
	if err := d.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blobs[d]; !ok {
		return ErrNotFound
	}
	delete(m.blobs, d)
	return nil
}
