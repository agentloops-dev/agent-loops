package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Warm pools (#5363, epic #5353, "Part 2: warm pools" of the machine set-up spec): a pool
// keeps min ready machines made, joined, with Agents, runtimes and the pool's projects, signed
// in to nothing, up to max size. A reconcile loop counts the pool's machines by state and
// makes or deletes to reach the target (= min ready) within max size and the pool's hours,
// making a new machine before it deletes an old one when there is room.
//
// A pool is one person's now (scope user); the wire says project and org already. One object
// a pool, v1/warm-pools/<id>.json, with the machines it holds. A machine that is claimed
// leaves the pool (the claim, #5364, takes it out), so max counts only ready machines and
// machines being set up. A machine that failed to set itself up is kept, as any failed
// machine is, and counts for neither.

const (
	warmPoolPrefix = "v1/warm-pools/"
	// defaultReplaceAfter: a ready machine is replaced after a day.
	defaultReplaceAfter = 24 * time.Hour
	// poolFailuresStop: this many makes that fail in a row stop the pool.
	poolFailuresStop = 3
	// poolJoinGiveUp: a made machine that has not set itself up by then has failed.
	poolJoinGiveUp = 30 * time.Minute
	// poolEvery is how often every pool is looked at.
	poolEvery = 30 * time.Second
	// poolClaimsKept: a pool keeps its claims of the last week, and no more than mostPoolClaims of
	// them, for its card (#5366); poolClaimsTimed of the latest give the median time to ready.
	poolClaimsKept  = 7 * 24 * time.Hour
	mostPoolClaims  = 500
	poolClaimsTimed = 20
)

type poolStage int

const (
	stageSettingUp poolStage = iota
	stageReady
	stageFailed
)

// poolBackend is what a pool needs of the machines: the providers and the set-up record.
type poolBackend interface {
	// Make makes a machine called name and returns the provider's id of its workspace.
	Make(person, provider, name, size, region string) (string, error)
	// Delete deletes a machine, joined or not.
	Delete(person, provider, workspace string) error
	// Stage says how far a machine's set-up has come: steps 1 to 6 done is ready.
	Stage(provider, workspace string, madeAt time.Time, now time.Time) poolStage
	// HasProvider says the provider is here and may make machines.
	HasProvider(provider string) bool
	// Version is the Agents release a new machine gets; Pins is a mark of the runtime pins.
	// A ready machine that was made with others is replaced at once.
	Version() string
	Pins() string
	// Stopped tells a person that a pool stopped making, and why.
	Stopped(person, pool, why string)
	// Ping says a ready machine can be claimed: its host answers and the provider still
	// lists it (#5365).
	Ping(provider, workspace string) bool
	// Bind makes the machine the person's, with the person's Stop when idle (minutes; nil
	// keeps the machine's), and sets steps 6 to 8 of its set-up to run again as a catch-up
	// (steps 1 to 5 are marked as done in the pool), with the pool's projects for step 6
	// (#6890). It returns the machine's host id.
	Bind(person, provider, workspace string, stopWhenIdle *int, projects []string) (host string, err error)
	// ProjectsWait is why a machine made now would not get the projects, or "": none of the
	// person's machines that has one is online (#6890).
	ProjectsWait(person string, projects []string) string
	// Detail is what a machine's set-up is doing now, as its running step's line says, for
	// the pool's card (#5366); "" when nothing says.
	Detail(provider, workspace string) string
	// ReadyAt is when a claimed machine's set-up was ready, nil before (#5366).
	ReadyAt(host string) *time.Time
}

type storedPoolMachine struct {
	Name      string    `json:"name"`
	Workspace string    `json:"workspace"`
	MadeAt    time.Time `json:"madeAt"`
	Version   string    `json:"version,omitempty"`
	Pins      string    `json:"pins,omitempty"`
}

// storedPoolClaim is one claim of a pool (#5366): when, the machine it gave, or that it found
// the pool empty and made a new machine.
type storedPoolClaim struct {
	At    time.Time `json:"at"`
	Host  string    `json:"host,omitempty"`
	Empty bool      `json:"empty,omitempty"`
}

type storedPool struct {
	ID           string              `json:"id"`
	Owner        string              `json:"owner"`
	Name         string              `json:"name"`
	Scope        wire.WarmPoolScope  `json:"scope"`
	Provider     string              `json:"provider"`
	Size         string              `json:"size,omitempty"`
	Region       string              `json:"region,omitempty"`
	Runtimes     []string            `json:"runtimes,omitempty"`
	Projects     []string            `json:"projects,omitempty"`
	MinReady     int                 `json:"minReady"`
	MaxSize      int                 `json:"maxSize"`
	ReplaceAfter float64             `json:"replaceAfter,omitempty"`
	Hours        *wire.WarmPoolHours `json:"hours,omitempty"`
	Failures     int                 `json:"failures,omitempty"`
	// SetAt is when the person last set the pool: only machines made after it count to stop
	// it for failed set-ups.
	SetAt    *time.Time          `json:"setAt,omitempty"`
	Stopped  string              `json:"stopped,omitempty"`
	Machines []storedPoolMachine `json:"machines,omitempty"`
	LastMade *time.Time          `json:"lastMade,omitempty"`
	Claims   []storedPoolClaim   `json:"claims,omitempty"`
}

func warmPoolKey(id string) string { return warmPoolPrefix + id + ".json" }

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func (p *storedPool) replaceAfter() time.Duration {
	if p.ReplaceAfter > 0 {
		return time.Duration(p.ReplaceAfter * float64(time.Second))
	}
	return defaultReplaceAfter
}

// target is what the pool aims at: min ready, which max size bounds. Auto-scale moves it later.
func (p *storedPool) target() int { return min(p.MinReady, p.MaxSize) }

// minutesOf is "HH:MM" as minutes into the day.
func minutesOf(s string) int {
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[3:])
	return h*60 + m
}

// inHours says the pool runs at now: a pool with no hours runs every day. Hours from later to
// earlier cross midnight and belong to the day they began.
func (p *storedPool) inHours(now time.Time) bool {
	h := p.Hours
	if h == nil {
		return true
	}
	loc := time.UTC
	if h.Timezone != nil && *h.Timezone != "" {
		if l, err := time.LoadLocation(*h.Timezone); err == nil {
			loc = l
		}
	}
	local := now.In(loc)
	at, from, to := local.Hour()*60+local.Minute(), minutesOf(h.From), minutesOf(h.To)
	day := func(d time.Weekday) bool {
		for _, x := range h.Days {
			if int64(d) == x {
				return true
			}
		}
		return false
	}
	switch {
	case from == to:
		return day(local.Weekday())
	case from < to:
		return day(local.Weekday()) && at >= from && at < to
	default:
		if at >= from {
			return day(local.Weekday())
		}
		return at < to && day((local.Weekday()+6)%7)
	}
}

// poolMachine is a stored machine with how far it has come.
type poolMachine struct {
	storedPoolMachine
	stage poolStage
}

// poolPlan is what a pass does: how many machines to make, and which to delete (by index
// into the machines given).
type poolPlan struct {
	makes   int
	deletes []int
}

// planPool is the reconcile rule, with no side effects (#5363).
func planPool(p *storedPool, machines []poolMachine, now time.Time, version, pins string) poolPlan {
	var ready, settingUp []int
	for i, m := range machines {
		switch m.stage {
		case stageReady:
			ready = append(ready, i)
		case stageSettingUp:
			settingUp = append(settingUp, i)
		}
	}
	byAge := func(list []int) {
		sort.SliceStable(list, func(a, b int) bool { return machines[list[a]].MadeAt.Before(machines[list[b]].MadeAt) })
	}
	byAge(ready)
	if p.Stopped != "" {
		return poolPlan{}
	}
	// Outside its hours, or with min 0, a pool holds no ready machine and makes none.
	if !p.inHours(now) || p.target() == 0 {
		return poolPlan{deletes: ready}
	}
	stale := func(m poolMachine) bool {
		return now.Sub(m.MadeAt) >= p.replaceAfter() || m.Version != version || m.Pins != pins
	}
	var freshReady, staleReady []int
	for _, i := range ready {
		if stale(machines[i]) {
			staleReady = append(staleReady, i)
		} else {
			freshReady = append(freshReady, i)
		}
	}
	target := p.target()
	held := len(ready) + len(settingUp)
	fresh := len(freshReady) + len(settingUp)
	plan := poolPlan{}
	if need := target - fresh; need > 0 {
		plan.makes = min(need, max(p.MaxSize-held, 0))
		// No room to make the replacement first: delete the oldest old machine, so a
		// pool at its max still refreshes. The pool is short until the new one is ready.
		if plan.makes < need && len(staleReady) > 0 {
			plan.deletes = append(plan.deletes, staleReady[0])
			staleReady = staleReady[1:]
			plan.makes = min(need, max(p.MaxSize-(held-1), 0))
		}
	}
	// An old machine goes once enough new ones are ready to serve in its place.
	if len(freshReady) >= target {
		plan.deletes = append(plan.deletes, staleReady...)
		staleReady = nil
	}
	// A pool whose min was lowered keeps only what it aims at: the oldest go.
	if surplus := len(freshReady) - target; surplus > 0 {
		plan.deletes = append(plan.deletes, freshReady[:surplus]...)
	}
	// A pool whose max was lowered holds no more than it.
	plan.deletes = cutToMax(plan, held, p.MaxSize, staleReady, freshReady)
	return plan
}

// cutToMax is the plan's deletes, and more of the ready machines (the old first, then the
// new, each oldest first) until the pool holds no more than max.
func cutToMax(plan poolPlan, held, most int, staleReady, freshReady []int) []int {
	deletes := plan.deletes
	gone := map[int]bool{}
	for _, i := range deletes {
		gone[i] = true
	}
	for over := held - len(gone) + plan.makes - most; over > 0; {
		cut := firstNotGone(gone, staleReady, freshReady)
		if cut < 0 {
			break
		}
		gone[cut] = true
		deletes = append(deletes, cut)
		over--
	}
	return deletes
}

// firstNotGone is the first index in the lists, in order, not in gone, or -1.
func firstNotGone(gone map[int]bool, lists ...[]int) int {
	for _, list := range lists {
		for _, i := range list {
			if !gone[i] {
				return i
			}
		}
	}
	return -1
}

// warmPools is the pools' reconcile loop and store.
type warmPools struct {
	s       *Service
	backend poolBackend
	mu      sync.Mutex
	// making are the machines being made now, by name: one can join, and ask for its set-up's
	// work, before its pool keeps it.
	makingMu sync.Mutex
	making   map[string]storedPool
}

func (w *warmPools) nowMaking(name string, p *storedPool) {
	w.makingMu.Lock()
	defer w.makingMu.Unlock()
	if p == nil {
		delete(w.making, name)
		return
	}
	if w.making == nil {
		w.making = map[string]storedPool{}
	}
	w.making[name] = *p
}

// poolOf is the pool that holds an unclaimed machine, or that makes it now; nil for none.
func (w *warmPools) poolOf(ctx context.Context, host *records.MachineRecord) (*storedPool, error) {
	if host == nil {
		return nil, nil
	}
	w.makingMu.Lock()
	p, ok := w.making[host.Name]
	w.makingMu.Unlock()
	if ok && w.s.ownerOf(host.ID) == p.Owner {
		return &p, nil
	}
	link := providers.LinkOf(host)
	if link == nil {
		return nil, nil
	}
	pools, err := w.all(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range pools {
		for _, m := range pools[i].Machines {
			if pools[i].Provider == link.Provider && m.Workspace == link.Workspace {
				return &pools[i], nil
			}
		}
	}
	return nil, nil
}

// failedInRow says that the latest poolFailuresStop machines made since the pool was set all
// failed to set themselves up, with the last one's reason. A failed machine is kept and
// counts for nothing, so without this a pool whose projects cannot be got makes one machine
// after another.
func (w *warmPools) failedInRow(p *storedPool, machines []poolMachine) (string, bool) {
	var made []poolMachine
	for _, m := range machines {
		if p.SetAt != nil && m.MadeAt.After(*p.SetAt) {
			made = append(made, m)
		}
	}
	if len(made) < poolFailuresStop {
		return "", false
	}
	sort.SliceStable(made, func(a, b int) bool { return made[a].MadeAt.After(made[b].MadeAt) })
	for _, m := range made[:poolFailuresStop] {
		if m.stage != stageFailed {
			return "", false
		}
	}
	why := "three machines in a row were not set up"
	if detail := w.backend.Detail(p.Provider, made[0].Workspace); detail != "" {
		why += ": " + detail
	}
	return why, true
}

func (w *warmPools) read(ctx context.Context, id string) (*storedPool, string, error) {
	object, err := w.s.cfg.Store.Get(ctx, warmPoolKey(id))
	if err != nil || object == nil {
		return nil, "", err
	}
	var p storedPool
	if json.Unmarshal(object.Data, &p) != nil {
		return nil, object.ETag, nil
	}
	return &p, object.ETag, nil
}

func (w *warmPools) write(ctx context.Context, p *storedPool, tag string) error {
	when := store.Absent
	if tag != "" {
		when = store.Matching(tag)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = w.s.cfg.Store.Put(ctx, warmPoolKey(p.ID), data, when)
	return err
}

// all is every pool, or the person's own when person is not "".
func (w *warmPools) all(ctx context.Context, person string) ([]storedPool, error) {
	keys, err := w.s.cfg.Store.List(ctx, warmPoolPrefix)
	if err != nil {
		return nil, err
	}
	var out []storedPool
	for _, k := range keys {
		id := strings.TrimSuffix(strings.TrimPrefix(k.Key, warmPoolPrefix), ".json")
		p, _, err := w.read(ctx, id)
		if err != nil {
			return nil, err
		}
		if p != nil && (person == "" || p.Owner == person) {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (w *warmPools) machines(p *storedPool, now time.Time) []poolMachine {
	out := make([]poolMachine, len(p.Machines))
	for i, m := range p.Machines {
		out[i] = poolMachine{m, w.backend.Stage(p.Provider, m.Workspace, m.MadeAt, now)}
	}
	return out
}

// wire is the pool as clients see it.
func (w *warmPools) wire(p *storedPool, now time.Time) wire.WarmPool {
	var ready, settingUp int64
	lines := []wire.WarmPoolMachine{}
	for _, m := range w.machines(p, now) {
		line := wire.WarmPoolMachine{Name: m.Name, MadeAt: wire.DateOf(m.MadeAt)}
		switch m.stage {
		case stageReady:
			ready++
			line.State = "ready"
		case stageSettingUp:
			settingUp++
			line.State = "setting-up"
			line.Detail = optional(w.backend.Detail(p.Provider, m.Workspace))
		default:
			line.State = "failed"
			line.Detail = optional(w.backend.Detail(p.Provider, m.Workspace))
		}
		lines = append(lines, line)
	}
	state := wire.WarmPoolStateKeeping
	switch {
	case p.Stopped != "":
		state = wire.WarmPoolStateStopped
	case p.MinReady == 0:
		state = wire.WarmPoolStateEmpty
	case !p.inHours(now):
		state = wire.WarmPoolStateOffHours
	}
	waiting := ""
	if state == wire.WarmPoolStateKeeping && int(ready+settingUp) < p.target() {
		waiting = w.backend.ProjectsWait(p.Owner, p.Projects)
	}
	return wire.WarmPool{
		WaitingBecause: optional(waiting),
		ID:             p.ID, Name: p.Name, Scope: p.Scope, Provider: p.Provider, Size: optional(p.Size), Region: optional(p.Region),
		Runtimes: p.Runtimes, Projects: p.Projects, MinReady: int64(p.MinReady), MaxSize: int64(p.MaxSize),
		Target: int64(p.target()), ReplaceAfter: optional(p.ReplaceAfter), Hours: p.Hours, State: state,
		Ready: optional(ready), SettingUp: optional(settingUp), FailedMakes: optional(int64(p.Failures)),
		StoppedBecause: optional(p.Stopped), Machines: lines, Claims: w.claims(p, now),
	}
}

// claims is the pool's last week of claims for its card (#5366): how many, how many found it
// empty, and the median time from a claim to the machine being ready over the latest that are.
func (w *warmPools) claims(p *storedPool, now time.Time) *wire.WarmPoolClaims {
	out := wire.WarmPoolClaims{}
	var times []float64
	for i := len(p.Claims) - 1; i >= 0; i-- {
		c := p.Claims[i]
		if now.Sub(c.At) > poolClaimsKept {
			continue
		}
		out.Count++
		if c.Empty {
			out.Empty++
			continue
		}
		if c.Host != "" && len(times) < poolClaimsTimed {
			if ready := w.backend.ReadyAt(c.Host); ready != nil && ready.After(c.At) {
				times = append(times, ready.Sub(c.At).Seconds())
			}
		}
	}
	if len(times) > 0 {
		sort.Float64s(times)
		median := times[len(times)/2]
		if len(times)%2 == 0 {
			median = (times[len(times)/2-1] + times[len(times)/2]) / 2
		}
		out.MedianSeconds = &median
	}
	return &out
}

// recordClaim keeps one claim on its pool, dropping those older than a week and past
// mostPoolClaims. A claim is given already: a pool that cannot keep the record only logs it.
func (w *warmPools) recordClaim(ctx context.Context, id string, c storedPoolClaim) {
	for range 3 {
		w.mu.Lock()
		p, tag, err := w.read(ctx, id)
		if err != nil || p == nil {
			w.mu.Unlock()
			return
		}
		kept := p.Claims[:0:0]
		for _, old := range p.Claims {
			if c.At.Sub(old.At) <= poolClaimsKept {
				kept = append(kept, old)
			}
		}
		kept = append(kept, c)
		if len(kept) > mostPoolClaims {
			kept = kept[len(kept)-mostPoolClaims:]
		}
		p.Claims = kept
		err = w.write(ctx, p, tag)
		w.mu.Unlock()
		if err == nil {
			return
		}
		if !isConflict(err) {
			w.s.log(fmt.Sprintf("warm pool %s: the claim was not kept: %v", p.Name, err))
			return
		}
	}
}

func poolID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var poolSlug = regexp.MustCompile(`[^a-z0-9]+`)

// machineName is a name for a new pool machine: letters, digits and dashes, starting with a
// letter, and not another machine's.
func machineName(pool string) string {
	slug := strings.Trim(poolSlug.ReplaceAllString(strings.ToLower(pool), "-"), "-")
	if slug == "" || slug[0] < 'a' || slug[0] > 'z' {
		slug = "pool-" + slug
	}
	if len(slug) > 24 {
		slug = strings.Trim(slug[:24], "-")
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return slug + "-" + hex.EncodeToString(b)
}

// reconcile runs one pass over a pool: it drops what the plan says, makes what it says, and
// keeps the outcome.
func (w *warmPools) reconcile(ctx context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, tag, err := w.read(ctx, id)
	if err != nil || p == nil {
		return err
	}
	now := time.Now()
	machines := w.machines(p, now)
	if p.SetAt == nil {
		p.SetAt = &now
	}
	if why, failed := w.failedInRow(p, machines); failed && p.Stopped == "" {
		p.Stopped = why
		w.backend.Stopped(p.Owner, p.Name, why)
	}
	plan := planPool(p, machines, now, w.backend.Version(), w.backend.Pins())
	if !w.backend.HasProvider(p.Provider) || plan.makes > 0 && w.backend.ProjectsWait(p.Owner, p.Projects) != "" {
		// With no make, an old machine is not deleted for its replacement either.
		if plan.makes > 0 && w.backend.HasProvider(p.Provider) {
			plan.deletes = nil
		}
		plan.makes = 0
	}
	gone := map[string]bool{}
	for _, i := range plan.deletes {
		m := machines[i]
		if err := w.backend.Delete(p.Owner, p.Provider, m.Workspace); err != nil {
			w.s.log(fmt.Sprintf("warm pool %s: delete %s: %v", p.Name, m.Name, err))
			continue
		}
		gone[m.Workspace] = true
	}
	kept := p.Machines[:0:0]
	for _, m := range p.Machines {
		if !gone[m.Workspace] {
			kept = append(kept, m)
		}
	}
	p.Machines = kept
	var making []string
	defer func() {
		for _, name := range making {
			w.nowMaking(name, nil)
		}
	}()
	for n := 0; n < plan.makes; n++ {
		name := machineName(p.Name)
		w.nowMaking(name, p)
		making = append(making, name)
		workspace, err := w.backend.Make(p.Owner, p.Provider, name, p.Size, p.Region)
		if err != nil {
			p.Failures++
			w.s.log(fmt.Sprintf("warm pool %s: make failed (%d in a row): %v", p.Name, p.Failures, err))
			if p.Failures >= poolFailuresStop {
				p.Stopped = errorText(err)
				w.backend.Stopped(p.Owner, p.Name, p.Stopped)
			}
			break
		}
		p.Failures = 0
		made := time.Now()
		p.LastMade = &made
		p.Machines = append(p.Machines, storedPoolMachine{Name: name, Workspace: workspace, MadeAt: made,
			Version: w.backend.Version(), Pins: w.backend.Pins()})
	}
	if err := w.write(ctx, p, tag); err != nil {
		if isConflict(err) {
			// Another write (a change of the pool) came between: the next pass sees it.
			return nil
		}
		return err
	}
	return nil
}

// reconcileAll runs a pass over every pool.
func (w *warmPools) reconcileAll(ctx context.Context) {
	pools, err := w.all(ctx, "")
	if err != nil {
		w.s.log("warm pool: " + err.Error())
		return
	}
	for _, p := range pools {
		if err := w.reconcile(ctx, p.ID); err != nil {
			w.s.log(fmt.Sprintf("warm pool %s: %v", p.Name, err))
		}
	}
}

// every runs a pass every poolEvery, until the service stops.
func (w *warmPools) every(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-w.s.stop:
			return
		case <-t.C:
			if w.s.phase.now() == phaseServing {
				w.reconcileAll(context.Background())
			}
		}
	}
}

func (w *warmPools) list(ctx context.Context, person string) (wire.WarmPools, error) {
	pools, err := w.all(ctx, person)
	if err != nil {
		return wire.WarmPools{}, fail(storeUnavailable, "The warm pools could not be read: %v", err)
	}
	now := time.Now()
	out := wire.WarmPools{Pools: []wire.WarmPool{}}
	for i := range pools {
		out.Pools = append(out.Pools, w.wire(&pools[i], now))
	}
	return out, nil
}

func validatePoolSet(set wire.WarmPoolSet) error {
	if strings.TrimSpace(set.Name) == "" {
		return fail(invalidParams, "A warm pool needs a name.")
	}
	if set.Scope != nil && *set.Scope != wire.WarmPoolScopeUser {
		return fail(invalidParams, "Only a pool of your own is built: a project or org pool comes later.")
	}
	if set.MaxSize < 1 {
		return fail(invalidParams, "Max size is at least 1.")
	}
	if set.MinReady < 0 {
		return fail(invalidParams, "Min ready is 0 or more.")
	}
	if set.MinReady > set.MaxSize {
		return fail(invalidParams, "Min ready (%d) is above max size (%d).", set.MinReady, set.MaxSize)
	}
	if set.ReplaceAfter != nil && *set.ReplaceAfter <= 0 {
		return fail(invalidParams, "Replace after is more than 0 seconds.")
	}
	if h := set.Hours; h != nil {
		if !hhmm.MatchString(h.From) || !hhmm.MatchString(h.To) {
			return fail(invalidParams, "Hours are from HH:MM to HH:MM.")
		}
		if len(h.Days) == 0 {
			return fail(invalidParams, "Hours name at least one day.")
		}
		for _, d := range h.Days {
			if d < 0 || d > 6 {
				return fail(invalidParams, "A day is 0 (Sunday) to 6 (Saturday).")
			}
		}
		if h.Timezone != nil && *h.Timezone != "" {
			if _, err := time.LoadLocation(*h.Timezone); err != nil {
				return fail(invalidParams, "There is no time zone called %s.", *h.Timezone)
			}
		}
	}
	return nil
}

// set is pool/set: it makes a pool or changes the person's own, and runs a pass at once.
func (w *warmPools) set(ctx context.Context, person string, set wire.WarmPoolSet) (wire.WarmPool, error) {
	if err := validatePoolSet(set); err != nil {
		return wire.WarmPool{}, err
	}
	if !w.backend.HasProvider(set.Provider) {
		return wire.WarmPool{}, fail(invalidParams, "There is no machine provider %q that can make machines.", set.Provider)
	}
	w.mu.Lock()
	var p *storedPool
	tag := ""
	if set.ID != nil && *set.ID != "" {
		var err error
		if p, tag, err = w.read(ctx, *set.ID); err != nil {
			w.mu.Unlock()
			return wire.WarmPool{}, fail(storeUnavailable, "The warm pool could not be read: %v", err)
		}
		if p == nil || p.Owner != person {
			w.mu.Unlock()
			return wire.WarmPool{}, fail(invalidParams, "There is no warm pool of yours with that id.")
		}
	} else {
		p = &storedPool{ID: poolID(), Owner: person, Scope: wire.WarmPoolScopeUser}
	}
	// A change is a fresh start: a pool that stopped on three failures tries again.
	p.Name, p.Provider, p.Size, p.Region = strings.TrimSpace(set.Name), set.Provider, deref(set.Size), deref(set.Region)
	p.Runtimes, p.Projects, p.Hours = set.Runtimes, set.Projects, set.Hours
	p.MinReady, p.MaxSize, p.ReplaceAfter = int(set.MinReady), int(set.MaxSize), deref(set.ReplaceAfter)
	now := time.Now()
	p.Failures, p.Stopped, p.SetAt = 0, "", &now
	err := w.write(ctx, p, tag)
	w.mu.Unlock()
	if err != nil {
		return wire.WarmPool{}, fail(storeUnavailable, "The warm pool could not be kept: %v", err)
	}
	if err := w.reconcile(ctx, p.ID); err != nil {
		w.s.log(fmt.Sprintf("warm pool %s: %v", p.Name, err))
	}
	if again, _, err := w.read(ctx, p.ID); err == nil && again != nil {
		p = again
	}
	return w.wire(p, time.Now()), nil
}

// delete is pool/delete: the pool and its machines that are not claimed.
func (w *warmPools) delete(ctx context.Context, person, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, _, err := w.read(ctx, id)
	if err != nil {
		return fail(storeUnavailable, "The warm pool could not be read: %v", err)
	}
	if p == nil || p.Owner != person {
		return fail(invalidParams, "There is no warm pool of yours with that id.")
	}
	var failed []string
	for _, m := range p.Machines {
		if err := w.backend.Delete(p.Owner, p.Provider, m.Workspace); err != nil {
			failed = append(failed, m.Name)
			w.s.log(fmt.Sprintf("warm pool %s: delete %s: %v", p.Name, m.Name, err))
		}
	}
	if len(failed) > 0 {
		return fail(internalError, "The pool was kept: %s could not be deleted. Try again.", strings.Join(failed, ", "))
	}
	return w.s.cfg.Store.Delete(ctx, warmPoolKey(id))
}

// claim is what a person asks of a pool (#5365): a machine of a provider and size, and the
// person's Stop when idle.
type claim struct {
	Provider     string
	Size         string
	Region       string
	StopWhenIdle *int
}

// claimed is the machine a claim gave. Host is "" for a new machine, which joins on its own
// first boot; FromPool says it came from a pool, and Note is what the machine's page says
// when it did not.
type claimed struct {
	Host      string
	Workspace string
	Name      string
	Pool      string
	FromPool  bool
	Note      string
}

// matchPools orders the person's pools that a claim matches: the provider and size are the
// pool's. A pool with a ready machine goes first, then the order the pools are in.
func matchPools(pools []storedPool, c claim, ready func(*storedPool) int) []storedPool {
	var with, without []storedPool
	for _, p := range pools {
		if p.Stopped != "" || p.Provider != c.Provider || p.Size != c.Size {
			continue
		}
		if ready(&p) > 0 {
			with = append(with, p)
		} else {
			without = append(without, p)
		}
	}
	return append(with, without...)
}

// takeMachine removes a machine from its pool in one write that holds only while the pool is
// as it was read, so two claims cannot take the same one (#5365). False when the machine is
// not the pool's any more; a conflict is an error to read again from.
func (w *warmPools) takeMachine(ctx context.Context, id, workspace string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, tag, err := w.read(ctx, id)
	if err != nil || p == nil {
		return false, err
	}
	kept := make([]storedPoolMachine, 0, len(p.Machines))
	found := false
	for _, m := range p.Machines {
		if m.Workspace == workspace {
			found = true
			continue
		}
		kept = append(kept, m)
	}
	if !found {
		return false, nil
	}
	p.Machines = kept
	return true, w.write(ctx, p, tag)
}

// oldestReady is the pool's oldest ready machine, if any.
func (w *warmPools) oldestReady(ctx context.Context, id string) (*storedPool, *storedPoolMachine) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, _, err := w.read(ctx, id)
	if err != nil || p == nil {
		return nil, nil
	}
	var best *storedPoolMachine
	for _, m := range w.machines(p, time.Now()) {
		if m.stage == stageReady && (best == nil || m.MadeAt.Before(best.MadeAt)) {
			m := m.storedPoolMachine
			best = &m
		}
	}
	return p, best
}

// claimFrom gives the person the oldest ready machine of one pool that answers, or says
// it had none. Each pass takes or deletes one machine, so the pool's size bounds the passes.
func (w *warmPools) claimFrom(ctx context.Context, person string, c claim, match storedPool, started time.Time) (claimed, bool, error) {
	for pass := 0; pass <= len(match.Machines); pass++ {
		p, m := w.oldestReady(ctx, match.ID)
		if m == nil {
			break
		}
		alive := w.backend.Ping(p.Provider, m.Workspace)
		took, err := w.takeMachine(ctx, p.ID, m.Workspace)
		if err != nil {
			if isConflict(err) {
				continue
			}
			return claimed{}, false, fail(storeUnavailable, "The warm pool could not be kept: %v", err)
		}
		if !took {
			continue
		}
		host, ok := w.bindTaken(person, c, p, m, alive)
		if !ok {
			continue
		}
		// The machine is the person's and never goes back. The pool makes another.
		w.recordClaim(ctx, p.ID, storedPoolClaim{At: started, Host: host})
		if err := w.reconcile(ctx, p.ID); err != nil {
			w.s.log(fmt.Sprintf("warm pool %s: %v", p.Name, err))
		}
		return claimed{Host: host, Workspace: m.Workspace, Name: m.Name, Pool: p.Name, FromPool: true}, true, nil
	}
	return claimed{}, false, nil
}

// bindTaken binds a machine taken from its pool to the person, and says its host. A
// machine that did not answer, or that could not be bound, is deleted instead.
func (w *warmPools) bindTaken(person string, c claim, p *storedPool, m *storedPoolMachine, alive bool) (string, bool) {
	if !alive {
		w.s.log(fmt.Sprintf("warm pool %s: %s did not answer a claim and is deleted", p.Name, m.Name))
		if err := w.backend.Delete(p.Owner, p.Provider, m.Workspace); err != nil {
			w.s.log(fmt.Sprintf("warm pool %s: delete %s: %v", p.Name, m.Name, err))
		}
		return "", false
	}
	host, err := w.backend.Bind(person, p.Provider, m.Workspace, c.StopWhenIdle, p.Projects)
	if err != nil {
		w.s.log(fmt.Sprintf("warm pool %s: %s could not be given: %v", p.Name, m.Name, err))
		if err := w.backend.Delete(p.Owner, p.Provider, m.Workspace); err != nil {
			w.s.log(fmt.Sprintf("warm pool %s: delete %s: %v", p.Name, m.Name, err))
		}
		return "", false
	}
	return host, true
}

// claim gives the person a machine (#5365, "A claim" of the spec): the oldest ready machine of
// a matching pool that answers, taken in one write, bound to the person, with the pool making
// its replacement. A machine that does not answer is deleted and the next is tried. With no
// ready machine it makes a new one and says so: a claim never waits on a pool.
func (w *warmPools) claim(ctx context.Context, person string, c claim) (claimed, error) {
	if !w.backend.HasProvider(c.Provider) {
		return claimed{}, fail(invalidParams, "There is no machine provider %q that can make machines.", c.Provider)
	}
	pools, err := w.all(ctx, person)
	if err != nil {
		return claimed{}, fail(storeUnavailable, "The warm pools could not be read: %v", err)
	}
	now := time.Now()
	started := now
	ready := func(p *storedPool) int {
		n := 0
		for _, m := range w.machines(p, now) {
			if m.stage == stageReady {
				n++
			}
		}
		return n
	}
	matches := matchPools(pools, c, ready)
	for _, match := range matches {
		got, ok, err := w.claimFrom(ctx, person, c, match, started)
		if err != nil || ok {
			return got, err
		}
	}
	// A pool that matched but had nothing ready counts the claim as one that found it empty.
	if len(matches) > 0 {
		w.recordClaim(ctx, matches[0].ID, storedPoolClaim{At: started, Empty: true})
	}
	name := machineName("machine")
	workspace, err := w.backend.Make(person, c.Provider, name, c.Size, c.Region)
	if err != nil {
		return claimed{}, fail(internalError, "No machine in a warm pool was ready, and a new one could not be made: %s", errorText(err))
	}
	return claimed{Workspace: workspace, Name: name,
		Note: "No warm pool machine was ready, so a new machine is being made."}, nil
}
