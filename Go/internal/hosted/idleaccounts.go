package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// Idle accounts (#6785, the design on epic #7092; Alex, 2026-11-27): a free account that
// nobody uses for idleAfter is paused, as a suspension the owner's sign-in lifts, and it is
// deleted pausedFor after that. A paid account is never paused: its clock starts on the day
// its plan ends. A deleted account leaves a tombstone for goneFor, so its address answers
// 410 gone, and machines stop dialling it.

const (
	// IdleOff, IdlePause and IdleDelete are Config.IdleAccounts: the sweep does nothing,
	// pauses only, or pauses and deletes (the default).
	IdleOff    = "off"
	IdlePause  = "pause"
	IdleDelete = "delete"

	idleAfter = 90 * day
	pausedFor = 90 * day
	goneFor   = 90 * day
	// idleDeletesMost is the most accounts one sweep deletes; the rest wait for the next.
	idleDeletesMost = 10
	// goneMost is the most tombstones kept: the sweep deletes the oldest past it.
	goneMost = 1000
	// usedDay is the form of Account.LastUsed: a day in UTC.
	usedDay = "2006-01-02"
	// idleReason is an idle pause's reason, which the account page says.
	idleReason = "Nobody used this account for 90 days."
	// Why a tombstone was written: the idle sweep, or the person's Delete Account.
	goneIdle    = "idle"
	goneDeleted = "deleted"
)

// store-bounds: kind=key-value; count=1_000; size=128B a record; age=90d; applied=on delete and hourly sweep
const goneFolder = hostedPrefix + "v1/gone/"

func goneKey(id string) string { return goneFolder + id + ".json" }

// Gone is a deleted account's tombstone: when, and why. Nothing of who it was.
type Gone struct {
	Deleted string `json:"deleted"`
	Why     string `json:"why"`
}

// dayOf is t's day in UTC, as LastUsed says it.
func dayOf(t time.Time) string { return t.UTC().Format(usedDay) }

// shownDay is a day as a line says it: 3 Mar 2027.
func shownDay(t time.Time) string { return t.UTC().Format("2 Jan 2006") }

// paid says the account is on a plan that is not free.
func paid(acc *Account) bool { return PlanOf(acc).ID != PlanFree }

// idlePaused says the service paused the account for idle, not the operator.
func idlePaused(acc *Account) bool { return acc.Suspended != nil && acc.Suspended.Idle }

// useOn marks the account used on day: LastUsed moves forward only, and an idle pause is
// lifted when lift is set (a sign-in, Unsuspend). It says whether it changed anything.
func useOn(acc *Account, day string, lift bool) bool {
	changed := false
	if acc.LastUsed < day {
		acc.LastUsed, changed = day, true
	}
	if lift && idlePaused(acc) {
		acc.Suspended, changed = nil, true
	}
	return changed
}

// markUsed writes today as the account's last use, at most once a day for each account
// in this copy of the service (Config.Used, and a socket open across a day).
func (s *Service) markUsed(ctx context.Context, id string) {
	today := dayOf(s.cfg.Now())
	s.mu.Lock()
	done := s.usedOn[id] == today
	s.mu.Unlock()
	if done {
		return
	}
	acc, err := s.accounts.update(ctx, id, func(acc *Account) { useOn(acc, today, false) })
	if err != nil {
		s.log(id + ": its last use: " + err.Error())
		return
	}
	if acc == nil {
		return
	}
	s.mu.Lock()
	if s.usedOn == nil {
		s.usedOn = map[string]string{}
	}
	// One entry for each account at most, and the accounts are bounded (MaxAccounts).
	s.usedOn[id] = today
	s.mu.Unlock()
}

// usedSoon is Config.Used for an account's copy: off the socket's goroutine.
func (s *Service) usedSoon(id string) func() {
	return func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.markUsed(ctx, id)
		}()
	}
}

// signedIn is the owner's sign-in to an account that exists: a use, which lifts an idle
// pause (#6785). It gives the account back as it is now.
func (s *Service) signedIn(ctx context.Context, acc *Account) *Account {
	today := dayOf(s.cfg.Now())
	if !useOn(&Account{LastUsed: acc.LastUsed, Suspended: acc.Suspended}, today, true) {
		return acc
	}
	got, err := s.accounts.update(ctx, acc.ID, func(a *Account) { useOn(a, today, true) })
	if err != nil || got == nil {
		if err != nil {
			s.log(acc.ID + ": its last use: " + err.Error())
		}
		return acc
	}
	if idlePaused(acc) {
		s.log(acc.ID + ": the owner signed in: no longer paused")
	}
	return got
}

// pausedLine is what a paused or suspended account's address says.
func (s *Service) pausedLine(acc *Account) string {
	if acc != nil && idlePaused(acc) && acc.Suspended.Deletes != nil {
		return "This hosted account is paused: nobody used it for 90 days. Sign in at https://" + s.cfg.Domain +
			"/account by " + shownDay(*acc.Suspended.Deletes) + " to keep it."
	}
	return "This hosted account is suspended. See https://" + s.cfg.Domain + "/account."
}

// gone is the tombstone of a deleted account, or nil.
func (s *Service) gone(ctx context.Context, id string) (*Gone, error) {
	o, err := s.cfg.Store.Get(ctx, goneKey(id))
	if err != nil || o == nil {
		return nil, err
	}
	var g Gone
	if err := json.Unmarshal(o.Data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// answerGone is what a deleted account's address answers while its tombstone is kept.
func (s *Service) answerGone(w http.ResponseWriter, g *Gone) {
	s.metrics.refused("gone")
	w.Header().Set("Agents-Refusal", controllink.Gone)
	when := g.Deleted
	if t, err := time.Parse(usedDay, g.Deleted); err == nil {
		when = shownDay(t)
	}
	plain(w, http.StatusGone, "This hosted account was deleted on "+when+
		". Your machines keep their folders and agents: connect them to another control plane.\n")
}

// writeGone writes an account's tombstone.
func writeGone(ctx context.Context, st store.Store, id, why string, now time.Time) error {
	data, _ := json.Marshal(Gone{Deleted: dayOf(now), Why: why})
	_, err := st.Put(ctx, goneKey(id), data, store.Always)
	return err
}

// removeAccount deletes an account as Delete Account does, with its tombstone first, so
// its address never answers 404 in between. A stop part way is finished by deleting again.
func (s *Service) removeAccount(ctx context.Context, acc *Account, why string) error {
	s.stopCopy(acc.ID)
	if err := writeGone(ctx, s.cfg.Store, acc.ID, why, s.cfg.Now()); err != nil {
		return fmt.Errorf("its tombstone: %w", err)
	}
	return s.accounts.remove(ctx, acc)
}

// sweepIdleAccounts is the hourly idle sweep: it writes the ship day as the last use of an
// account from before LastUsed, pauses the free accounts idle for idleAfter, deletes the
// paused ones past their date (idleDeletesMost a run), and ages the tombstones out.
func (s *Service) sweepIdleAccounts(ctx context.Context, now time.Time) error {
	var failed []error
	if mode := s.cfg.IdleAccounts; mode != IdleOff {
		list, err := s.accounts.all(ctx)
		if err != nil {
			return err
		}
		deleted := 0
		for i := range list {
			acc := &list[i]
			did, err := s.idleAccount(ctx, acc, now, mode, deleted < idleDeletesMost)
			if err != nil {
				failed = append(failed, fmt.Errorf("%s: %w", acc.ID, err))
			}
			if did {
				deleted++
			}
		}
	}
	if err := sweepGone(ctx, s.cfg.Store, now); err != nil {
		failed = append(failed, fmt.Errorf("tombstones: %w", err))
	}
	return errors.Join(failed...)
}

// idleAccount is the sweep's step for one account; it says whether it deleted it.
func (s *Service) idleAccount(ctx context.Context, acc *Account, now time.Time, mode string, mayDelete bool) (bool, error) {
	today := dayOf(now)
	switch {
	case acc.Suspended != nil && !acc.Suspended.Idle:
		// The operator's: neither paused nor deleted here.
		return false, nil
	case paid(acc):
		if idlePaused(acc) {
			// Paid while paused: a paid account is never paused.
			_, err := s.accounts.update(ctx, acc.ID, func(a *Account) { useOn(a, today, true) })
			return false, err
		}
		return false, nil
	case acc.LastUsed == "":
		_, err := s.accounts.update(ctx, acc.ID, func(a *Account) {
			if a.LastUsed == "" {
				a.LastUsed = today
			}
		})
		return false, err
	case idlePaused(acc):
		if mode != IdleDelete || !mayDelete || acc.Suspended.Deletes == nil || now.Before(*acc.Suspended.Deletes) {
			return false, nil
		}
		if err := s.removeAccount(ctx, acc, goneIdle); err != nil {
			return false, err
		}
		s.log(acc.ID + ": deleted, paused for 90 days after 90 days idle")
		return true, nil
	}
	last, err := time.Parse(usedDay, acc.LastUsed)
	if err != nil || now.Sub(last) < idleAfter {
		return false, nil
	}
	deletes := now.Add(pausedFor).UTC()
	got, err := s.accounts.update(ctx, acc.ID, func(a *Account) {
		// Used, paid or suspended since it was read: leave it.
		if a.Suspended != nil || paid(a) || a.LastUsed != acc.LastUsed {
			return
		}
		a.Suspended = &Suspension{Reason: idleReason, At: now.UTC(), Idle: true, Deletes: &deletes}
	})
	if err != nil || got == nil || !idlePaused(got) {
		return false, err
	}
	s.stopCopy(acc.ID)
	s.log(acc.ID + ": paused, idle since " + acc.LastUsed + "; deleted on " + dayOf(deletes) + " unless the owner signs in")
	return false, nil
}

// sweepGone deletes the tombstones older than goneFor, and the oldest past goneMost. One it
// cannot read is deleted: it would hold nothing up.
func sweepGone(ctx context.Context, st store.Store, now time.Time) error {
	type kept struct{ key, day string }
	var all []kept
	err := eachRecord(ctx, st, goneFolder, func(key string, o *store.Object) error {
		var g Gone
		if json.Unmarshal(o.Data, &g) != nil {
			return st.Delete(ctx, key)
		}
		t, err := time.Parse(usedDay, g.Deleted)
		if err != nil || now.Sub(t) > goneFor {
			return st.Delete(ctx, key)
		}
		all = append(all, kept{key, g.Deleted})
		return nil
	})
	if err != nil || len(all) <= goneMost {
		return err
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].day != all[j].day {
			return all[i].day < all[j].day
		}
		return strings.Compare(all[i].key, all[j].key) < 0
	})
	for _, k := range all[:len(all)-goneMost] {
		if err := st.Delete(ctx, k.key); err != nil {
			return err
		}
	}
	return nil
}
