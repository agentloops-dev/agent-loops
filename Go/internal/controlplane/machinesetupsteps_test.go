package controlplane

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// stepIDs is a set-up's steps, by id, in the order it keeps them.
func stepIDs(st *storedSetup) []wire.MachineSetupStepID {
	var out []wire.MachineSetupStepID
	for _, s := range st.Steps {
		out = append(out, s.ID)
	}
	return out
}

// runSteps makes each step running, then done, and fails the test on a refusal.
func runSteps(t *testing.T, st *storedSetup, now time.Time, ids ...wire.MachineSetupStepID) {
	t.Helper()
	for _, id := range ids {
		for _, state := range []wire.MachineSetupState{wire.MachineSetupStateRunning, wire.MachineSetupStateDone} {
			if _, _, err := st.report(rep(id, state), now); err != nil {
				t.Fatalf("%s %s: %v", id, state, err)
			}
		}
	}
}

// #7167: a set-up kept before root set-up and your set-up, of exactly eight steps, reads as it
// did, and one that this build cannot read is none.
func TestAnEightStepSetUpFromBeforeReadsAsItDid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	const old = `{"steps":[` +
		`{"id":"create","state":"done","startedAt":"2026-11-01T10:00:00Z","endedAt":"2026-11-01T10:00:30Z","detail":"Made"},` +
		`{"id":"join","state":"done","startedAt":"2026-11-01T10:00:30Z","endedAt":"2026-11-01T10:01:00Z"},` +
		`{"id":"install","state":"done","startedAt":"2026-11-01T10:01:00Z","endedAt":"2026-11-01T10:02:00Z"},` +
		`{"id":"compare","state":"done","startedAt":"2026-11-01T10:02:00Z","endedAt":"2026-11-01T10:02:01Z","detail":"Both show 123456"},` +
		`{"id":"runtimes","state":"done","startedAt":"2026-11-01T10:02:01Z","endedAt":"2026-11-01T10:03:00Z"},` +
		`{"id":"projects","state":"running","startedAt":"2026-11-01T10:02:01Z","detail":"app"},` +
		`{"id":"signIn","state":"waiting"},{"id":"smoke","state":"waiting"}],` +
		`"startedAt":"2026-11-01T10:00:00Z","expectedCode":"123456"}`
	put := func(host, data string) {
		t.Helper()
		if _, err := p.s.cfg.Store.Put(ctx, machineSetupKey(host), []byte(data), store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	put("old", old)
	stored, _, err := p.s.readMachineSetup(ctx, "old")
	if err != nil || stored == nil {
		t.Fatalf("an eight-step set-up from before does not read: %v", err)
	}
	if !slices.Equal(stepIDs(stored), setupSteps) || stored.Expect != "123456" || stored.Steps[5].Detail != "app" {
		t.Fatalf("%+v", stored)
	}
	// It goes on as it did: the projects end, then sign-in and smoke, and it is ready.
	now := time.Now()
	if _, _, err := stored.report(rep(wire.MachineSetupStepIDProjects, wire.MachineSetupStateDone), now); err != nil {
		t.Fatal(err)
	}
	runSteps(t, stored, now, wire.MachineSetupStepIDSignIn, wire.MachineSetupStepIDSmoke)
	if stored.Ready == nil || len(stored.Steps) != 8 {
		t.Fatalf("not ready: %+v", stored)
	}
	// Written again, it is the record it was, with the steps it had.
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var again storedSetup
	if err := json.Unmarshal(data, &again); err != nil || !again.readable() || !slices.Equal(stepIDs(&again), setupSteps) {
		t.Fatalf("%v %s", err, data)
	}

	for name, data := range map[string]string{
		"unknown": `{"steps":[{"id":"create","state":"done"},{"id":"later","state":"waiting"}],"startedAt":"2026-11-01T10:00:00Z"}`,
		"twice":   `{"steps":[{"id":"create","state":"done"},{"id":"create","state":"done"}],"startedAt":"2026-11-01T10:00:00Z"}`,
		"short":   `{"steps":[{"id":"create","state":"done"},{"id":"join","state":"done"}],"startedAt":"2026-11-01T10:00:00Z"}`,
		"notJSON": `{"steps":`,
		"noSteps": `{"startedAt":"2026-11-01T10:00:00Z"}`,
	} {
		put(name, data)
		if stored, _, err := p.s.readMachineSetup(ctx, name); err != nil || stored != nil {
			t.Errorf("%s reads: %+v %v", name, stored, err)
		}
	}
}

// #7167: root set-up and your set-up come when the host says them, each in its place, and each
// waits for its steps: root for compare, yours for root, the projects for yours, and sign-in
// and smoke for both. Runtimes do not wait for either.
func TestRootAndYourSetUpWaitForTheirSteps(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := newStoredSetup(now)
	runSteps(t, st, now, setupSteps[:4]...)
	hash := "ab12"
	root := rep(wire.MachineSetupStepIDRoot, wire.MachineSetupStateRunning)
	root.Hash = &hash
	if _, _, err := st.report(root, now); err != nil {
		t.Fatal(err)
	}
	runSteps(t, st, now, wire.MachineSetupStepIDRuntimes)
	if _, _, err := st.report(rep(wire.MachineSetupStepIDUser, wire.MachineSetupStateRunning), now); err == nil {
		t.Fatal("your set-up began before root set-up was done")
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDRoot, wire.MachineSetupStateDone), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDUser, wire.MachineSetupStateRunning), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDProjects, wire.MachineSetupStateRunning), now); err == nil {
		t.Fatal("the projects began before your set-up was done")
	}
	runSteps(t, st, now, wire.MachineSetupStepIDUser, wire.MachineSetupStepIDProjects)
	want := []wire.MachineSetupStepID{wire.MachineSetupStepIDCreate, wire.MachineSetupStepIDJoin, wire.MachineSetupStepIDInstall,
		wire.MachineSetupStepIDCompare, wire.MachineSetupStepIDRoot, wire.MachineSetupStepIDRuntimes, wire.MachineSetupStepIDUser,
		wire.MachineSetupStepIDProjects, wire.MachineSetupStepIDSignIn, wire.MachineSetupStepIDSmoke}
	if !slices.Equal(stepIDs(st), want) || st.Ready != nil {
		t.Fatalf("%v %v", stepIDs(st), st.Ready)
	}
	runSteps(t, st, now, wire.MachineSetupStepIDSignIn, wire.MachineSetupStepIDSmoke)
	if st.Ready == nil {
		t.Fatal("not ready when every step is done")
	}
	row := st.wire(now, nil).Steps[4]
	if row.ID != wire.MachineSetupStepIDRoot || row.Hash == nil || *row.Hash != hash {
		t.Fatalf("root's row: %+v", row)
	}

	// A step said after one that waits for it began came too late to run first.
	late := newStoredSetup(now)
	runSteps(t, late, now, setupSteps[:4]...)
	runSteps(t, late, now, wire.MachineSetupStepIDProjects)
	if _, _, err := late.report(rep(wire.MachineSetupStepIDUser, wire.MachineSetupStateRunning), now); err == nil {
		t.Fatal("your set-up was taken after the projects")
	}
	if len(late.Steps) != 8 {
		t.Fatalf("a refused step was kept: %v", stepIDs(late))
	}
	warning := true
	if _, _, err := late.report(wire.MachineSetupReport{Step: wire.MachineSetupStepIDRoot, State: wire.MachineSetupStateDone, Warning: &warning}, now); err != nil {
		t.Fatal(err)
	}
	if row := late.wire(now, nil).Steps[4]; row.Warning == nil || !*row.Warning {
		t.Fatalf("no warning: %+v", row)
	}
}

// #7167: a step that runs beside one that failed still says that it ended; one that had not
// begun waits for Try Again.
func TestAStepBesideAFailedOneStillEnds(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := newStoredSetup(now)
	runSteps(t, st, now, setupSteps[:4]...)
	for _, id := range []wire.MachineSetupStepID{wire.MachineSetupStepIDRoot, wire.MachineSetupStepIDRuntimes} {
		if _, _, err := st.report(rep(id, wire.MachineSetupStateRunning), now); err != nil {
			t.Fatal(err)
		}
	}
	why := "root.sh exited 1"
	if _, _, err := st.report(wire.MachineSetupReport{Step: wire.MachineSetupStepIDRoot, State: wire.MachineSetupStateFailed, Error: &why}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDRuntimes, wire.MachineSetupStateDone), now); err != nil {
		t.Fatalf("the runtimes could not end after root set-up failed: %v", err)
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDProjects, wire.MachineSetupStateRunning), now); err == nil {
		t.Fatal("a step began after one failed")
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDProjects, wire.MachineSetupStateDone), now); err == nil {
		t.Fatal("a step that had not begun ended after one failed")
	}
	if !st.retry() || st.step(wire.MachineSetupStepIDRoot).State != wire.MachineSetupStateWaiting {
		t.Fatalf("Try Again: %+v", st.Steps)
	}
}

// #7167: a warm pool's machine is ready only once root set-up is done, its claim keeps root set-up
// as the pool's, and your set-up runs after the claim, after the projects.
func TestAPoolMachineNeedsRootSetUpAndItsClaimKeepsIt(t *testing.T) {
	t.Parallel()
	p, _, made, pool := poolPlane(t)
	ctx := context.Background()
	b := poolsBackend{p.s}
	stage := func() poolStage { return b.Stage("do-1", "ws-1", pool.Machines[0].MadeAt, time.Now()) }
	must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDRoot, wire.MachineSetupStateRunning)))
	for _, state := range []wire.MachineSetupState{wire.MachineSetupStateRunning, wire.MachineSetupStateDone} {
		must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDProjects, state)))
	}
	if stage() != stageSettingUp {
		t.Fatal("ready before root set-up was done")
	}
	must(t, p.s.setupStep(ctx, made, rep(wire.MachineSetupStepIDRoot, wire.MachineSetupStateDone)))
	if stage() != stageReady {
		t.Fatal("not ready with root set-up and its projects")
	}
	if _, err := b.Bind(p.s.localOwner(), "do-1", "ws-1", nil, pool.Projects); err != nil {
		t.Fatal(err)
	}
	stored, _, err := p.s.readMachineSetup(ctx, made)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if root := stored.step(wire.MachineSetupStepIDRoot); root.State != wire.MachineSetupStateDone || !root.FromPool {
		t.Fatalf("the claim did not keep root set-up: %+v", root)
	}
	if next := stored.next(); next == nil || *next != wire.MachineSetupStepIDProjects {
		t.Fatalf("next: %v", next)
	}
	for _, id := range []wire.MachineSetupStepID{wire.MachineSetupStepIDProjects, wire.MachineSetupStepIDUser,
		wire.MachineSetupStepIDSignIn, wire.MachineSetupStepIDSmoke} {
		for _, state := range []wire.MachineSetupState{wire.MachineSetupStateRunning, wire.MachineSetupStateDone} {
			must(t, p.s.setupStep(ctx, made, rep(id, state)))
		}
	}
	stored, _, _ = p.s.readMachineSetup(ctx, made)
	ids := stepIDs(stored)
	if stored.Ready == nil || slices.Index(ids, wire.MachineSetupStepIDUser) != slices.Index(ids, wire.MachineSetupStepIDProjects)+1 {
		t.Fatalf("%v %v", ids, stored.Ready)
	}
}

// #7154: a Root set-up that the make says with a warning (a provider that can't run root.sh, or
// no lock) is done once Compare digits is, with its words; one the provider runs waits for the
// machine to say it.
func TestAWarnedRootSetUpIsDoneOnceCompareDigitsIs(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, warned := range []bool{true, false} {
		st := newStoredSetup(now)
		runSteps(t, st, now, wire.MachineSetupStepIDCreate)
		words := "Codespaces can’t run root set-up. Put it in the image."
		root := wire.MachineSetupReport{Step: wire.MachineSetupStepIDRoot, State: wire.MachineSetupStateWaiting, Detail: &words}
		if warned {
			root.Warning = &warned
		}
		if _, _, err := st.report(root, now); err != nil {
			t.Fatal(err)
		}
		runSteps(t, st, now, wire.MachineSetupStepIDJoin, wire.MachineSetupStepIDInstall)
		if got := st.step(wire.MachineSetupStepIDRoot).State; got != wire.MachineSetupStateWaiting {
			t.Fatalf("warned %v: root set-up is %s before Compare digits", warned, got)
		}
		runSteps(t, st, now, wire.MachineSetupStepIDCompare)
		got := st.step(wire.MachineSetupStepIDRoot)
		if warned && (got.State != wire.MachineSetupStateDone || got.Detail != words || !got.Warning) ||
			!warned && got.State != wire.MachineSetupStateWaiting {
			t.Errorf("warned %v: %+v", warned, got)
		}
	}
}
