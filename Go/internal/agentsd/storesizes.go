package agentsd

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/storesizes"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// How large this machine's declared stores are (#6906): measured once an hour, after the
// retention sweep, into store-sizes.json. stores/list and the app tool list_stores read the
// last measurement; a read measures nothing.

const (
	// storeSizesStale is twice the hourly measurement.
	storeSizesStale = 2 * retentionSweepEvery
	// storeSizesToolPage is the tool's page when it is not said: an agent reads each row.
	storeSizesToolPage = 50
)

func (d *Daemon) openStoreSizes() *store.JSONFile[storesizes.Measure] {
	return smallFile(d, store.Spec{Name: "store-sizes.json", Kind: "key-value", Scope: "machine",
		Bounds: store.Bounds{MaxRecordBytes: 4 << 10, MaxCount: 200, MaxAge: store.MaxAge, Action: store.EvictOldest}},
		func() string { return filepath.Join(d.Store.Loc.Root, "store-sizes.json") },
		func(m *storesizes.Measure) *wire.Date { return &m.At })
}

// storeRows is this machine's declared stores: the catalogue's, and each small file by its Spec.
func (d *Daemon) storeRows() []storesizes.Row {
	rows := storesizes.Catalogue(wire.StoreOwnerMachine)
	for _, f := range d.smallFiles {
		spec := f.Spec()
		rows = append(rows, storesizes.Row{ID: "machine." + strings.TrimSuffix(spec.Name, ".json"), Owner: wire.StoreOwnerMachine,
			Scope: spec.Scope, Kind: spec.Kind, MaxRecords: int64(spec.Bounds.MaxCount),
			At: &storesizes.Place{Base: filepath.Dir(f.Path()), Files: []string{filepath.Base(f.Path())}, Count: "items"}})
	}
	return rows
}

// storeBases is the folders that a row's files are in; none when this machine has no such place.
func (d *Daemon) storeBases(base string) []string {
	home := d.personalHome()
	switch base {
	case "root":
		base = d.Store.Loc.Root
	case "cache":
		base = d.Store.Loc.Cache
	case "logs":
		base = d.Store.Loc.Logs
	case "settings":
		base = d.SettingsDir
	case "backups":
		base = d.backupFolder()
	case "folders":
		return d.folderPaths()
	case "home":
		base = home
	case "secrets":
		if base = ""; home != "" {
			base = filepath.Join(home, ".agents-secrets")
		}
	}
	if base == "" || base == "." {
		return nil
	}
	return []string{base}
}

// measureStores measures each store, and keeps the result.
func (d *Daemon) measureStores() {
	before, err := d.storeSizes.Read()
	if err != nil {
		log.Printf("store-sizes.json: %v", err)
	}
	got := map[string]storesizes.Measure{}
	for _, r := range d.storeRows() {
		if d.ctx.Err() != nil {
			return
		}
		switch {
		case r.ID == "machine.account-homes":
			got[r.ID] = storesizes.Parts(nowFunc(), d.accountHomeBytes())
		case r.At != nil:
			got[r.ID] = r.Files(d.storeBases(r.At.Base), before[r.ID], nowFunc())
		}
	}
	if err := d.storeSizes.Update(func(m map[string]storesizes.Measure) error {
		clear(m)
		for id, measure := range got {
			m[id] = measure
		}
		return nil
	}); err != nil {
		log.Printf("store-sizes.json: could not save: %v", err)
	}
}

// accountHomeBytes is the bytes of the conversations in each account's home, as their bound counts.
func (d *Daemon) accountHomeBytes() []int64 {
	var out []int64
	for runtimeID := range accountKinds {
		dir := filepath.Join(d.Store.Loc.Root, "runtimes", "accounts", runtimeID)
		homes, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, h := range homes {
			if !h.IsDir() {
				continue
			}
			var n int64
			for _, c := range accountConversations(runtimeID, filepath.Join(dir, h.Name())) {
				n += c.bytes
			}
			out = append(out, n)
		}
	}
	return out
}

func (d *Daemon) registerStoreSizes() {
	On(d, wire.CallStoresList, func(_ context.Context, _ *Client, q wire.StoreSizesQuery) (wire.StoreSizes, error) {
		return d.storeSizesPage(q)
	})
}

func (d *Daemon) storeSizesPage(q wire.StoreSizesQuery) (wire.StoreSizes, error) {
	// A file that cannot be read has no measurement: each store then says so.
	measured, err := d.storeSizes.Read()
	if err != nil {
		log.Printf("store-sizes.json: %v", err)
	}
	page, err := storesizes.Page(d.storeRows(), measured, q, nowFunc(), storeSizesStale)
	if err != nil {
		return page, Invalid("Ask for 1 to %d stores a page.", storesizes.MostPage)
	}
	return page, nil
}

// listStoresTool is list_stores: a page as JSON, with times an agent can read.
func (d *Daemon) listStoresTool(_ wire.UUID, args map[string]json.RawMessage) (string, error) {
	limit := int64(storeSizesToolPage)
	if raw := strings.Trim(string(args["limit"]), `"`); raw != "" && raw != "null" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return "", Invalid("limit is a number from 1 to %d.", storesizes.MostPage)
		}
		limit = n
	}
	q := wire.StoreSizesQuery{Limit: &limit}
	if c := argString(args, "cursor"); c != "" {
		q.Cursor = &c
	}
	page, err := d.storeSizesPage(q)
	if err != nil {
		return "", err
	}
	type row struct {
		wire.StoreSize
		MeasuredAt string `json:"measuredAt,omitempty"`
	}
	out := struct {
		Stores []row   `json:"stores"`
		Total  int64   `json:"total"`
		Cursor *string `json:"cursor,omitempty"`
	}{[]row{}, page.Total, page.Cursor}
	for _, s := range page.Stores {
		r := row{StoreSize: s}
		if s.MeasuredAt != nil {
			r.MeasuredAt = s.MeasuredAt.Time().UTC().Format(time.RFC3339)
		}
		out.Stores = append(out.Stores, r)
	}
	b, err := json.Marshal(out)
	return string(b), err
}
