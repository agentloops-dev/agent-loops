package skillcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Sidecar is what the lock files have no field for (CatalogSidecar, catalog-skills.json in
// the daemon's root): the commit a skill was taken at, its date, the folder's tree SHA and
// the catalogue. Losing it costs a "Taken at" line and one API call, nothing more.
type Sidecar struct {
	Version int                      `json:"version"`
	Skills  map[string]SidecarRecord `json:"skills"`
}

// SidecarRecord's dates are ISO 8601 to the second, as Swift's .iso8601 writes them.
type SidecarRecord struct {
	Commit      string     `json:"commit"`
	CommittedAt *time.Time `json:"committedAt,omitempty"`
	TreeSHA     string     `json:"treeSHA"`
	Catalogue   string     `json:"catalogue"`
	AddedAt     time.Time  `json:"addedAt"`
	// FolderHash is the CLI's hash of the folder as written, which "edited since it was
	// added" is judged against.
	FolderHash *string `json:"folderHash,omitempty"`
	// GoneSince is when the hourly sweep first found the skill not there.
	GoneSince *time.Time `json:"goneSince,omitempty"`
}

// SidecarKey is personal/<name>, or <folder>/<name>.
func SidecarKey(d wire.SkillDestination, name string) string {
	if d.Kind == wire.SkillDestinationKindFolder && d.Folder != nil {
		return filepath.Clean(*d.Folder) + "/" + name
	}
	return "personal/" + name
}

// LoadSidecar reads the file; one that does not decode is set aside and this starts empty.
func LoadSidecar(path string) Sidecar {
	empty := Sidecar{Version: 1, Skills: map[string]SidecarRecord{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || err != nil {
		return empty
	}
	var s Sidecar
	if json.Unmarshal(b, &s) != nil {
		aside := fmt.Sprintf("%s.unreadable-%d", path, time.Now().Unix())
		if err := os.Rename(path, aside); err != nil {
			log.Printf("%s: unreadable, and could not be set aside: %v", filepath.Base(path), err)
		}
		store.TrimSetAsides(path)
		log.Printf("%s: unreadable; kept as %s; skills added from the catalogue show without where they were taken from",
			filepath.Base(path), filepath.Base(aside))
		return empty
	}
	if s.Skills == nil {
		s.Skills = map[string]SidecarRecord{}
	}
	return s
}

// The limits of each catalogue file, this one and catalog-mcp.json (#6732, #6733). A record is
// kept while its item is installed, whatever its age; the hourly sweep takes one out 30 days
// after its item went (GoneSince).
const (
	sidecarMost       = 2000
	sidecarRecordMost = 8 << 10
)

// SidecarMu is held over a change of either file: it is read, changed and written whole.
var SidecarMu sync.Mutex

// UpdateSidecar changes the records and writes the file, if they changed.
func UpdateSidecar(path string, change func(map[string]SidecarRecord)) error {
	SidecarMu.Lock()
	defer SidecarMu.Unlock()
	s := LoadSidecar(path)
	if changed, err := ChangeRecords(s.Skills, change); !changed || err != nil {
		return err
	}
	return s.save(path)
}

// ChangeRecords makes a change to a catalogue file's records, and says whether it changed them.
// It refuses a record changed to over its largest size, and one more than the count. The sweep's
// mark (goneSince) is not in the size, and one that changes only there is never refused: a
// record at its largest, or over it from before there was one, does not stop the sweep.
func ChangeRecords[R any](records map[string]R, change func(map[string]R)) (bool, error) {
	count := len(records)
	before := make(map[string][]byte, count)
	for key, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			return false, err
		}
		before[key] = b
	}
	change(records)
	changed := len(records) != count
	for key, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			return false, err
		}
		if old, ok := before[key]; ok && bytes.Equal(old, b) {
			continue
		}
		changed = true
		bare, err := unmarked(b)
		if err != nil {
			return false, err
		}
		if len(key)+len(bare) <= sidecarRecordMost {
			continue
		}
		was, err := unmarked(before[key])
		if err != nil {
			return false, err
		}
		if !bytes.Equal(was, bare) {
			return false, fmt.Errorf("a catalogue item's record is at most %d bytes: %w", sidecarRecordMost, store.ErrLimit)
		}
	}
	if len(records) > sidecarMost && len(records) > count {
		return false, fmt.Errorf("at most %d catalogue items are kept; remove one first: %w", sidecarMost, store.ErrLimit)
	}
	return changed, nil
}

// unmarked is a record's JSON without the sweep's mark; nil for no record.
func unmarked(record []byte) ([]byte, error) {
	if record == nil {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record, &fields); err != nil {
		return nil, err
	}
	delete(fields, "goneSince")
	return json.Marshal(fields)
}

func (s Sidecar) save(path string) error {
	for k, r := range s.Skills {
		r.AddedAt = r.AddedAt.UTC().Truncate(time.Second)
		if r.CommittedAt != nil {
			t := r.CommittedAt.UTC().Truncate(time.Second)
			r.CommittedAt = &t
		}
		s.Skills[k] = r
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// store-bounds: kind=key-value; count=2000; size=8KiB a skill, and the sweep's mark; age=until-deleted; applied=on write (refused), and hourly sweep
	return store.WriteAtomically(path, b, 0o644)
}
