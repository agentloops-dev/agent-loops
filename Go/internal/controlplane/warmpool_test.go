package controlplane

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

var poolNow = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC) // a Monday

func poolOf(min, max int) *storedPool {
	return &storedPool{ID: "p", Owner: "alex", Name: "dev", Scope: wire.WarmPoolScopeUser, Provider: "do", MinReady: min, MaxSize: max}
}

// m is a machine made age ago.
func m(stage poolStage, age time.Duration) poolMachine {
	return poolMachine{storedPoolMachine{MadeAt: poolNow.Add(-age), Version: "v1"}, stage}
}

func plan(p *storedPool, ms ...poolMachine) poolPlan { return planPool(p, ms, poolNow, "v1", "") }

// #5363: the pool makes up to min ready, counting machines being set up, and never goes past max.
func TestPlanPoolMakesToMinWithinMax(t *testing.T) {
	t.Parallel()
	if got := plan(poolOf(3, 5)); got.makes != 3 || len(got.deletes) != 0 {
		t.Fatalf("an empty pool makes min: %+v", got)
	}
	if got := plan(poolOf(3, 5), m(stageReady, time.Hour), m(stageSettingUp, 0)); got.makes != 1 {
		t.Fatalf("setting up counts towards min: %+v", got)
	}
	// Max counts machines set up as well as ready; a failed one counts for neither.
	if got := plan(poolOf(3, 3), m(stageReady, 0), m(stageFailed, 0)); got.makes != 2 {
		t.Fatalf("a failed machine does not count: %+v", got)
	}
	if got := plan(poolOf(0, 3)); got.makes != 0 {
		t.Fatalf("min 0 makes nothing: %+v", got)
	}
}

// An old machine is replaced by making first when there is room, deleting once the new one is ready.
func TestPlanPoolMakesBeforeItDeletes(t *testing.T) {
	t.Parallel()
	old := m(stageReady, 25*time.Hour)
	got := plan(poolOf(1, 2), old)
	if got.makes != 1 || len(got.deletes) != 0 {
		t.Fatalf("room: make the new one first: %+v", got)
	}
	got = plan(poolOf(1, 2), old, m(stageSettingUp, 0))
	if got.makes != 0 || len(got.deletes) != 0 {
		t.Fatalf("the old one stays while the new one sets up: %+v", got)
	}
	got = plan(poolOf(1, 2), old, m(stageReady, time.Minute))
	if got.makes != 0 || len(got.deletes) != 1 || got.deletes[0] != 0 {
		t.Fatalf("the old one goes once a new one is ready: %+v", got)
	}
	// At max there is no room: the old one goes first, so the pool still refreshes.
	got = plan(poolOf(1, 1), old)
	if got.makes != 1 || len(got.deletes) != 1 {
		t.Fatalf("no room: delete then make: %+v", got)
	}
}

// A release or a runtime pin that changed makes ready machines old at once.
func TestPlanPoolReplacesOldSoftware(t *testing.T) {
	t.Parallel()
	ms := []poolMachine{m(stageReady, time.Minute)}
	if got := planPool(poolOf(1, 2), ms, poolNow, "v2", ""); got.makes != 1 {
		t.Fatalf("a newer Agents makes a new machine: %+v", got)
	}
	if got := planPool(poolOf(1, 2), ms, poolNow, "v1", "pins-2"); got.makes != 1 {
		t.Fatalf("a changed pin makes a new machine: %+v", got)
	}
}

// Lowering min or max drops the oldest machines.
func TestPlanPoolShrinks(t *testing.T) {
	t.Parallel()
	got := plan(poolOf(1, 3), m(stageReady, 3*time.Hour), m(stageReady, 2*time.Hour), m(stageReady, time.Hour))
	if got.makes != 0 || len(got.deletes) != 2 || got.deletes[0] != 0 || got.deletes[1] != 1 {
		t.Fatalf("the oldest go: %+v", got)
	}
	got = plan(poolOf(2, 2), m(stageReady, 3*time.Hour), m(stageReady, 2*time.Hour), m(stageReady, time.Hour), m(stageSettingUp, 0))
	if len(got.deletes) < 2 {
		t.Fatalf("max lowered to 2 holds 2: %+v", got)
	}
}

func TestPlanPoolHoursAndStop(t *testing.T) {
	t.Parallel()
	p := poolOf(2, 3)
	tz := "UTC"
	p.Hours = &wire.WarmPoolHours{Days: []int64{1, 2, 3, 4, 5}, From: "09:00", To: "17:00", Timezone: &tz}
	if got := planPool(p, []poolMachine{m(stageReady, 0)}, poolNow, "v1", ""); got.makes != 1 {
		t.Fatalf("in hours: %+v", got)
	}
	evening := poolNow.Add(8 * time.Hour)
	if got := planPool(p, []poolMachine{m(stageReady, 0)}, evening, "v1", ""); got.makes != 0 || len(got.deletes) != 1 {
		t.Fatalf("outside hours the ready ones go and none is made: %+v", got)
	}
	sunday := poolNow.Add(-24 * time.Hour)
	if p.inHours(sunday) {
		t.Fatal("a Sunday is not a day")
	}
	// Hours that cross midnight belong to the day they began.
	p.Hours = &wire.WarmPoolHours{Days: []int64{1}, From: "22:00", To: "06:00"}
	if !p.inHours(time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)) || p.inHours(time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)) {
		t.Fatal("Monday night runs to Tuesday morning only")
	}
	p.Hours = nil
	p.Stopped = "refused"
	if got := plan(p); got.makes != 0 {
		t.Fatalf("a stopped pool makes nothing: %+v", got)
	}
}

type fakePools struct {
	dead          map[string]bool
	bound         []string
	boundProjects [][]string
	made, deleted []string
	failMake      error
	stages        map[string]poolStage
	stopped       string
	wait          string
	readyAt       map[string]*time.Time
}

func (f *fakePools) Make(person, provider, name, size, region string) (string, error) {
	if f.failMake != nil {
		return "", f.failMake
	}
	f.made = append(f.made, name)
	return "ws-" + name, nil
}
func (f *fakePools) Delete(person, provider, workspace string) error {
	f.deleted = append(f.deleted, workspace)
	return nil
}
func (f *fakePools) Stage(provider, workspace string, madeAt, now time.Time) poolStage {
	return f.stages[workspace]
}
func (f *fakePools) HasProvider(string) bool { return true }
func (f *fakePools) Version() string         { return "v1" }
func (f *fakePools) Pins() string            { return "" }
func (f *fakePools) Stopped(person, pool, why string) {
	f.stopped = why
}

func testPools(t *testing.T) (*warmPools, *fakePools) {
	t.Helper()
	s := &Service{cfg: Config{Store: store.NewMemory(), Log: func(string) {}}, stop: make(chan struct{})}
	f := &fakePools{stages: map[string]poolStage{}, dead: map[string]bool{}, readyAt: map[string]*time.Time{}}
	return &warmPools{s: s, backend: f}, f
}

func TestPoolSetValidatesAndMakes(t *testing.T) {
	t.Parallel()
	w, f := testPools(t)
	ctx := context.Background()
	for _, set := range []wire.WarmPoolSet{
		{Name: "dev", Provider: "do", MinReady: 3, MaxSize: 2},
		{Name: "dev", Provider: "do", MinReady: 0, MaxSize: 0},
		{Name: " ", Provider: "do", MinReady: 1, MaxSize: 1},
		{Name: "dev", Provider: "do", MinReady: -1, MaxSize: 1},
	} {
		if _, err := w.set(ctx, "alex", set); err == nil {
			t.Fatalf("refused: %+v", set)
		}
	}
	org := wire.WarmPoolScopeOrg
	if _, err := w.set(ctx, "alex", wire.WarmPoolSet{Name: "dev", Provider: "do", MinReady: 1, MaxSize: 1, Scope: &org}); err == nil {
		t.Fatal("only user is built")
	}
	pool, err := w.set(ctx, "alex", wire.WarmPoolSet{Name: "dev", Provider: "do", MinReady: 2, MaxSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.made) != 2 || pool.Target != 2 || *pool.SettingUp != 2 || pool.State != wire.WarmPoolStateKeeping {
		t.Fatalf("made %v: %+v", f.made, pool)
	}
	// Machines ready, then a claim-free pass changes nothing.
	for _, n := range f.made {
		f.stages["ws-"+n] = stageReady
	}
	w.reconcileAll(ctx)
	if len(f.made) != 2 || len(f.deleted) != 0 {
		t.Fatalf("steady: made %v deleted %v", f.made, f.deleted)
	}
	// Someone else's pool is not reached.
	if _, err := w.list(ctx, "sam"); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.list(ctx, "sam"); len(got.Pools) != 0 {
		t.Fatal("pools are a person's own")
	}
	if err := w.delete(ctx, "sam", pool.ID); err == nil {
		t.Fatal("sam may not delete alex's pool")
	}
	if err := w.delete(ctx, "alex", pool.ID); err != nil || len(f.deleted) != 2 {
		t.Fatalf("delete takes its machines: %v %v", err, f.deleted)
	}
}

// Three makes that fail in a row stop the pool, with why; a change starts it again.
func TestPoolStopsAfterThreeFailedMakes(t *testing.T) {
	t.Parallel()
	w, f := testPools(t)
	ctx := context.Background()
	f.failMake = errors.New("DigitalOcean refused the size")
	pool, err := w.set(ctx, "alex", wire.WarmPoolSet{Name: "dev", Provider: "do", MinReady: 2, MaxSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	if pool.State == wire.WarmPoolStateStopped {
		t.Fatalf("one failure does not stop it: %+v", pool)
	}
	w.reconcileAll(ctx)
	w.reconcileAll(ctx)
	got, _ := w.list(ctx, "alex")
	p := got.Pools[0]
	if p.State != wire.WarmPoolStateStopped || p.StoppedBecause == nil || *p.StoppedBecause != "DigitalOcean refused the size" || f.stopped == "" {
		t.Fatalf("stopped with why: %+v", p)
	}
	before := fmt.Sprint(len(f.made))
	f.failMake = nil
	w.reconcileAll(ctx)
	if fmt.Sprint(len(f.made)) != before {
		t.Fatal("a stopped pool makes nothing")
	}
	pool, err = w.set(ctx, "alex", wire.WarmPoolSet{ID: &p.ID, Name: "dev", Provider: "do", MinReady: 2, MaxSize: 3})
	if err != nil || pool.State != wire.WarmPoolStateKeeping || len(f.made) != 2 {
		t.Fatalf("a change starts it again: %v %+v", err, pool)
	}
}

func (f *fakePools) Ping(provider, workspace string) bool { return !f.dead[workspace] }
func (f *fakePools) Detail(provider, workspace string) string {
	if f.stages[workspace] == stageSettingUp {
		return "installing Claude"
	}
	return ""
}
func (f *fakePools) ProjectsWait(string, []string) string { return f.wait }
func (f *fakePools) ReadyAt(host string) *time.Time       { return f.readyAt[host] }
func (f *fakePools) Bind(person, provider, workspace string, stop *int, projects []string) (string, error) {
	f.bound = append(f.bound, person+":"+workspace)
	f.boundProjects = append(f.boundProjects, projects)
	return "host-" + workspace, nil
}

// #5365: a claim takes the oldest ready machine that answers, deletes one that does not, binds
// it, and the pool makes a replacement; an empty pool falls back to a new machine.
func TestClaimTakesOldestReadyAndReplacesIt(t *testing.T) {
	t.Parallel()
	w, f := testPools(t)
	ctx := context.Background()
	pool, err := w.set(ctx, "alex", wire.WarmPoolSet{Name: "dev", Provider: "do", MinReady: 2, MaxSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	stored, tag, _ := w.read(ctx, pool.ID)
	for i := range stored.Machines {
		stored.Machines[i].MadeAt = poolNow.Add(time.Duration(i) * -time.Hour) // the last is the oldest
		f.stages[stored.Machines[i].Workspace] = stageReady
	}
	if err := w.write(ctx, stored, tag); err != nil {
		t.Fatal(err)
	}
	oldest, newer := stored.Machines[1].Workspace, stored.Machines[0].Workspace
	f.dead[oldest] = true
	got, err := w.claim(ctx, "alex", claim{Provider: "do"})
	if err != nil || !got.FromPool || got.Workspace != newer {
		t.Fatalf("the oldest did not answer, so the next is taken: %+v %v", got, err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != oldest || len(f.bound) != 1 {
		t.Fatalf("deleted %v bound %v", f.deleted, f.bound)
	}
	after, _, _ := w.read(ctx, pool.ID)
	if len(after.Machines) != 2 || after.Machines[0].Workspace == newer || after.Machines[1].Workspace == newer {
		t.Fatalf("a claimed machine leaves the pool and a new one is made: %+v", after.Machines)
	}
	// The pool's one machine is still setting up: the claim falls back to a new machine.
	got, err = w.claim(ctx, "alex", claim{Provider: "do"})
	if err != nil || got.FromPool || got.Note == "" || got.Host != "" {
		t.Fatalf("an empty pool falls back and says so: %+v %v", got, err)
	}
	// Another provider or size matches no pool.
	if got, _ = w.claim(ctx, "alex", claim{Provider: "do", Size: "big"}); got.FromPool {
		t.Fatal("size must match")
	}
}

// #6890: a claim hands the pool's projects to the bind, which the machine's step 6
// brings up to date.
func TestClaimBindsWithThePoolsProjects(t *testing.T) {
	t.Parallel()
	w, f := testPools(t)
	ctx := context.Background()
	pool, err := w.set(ctx, "alex", wire.WarmPoolSet{Name: "dev", Provider: "do", MinReady: 1, MaxSize: 2,
		Projects: []string{"/Users/alex/Agents"}})
	if err != nil {
		t.Fatal(err)
	}
	stored, _, _ := w.read(ctx, pool.ID)
	f.stages[stored.Machines[0].Workspace] = stageReady
	got, err := w.claim(ctx, "alex", claim{Provider: "do"})
	if err != nil || !got.FromPool {
		t.Fatalf("claim: %+v %v", got, err)
	}
	if len(f.boundProjects) != 1 || len(f.boundProjects[0]) != 1 || f.boundProjects[0][0] != "/Users/alex/Agents" {
		t.Fatalf("the bind gets the pool's projects: %v", f.boundProjects)
	}
}

// #5366: a pool's card lists its machines with what each is doing, and its last week of claims:
// how many, how many found it empty, and the median time to ready.
func TestPoolCardHasMachinesAndClaims(t *testing.T) {
	t.Parallel()
	w, f := testPools(t)
	ctx := context.Background()
	pool, err := w.set(ctx, "alex", wire.WarmPoolSet{Name: "dev", Provider: "do", MinReady: 1, MaxSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(pool.Machines) != 1 || pool.Machines[0].State != "setting-up" || pool.Machines[0].Detail == nil ||
		*pool.Machines[0].Detail != "installing Claude" {
		t.Fatalf("a machine setting up says what it does: %+v", pool.Machines)
	}
	// Nothing ready: the claim falls back and counts as one that found the pool empty.
	if got, err := w.claim(ctx, "alex", claim{Provider: "do"}); err != nil || got.FromPool {
		t.Fatalf("%+v %v", got, err)
	}
	f.stages["ws-"+f.made[0]] = stageReady
	got, err := w.claim(ctx, "alex", claim{Provider: "do"})
	if err != nil || !got.FromPool {
		t.Fatalf("%+v %v", got, err)
	}
	ready := time.Now().Add(20 * time.Second)
	f.readyAt[got.Host] = &ready
	list, _ := w.list(ctx, "alex")
	c := list.Pools[0].Claims
	if c == nil || c.Count != 2 || c.Empty != 1 || c.MedianSeconds == nil || *c.MedianSeconds < 15 || *c.MedianSeconds > 25 {
		t.Fatalf("two claims, one empty, about 20 s to ready: %+v", c)
	}
	// A claim older than a week is dropped from the card.
	stored, tag, _ := w.read(ctx, pool.ID)
	stored.Claims[0].At = time.Now().Add(-8 * 24 * time.Hour)
	if err := w.write(ctx, stored, tag); err != nil {
		t.Fatal(err)
	}
	list, _ = w.list(ctx, "alex")
	if c := list.Pools[0].Claims; c.Count != 1 || c.Empty != 0 {
		t.Fatalf("the old claim is out: %+v", c)
	}
}
