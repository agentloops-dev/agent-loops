package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/storesizes"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// How large this control plane's declared stores are (#6906): measured from the store's listing
// after the hourly sweep, at most once in storeSizesEvery, by whichever copy comes first.
// reports/storeSizes reads the last measurement; a read measures nothing. The store is one
// organisation's, so no other's is in it.

const (
	// store-bounds: kind=key-value; count=1; size=128KiB a record; age=until-deleted; applied=on write, each measurement in place of the last
	storeSizesKey       = "v1/store-sizes.json"
	storeSizesMostBytes = 128 << 10
	storeSizesEvery     = 6 * time.Hour
)

// storeSizesRecord is storeSizesKey.
type storeSizesRecord struct {
	At     wire.Date                     `json:"at"`
	Stores map[string]storesizes.Measure `json:"stores"`
}

func (s *Service) readStoreSizes(ctx context.Context) (storeSizesRecord, *store.Object, error) {
	var r storeSizesRecord
	object, err := s.cfg.Store.Get(ctx, storeSizesKey)
	if err == nil && object != nil && json.Unmarshal(object.Data, &r) != nil {
		s.log("store: the store sizes do not read, so they are measured again")
		r = storeSizesRecord{}
	}
	return r, object, err
}

// measureStores measures each store when the last measurement is storeSizesEvery old. Another
// copy that measured at the same time wins: either result is as good.
func (s *Service) measureStores(ctx context.Context, now time.Time) error {
	last, object, err := s.readStoreSizes(ctx)
	if err != nil {
		return err
	}
	if age := now.Sub(last.At.Time()); age >= 0 && age < storeSizesEvery {
		return nil
	}
	record := storeSizesRecord{Stores: map[string]storesizes.Measure{}}
	for _, r := range storesizes.Catalogue(wire.StoreOwnerControlPlane) {
		select {
		case <-s.stop:
			return nil
		default:
		}
		if s.phase.now() != phaseServing {
			return nil
		}
		if r.At != nil {
			record.Stores[r.ID] = r.Keys(ctx, s.cfg.Store, now)
		}
	}
	record.At = wire.DateOf(now)
	data, err := records.Encode(record)
	if err != nil {
		return err
	}
	if err := records.Fits(ctx, s.cfg.Store, storeSizesKey, "The store sizes", data, storeSizesMostBytes); err != nil {
		return err
	}
	when := store.Absent
	if object != nil {
		when = store.Matching(object.ETag)
	}
	if _, err := s.cfg.Store.Put(ctx, storeSizesKey, data, when); err != nil && !store.IsConflict(err) {
		return err
	}
	return nil
}

// storeSizes is reports/storeSizes: an admin sees the control plane's stores, and anyone the
// client stores that are declared.
func (s *Service) storeSizes(ctx context.Context, q wire.StoreSizesQuery, user string) (wire.StoreSizes, error) {
	rows := storesizes.Catalogue(wire.StoreOwnerClient)
	var last storeSizesRecord
	if s.isAdmin(user) {
		rows = append(rows, storesizes.Catalogue(wire.StoreOwnerControlPlane)...)
		var err error
		if last, _, err = s.readStoreSizes(ctx); err != nil {
			return wire.StoreSizes{}, fail(storeUnavailable, "The store sizes could not be read: %v", err)
		}
	}
	page, err := storesizes.Page(rows, last.Stores, q, time.Now(), 2*storeSizesEvery)
	if errors.Is(err, storesizes.ErrQuery) {
		return page, fail(invalidParams, "Ask for 1 to %d stores a page.", storesizes.MostPage)
	}
	return page, err
}
