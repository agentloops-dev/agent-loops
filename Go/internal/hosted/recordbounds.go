package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// The bounds of the service's own lists (#6782 to #6784, #6786; Alex, 2026-11-24): each
// record has a largest size and each list a count, and a write over one is refused. An
// invite goes when it expires, and an allowed login and a place on the waitlist 90 days
// after they were made. A block has no age: one that aged out would let the person back
// in. Idle accounts are swept in the same hour (idleaccounts.go).

const (
	// recordMost is the largest record of these lists.
	recordMost = 4 << 10
	// recordLife is how long an allowed login or a place on the waitlist is kept, and the
	// longest life of an invite.
	recordLife = 90 * 24 * time.Hour
	// recordsSweepEvery is how often the sweep below runs.
	recordsSweepEvery = time.Hour
	invitesMost       = 1000
	allowMost         = 10000
	// blockedMost bounds the blocked sign-ins, and the blocked emails, each.
	blockedMost = 10000
)

// putRecord writes a record of these lists, and refuses one over its largest size.
func putRecord(ctx context.Context, st store.Store, key string, data []byte, when store.Condition) error {
	if len(data) > recordMost {
		return fmt.Errorf("the record is %d bytes, and the most is %d", len(data), recordMost)
	}
	_, err := st.Put(ctx, key, data, when)
	return err
}

// putListed is putRecord for a list of at most most records under prefix, leaving out those
// under but: a new one past it is refused, and one that is there is replaced. full says what
// to do.
func putListed(ctx context.Context, st store.Store, key string, data []byte, prefix, but string, most int, full string) error {
	if o, err := st.Get(ctx, key); err != nil {
		return err
	} else if o == nil {
		keys, err := st.List(ctx, prefix)
		if err != nil {
			return err
		}
		n := 0
		for _, k := range keys {
			if but == "" || !strings.HasPrefix(k.Key, but) {
				n++
			}
		}
		if n >= most {
			return fmt.Errorf("there are %d already, the most: %s", n, full)
		}
	}
	return putRecord(ctx, st, key, data, store.Always)
}

// putBlock blocks a sign-in.
func putBlock(ctx context.Context, st store.Store, user SignIn, data []byte) error {
	return putListed(ctx, st, blockedKey(user), data, blockedFolder, blockedEmailFolder, blockedMost, "unblock some sign-ins first")
}

// putEmailBlock blocks an email, by its mark.
func putEmailBlock(ctx context.Context, st store.Store, mark string, data []byte) error {
	return putListed(ctx, st, blockedEmailKey(mark), data, blockedEmailFolder, "", blockedMost, "unblock some emails first")
}

// sweepRecords ages these lists out, once an hour. It runs beside the minute's sweeps, so a
// long list holds none of them up.
func (s *Service) sweepRecords() {
	now := s.cfg.Now()
	s.mu.Lock()
	due := !s.stopped && now.Sub(s.recordsSwept) >= recordsSweepEvery
	if due {
		s.recordsSwept = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := sweepRecords(ctx, s.cfg.Store, now); err != nil {
			s.log("records sweep: " + err.Error())
		}
		if err := s.sweepIdleAccounts(ctx, now); err != nil {
			s.log("idle accounts sweep: " + err.Error())
		}
	}()
}

// sweepRecords deletes what is past its age. A record it cannot read, or with no time, is
// kept and counts against its list. A list that fails does not stop the others.
func sweepRecords(ctx context.Context, st store.Store, now time.Time) error {
	var failed []error
	for _, list := range []struct {
		name  string
		sweep func(context.Context, store.Store, time.Time) error
	}{{"invites", sweepInvites}, {"allow list", sweepAllowed}, {"waitlist", sweepWaitlist}} {
		if err := list.sweep(ctx, st, now); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", list.name, err))
		}
	}
	return errors.Join(failed...)
}

// sweepInvites deletes the invites past their expiry, used or not: the gate refuses one that
// is not there as used. One made before the limit on an invite's life is cut to it.
// One too large to write again (#7197) has who used it, so it is used: it goes recordLife after
// its use.
func sweepInvites(ctx context.Context, st store.Store, now time.Time) error {
	return eachRecord(ctx, st, invitesFolder, func(key string, o *store.Object) error {
		var rec Invite
		if json.Unmarshal(o.Data, &rec) != nil || rec.Expires.IsZero() {
			return nil
		}
		if !now.Before(rec.Expires) {
			return st.Delete(ctx, key)
		}
		if latest := now.Add(recordLife).UTC(); rec.Expires.After(latest) {
			rec.Expires = latest
			data, _ := json.Marshal(rec)
			if len(data) > recordMost {
				if rec.Used != nil && now.Sub(rec.Used.At) <= recordLife {
					return nil
				}
				return st.Delete(ctx, key)
			}
			if err := putRecord(ctx, st, key, data, store.Matching(o.ETag)); err != nil && !store.IsConflict(err) {
				return err
			}
		}
		return nil
	})
}

// sweepAllowed deletes the logins allowed recordLife ago. An account made by one stays.
func sweepAllowed(ctx context.Context, st store.Store, now time.Time) error {
	return eachRecord(ctx, st, allowFolder, func(key string, o *store.Object) error {
		var rec struct {
			At time.Time `json:"at"`
		}
		if json.Unmarshal(o.Data, &rec) != nil || rec.At.IsZero() || now.Sub(rec.At) <= recordLife {
			return nil
		}
		return st.Delete(ctx, key)
	})
}

// sweepWaitlist deletes the entries made recordLife ago, then the places that old, whose
// keys say when: a place without an entry goes too.
func sweepWaitlist(ctx context.Context, st store.Store, now time.Time) error {
	err := eachRecord(ctx, st, waitingFolder, func(key string, o *store.Object) error {
		var entry Waiting
		if json.Unmarshal(o.Data, &entry) != nil || entry.At.IsZero() || now.Sub(entry.At) <= recordLife {
			return nil
		}
		return st.Delete(ctx, key)
	})
	if err != nil {
		return err
	}
	keys, err := st.List(ctx, waitlistFolder)
	if err != nil {
		return err
	}
	for _, k := range keys {
		at, ok := placeTime(strings.TrimPrefix(k.Key, waitlistFolder))
		if !ok || now.Sub(at) <= recordLife {
			continue
		}
		if err := st.Delete(ctx, k.Key); err != nil {
			return err
		}
	}
	return nil
}

// placeTime is when a place was made: its order, twenty digits of nanoseconds, then whose.
func placeTime(place string) (time.Time, bool) {
	if len(place) < 22 || place[21] != '-' {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(place[1:21], 10, 64)
	return time.Unix(0, n), err == nil
}

// eachRecord gives each record under prefix to f.
func eachRecord(ctx context.Context, st store.Store, prefix string, f func(key string, o *store.Object) error) error {
	keys, err := st.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, k := range keys {
		o, err := st.Get(ctx, k.Key)
		if err != nil {
			return err
		}
		if o == nil {
			continue
		}
		if err := f(k.Key, o); err != nil {
			return err
		}
	}
	return nil
}
