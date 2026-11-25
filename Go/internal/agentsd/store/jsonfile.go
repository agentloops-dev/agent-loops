package store

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The small-file type (#6669, docs/explanation/data-stores.md): a few entries by key in one JSON
// file, read and written whole, with its limits declared where it is opened. The hourly sweep
// holds the file to them.

// Action is what a store over its count does.
type Action string

const (
	// EvictOldest lets the oldest entries go, in the sweep.
	EvictOldest Action = "evict-oldest"
	// Refuse fails the write that would pass the limit: for data a person made.
	Refuse Action = "refuse"
)

// MaxAge is the longest an entry of any store is kept.
const MaxAge = 90 * 24 * time.Hour

// Bounds are a store's limits; each one is required.
type Bounds struct {
	MaxRecordBytes int           // one entry, its key and its value
	MaxCount       int           // the entries
	MaxAge         time.Duration // from the entry's own time, at most MaxAge
	Action         Action
}

// Spec declares a store: a stable name, its kind and whose data it holds.
type Spec struct {
	Name, Kind, Scope string
	Bounds            Bounds
}

func (s Spec) check() error {
	b := s.Bounds
	switch {
	case s.Name == "" || s.Kind == "" || s.Scope == "":
		return errors.New("needs a name, a kind and a scope")
	case b.MaxRecordBytes <= 0 || b.MaxCount <= 0:
		return errors.New("needs a largest entry and a count")
	case b.MaxAge <= 0 || b.MaxAge > MaxAge:
		return fmt.Errorf("needs an age of at most %v", MaxAge)
	case b.Action != EvictOldest && b.Action != Refuse:
		return errors.New("needs evict or refuse")
	}
	return nil
}

// ErrLimit is a write that a store's limits refuse.
var ErrLimit = errors.New("over the store's limit")

// JSONFile is a bounded JSON file of entries by key.
type JSONFile[T any] struct {
	spec Spec
	path func() string
	at   func(*T) *wire.Date
	now  func() time.Time
	mu   sync.Mutex
}

// OpenJSONFile is the store that spec declares. path is asked at each use. at is an entry's time
// of creation: the store sets it on an entry that has none (a new one, or one from before the
// store had limits) or a time still to come. A spec with no limits is a bug, so it panics.
func OpenJSONFile[T any](spec Spec, path func() string, at func(*T) *wire.Date, now func() time.Time) *JSONFile[T] {
	if err := spec.check(); err != nil {
		panic(fmt.Sprintf("store %q: %v", spec.Name, err))
	}
	return &JSONFile[T]{spec: spec, path: path, at: at, now: now}
}

// Read is the entries; a missing file has none. A file that cannot be decoded gives its error.
func (f *JSONFile[T]) Read() (map[string]T, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, _, err := f.read()
	return entries, err
}

// Update changes the entries and writes the file, if they changed. A file that cannot be decoded
// is set aside and starts again from none. It refuses an entry changed to over its largest size,
// and, for a store that refuses, an entry more than the count.
func (f *JSONFile[T]) Update(change func(map[string]T) error) error {
	return f.update(change, true)
}

// UpdateKeepingAll is Update for the entries of a file from before the store had its limits, when a
// store that refuses must lose none (CarryOver's copy set aside is trimmed): it keeps an entry over the largest size, and more entries
// than the count. They are dated, so they go by age, and Update refuses to add to them. In a
// store that evicts, the sweep lets them go.
func (f *JSONFile[T]) UpdateKeepingAll(change func(map[string]T) error) error {
	return f.update(change, false)
}

func (f *JSONFile[T]) update(change func(map[string]T) error, limited bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, old, err := f.read()
	if errors.Is(err, errUndecodable) {
		if err = f.setAside(); err != nil {
			return err
		}
		entries, old = map[string]T{}, nil
	}
	if err != nil {
		return err
	}
	dropped := f.expire(entries)
	count := len(entries)
	before := make(map[string][]byte, count)
	for key, v := range entries {
		if before[key], err = diskjson.Marshal(v); err != nil {
			return err
		}
	}
	if err := change(entries); err != nil {
		return err
	}
	b := f.spec.Bounds
	for key, v := range entries {
		data, err := diskjson.Marshal(v)
		if err != nil {
			return err
		}
		if limited && len(key)+len(data) > b.MaxRecordBytes && !bytes.Equal(data, before[key]) {
			return fmt.Errorf("%s: an entry of %d bytes, at most %d: %w", f.spec.Name, len(key)+len(data), b.MaxRecordBytes, ErrLimit)
		}
	}
	if limited && b.Action == Refuse && len(entries) > b.MaxCount && len(entries) > count {
		return fmt.Errorf("%s: full at %d entries; remove one first: %w", f.spec.Name, b.MaxCount, ErrLimit)
	}
	dropped += f.expire(entries)
	// Past the high-water mark the sweep runs at once: the file is being written anyway.
	if n := len(entries); n > 2*b.MaxCount {
		f.evict(entries)
		dropped += n - len(entries)
	}
	if err := f.write(entries, old); err != nil {
		return err
	}
	if dropped > 0 {
		log.Printf("retention: %d entries of %s past its limits", dropped, f.Name())
	}
	return nil
}

// CarryOver writes the entries of old, the file's bytes in an earlier form, in this form. An entry
// over its largest size is left out and named in the log, and then old is kept whole, set aside.
// So it is when whole is false: old has more than entries.
func (f *JSONFile[T]) CarryOver(old []byte, whole bool, entries map[string]T) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := len(entries)
	f.expire(entries)
	dropped := count - len(entries)
	var large []string
	for key, v := range entries {
		data, err := diskjson.Marshal(v)
		if err != nil {
			return err
		}
		if size := len(key) + len(data); size > f.spec.Bounds.MaxRecordBytes {
			large = append(large, fmt.Sprintf("%.200q of %d bytes", key, size))
			delete(entries, key)
		}
	}
	if len(large) > 0 || !whole {
		// A copy, not a rename: until the new file is written, the old one is there.
		aside := f.asideName()
		// store-bounds: not a store: the old file, under the set-aside copies' bounds
		if err := WriteAtomically(aside, old, 0o600); err != nil {
			return err
		}
		TrimSetAsides(f.path())
		slices.Sort(large)
		for _, l := range large {
			log.Printf("%s: did not carry over %s, at most %d; the old file is %s", f.Name(), l, f.spec.Bounds.MaxRecordBytes, aside)
		}
	}
	count = len(entries)
	f.evict(entries)
	if err := f.write(entries, old); err != nil {
		return err
	}
	if dropped += count - len(entries); dropped > 0 {
		log.Printf("retention: %d entries of %s past its limits", dropped, f.Name())
	}
	return nil
}

// Sweep takes the file under its limits, and says how many entries went.
func (f *JSONFile[T]) Sweep() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, old, err := f.read()
	if err != nil || old == nil {
		return 0, err
	}
	count := len(entries)
	f.expire(entries)
	f.evict(entries)
	return count - len(entries), f.write(entries, old)
}

// Path is the store's file.
func (f *JSONFile[T]) Path() string { return f.path() }

// Spec is the store as declared.
func (f *JSONFile[T]) Spec() Spec { return f.spec }

// Name is the store's name.
func (f *JSONFile[T]) Name() string { return f.spec.Name }

var errUndecodable = errors.New("not the JSON this store writes")

func (f *JSONFile[T]) read() (map[string]T, []byte, error) {
	entries := map[string]T{}
	data, err := os.ReadFile(f.path())
	if errors.Is(err, fs.ErrNotExist) {
		return entries, nil, nil
	}
	if err != nil {
		return entries, nil, err
	}
	if err := diskjson.Unmarshal(data, &entries); err != nil {
		return map[string]T{}, data, fmt.Errorf("%s: %w: %w", f.spec.Name, errUndecodable, err)
	}
	if entries == nil {
		entries = map[string]T{}
	}
	return entries, data, nil
}

func (f *JSONFile[T]) write(entries map[string]T, old []byte) error {
	if old == nil && len(entries) == 0 {
		return nil
	}
	data, err := diskjson.Marshal(entries)
	if err != nil {
		return err
	}
	if bytes.Equal(data, old) {
		return nil
	}
	// store-bounds: not a store: each store that writes here declares its Spec
	return WriteAtomically(f.path(), data, 0o600)
}

func (f *JSONFile[T]) setAside() error {
	file := f.path()
	if err := os.Rename(file, f.asideName()); err != nil {
		return err
	}
	TrimSetAsides(file)
	return nil
}

func (f *JSONFile[T]) asideName() string {
	return f.path() + ".corrupt-" + strconv.FormatInt(f.now().Unix(), 10)
}

// expire dates the entries with no time, and drops those past their age: how many.
func (f *JSONFile[T]) expire(entries map[string]T) int {
	now, count := f.now(), len(entries)
	for key, v := range entries {
		at := f.at(&v)
		switch {
		case *at == 0 || at.Time().After(now):
			*at = wire.DateOf(now)
			entries[key] = v
		case now.Sub(at.Time()) >= f.spec.Bounds.MaxAge:
			delete(entries, key)
		}
	}
	return count - len(entries)
}

// evict drops what is over the largest entry, then the oldest, for a store that evicts.
func (f *JSONFile[T]) evict(entries map[string]T) {
	b := f.spec.Bounds
	if b.Action != EvictOldest {
		return
	}
	type aged struct {
		key string
		at  wire.Date
	}
	var all []aged
	for key, v := range entries {
		if data, err := diskjson.Marshal(v); err != nil || len(key)+len(data) > b.MaxRecordBytes {
			delete(entries, key)
			continue
		}
		all = append(all, aged{key, *f.at(&v)})
	}
	slices.SortFunc(all, func(x, y aged) int { return cmp.Or(cmp.Compare(x.at, y.at), cmp.Compare(x.key, y.key)) })
	for _, e := range all[:max(0, len(all)-b.MaxCount)] {
		delete(entries, e.key)
	}
}
