package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
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

// #6760: the set-up times go at 90 days, on write and in the sweep, at most 50 a step, and a
// step with none left is never slower than usual.
func TestMachineSetupTimesAgeOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem := store.NewMemory()
	times := setupTimeSeries{mem}
	now := time.Now()
	step := wire.MachineSetupStepIDCreate
	count := func(id wire.MachineSetupStepID) int {
		t.Helper()
		points, _, err := times.Range(ctx, string(id), now.Add(-10*setupTimesKept), now.Add(10*setupTimesKept), "hour", "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		return len(points)
	}
	add := func(id wire.MachineSetupStepID, at time.Time) {
		t.Helper()
		if err := times.Append(ctx, store.Point{Series: string(id), ID: "p", At: at, Value: 20}); err != nil {
			t.Fatal(err)
		}
	}
	// From before #6760, with no times: the sweep drops them.
	if _, err := mem.Put(ctx, machineSetupTimes, []byte(`{"create":[10,20]}`), store.Absent); err != nil {
		t.Fatal(err)
	}
	if n := count(step); n != 0 {
		t.Fatalf("undated times were read: %d", n)
	}
	if _, err := sweep(ctx, records.New(mem), now, mostPerSweep, nil); err != nil {
		t.Fatal(err)
	}
	if o, err := mem.Get(ctx, machineSetupTimes); err != nil || o == nil || string(o.Data) != "{}" {
		t.Fatalf("undated times stayed: %v %v", o, err)
	}
	// On write: too old goes, the same twice is one, and 50 are kept.
	add(step, now.Add(-setupTimesKept-time.Minute))
	if n := count(step); n != 0 {
		t.Fatalf("an old time was kept: %d", n)
	}
	add(step, now)
	add(step, now)
	if n := count(step); n != 1 {
		t.Fatalf("a point twice: %d", n)
	}
	for i := range setupSteps {
		for j := range mostSetupTimes + 5 {
			add(setupSteps[i], now.Add(time.Duration(-j)*time.Second-time.Nanosecond))
		}
	}
	if n := count(step); n != mostSetupTimes {
		t.Fatalf("%d kept of one step", n)
	}
	if o, err := mem.Get(ctx, machineSetupTimes); err != nil || len(o.Data) > 32<<10 {
		t.Fatalf("over 32 KiB: %v", err)
	}
	// What a running step is compared with, then nothing once the sweep has been.
	s := &Service{cfg: Config{Store: mem}}
	st := newStoredSetup(now)
	if _, _, err := st.report(rep(step, wire.MachineSetupStateRunning), now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(3 * time.Minute)
	if got := st.wire(later, s.readSetupTimes(ctx, st)).Steps[0].Slower; got == nil || !*got {
		t.Fatal("slower than its median")
	}
	if _, err := sweep(ctx, records.New(mem), now.Add(setupTimesKept+time.Minute), mostPerSweep, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(step); n != 0 {
		t.Fatalf("%d left after 90 days", n)
	}
	if got := st.wire(later, s.readSetupTimes(ctx, st)).Steps[0].Slower; got != nil {
		t.Fatal("slower with no times left")
	}
}

// #7528: a made machine's Your set-up, sent by itself, says why while the sender tries again,
// and fails with the reason when it gives up, so its projects do not wait with no words. A step
// that began on the machine keeps what the machine says.
func TestYourSetUpSaysWhyItsAutomaticSendFailed(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	ctx := context.Background()
	const made = "notsent"
	for _, id := range setupSteps[:4] {
		p.s.madeStep(made, rep(id, wire.MachineSetupStateRunning))
		p.s.madeStep(made, rep(id, wire.MachineSetupStateDone))
	}
	notInLock := &providers.Failure{Code: wire.FailureNotInLock, Message: "This machine is not in your lock."}
	// No Your set-up in the set-up: nothing changes.
	p.s.setupNotSent(made, "mac", notInLock, true)
	if stored, _, _ := p.s.readMachineSetup(ctx, made); stored.index(wire.MachineSetupStepIDUser) >= 0 {
		t.Fatalf("a set-up with no Your set-up got one: %+v", stored.Steps)
	}
	p.s.setupAsked(ctx, made, true)
	user := func() storedSetupStep {
		t.Helper()
		stored, _, err := p.s.readMachineSetup(ctx, made)
		if err != nil || stored == nil {
			t.Fatal(err)
		}
		return *stored.step(wire.MachineSetupStepIDUser)
	}
	p.s.setupNotSent(made, "mac", notInLock, false)
	if u := user(); u.State != wire.MachineSetupStateNeedsYou || !strings.Contains(u.Detail, "lock") {
		t.Fatalf("while the sender tries again: %+v", u)
	}
	p.s.setupNotSent(made, "mac", notInLock, true)
	u := user()
	if u.State != wire.MachineSetupStateFailed || !strings.Contains(u.Error, "mac could not send setup.sh: This machine is not in your lock. Use Send Set-up") {
		t.Fatalf("after the last try: %+v", u)
	}
	// Send Set-up then reaches it, and it runs: a send that fails after does not change it.
	hash := strings.Repeat("a", 64)
	if err := p.s.setupStep(ctx, made, wire.MachineSetupReport{Step: wire.MachineSetupStepIDUser, State: wire.MachineSetupStateRunning, Hash: &hash}); err != nil {
		t.Fatal(err)
	}
	p.s.setupNotSent(made, "mac", &providers.Failure{Code: providers.CodeHostOffline, Message: "notsent is offline."}, false)
	if u := user(); u.State != wire.MachineSetupStateRunning || u.Error != "" {
		t.Fatalf("a failed send changed a running Your set-up: %+v", u)
	}

	// Any other refusal fails the step at once: the sender does not try it again.
	const other = "refused"
	for _, id := range setupSteps[:4] {
		p.s.madeStep(other, rep(id, wire.MachineSetupStateRunning))
		p.s.madeStep(other, rep(id, wire.MachineSetupStateDone))
	}
	p.s.setupAsked(ctx, other, true)
	p.s.setupNotSent(other, "mac", &providers.Failure{Code: providers.CodeHostOffline, Message: "refused did not answer"}, false)
	stored, _, _ := p.s.readMachineSetup(ctx, other)
	if u := stored.step(wire.MachineSetupStepIDUser); u.State != wire.MachineSetupStateFailed || !strings.Contains(u.Error, "refused did not answer. Use") {
		t.Fatalf("an offline target: %+v", u)
	}
}
