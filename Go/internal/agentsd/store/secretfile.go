package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/sizewords"
)

// The secret kind (#6669, docs/explanation/data-stores.md): a small file in ~/.agents-secrets,
// private to the person, read and written whole. Its entries have no age: they stay until they
// are removed. It refuses: no entry is evicted, and a file over a limit still opens and can be
// made smaller.

// SecretPart is one collection of entries of a secret file, with its limits.
type SecretPart[T any] struct {
	// What the entries are, for the words of a refusal: never a name or a value.
	What           string
	MaxRecordBytes int // one entry, its key and its value
	MaxCount       int
	// Remedy is what a person does when it is full.
	Remedy  string
	Entries func(T) map[string]any
}

// SecretFile is a bounded file of secrets.
type SecretFile[T any] struct {
	Name  string
	Parts []SecretPart[T]
}

// ErrUndecodable is a file that is there and is not the JSON its store writes.
var ErrUndecodable = errors.New("not the JSON this store writes")

// LimitError is a write that the limits of a secret store refuse. Its words hold no name and
// no value.
type LimitError struct{ Words string }

func (e LimitError) Error() string { return e.Words }

// Unwrap makes it an ErrLimit.
func (e LimitError) Unwrap() error { return ErrLimit }

// Entries is a collection as a part takes it.
func Entries[V any](m map[string]V) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Read is the file as it is, over a limit or not; a missing file is the zero T.
func (f SecretFile[T]) Read(path string) (T, error) {
	var doc T
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return doc, err
	}
	if json.Unmarshal(b, &doc) != nil {
		var none T
		// Not the decoder's words: they can quote the file.
		return none, fmt.Errorf("%s: %w", f.Name, ErrUndecodable)
	}
	return doc, nil
}

// Write writes doc, or removes the file when doc has no entries. It refuses an entry that grows
// over its largest size, and a collection that grows over its count, against the file as it is.
// It does not write over a file that does not read. The caller holds the store's lock.
func (f SecretFile[T]) Write(path string, doc T) error {
	if err := f.Check(path, doc); err != nil {
		return err
	}
	count := 0
	for _, p := range f.Parts {
		count += len(p.Entries(doc))
	}
	if count == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Only tightens a folder made before; the file itself is 0600 whatever the folder is.
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a folder, which needs x to be opened
		log.Printf("%s: could not make its folder private: %v", f.Name, err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	// store-bounds: not a store: each store that writes here declares its limits in its parts
	return WriteAtomically(path, b, 0o600)
}

// Check is whether Write takes doc, with nothing written.
func (f SecretFile[T]) Check(path string, doc T) error {
	old, err := f.Read(path)
	if err != nil {
		return err
	}
	for _, p := range f.Parts {
		if err := p.check(p.Entries(old), p.Entries(doc)); err != nil {
			return err
		}
	}
	return nil
}

func (p SecretPart[T]) check(old, entries map[string]any) error {
	if len(entries) > p.MaxCount && len(entries) > len(old) {
		return LimitError{fmt.Sprintf("this machine keeps at most %d %s; %s", p.MaxCount, p.What, p.Remedy)}
	}
	for key, v := range entries {
		size, err := entryBytes(key, v)
		if err != nil {
			return err
		}
		if size <= p.MaxRecordBytes {
			continue
		}
		was := 0
		if o, ok := old[key]; ok {
			if was, err = entryBytes(key, o); err != nil {
				return err
			}
		}
		if size > was {
			return LimitError{fmt.Sprintf("each of the %s can be at most %s; give it a shorter name or value", p.What, sizewords.Int(p.MaxRecordBytes))}
		}
	}
	return nil
}

func entryBytes(key string, v any) (int, error) {
	b, err := json.Marshal(v)
	return len(key) + len(b), err
}
