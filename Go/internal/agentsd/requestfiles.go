package agentsd

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	storekinds "github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

const (
	requestMaxBytes = 256 << 10 // one record
	// requestMaxNew leaves room in a record for what comes after it opens: its waiters, its
	// deferral and its answer.
	requestMaxNew = 192 << 10
	// requestMaxGrown is the most an open one grows to, and leaves room for its answer.
	requestMaxGrown   = 224 << 10
	requestMaxWaiters = 100
	requestMaxAnswer  = 16 << 10
	requestPage       = 200
)

// requestMaxAge is the longest a request is kept from when it was asked, open or ended (#6718).
const requestMaxAge = 90 * 24 * time.Hour

// requestDocuments is the document interface to the requests (#6670).
type requestDocuments interface {
	Get(id string) ([]byte, error)
	// Put refuses a record over its largest size.
	Put(id string, data []byte) error
	// Delete deletes a record; one that is not there is not an error.
	Delete(id string) error
	// List is the ids after cursor, and the cursor of the next page ("" at the end).
	List(cursor string, limit int) ([]string, string, error)
	// Expire deletes what is no record List gives (the rest of a cut write) and what isRecord
	// says is none of ours, last written before before.
	Expire(before time.Time, isRecord func(id string) bool)
}

// requestFiles keeps each request as <dir>/<id>.json.
type requestFiles struct {
	dir  string
	spec storekinds.Spec
}

// requestRecords is this machine's requests. An ended one is history, and evicts the oldest
// first; a new one past the count of open ones is refused (requestRefusedLocked).
func (d *Daemon) requestRecords() requestDocuments {
	spec := storekinds.Spec{Name: "machine.requests", Kind: "document", Scope: "machine", Bounds: storekinds.Bounds{
		MaxRecordBytes: requestMaxBytes, MaxCount: int64(requestFileLimit), MaxAge: requestMaxAge}}
	if err := spec.Validate("document"); err != nil {
		panic(err)
	}
	return requestFiles{d.requestsDir(), spec}
}

func (r requestFiles) path(id string) (string, error) {
	if id == "" || strings.HasPrefix(id, ".") || filepath.Base(id) != id {
		return "", fmt.Errorf("%q is not a request's id", id)
	}
	return filepath.Join(r.dir, id+".json"), nil
}

func (r requestFiles) Get(id string) ([]byte, error) {
	path, err := r.path(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (r requestFiles) Put(id string, data []byte) error {
	path, err := r.path(id)
	if err != nil {
		return err
	}
	if limit := r.spec.Bounds.MaxRecordBytes; int64(len(data)) > limit {
		return &storekinds.LimitError{Name: r.spec.Name, Size: int64(len(data)), Limit: limit}
	}
	// store-bounds: kind=document; count=1_000 records of 256KiB; age=90d; applied=hourly sweep, and on write at twice the count
	return store.WriteAtomically(path, data, 0o600)
}

func (r requestFiles) Delete(id string) error {
	path, err := r.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (r requestFiles) List(cursor string, limit int) ([]string, string, error) {
	entries, err := os.ReadDir(r.dir) // sorted by name
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var out []string
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || strings.HasPrefix(id, ".") || e.Name() <= cursor {
			continue
		}
		if len(out) == limit {
			return out, out[limit-1] + ".json", nil
		}
		out = append(out, id)
	}
	return out, "", nil
}

func (r requestFiles) Expire(before time.Time, isRecord func(id string) bool) {
	entries, _ := os.ReadDir(r.dir)
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !e.Type().IsRegular() || ok && isRecord(id) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(before) {
			if err := os.Remove(filepath.Join(r.dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("requests: %v", err)
			}
		}
	}
}
