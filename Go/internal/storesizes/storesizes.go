// Package storesizes says how large each declared data store is, and how much of its bounds it
// uses (#6906, epic #6669): sizes and counts only. catalogue.json has one row for each store
// that a `store-bounds:` comment or a Spec declares; scripts/check-store-bounds.py fails when
// the two do not agree. A machine and a control plane measure on a timer and keep the result;
// a query reads the last one and measures nothing.
package storesizes

import (
	_ "embed"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

//go:embed catalogue.json
var catalogueJSON []byte

// Row is a declared store.
type Row struct {
	ID    string          `json:"id"`
	Owner wire.StoreOwner `json:"owner"`
	Scope string          `json:"scope"`
	Kind  string          `json:"kind"`
	// The limits; 0 is no such limit. LimitPer is what each is for, when not the whole store.
	MaxBytes   int64  `json:"maxBytes"`
	MaxRecords int64  `json:"maxRecords"`
	LimitPer   string `json:"limitPer"`
	// At is where it is measured; nil when it is not, and Why says why not.
	At  *Place `json:"at"`
	Why string `json:"why"`
}

// Place is where a store is measured: files on a machine, or keys of a control plane's store.
type Place struct {
	// Base is the folder the globs of Files are in: root, logs, settings, secrets, home, or
	// folders (each folder of the machine). A match that is a folder is all the files in it.
	Base  string   `json:"base"`
	Files []string `json:"files"`
	// Custom is a store its owner measures in code.
	Custom bool `json:"custom"`
	// Prefix is a key, or a family of keys that ends in "/". Walk "folder" lists each folder
	// under it apart, and in each only Sub.
	Prefix string `json:"prefix"`
	Walk   string `json:"walk"`
	Sub    string `json:"sub"`
	// Skip and Only choose the keys that are records: those that do not end in Skip, and those
	// that contain Only.
	Skip string `json:"skip"`
	Only string `json:"only"`
	// Count is what a record is: files (or objects), items (of the JSON file), items:<field>,
	// or fields:<a>,<b>, where the limit is for each field. Empty counts none.
	Count string `json:"count"`
	// Part is what LimitPer counts: file, pattern, base or folder; object, folder or folders (two
	// levels of them) of keys.
	Part string `json:"part"`
}

// Catalogue is the declared stores of owner, by id.
func Catalogue(owner wire.StoreOwner) []Row {
	var all []Row
	if err := json.Unmarshal(catalogueJSON, &all); err != nil {
		panic("storesizes: catalogue.json: " + err.Error())
	}
	return slices.DeleteFunc(all, func(r Row) bool { return r.Owner != owner })
}

// Measure is one completed measurement of a store.
type Measure struct {
	At      wire.Date `json:"at"`
	Bytes   *int64    `json:"bytes,omitempty"`
	Records *int64    `json:"records,omitempty"`
	// The largest part, of a store whose limits are for each part.
	LargestBytes   *int64 `json:"largestBytes,omitempty"`
	LargestRecords *int64 `json:"largestRecords,omitempty"`
	// Why it could not be measured.
	Why string `json:"why,omitempty"`
	// Stamp is the files' state when Records was counted: the same is not counted again.
	Stamp string `json:"stamp,omitempty"`
}

// MostPage is the largest page.
const MostPage = 100

// ErrQuery is a limit that is not 1 to MostPage.
var ErrQuery = errors.New("ask for 1 to 100 stores a page")

// Page is the answer to a query over rows: what measured has of each, at now. A measurement
// older than staleAfter is stale.
func Page(rows []Row, measured map[string]Measure, q wire.StoreSizesQuery, now time.Time, staleAfter time.Duration) (wire.StoreSizes, error) {
	limit := int64(MostPage)
	if q.Limit != nil {
		limit = *q.Limit
	}
	if limit < 1 || limit > MostPage {
		return wire.StoreSizes{}, ErrQuery
	}
	rows = slices.Clone(rows)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	out := wire.StoreSizes{Stores: []wire.StoreSize{}, Total: int64(len(rows))}
	for _, r := range rows {
		if q.Cursor != nil && r.ID <= *q.Cursor {
			continue
		}
		if int64(len(out.Stores)) == limit {
			last := out.Stores[limit-1].ID
			out.Cursor = &last
			break
		}
		m, ok := measured[r.ID]
		out.Stores = append(out.Stores, r.size(m, ok, now, staleAfter))
	}
	return out, nil
}

func (r Row) size(m Measure, measured bool, now time.Time, staleAfter time.Duration) wire.StoreSize {
	s := wire.StoreSize{ID: r.ID, Owner: r.Owner, Scope: r.Scope, Kind: r.Kind, State: wire.StoreSizeStateUnavailable,
		MaxBytes: some(r.MaxBytes), MaxRecords: some(r.MaxRecords)}
	if r.LimitPer != "" {
		s.LimitPer = &r.LimitPer
	}
	why := r.Why
	switch {
	case why == "" && r.Owner == wire.StoreOwnerClient:
		why = "A client does not send its measurements."
	case why == "" && !measured:
		why = "Not measured yet."
	case why == "":
		why = m.Why
	}
	if why != "" {
		s.Why = &why
		return s
	}
	s.Bytes, s.Records, s.MeasuredAt = m.Bytes, m.Records, &m.At
	if m.Bytes != nil && m.Records != nil && *m.Records > 0 {
		average := *m.Bytes / *m.Records
		s.AverageRecordBytes = &average
	}
	bytes, records := m.Bytes, m.Records
	if r.LimitPer != "" {
		bytes, records = m.LargestBytes, m.LargestRecords
	}
	s.PercentBytes, s.PercentRecords = percent(bytes, r.MaxBytes), percent(records, r.MaxRecords)
	switch {
	case s.PercentBytes == nil && s.PercentRecords == nil:
		why = "What its limits count is not measured."
		s.Why = &why
	case over(bytes, r.MaxBytes) || over(records, r.MaxRecords):
		s.State = wire.StoreSizeStateOver
	case now.Sub(m.At.Time()) > staleAfter:
		s.State = wire.StoreSizeStateStale
	default:
		s.State = wire.StoreSizeStateCurrent
	}
	return s
}

func some(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

func percent(n *int64, limit int64) *float64 {
	if n == nil || limit <= 0 {
		return nil
	}
	p := float64(*n) / float64(limit) * 100
	return &p
}

func over(n *int64, limit int64) bool { return n != nil && limit > 0 && *n > limit }

// tally adds up a measurement: the whole, and its parts.
type tally struct {
	bytes, records             int64
	partBytes                  map[string]int64
	partRecords                map[string]int64
	bytesUnknown, countUnknown bool
}

func newTally() *tally {
	return &tally{partBytes: map[string]int64{}, partRecords: map[string]int64{}}
}

func (t *tally) add(part string, bytes, records int64) {
	t.bytes += bytes
	t.records += records
	t.partBytes[part] += bytes
	t.partRecords[part] += records
}

// Measure is what was added, at now. parts says that the store's limits are for each part.
func (t *tally) measure(now time.Time, count, parts bool) Measure {
	m := Measure{At: wire.DateOf(now)}
	if !t.bytesUnknown {
		m.Bytes = &t.bytes
		if parts {
			m.LargestBytes = largest(t.partBytes)
		}
	}
	if count && !t.countUnknown {
		m.Records = &t.records
		if parts {
			m.LargestRecords = largest(t.partRecords)
		}
	}
	return m
}

func largest(parts map[string]int64) *int64 {
	var most int64
	for _, n := range parts {
		most = max(most, n)
	}
	return &most
}

// Unmeasured is a measurement that failed at now, with the words for a person.
func Unmeasured(now time.Time, why string) Measure { return Measure{At: wire.DateOf(now), Why: why} }

// Parts is a measurement of a store from each part's bytes, at now.
func Parts(now time.Time, bytes []int64) Measure {
	var total, most int64
	for _, n := range bytes {
		total, most = total+n, max(most, n)
	}
	return Measure{At: wire.DateOf(now), Bytes: &total, LargestBytes: &most}
}
