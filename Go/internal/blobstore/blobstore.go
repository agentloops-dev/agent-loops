// Package blobstore is the artifacts' blob store (#5715, epic #5713): content-addressed bytes
// at "sha256:<hex>" behind one interface. Folder is the backend for a folder (plain files, no
// SQLite); Bucket is the one for an S3-compatible bucket (#5954), whose keys are the same paths
// under the bucket's prefix (Digest.Path); Memory is the one for tests.
package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Bounds of the store (data principles: every store is bounded).
const (
	// MaxBlobBytes is the largest blob a Put takes.
	MaxBlobBytes = 1 << 30
	// MaxListLimit is the most a List returns in one page; a larger Limit is cut to it.
	MaxListLimit = 1000
	// DefaultListLimit is the page size when List's Limit is 0.
	DefaultListLimit = 100
)

var (
	// ErrNotFound is a Get, Head or Delete of a digest that is not stored.
	ErrNotFound = errors.New("blobstore: blob not found")
	// ErrBadDigest is a digest that is not "sha256:" and 64 lower-case hex digits.
	ErrBadDigest = errors.New("blobstore: bad digest")
	// ErrTooLarge is a Put over MaxBlobBytes.
	ErrTooLarge = errors.New("blobstore: blob too large")
	// ErrMismatch is a Put whose bytes do not hash to the digest the caller named, or a stored
	// blob whose bytes do not hash to its digest (found on a read).
	ErrMismatch = errors.New("blobstore: bytes do not match digest")
)

// Digest is a blob's id: "sha256:" and the hash as 64 lower-case hex digits.
type Digest string

const prefix = "sha256:"

// Of returns the digest of data.
func Of(data []byte) Digest {
	sum := sha256.Sum256(data)
	return Digest(prefix + hex.EncodeToString(sum[:]))
}

// Valid reports whether d is a well-formed digest.
func (d Digest) Valid() bool { return d.Validate() == nil }

// Validate returns ErrBadDigest when d is not well formed. Nothing else is accepted as a path
// part, so a digest can never name a file outside the store.
func (d Digest) Validate() error {
	h, ok := strings.CutPrefix(string(d), prefix)
	if !ok || len(h) != 64 {
		return fmt.Errorf("%w: %q", ErrBadDigest, string(d))
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: %q", ErrBadDigest, string(d))
		}
	}
	return nil
}

// Hex is the hash without its "sha256:". Call it on a valid digest.
func (d Digest) Hex() string { return strings.TrimPrefix(string(d), prefix) }

// Path is the blob's key under a store's root or a bucket's prefix, as OCI's image layout:
// "blobs/sha256/<hex>".
func (d Digest) Path() string { return "blobs/sha256/" + d.Hex() }

// Info is what Head and List say about a blob.
type Info struct {
	Digest Digest
	Size   int64
	// Stored is when the blob was first written.
	Stored time.Time
}

// Page is one page of List: blobs in digest order, and where the next page starts.
type Page struct {
	Blobs []Info
	// Next is "" on the last page; else pass it as List's After for the next one.
	Next Digest
}

// Store is the blob store.
type Store interface {
	// Put stores data and returns its digest and whether it was new. A put of bytes that are
	// stored already is a no-op that leaves the blob as it is (dedupe).
	Put(ctx context.Context, data []byte) (Info, bool, error)
	// PutAs is Put, refusing with ErrMismatch when data does not hash to d.
	PutAs(ctx context.Context, d Digest, data []byte) (Info, bool, error)
	// Get returns the blob's bytes, or ErrNotFound. A folder's blob whose bytes do not hash to
	// its digest is ErrMismatch.
	Get(ctx context.Context, d Digest) ([]byte, error)
	// Open streams the blob; the caller closes it.
	Open(ctx context.Context, d Digest) (io.ReadCloser, error)
	// Head returns the blob's info, or ErrNotFound.
	Head(ctx context.Context, d Digest) (Info, error)
	// List returns up to limit blobs whose digest is after "after", in digest order.
	// limit is bounded by MaxListLimit; 0 is DefaultListLimit.
	List(ctx context.Context, after Digest, limit int) (Page, error)
	// Delete removes the blob, or returns ErrNotFound. Callers delete only what garbage
	// collection finds unreferenced; the store does not know references.
	Delete(ctx context.Context, d Digest) error
}

func pageLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultListLimit
	case n > MaxListLimit:
		return MaxListLimit
	}
	return n
}

// checkAfter accepts "" or a digest-shaped cursor.
func checkAfter(after Digest) error {
	if after == "" {
		return nil
	}
	return after.Validate()
}

// checkPut is what every backend's PutAs checks before it stores: a well-formed digest, a blob
// within the bound, and bytes that hash to the digest.
func checkPut(d Digest, data []byte) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if len(data) > MaxBlobBytes {
		return ErrTooLarge
	}
	if Of(data) != d {
		return ErrMismatch
	}
	return nil
}

// Wire is the info as the wire says it.
func (i Info) Wire() wire.BlobInfo {
	return wire.BlobInfo{Digest: string(i.Digest), Size: i.Size, Stored: i.Stored.UTC().Format(time.RFC3339)}
}

// Wire is the page as the wire says it.
func (p Page) Wire() wire.BlobPage {
	out := wire.BlobPage{Blobs: make([]wire.BlobInfo, 0, len(p.Blobs))}
	for _, b := range p.Blobs {
		out.Blobs = append(out.Blobs, b.Wire())
	}
	if p.Next != "" {
		n := string(p.Next)
		out.Next = &n
	}
	return out
}
