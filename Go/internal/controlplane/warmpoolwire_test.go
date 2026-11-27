package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// poolPlane is a control plane with a provider, a machine of the person that has /Users/a/app
// in a project, and a pool with one machine that has joined.
func poolPlane(t *testing.T) (p *plane, mac *fakeHost, made string, pool *storedPool) {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemory()
	record := `{"id":"do-1","name":"DigitalOcean","kind":"commands","commands":{"preset":"doctl"},"secretNames":[],` +
		`"capabilities":["create"],"contract":1,"rev":1}`
	if _, err := st.Put(ctx, providers.ProviderKey("do-1"), []byte(record), store.Absent); err != nil {
		t.Fatal(err)
	}
	p = startPlaneWith(t, st, controllink.NewPrivateKey(), func(c *Config) { c.ProviderHome = t.TempDir() })
	mac = p.host("mac")
	folders, _ := json.Marshal(storedProjectFolders{Folders: []wire.JoinedFolder{{Folder: "file:///Users/a/app/", Name: "app", Project: "prj-app"}}})
	if _, err := st.Put(ctx, projectFoldersKey(mac.member.Host), folders, store.Absent); err != nil {
		t.Fatal(err)
	}
	made = "poolhost6890"
	shown, err := p.s.Codes().Issue(withMadeMachine(ctx, made), true, false, testCodeLife)
	if err != nil {
		t.Fatal(err)
	}
	code, err := controllink.ParseCode(shown.Text)
	if err != nil {
		t.Fatal(err)
	}
	p.s.madeStep(made, rep(wire.MachineSetupStepIDCreate, wire.MachineSetupStateRunning))
	p.s.madeFor(made, "do-1", "ws-1")
	pool = &storedPool{ID: "pool1", Owner: p.s.localOwner(), Name: "dev", Provider: "do-1", MinReady: 1, MaxSize: 1,
		Projects: []string{"/Users/a/app"},
		Machines: []storedPoolMachine{{Name: "dev-1", Workspace: "ws-1", MadeAt: time.Now(), Version: p.s.cfg.Version}}}
	if err := p.s.pools.write(ctx, pool, ""); err != nil {
		t.Fatal(err)
	}
	if host, _, _ := p.rawHostWith("dev-1", code); host != made {
		t.Fatalf("joined as %q", host)
	}
	waitFor(t, func() bool { return p.s.providers.PoolHost("do-1", "ws-1") != nil })
	must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDInstall, wire.MachineSetupStateDone)))
	digits := code.Digits()
	compare := rep(wire.MachineSetupStepIDCompare, wire.MachineSetupStateDone)
	compare.Code = &digits
	must(t, p.s.setupStep(ctx, made, compare))
	must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDRuntimes, wire.MachineSetupStateDone)))
	return p, mac, made, pool
}

// #6890, #6896: over the real backend. A pool machine is told the pool's projects and who has
// them, is ready only once it has them, and after the claim steps 6 to 8 run and end.
func TestAPoolMachineIsReadyWithItsProjectsAndAClaimRunsTheRest(t *testing.T) {
	t.Parallel()
	p, mac, made, pool := poolPlane(t)
	ctx := context.Background()
	b := poolsBackend{p.s}
	stage := func() poolStage { return b.Stage("do-1", "ws-1", pool.Machines[0].MadeAt, time.Now()) }
	ask := func() wire.MachineSetupWork {
		t.Helper()
		got, ok, err := p.s.methods.hostReported(ctx, made, wire.CallMachineSetupWork.Name, json.RawMessage(`{}`))
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		return got.(wire.MachineSetupWork)
	}
	work := ask()
	if !work.Pool || work.Claimed || work.Done || work.From != nil || len(work.Projects) != 1 {
		t.Fatalf("%+v", work)
	}
	if got := work.Projects[0]; got.Path != "/Users/a/app" || *got.Project != "prj-app" || len(got.Sources) != 1 ||
		string(got.Sources[0].Machine) != mac.member.Host || !got.Sources[0].Online {
		t.Fatalf("%+v", got)
	}
	if stage() != stageSettingUp {
		t.Fatal("ready with no projects")
	}
	must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDProjects, wire.MachineSetupStateRunning)))
	if stage() != stageSettingUp {
		t.Fatal("ready while the projects come")
	}
	must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDProjects, wire.MachineSetupStateDone)))
	if stage() != stageReady {
		t.Fatal("not ready with its projects")
	}

	host, err := b.Bind(p.s.localOwner(), "do-1", "ws-1", nil, pool.Projects)
	if err != nil || host != made {
		t.Fatal(host, err)
	}
	// The claim took it out of its pool.
	if _, err := p.s.pools.takeMachine(ctx, pool.ID, "ws-1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range setupSteps[5:] {
		if work = ask(); !work.Claimed || work.Done || work.From == nil || *work.From != id || len(work.Projects) != 1 {
			t.Fatalf("%s: %+v", id, work)
		}
		must(t, p.s.setupStep(ctx, made, rep(id, wire.MachineSetupStateRunning)))
		if id == wire.MachineSetupStepIDProjects {
			failAndTryAgain(t, p, made, ask)
		}
		must(t, p.s.setupStep(ctx, made, rep(id, wire.MachineSetupStateDone)))
	}
	if work = ask(); !work.Done || b.ReadyAt(made) == nil {
		t.Fatalf("the claimed machine is not ready: %+v", work)
	}
	for i, step := range p.s.machineSetup(ctx, made).Steps {
		if step.State != wire.MachineSetupStateDone || (i < 5) != (step.FromPool != nil && *step.FromPool) {
			t.Fatalf("step %s: %+v", step.ID, step)
		}
	}
	// A machine no pool made has nothing to ask for.
	if got, err := p.s.setupWork(ctx, mac.member.Host); err != nil || got.Pool || !got.Done {
		t.Fatalf("%+v %v", got, err)
	}
}

// failAndTryAgain: a failed step 6 waits for Try Again, and then runs again.
func failAndTryAgain(t *testing.T, p *plane, made string, ask func() wire.MachineSetupWork) {
	t.Helper()
	ctx, id := context.Background(), wire.MachineSetupStepIDProjects
	why := "app: Mac is offline"
	must(t, p.s.setupStep(ctx, made, wire.MachineSetupReport{Step: id, State: wire.MachineSetupStateFailed, Error: &why}))
	if work := ask(); work.From != nil {
		t.Fatalf("a failed step runs again by itself: %+v", work)
	}
	must(t, p.s.retrySetup(ctx, made))
	if work := ask(); work.From == nil || *work.From != id {
		t.Fatalf("Try Again: %+v", work)
	}
	must(t, p.s.setupStep(ctx, made, rep(id, wire.MachineSetupStateRunning)))
}

// #6890: a project that did not come fails step 6 and the machine, with the reason.
func TestAPoolMachineWhoseProjectsDidNotComeFailed(t *testing.T) {
	t.Parallel()
	p, _, made, pool := poolPlane(t)
	ctx := context.Background()
	why := "app: Mac is offline"
	must(t, p.s.setupStep(ctx, made, wire.MachineSetupReport{Step: wire.MachineSetupStepIDProjects, State: wire.MachineSetupStateFailed, Error: &why}))
	b := poolsBackend{p.s}
	if b.Stage("do-1", "ws-1", pool.Machines[0].MadeAt, time.Now()) != stageFailed || b.Detail("do-1", "ws-1") != why {
		t.Fatal(b.Detail("do-1", "ws-1"))
	}
}

// #6890: a machine that joins before its pool keeps it is the pool's all the same.
func TestAMachineBeingMadeIsItsPools(t *testing.T) {
	t.Parallel()
	p, mac, _, pool := poolPlane(t)
	h := p.s.records.Host(mac.member.Host)
	p.s.pools.nowMaking(h.Name, pool)
	if got, err := p.s.pools.poolOf(context.Background(), h, nil); err != nil || got == nil || got.ID != pool.ID {
		t.Fatal(got, err)
	}
	p.s.pools.nowMaking(h.Name, nil)
	if got, err := p.s.pools.poolOf(context.Background(), h, nil); err != nil || got != nil {
		t.Fatal(got, err)
	}
}

// #6890: no machine is made, or old one deleted, while no machine that has the projects is online.
func TestAPoolWaitsForAMachineThatHasItsProjects(t *testing.T) {
	t.Parallel()
	p, mac, _, pool := poolPlane(t)
	b := poolsBackend{p.s}
	if why := b.ProjectsWait(pool.Owner, pool.Projects); why != "" {
		t.Fatal(why)
	}
	mac.stop()
	p.waitFor(func() bool { return !p.s.providers.PoolOnline(mac.member.Host) }, "the Mac offline")
	if why := b.ProjectsWait(pool.Owner, pool.Projects); !strings.HasSuffix(why, " is offline, and it has app") {
		t.Fatal(why)
	}
	if why := b.ProjectsWait(pool.Owner, []string{"/Users/a/other"}); why != "No machine of yours has /Users/a/other in a project" {
		t.Fatal(why)
	}
	if why := b.ProjectsWait(pool.Owner, nil); why != "" {
		t.Fatal(why)
	}

	w, f := testPools(t)
	f.wait = "Mac is offline, and it has app"
	got, err := w.set(context.Background(), "alex", wire.WarmPoolSet{Name: "dev", Provider: "do", MinReady: 1, MaxSize: 2})
	if err != nil || len(f.made) != 0 || got.WaitingBecause == nil || *got.WaitingBecause != f.wait {
		t.Fatalf("%+v %v %v", got, f.made, err)
	}
	f.wait = ""
	w.reconcileAll(context.Background())
	if len(f.made) != 1 {
		t.Fatal(f.made)
	}
}

// #6890: a pool whose machines fail to set themselves up stops, and does not make one after
// another; a change starts it again.
func TestAPoolStopsAfterThreeFailedSetUps(t *testing.T) {
	t.Parallel()
	w, f := testPools(t)
	ctx := context.Background()
	set := wire.WarmPoolSet{Name: "dev", Provider: "do", MinReady: 1, MaxSize: 2}
	pool, err := w.set(ctx, "alex", set)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		for _, n := range f.made {
			f.stages["ws-"+n] = stageFailed
		}
		w.reconcileAll(ctx)
	}
	if len(f.made) != 3 || !strings.HasPrefix(f.stopped, "three machines in a row were not set up") {
		t.Fatalf("made %d, stopped %q", len(f.made), f.stopped)
	}
	set.ID = &pool.ID
	if pool, err = w.set(ctx, "alex", set); err != nil || len(f.made) != 4 || pool.State != wire.WarmPoolStateKeeping {
		t.Fatalf("%+v %v made %d", pool, err, len(f.made))
	}
}

// #6890: a pool machine asks for its work before the control plane has linked it to the
// workspace it was made as. It must hear of its projects, not that nothing is left.
func TestAPoolMachineThatIsNotLinkedYetHasItsProjects(t *testing.T) {
	t.Parallel()
	p, _, made, _ := poolPlane(t)
	ctx := context.Background()
	record := p.s.records.Host(made)
	record.Workspace = nil
	must(t, p.s.records.SaveHost(ctx, *record))
	work, err := p.s.setupWork(ctx, made)
	if err != nil {
		t.Fatal(err)
	}
	if !work.Pool || work.Done || len(work.Projects) != 1 {
		t.Fatalf("%+v", work)
	}
}

// #7546: a failed Your set-up ends the host's wait: machine/setupWork says userFailed, not
// waitUser, and gives no projects to get. When it runs again, the host waits for it again.
func TestAFailedYourSetUpStopsTheProjects(t *testing.T) {
	t.Parallel()
	p, _, made, _ := poolPlane(t)
	ctx := context.Background()
	for _, state := range []wire.MachineSetupState{wire.MachineSetupStateRunning, wire.MachineSetupStateFailed} {
		must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDUser, state)))
	}
	work, err := p.s.setupWork(ctx, made)
	if err != nil {
		t.Fatal(err)
	}
	if work.UserFailed == nil || !*work.UserFailed || work.WaitUser != nil || len(work.Projects) != 0 {
		t.Fatalf("%+v", work)
	}
	stored, _, _ := p.s.readMachineSetup(ctx, made)
	if projects := stored.step(wire.MachineSetupStepIDProjects); projects.State != wire.MachineSetupStateWaiting || projects.Detail != notStartedWords {
		t.Fatalf("%+v", projects)
	}
	must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDUser, wire.MachineSetupStateRunning)))
	if work, err = p.s.setupWork(ctx, made); err != nil || work.WaitUser == nil || !*work.WaitUser || work.UserFailed != nil {
		t.Fatalf("%+v %v", work, err)
	}
}
