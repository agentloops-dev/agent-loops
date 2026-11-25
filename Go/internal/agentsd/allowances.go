package agentsd

import (
	"bytes"
	"cmp"
	"context"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Runtime accounts and allowances, as the Swift host keeps them on a server: an account
// per runtime, in memory only (unknown until a runtime is started); each credential's
// allowance in <root>/allowances.json, shared sign-ins merged from the Mac by
// pool/applyAllowances (the newer change wins); runtimes/allowances the Pool page's rows,
// runtimes/allowancesChanged at most once a second.

type allowanceBook struct {
	allowMu      sync.Mutex
	allowLoaded  bool
	allowances   map[string]wire.AllowanceState
	accounts     map[string]wire.RuntimeAccount
	allowPending bool
	allowTold    time.Time
	allowStopped bool
}

func (d *Daemon) allowancesPath() string { return filepath.Join(d.Store.Loc.Root, "allowances.json") }

func (d *Daemon) allowancesLocked() map[string]wire.AllowanceState {
	if !d.allowLoaded {
		d.allowLoaded = true
		d.allowances = map[string]wire.AllowanceState{}
		var list []wire.AllowanceState
		readStore(d.allowancesPath(), &list, "starting with every runtime available")
		for _, s := range list {
			if s, ok := agedAllowance(s, nowFunc()); ok {
				d.allowances[s.CredentialKey] = s
			}
		}
	}
	return d.allowances
}

// An allowance is made from what the runtimes said, so it ages out, and the oldest goes first
// (#6747). It stays in the data folder: it is this machine's reading, and no configuration. What
// a person said ("it works again") is what a credential with no record is, so nothing is lost.
const (
	allowancesMost     = 500
	allowanceMostBytes = 64 << 10
	allowanceKept      = store.MaxAge
)

// allowanceNews is when a credential's record last heard something.
func allowanceNews(s wire.AllowanceState) wire.Date {
	at := s.ChangedAt
	if s.Reading != nil {
		at = max(at, s.Reading.At)
	}
	for _, w := range s.Weeks {
		at = max(at, w.At)
	}
	return at
}

// fromTheFuture says if a time is more than a day ahead: two machines' clocks differ by less.
func fromTheFuture(d wire.Date, at time.Time) bool { return d.Time().Sub(at) > 24*time.Hour }

// agedAllowance is a credential's record without what is past 90 days; false when nothing is
// left of it. One that is out until a time still to come stays, whatever its age.
func agedAllowance(s wire.AllowanceState, at time.Time) (wire.AllowanceState, bool) {
	old := func(d wire.Date) bool { return at.Sub(d.Time()) >= allowanceKept }
	// A clock that was wrong must not keep a record for longer.
	honest := func(d wire.Date) wire.Date {
		if fromTheFuture(d, at) {
			return wire.DateOf(at)
		}
		return d
	}
	s.ChangedAt = honest(s.ChangedAt)
	if s.Reading != nil {
		r := *s.Reading
		r.At = honest(r.At)
		s.Reading = &r
	}
	s.Weeks = slices.Clone(s.Weeks)
	for i := range s.Weeks {
		s.Weeks[i].At = honest(s.Weeks[i].At)
	}
	out := false
	if o := s.Status.Out; o != nil && o.Until != nil {
		out = o.Until.Time().After(at) && o.Until.Time().Sub(at) < allowanceKept
	}
	if old(allowanceNews(s)) && !out {
		return s, false
	}
	s.Weeks = slices.DeleteFunc(s.Weeks, func(w wire.AllowanceReading) bool { return old(w.At) })
	s.RateLimitStreak = slices.DeleteFunc(slices.Clone(s.RateLimitStreak), old)
	if s.Reading != nil && old(s.Reading.At) {
		s.Reading = nil
	}
	if s.LastRateLimit != nil && old(allowanceRateLimitAt(s)) {
		s.LastRateLimit = nil
	}
	return s, true
}

// allowanceRateLimitAt is the last time that a rate limit could have been said: it has no time
// of its own, so when it resets, or else when the record changed.
func allowanceRateLimitAt(s wire.AllowanceState) wire.Date {
	if s.LastRateLimit.ResetsAt != nil {
		return max(*s.LastRateLimit.ResetsAt, s.ChangedAt)
	}
	return s.ChangedAt
}

// boundAllowancesLocked holds the records to their limits.
func (d *Daemon) boundAllowancesLocked(at time.Time) {
	for key, s := range d.allowances {
		s, ok := agedAllowance(s, at)
		if ok && allowanceBytes(s) > allowanceMostBytes {
			s.LastRateLimit, s.Reading, s.Weeks, s.RateLimitStreak = nil, nil, nil, []wire.Date{}
			ok = allowanceBytes(s) <= allowanceMostBytes
		}
		if !ok {
			delete(d.allowances, key)
			continue
		}
		d.allowances[key] = s
	}
	keys := slices.SortedFunc(maps.Keys(d.allowances), func(a, b string) int {
		return cmp.Or(cmp.Compare(allowanceNews(d.allowances[a]), allowanceNews(d.allowances[b])), cmp.Compare(a, b))
	})
	for _, key := range keys[:max(0, len(keys)-allowancesMost)] {
		delete(d.allowances, key)
	}
}

func allowanceBytes(s wire.AllowanceState) int {
	b, err := diskjson.Marshal(s)
	if err != nil {
		return allowanceMostBytes + 1
	}
	return len(b)
}

// sweepAllowances is the hourly sweep: the file is written only when a record went or changed.
func (d *Daemon) sweepAllowances(at time.Time) {
	d.allowMu.Lock()
	defer d.allowMu.Unlock()
	was, err := diskjson.Marshal(d.allowancesLocked())
	if err != nil {
		log.Printf("allowances: could not sweep: %v", err)
		return
	}
	d.boundAllowancesLocked(at)
	if is, err := diskjson.Marshal(d.allowances); err != nil || !bytes.Equal(was, is) {
		d.saveAllowancesLocked()
	}
}

func (d *Daemon) saveAllowancesLocked() {
	d.boundAllowancesLocked(nowFunc())
	list := []wire.AllowanceState{}
	for _, s := range d.allowances {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CredentialKey < list[j].CredentialKey })
	b, err := diskjson.Marshal(list)
	if err == nil {
		// store-bounds: kind=key-value; count=500; size=64KiB a credential; age=90d; applied=on write, and hourly sweep
		err = store.WriteAtomically(d.allowancesPath(), b, 0o644)
	}
	if err != nil {
		log.Printf("allowances: could not save: %v", err)
	}
}

// ownKey is a runtime's own credential: its sign-in.
func ownKey(runtime string) string {
	return runtime + ":sign-in"
}

func isSharedKey(k string) bool { return strings.HasSuffix(k, ":sign-in") }

func availableState(key string) wire.AllowanceState {
	t := now()
	return wire.AllowanceState{CredentialKey: key, EntryID: store.NewUUID(), Status: wire.AllowanceStateStatus{Available: &struct{}{}},
		Since: t, ChangedAt: t, LearnedFrom: wire.AllowanceStateSourcePerson, Spent: wire.AllowanceStateSpent{Known: &wire.AllowanceStateSpentKnown{}},
		RateLimitStreak: []wire.Date{}}
}

// accounts is runtimes/accounts: one per runtime, unknown until one has started.
func (d *Daemon) runtimeAccounts() []wire.RuntimeAccount {
	d.allowMu.Lock()
	defer d.allowMu.Unlock()
	out := []wire.RuntimeAccount{}
	for _, r := range catalog {
		a, ok := d.accounts[r.ID]
		if !ok {
			a = wire.RuntimeAccount{RuntimeID: r.ID, State: wire.RuntimeAccountStateUnknown, AuthMethods: []wire.ACPAuthMethod{},
				Providers: []wire.ACPProviderInfo{}, CheckedAt: now()}
		}
		out = append(out, a)
	}
	return out
}

// noteAccount is noteAccount: a handshake says the runtime is ready, and how it signs in.
func (d *Daemon) noteAccount(runtime string, canSteer bool, caps wire.ACPPromptCapabilities) {
	d.allowMu.Lock()
	if d.accounts == nil {
		d.accounts = map[string]wire.RuntimeAccount{}
	}
	a, ok := d.accounts[runtime]
	if !ok {
		a = wire.RuntimeAccount{RuntimeID: runtime, AuthMethods: []wire.ACPAuthMethod{}, Providers: []wire.ACPProviderInfo{}}
	}
	if a.State != wire.RuntimeAccountStateNeedsSignIn {
		a.State = wire.RuntimeAccountStateReady
	}
	a.CanSteer, a.PromptCapabilities, a.CheckedAt = canSteer, caps, now()
	d.accounts[runtime] = a
	d.allowMu.Unlock()
	Broadcast(d, wire.NotifyRuntimeAccount, a)
}

// unusable is why a runtime's own sign-in cannot be used now, or nil; installed is each
// runtime runtimeAllowances located once for all its rows (#2251).
func (d *Daemon) unusable(runtime string, installed map[string]bool) *string {
	rt := runtimeByID(runtime)
	if rt == nil {
		s := "not a runtime this app knows"
		return &s
	}
	if !installed[runtime] {
		s := "not installed"
		return &s
	}
	if d.needsSignIn(runtime) != nil {
		s := "not signed in"
		return &s
	}
	return nil
}

// runtimeAllowances is runtimes/allowances: a row each credential, by runtime name, each
// runtime's own sign-in first, then its added accounts in the order they were added
// (#840); the shared sign-ins.
func (d *Daemon) runtimeAllowances() wire.RuntimeAllowances {
	profiles := map[string]wire.AccountProfile{}
	added := map[string]int{}
	for i, p := range d.accountProfiles() {
		k := accountKey(p.RuntimeID, &p.ID)
		profiles[k], added[k] = p, i
	}
	d.allowMu.Lock()
	states := d.allowancesLocked()
	keys := map[string]bool{}
	installed := map[string]bool{}
	for i := range catalog {
		if p, _, _ := d.locate(&catalog[i]); p != "" {
			keys[ownKey(catalog[i].ID)] = true
			installed[catalog[i].ID] = true
		}
	}
	for k := range states {
		// A removed account's state is not a row.
		if keyAccount(k) == "" || profiles[k].ID != "" {
			keys[k] = true
		}
	}
	for k := range profiles {
		keys[k] = true
	}
	var list []string
	for k := range keys {
		list = append(list, k)
	}
	sortAllowanceKeys(list, added)
	out := wire.RuntimeAllowances{Rows: []wire.RuntimeAllowancesRow{}, At: now(), Shared: []wire.AllowanceState{}}
	for _, k := range list {
		s, ok := states[k]
		if !ok {
			s = availableState(k)
		}
		row := wire.RuntimeAllowancesRow{CredentialKey: k, State: s}
		if p, ok := profiles[k]; ok {
			row.AccountID, row.AccountName = &p.ID, &p.Name
		}
		out.Rows = append(out.Rows, row)
		if ok && isSharedKey(k) {
			out.Shared = append(out.Shared, s)
		}
	}
	d.allowMu.Unlock()
	d.markAllowanceRowsUnusable(out.Rows, profiles, installed)
	sort.Slice(out.Shared, func(i, j int) bool { return out.Shared[i].CredentialKey < out.Shared[j].CredentialKey })
	return out
}

// allowanceKeyRuntime is the runtime part of a credential key.
func allowanceKeyRuntime(k string) string { r, _, _ := strings.Cut(k, ":"); return r }

// sortAllowanceKeys puts credential keys in the catalog's order of runtimes, each runtime's
// own sign-in first, then its added accounts in the order they were added.
func sortAllowanceKeys(list []string, added map[string]int) {
	order := map[string]int{}
	for i, r := range catalog {
		order[r.ID] = i
	}
	sort.Slice(list, func(i, j int) bool {
		ri, rj := allowanceKeyRuntime(list[i]), allowanceKeyRuntime(list[j])
		oi, ok1 := order[ri]
		oj, ok2 := order[rj]
		if !ok1 {
			oi = len(order)
		}
		if !ok2 {
			oj = len(order)
		}
		if oi != oj {
			return oi < oj
		}
		if (list[i] == ownKey(ri)) != (list[j] == ownKey(rj)) {
			return list[i] == ownKey(ri)
		}
		ai, ok1 := added[list[i]]
		aj, ok2 := added[list[j]]
		if ok1 && ok2 {
			return ai < aj
		}
		if ok1 != ok2 {
			return ok2
		}
		return list[i] < list[j]
	})
}

// markAllowanceRowsUnusable says on each row why it cannot be used, if it cannot, and
// gives an org account its tokens today.
func (d *Daemon) markAllowanceRowsUnusable(rows []wire.RuntimeAllowancesRow, profiles map[string]wire.AccountProfile, installed map[string]bool) {
	for i := range rows {
		rt := allowanceKeyRuntime(rows[i].CredentialKey)
		if p, ok := profiles[rows[i].CredentialKey]; ok {
			rows[i].Unusable = d.accountUnusable(p, installed)
			// An org account has no plan reading: its tokens today instead (#999, FR-025).
			if route := accountRoute(&p); route != "" {
				rows[i].TokensToday = new(d.routeTokensToday(p.RuntimeID, route, time.Now()))
			}
			continue
		}
		rows[i].Unusable = d.unusable(rt, installed)
		if rt == "claude" && rows[i].CredentialKey == ownKey(rt) && rows[i].Unusable == nil {
			rows[i].SetupNote = chromeHostNote()
		}
	}
}

// tellAllowances is runtimes/allowancesChanged, at most once a second.
func (d *Daemon) tellAllowances() {
	d.allowMu.Lock()
	if d.allowPending {
		d.allowMu.Unlock()
		return
	}
	wait := time.Until(d.allowTold.Add(time.Second))
	d.allowPending = true
	d.allowMu.Unlock()
	d.goJob("allowances changed", func() {
		if wait > 0 {
			time.Sleep(wait)
		}
		d.allowMu.Lock()
		d.allowPending, d.allowTold = false, time.Now()
		stopped := d.allowStopped
		d.allowMu.Unlock()
		if stopped {
			return
		}
		Broadcast(d, wire.NotifyRuntimesAllowancesChanged, d.runtimeAllowances())
	})
}

// raiseAllowanceBack is cost.allowance_back for a credential, how it came back.
func (d *Daemon) raiseAllowanceBack(key, how string) {
	runtime, _, _ := strings.Cut(key, ":")
	details := map[string]string{"runtime": runtime, "how": how}
	if id := keyAccount(key); id != "" {
		details["account"] = id
	}
	d.raise(wire.EventDraft{Name: "cost.allowance_back", At: now(), Scope: macScope(), Sentence: d.credentialName(key) + "’s allowance came back.",
		Details: details})
}

func isOutState(s wire.AllowanceState) bool { return s.Status.Out != nil }

// markAvailable is runtimes/markAvailable: the person says it works again.
func (d *Daemon) markAvailable(key string) wire.RuntimeAllowances {
	d.allowMu.Lock()
	states := d.allowancesLocked()
	s, ok := states[key]
	changed := false
	if ok && s.Status.Available == nil {
		t := now()
		s.Status = wire.AllowanceStateStatus{Available: &struct{}{}}
		s.Since, s.ChangedAt, s.LearnedFrom = t, t, wire.AllowanceStateSourcePerson
		s.Spent = wire.AllowanceStateSpent{Known: &wire.AllowanceStateSpentKnown{}}
		states[key] = s
		d.saveAllowancesLocked()
		changed = true
	}
	d.allowMu.Unlock()
	if changed {
		d.raiseAllowanceBack(key, "person")
		d.allowanceBack(key)
		d.tellAllowances()
	}
	return d.runtimeAllowances()
}

// applyAllowances is pool/applyAllowances: the Mac's shared sign-ins, the newer change winning.
func (d *Daemon) applyAllowances(states []wire.AllowanceState) bool {
	d.allowMu.Lock()
	mine := d.allowancesLocked()
	changed := false
	for _, s := range states {
		if !isSharedKey(s.CredentialKey) {
			continue
		}
		// Another machine can still have what aged out here.
		ahead := fromTheFuture(allowanceNews(s), nowFunc())
		s, fresh := agedAllowance(s, nowFunc())
		if ahead || !fresh {
			continue
		}
		s.Weeks, _ = withWeeks(nil, s.Weeks)
		had, ok := mine[s.CredentialKey]
		switch {
		case !ok || s.ChangedAt > had.ChangedAt:
			if ok && isOutState(had) && isOutState(s) && had.Since < s.Since {
				s.Since = had.Since
			}
			if ok {
				s.Weeks, _ = withWeeks(s.Weeks, had.Weeks)
			}
			mine[s.CredentialKey], changed = s, true
		default:
			newer := s.Reading != nil && (had.Reading == nil || s.Reading.At > had.Reading.At)
			if newer {
				had.Reading = s.Reading
			}
			weeks, more := withWeeks(had.Weeks, s.Weeks)
			if newer || more {
				had.Weeks = weeks
				mine[s.CredentialKey], changed = had, true
			}
		}
	}
	if changed {
		d.saveAllowancesLocked()
	}
	d.allowMu.Unlock()
	if changed {
		d.tellAllowances()
	}
	return changed
}

func (d *Daemon) registerAllowances() {
	On(d, wire.CallRuntimesAccounts, func(ctx context.Context, c *Client, _ wire.Empty) ([]wire.RuntimeAccount, error) {
		return d.runtimeAccounts(), nil
	})
	On(d, wire.CallRuntimesAllowances, func(ctx context.Context, c *Client, _ *string) (wire.RuntimeAllowances, error) {
		return d.runtimeAllowances(), nil
	})
	On(d, wire.CallRuntimesMarkAvailable, func(ctx context.Context, c *Client, r wire.MarkRuntimeAvailable) (wire.RuntimeAllowances, error) {
		return d.markAvailable(r.CredentialKey), nil
	})
	On(d, wire.CallPoolApplyAllowances, func(ctx context.Context, c *Client, r wire.ApplyAllowancesRequest) (wire.Changed, error) {
		return wire.Changed(d.applyAllowances(r.States)), nil
	})
}
