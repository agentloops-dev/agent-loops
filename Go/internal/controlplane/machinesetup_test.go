package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func rep(step wire.MachineSetupStepID, state wire.MachineSetupState) wire.MachineSetupReport {
	return wire.MachineSetupReport{Step: step, State: state}
}

// #5355: the eight steps run in order, 5 and 6 together; a failure stops the run, keeps what
// is done, and Try Again runs the failed step and the ones after it.
func TestMachineSetupStepsRunInOrderAndRetry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := newStoredSetup(now)
	if len(st.Steps) != 8 {
		t.Fatalf("eight steps: %d", len(st.Steps))
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDJoin, wire.MachineSetupStateRunning), now); err == nil {
		t.Fatal("step 2 cannot begin before step 1 is done")
	}
	for i, id := range setupSteps[:4] {
		at := now.Add(time.Duration(i) * time.Second)
		if _, _, err := st.report(rep(id, wire.MachineSetupStateRunning), at); err != nil {
			t.Fatal(err)
		}
		took, finished, err := st.report(rep(id, wire.MachineSetupStateDone), at.Add(time.Second))
		if err != nil || !finished || took != time.Second {
			t.Fatalf("%s: %v %v %v", id, took, finished, err)
		}
	}
	// Runtimes and projects together; signing in waits for both.
	for _, id := range []wire.MachineSetupStepID{wire.MachineSetupStepIDRuntimes, wire.MachineSetupStepIDProjects} {
		if _, _, err := st.report(rep(id, wire.MachineSetupStateRunning), now); err != nil {
			t.Fatalf("%s together: %v", id, err)
		}
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDRuntimes, wire.MachineSetupStateDone), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDSignIn, wire.MachineSetupStateRunning), now); err == nil {
		t.Fatal("signing in waits for projects")
	}
	msg := "script exited 1"
	if _, _, err := st.report(wire.MachineSetupReport{Step: wire.MachineSetupStepIDProjects, State: wire.MachineSetupStateFailed, Error: &msg}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDSignIn, wire.MachineSetupStateRunning), now); err == nil {
		t.Fatal("a failed run stops")
	}
	if !st.retry() {
		t.Fatal("a failed step is retried")
	}
	if st.Steps[4].State != wire.MachineSetupStateDone || st.Steps[5].State != wire.MachineSetupStateWaiting || st.Steps[5].Error != "" {
		t.Fatalf("steps before the failed one stay done; it and the rest wait: %+v", st.Steps)
	}
	if st.retry() {
		t.Fatal("nothing failed")
	}
	for _, id := range setupSteps[5:] {
		for _, state := range []wire.MachineSetupState{wire.MachineSetupStateRunning, wire.MachineSetupStateDone} {
			if _, _, err := st.report(rep(id, state), now.Add(time.Minute)); err != nil {
				t.Fatalf("%s %s: %v", id, state, err)
			}
		}
	}
	if st.Ready == nil {
		t.Fatal("ready when step 8 is done")
	}
}

func TestMachineSetupSaysSlowerThanUsual(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := newStoredSetup(now)
	if _, _, err := st.report(rep(wire.MachineSetupStepIDCreate, wire.MachineSetupStateRunning), now); err != nil {
		t.Fatal(err)
	}
	times := map[wire.MachineSetupStepID][]float64{wire.MachineSetupStepIDCreate: {10, 20, 30}}
	if s := st.wire(now.Add(90*time.Second), times).Steps[0].Slower; s != nil {
		t.Fatal("90 s is within the 2 minute floor")
	}
	if s := st.wire(now.Add(3*time.Minute), times).Steps[0].Slower; s == nil || !*s {
		t.Fatal("3 minutes is past 3 times a 20 s median and the floor")
	}
	if s := st.wire(now.Add(time.Hour), nil).Steps[0].Slower; s != nil {
		t.Fatal("no past runs, no median")
	}
}

// Compare digits (#5359): a machine the control plane made must show the digits the control
// plane made from its token; another machine's stops the run, and it is kept.
func TestCompareDigitsOfAMadeMachine(t *testing.T) {
	now := time.Now()
	token := controllink.TokenCode([]byte("the secret of the join token....."))
	run := func(shown *string) *storedSetup {
		st := newStoredSetup(now)
		st.Expect = token
		for _, id := range []wire.MachineSetupStepID{wire.MachineSetupStepIDCreate, wire.MachineSetupStepIDJoin, wire.MachineSetupStepIDInstall} {
			if _, _, err := st.report(rep(id, wire.MachineSetupStateRunning), now); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.report(rep(id, wire.MachineSetupStateDone), now); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := st.report(rep(wire.MachineSetupStepIDCompare, wire.MachineSetupStateRunning), now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.report(wire.MachineSetupReport{Step: wire.MachineSetupStepIDCompare, State: wire.MachineSetupStateDone, Code: shown}, now); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := run(&token); st.Steps[3].State != wire.MachineSetupStateDone || st.Steps[3].Detail != "Both show "+token {
		t.Fatalf("the same digits: %+v", st.Steps[3])
	}
	other := controllink.TokenCode([]byte("another token's secret.........."))
	for name, shown := range map[string]*string{"other": &other, "none": nil} {
		st := run(shown)
		if st.Steps[3].State != wire.MachineSetupStateFailed || st.Steps[3].Error == "" || !st.failed() {
			t.Fatalf("%s digits: %+v", name, st.Steps[3])
		}
		if _, _, err := st.report(rep(wire.MachineSetupStepIDRuntimes, wire.MachineSetupStateRunning), now); err == nil {
			t.Fatalf("%s digits: the run went on", name)
		}
	}
	// A machine that no digits were made for (a pasted code) is not compared here.
	st := newStoredSetup(now)
	for _, id := range setupSteps[:3] {
		if _, _, err := st.report(rep(id, wire.MachineSetupStateRunning), now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.report(rep(id, wire.MachineSetupStateDone), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDCompare, wire.MachineSetupStateRunning), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.report(rep(wire.MachineSetupStepIDCompare, wire.MachineSetupStateDone), now); err != nil {
		t.Fatal(err)
	}
	if st.Steps[3].State != wire.MachineSetupStateDone {
		t.Fatalf("no digits made: %+v", st.Steps[3])
	}
}

// #5765: a machine the control plane makes has its set-up from the press of Make, under the
// host id its code joins it as: step 1 while the provider makes it, step 2 until it joins.
// Its join makes step 2 done and keeps the digits, and the host's own "steps 1 and 2 are
// done" keeps the provider's words. Steps 3 on come from the installer.
func TestAMadeMachinesSetUpBeginsAtMakeAndJoinsUnderItsID(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	ctx := context.Background()
	const made = "madehost"
	shown, err := p.s.Codes().Issue(withMadeMachine(ctx, made), true, false, testCodeLife)
	if err != nil {
		t.Fatal(err)
	}
	code, err := controllink.ParseCode(shown.Text)
	if err != nil {
		t.Fatal(err)
	}
	words := "Made at Stand-in in 3s"
	p.s.madeStep(made, rep(wire.MachineSetupStepIDCreate, wire.MachineSetupStateRunning))
	p.s.madeStep(made, wire.MachineSetupReport{Step: wire.MachineSetupStepIDCreate, State: wire.MachineSetupStateDone, Detail: &words})
	p.s.madeStep(made, rep(wire.MachineSetupStepIDJoin, wire.MachineSetupStateRunning))
	if setup := p.s.machineSetup(ctx, made); setup == nil || setup.Steps[0].State != wire.MachineSetupStateDone ||
		setup.Steps[1].State != wire.MachineSetupStateRunning {
		t.Fatalf("before the join: %+v", setup)
	}

	host, _, _ := p.rawHostWith("made-1", code)
	if host != made {
		t.Fatalf("joined as %q, not the id its set-up is kept under", host)
	}
	stored, _, err := p.s.readMachineSetup(ctx, made)
	if err != nil || stored == nil {
		t.Fatalf("%v %+v", err, stored)
	}
	if stored.Steps[1].State != wire.MachineSetupStateDone || stored.Expect != code.Digits() {
		t.Fatalf("after the join: %+v", stored)
	}
	// The host says steps 1 and 2 are done as it forwards the installer's first report.
	for _, id := range []wire.MachineSetupStepID{wire.MachineSetupStepIDCreate, wire.MachineSetupStepIDJoin} {
		if err := p.s.setupStep(ctx, made, rep(id, wire.MachineSetupStateDone)); err != nil {
			t.Fatal(err)
		}
	}
	// The installer's compare, with the digits its code shows.
	digits := code.Digits()
	for _, r := range []wire.MachineSetupReport{rep(wire.MachineSetupStepIDInstall, wire.MachineSetupStateRunning),
		rep(wire.MachineSetupStepIDInstall, wire.MachineSetupStateDone),
		{Step: wire.MachineSetupStepIDCompare, State: wire.MachineSetupStateDone, Code: &digits}} {
		if err := p.s.setupStep(ctx, made, r); err != nil {
			t.Fatal(err)
		}
	}
	setup := p.s.machineSetup(ctx, made)
	if deref(setup.Steps[0].Detail) != words {
		t.Errorf("the provider's words went: %+v", setup.Steps[0])
	}
	if setup.Steps[3].State != wire.MachineSetupStateDone {
		t.Errorf("compare: %+v", setup.Steps[3])
	}
}

// A made machine whose join wait ran out (step 2 failed) that joins after all has step 2 done.
func TestALateJoinMakesAFailedJoinStepDone(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	ctx := context.Background()
	const made = "latehost"
	shown, err := p.s.Codes().Issue(withMadeMachine(ctx, made), true, false, testCodeLife)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := controllink.ParseCode(shown.Text)
	problem := "It didn't join in 10 minutes."
	p.s.madeStep(made, rep(wire.MachineSetupStepIDCreate, wire.MachineSetupStateRunning))
	p.s.madeStep(made, rep(wire.MachineSetupStepIDCreate, wire.MachineSetupStateDone))
	p.s.madeStep(made, rep(wire.MachineSetupStepIDJoin, wire.MachineSetupStateRunning))
	p.s.madeStep(made, wire.MachineSetupReport{Step: wire.MachineSetupStepIDJoin, State: wire.MachineSetupStateFailed, Error: &problem})
	if host, _, _ := p.rawHostWith("late-1", code); host != made {
		t.Fatalf("joined as %q", host)
	}
	setup := p.s.machineSetup(ctx, made)
	if setup.Steps[1].State != wire.MachineSetupStateDone || setup.Steps[1].Error != nil {
		t.Fatalf("join: %+v", setup.Steps[1])
	}
}

// #6075: a machine a provider made is linked to its workspace as it joins, by the set-up its
// host id keeps, also when the control plane restarted between the make and the join and so
// lost the wait it kept in memory.
func TestAMadeMachineIsLinkedAsItJoinsAfterARestart(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	ctx := context.Background()
	record := `{"id":"do-1","name":"DigitalOcean","kind":"commands","commands":{"preset":"doctl"},"secretNames":[],` +
		`"capabilities":["create"],"contract":1,"rev":1}`
	if _, err := st.Put(ctx, providers.ProviderKey("do-1"), []byte(record), store.Absent); err != nil {
		t.Fatal(err)
	}
	private := controllink.NewPrivateKey()
	withProviders := func(c *Config) { c.ProviderHome = t.TempDir() }
	before := startPlaneWith(t, st, private, withProviders)
	const made = "madehost6075"
	shown, err := before.s.Codes().Issue(withMadeMachine(ctx, made), true, false, testCodeLife)
	if err != nil {
		t.Fatal(err)
	}
	code, err := controllink.ParseCode(shown.Text)
	if err != nil {
		t.Fatal(err)
	}
	// What providers/create does once the provider has made it.
	before.s.madeStep(made, rep(wire.MachineSetupStepIDCreate, wire.MachineSetupStateRunning))
	before.s.madeFor(made, "do-1", "ws-6075")
	before.s.Stop()

	after := startPlaneWith(t, st, private, withProviders)
	code.URL = after.url
	if host, _, _ := after.rawHostWith("pool-2-6924c0", code); host != made {
		t.Fatalf("joined as %q", host)
	}
	waitFor(t, func() bool {
		link := providers.LinkOf(after.s.Records().Host(made))
		return link != nil && link.Provider == "do-1" && link.Workspace == "ws-6075"
	})
}
