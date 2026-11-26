package controlplane

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A machine's set-up (#5355, epic #5353, "The steps" of the machine set-up spec): the steps
// the control plane keeps for each machine, in order, with each one's start and end. Every
// set-up has the eight of setupSteps; root set-up and your set-up (#7167) are added when the
// host first says them, as each runs only when its script was sent. A step is read by its id,
// and setupWaits says what each one waits for. The
// installer on the machine (agents-host install --join) says each step as machine/setupStep;
// the control plane makes step 1 itself and calls BeginSetup. A failed machine is kept, not
// deleted, and machines/retrySetup runs the failed step and the ones after it.
//
// One object a machine, v1/machine-setup/<host>.json. The durations of finished steps, for
// "Slower than usual", are in machinesetuptimes.go.

const (
	// store-bounds: kind=document; count=1 a machine; size=512KiB a record; age=until-deleted; applied=on write, the hourly sweep deletes one 90 days after it ended, or whose machine is gone
	machineSetupPrefix = "v1/machine-setup/"
	// machineSetupMost is the largest record (#6968): the pool's projects (its record is 256 KiB
	// at most) and each step's words, cut at setupWordsMost.
	machineSetupMost = 512 << 10
	setupWordsMost   = 2 << 10
	// endedSetupKept is how long a set-up that ended, ready or failed, is kept. One that has not
	// ended has no age: a warm pool's machine waits for its claim that way.
	endedSetupKept = 90 * 24 * time.Hour
	// unjoinedSetupKept is how long the set-up of a machine with no record is kept after its
	// last change: a machine being made has no record until it joins.
	unjoinedSetupKept = 7 * 24 * time.Hour
	// slowFactor and slowFloor: a running step is slower than usual after 3 times its median,
	// and at least 2 minutes.
	slowFactor = 3
	slowFloor  = 2 * time.Minute
)

// setupSteps are the eight steps that every set-up has, in order.
var setupSteps = []wire.MachineSetupStepID{
	wire.MachineSetupStepIDCreate, wire.MachineSetupStepIDJoin, wire.MachineSetupStepIDInstall,
	wire.MachineSetupStepIDCompare, wire.MachineSetupStepIDRuntimes, wire.MachineSetupStepIDProjects,
	wire.MachineSetupStepIDSignIn, wire.MachineSetupStepIDSmoke,
}

// setupOrder is every step a set-up can have, in the order that a record keeps them: root set-up
// and your set-up (#7167, gate 1 of #7151, section 1) only when the host says them.
var setupOrder = []wire.MachineSetupStepID{
	wire.MachineSetupStepIDCreate, wire.MachineSetupStepIDJoin, wire.MachineSetupStepIDInstall,
	wire.MachineSetupStepIDCompare, wire.MachineSetupStepIDRoot, wire.MachineSetupStepIDRuntimes,
	wire.MachineSetupStepIDUser, wire.MachineSetupStepIDProjects, wire.MachineSetupStepIDSignIn,
	wire.MachineSetupStepIDSmoke,
}

// setupWaits is what each step waits for: each of these that the set-up has must be done before
// it begins or ends. Runtimes and projects run together. Your set-up runs on the host when its
// script comes (#7155), beside the installer's steps after compare, so no step waits for it: the
// installer cannot know that one is coming. A project's script and setup.sh still run one at a
// time on the machine. Smoke is last.
var setupWaits = map[wire.MachineSetupStepID][]wire.MachineSetupStepID{
	wire.MachineSetupStepIDJoin:     {wire.MachineSetupStepIDCreate},
	wire.MachineSetupStepIDInstall:  {wire.MachineSetupStepIDJoin},
	wire.MachineSetupStepIDCompare:  {wire.MachineSetupStepIDInstall},
	wire.MachineSetupStepIDRoot:     {wire.MachineSetupStepIDCompare},
	wire.MachineSetupStepIDRuntimes: {wire.MachineSetupStepIDCompare},
	wire.MachineSetupStepIDUser:     {wire.MachineSetupStepIDRoot, wire.MachineSetupStepIDCompare},
	wire.MachineSetupStepIDProjects: {wire.MachineSetupStepIDCompare},
	wire.MachineSetupStepIDSignIn:   {wire.MachineSetupStepIDRuntimes, wire.MachineSetupStepIDProjects},
	wire.MachineSetupStepIDSmoke: {wire.MachineSetupStepIDRoot, wire.MachineSetupStepIDRuntimes,
		wire.MachineSetupStepIDProjects, wire.MachineSetupStepIDSignIn},
}

// claimSteps are the steps that a claim from a warm pool runs again, or for the first time: the
// pool did the others.
var claimSteps = map[wire.MachineSetupStepID]bool{
	wire.MachineSetupStepIDProjects: true, wire.MachineSetupStepIDUser: true,
	wire.MachineSetupStepIDSignIn: true, wire.MachineSetupStepIDSmoke: true,
}

// knownStep says that id is a step of this build.
func knownStep(id wire.MachineSetupStepID) bool { return slices.Contains(setupOrder, id) }

// storedSetupStep is a step as kept: times as times.
type storedSetupStep struct {
	ID       wire.MachineSetupStepID `json:"id"`
	State    wire.MachineSetupState  `json:"state"`
	Started  *time.Time              `json:"startedAt,omitempty"`
	Ended    *time.Time              `json:"endedAt,omitempty"`
	Detail   string                  `json:"detail,omitempty"`
	Error    string                  `json:"error,omitempty"`
	FromPool bool                    `json:"fromPool,omitempty"`
	// Hash is the script that the root or user step ran (#7167); Warning, a step done with a
	// warning in its words.
	Hash    string `json:"hash,omitempty"`
	Warning bool   `json:"warning,omitempty"`
}

type storedSetup struct {
	Steps   []storedSetupStep `json:"steps"`
	Started time.Time         `json:"startedAt"`
	Ready   *time.Time        `json:"readyAt,omitempty"`
	// Expect is the digits the control plane made from the join token of a machine it made
	// (#5359). The compare step is done only when the host shows the same; "" for a machine
	// that came by a code a person pasted, or asked to join, where a person compares.
	Expect string `json:"expectedCode,omitempty"`
	// Provider and Workspace are the workspace a provider made this machine as (#6075): the
	// host that joins as this id is linked to it, also after the control plane restarted.
	Provider  string `json:"provider,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	// Projects are the pool's projects of a machine that a person claimed (#6890): its step 6.
	Projects []string `json:"projects,omitempty"`
	// ScriptOnly is the set-up of a machine that no installer set up (#7155): a server that
	// joined by hand, or a Mac, sent setup.sh. It has only the steps its host said.
	ScriptOnly bool `json:"scriptOnly,omitempty"`
}

// claimed says that a person claimed the machine from a warm pool: the claim marks the steps
// that the pool did.
func (st *storedSetup) claimed() bool {
	create := st.step(wire.MachineSetupStepIDCreate)
	return create != nil && create.FromPool
}

// next is the first of the claim's steps that waits for the host to run it, or nil. A failed
// step waits for Try Again.
func (st *storedSetup) next() *wire.MachineSetupStepID {
	if st.failed() {
		return nil
	}
	for _, step := range st.Steps {
		// The host runs your set-up when its script comes (#7155), not as a step of the claim's.
		if claimSteps[step.ID] && step.ID != wire.MachineSetupStepIDUser && step.State != wire.MachineSetupStateDone {
			id := step.ID
			return &id
		}
	}
	return nil
}

// index is the place of a step in this set-up, or -1 when it has none.
func (st *storedSetup) index(id wire.MachineSetupStepID) int {
	return slices.IndexFunc(st.Steps, func(s storedSetupStep) bool { return s.ID == id })
}

// step is the step id of this set-up, or nil.
func (st *storedSetup) step(id wire.MachineSetupStepID) *storedSetupStep {
	if i := st.index(id); i >= 0 {
		return &st.Steps[i]
	}
	return nil
}

// waitsFor is what step id waits for in this set-up when it has them: setupWaits, but a claimed
// machine's projects do not wait for its set-up.
func (st *storedSetup) waitsFor(id wire.MachineSetupStepID) []wire.MachineSetupStepID {
	return slices.DeleteFunc(slices.Clone(setupWaits[id]), func(w wire.MachineSetupStepID) bool {
		return id == wire.MachineSetupStepIDProjects && w == wire.MachineSetupStepIDUser && st.claimed()
	})
}

// waits is what step id waits for in this set-up: the steps of waitsFor that it has.
func (st *storedSetup) waits(id wire.MachineSetupStepID) []wire.MachineSetupStepID {
	return slices.DeleteFunc(st.waitsFor(id), func(w wire.MachineSetupStepID) bool { return st.index(w) < 0 })
}

// add puts a step that this set-up does not have yet in its place, waiting. It refuses one that
// a step already begun waits for: it came too late to run first.
func (st *storedSetup) add(id wire.MachineSetupStepID) error {
	for _, s := range st.Steps {
		if s.State != wire.MachineSetupStateWaiting && slices.Contains(st.waitsFor(s.ID), id) {
			return fail(invalidParams, "Step %s cannot begin after step %s has.", id, s.ID)
		}
	}
	at := slices.Index(setupOrder, id)
	if id == wire.MachineSetupStepIDUser && st.claimed() {
		// A claimed machine's projects came before the claim, and its set-up after it.
		at = slices.Index(setupOrder, wire.MachineSetupStepIDProjects)
	}
	i := slices.IndexFunc(st.Steps, func(s storedSetupStep) bool { return slices.Index(setupOrder, s.ID) > at })
	if i < 0 {
		i = len(st.Steps)
	}
	st.Steps = slices.Insert(st.Steps, i, storedSetupStep{ID: id, State: wire.MachineSetupStateWaiting})
	st.Ready = nil
	return nil
}

// readable says that a kept set-up is one this build reads: each step once and known, and the
// eight that every set-up has (#7167). A record from before it, of the eight, is one.
func (st *storedSetup) readable() bool {
	seen := map[wire.MachineSetupStepID]bool{}
	for _, s := range st.Steps {
		if !knownStep(s.ID) || seen[s.ID] {
			return false
		}
		seen[s.ID] = true
	}
	if st.ScriptOnly {
		return len(st.Steps) > 0
	}
	for _, id := range setupSteps {
		if !seen[id] {
			return false
		}
	}
	return true
}

func machineSetupKey(host string) string {
	return machineSetupPrefix + strings.ToLower(host) + ".json"
}

func newStoredSetup(now time.Time) *storedSetup {
	out := &storedSetup{Started: now}
	for _, id := range setupSteps {
		out.Steps = append(out.Steps, storedSetupStep{ID: id, State: wire.MachineSetupStateWaiting})
	}
	return out
}

// canStart says whether step id may begin or end now: every step it waits for is done.
func (st *storedSetup) canStart(id wire.MachineSetupStepID) bool {
	for _, w := range st.waits(id) {
		if st.step(w).State != wire.MachineSetupStateDone {
			return false
		}
	}
	return true
}

// allDone says that every step is done.
func (st *storedSetup) allDone() bool {
	return !slices.ContainsFunc(st.Steps, func(s storedSetupStep) bool { return s.State != wire.MachineSetupStateDone })
}

// failedStep is the first step that failed, or nil.
func (st *storedSetup) failedStep() *storedSetupStep {
	for i := range st.Steps {
		if st.Steps[i].State == wire.MachineSetupStateFailed {
			return &st.Steps[i]
		}
	}
	return nil
}

// failedBesides says that a step other than id failed.
func (st *storedSetup) failedBesides(id wire.MachineSetupStepID) bool {
	return slices.ContainsFunc(st.Steps, func(s storedSetupStep) bool { return s.ID != id && s.State == wire.MachineSetupStateFailed })
}

func (st *storedSetup) failed() bool {
	for _, s := range st.Steps {
		if s.State == wire.MachineSetupStateFailed {
			return true
		}
	}
	return false
}

// report applies one step's report at now and returns the duration of a step that is done.
func (st *storedSetup) report(r wire.MachineSetupReport, now time.Time) (time.Duration, bool, error) {
	if !knownStep(r.Step) {
		return 0, false, fail(invalidParams, "There is no set-up step %q.", r.Step)
	}
	if st.index(r.Step) < 0 {
		if err := st.add(r.Step); err != nil {
			return 0, false, err
		}
	}
	i := st.index(r.Step)
	if r.Step == wire.MachineSetupStepIDCompare && r.State == wire.MachineSetupStateDone && st.Expect != "" {
		r = st.compared(r)
	}
	step := &st.Steps[i]
	detail, problem := "", ""
	if r.Detail != nil {
		detail = *r.Detail
	}
	if r.Error != nil {
		problem = *r.Error
	}
	step.mark(r)
	if step.State == r.State {
		// Said again, as a progress line. One with no words (the host says steps 1 and 2 are
		// done when it has joined) keeps the words the step has.
		if r.Detail != nil || r.Error != nil {
			step.Detail = detail
			step.Error = problem
		}
		return 0, false, nil
	}
	if step.runsAgain(r, now, detail, problem) {
		return 0, false, nil
	}
	if step.State == wire.MachineSetupStateDone {
		return 0, false, fail(invalidParams, "Step %s is done already.", r.Step)
	}
	switch r.State {
	case wire.MachineSetupStateWaiting:
		return 0, false, fail(invalidParams, "A step is made to wait again by Try Again.")
	case wire.MachineSetupStateFailed:
		// It stops the run: a step that never began has its start at the failure.
	default:
		// A step that runs beside one that failed still says that it ended (#7167): it is the
		// steps that had not begun that wait for Try Again.
		// Your set-up runs beside the installer's steps (#7155): its failure stops none of them.
		ended := r.State == wire.MachineSetupStateDone && step.State != wire.MachineSetupStateWaiting
		if st.failedBesides(wire.MachineSetupStepIDUser) && !ended || !st.canStart(r.Step) {
			return 0, false, fail(invalidParams, "Step %s cannot be %s before the steps ahead of it are done.", r.Step, r.State)
		}
	}
	if step.Started == nil {
		t := now
		step.Started = &t
	}
	step.State, step.Detail, step.Error = r.State, detail, problem
	var took time.Duration
	finished := false
	if r.State == wire.MachineSetupStateDone || r.State == wire.MachineSetupStateFailed {
		t := now
		step.Ended = &t
		if r.State == wire.MachineSetupStateDone {
			took, finished = now.Sub(*step.Started), true
		}
	} else {
		step.Ended = nil
	}
	if r.State == wire.MachineSetupStateDone {
		st.endWarned(now)
	}
	if st.allDone() {
		t := now
		st.Ready = &t
	}
	return took, finished, nil
}

// mark takes what a report says of the step beside its state: from a pool, the script's hash and
// a warning.
func (step *storedSetupStep) mark(r wire.MachineSetupReport) {
	if r.FromPool != nil && *r.FromPool {
		step.FromPool = true
	}
	if r.Hash != nil {
		step.Hash = *r.Hash
	}
	if r.Warning != nil {
		step.Warning = *r.Warning
	}
}

// runsAgain starts your set-up again when its host says it runs after it ended (#7155): Run
// Again, Send Set-up or Try Again. A set-up that was ready stays ready while it runs.
func (step *storedSetupStep) runsAgain(r wire.MachineSetupReport, now time.Time, detail, problem string) bool {
	if r.Step != wire.MachineSetupStepIDUser || r.State != wire.MachineSetupStateRunning ||
		step.State != wire.MachineSetupStateDone && step.State != wire.MachineSetupStateFailed {
		return false
	}
	t := now
	step.State, step.Started, step.Ended, step.Detail, step.Error = r.State, &t, nil, detail, problem
	return true
}

// endWarned ends each waiting step that the control plane said with a warning, as soon as the
// steps it waits for are done: a Root set-up that the provider cannot run, or with no lock
// (#7154). Nothing on the machine says it, because root.sh never runs there.
func (st *storedSetup) endWarned(now time.Time) {
	for i := range st.Steps {
		s := &st.Steps[i]
		if s.State == wire.MachineSetupStateWaiting && s.Warning && st.canStart(s.ID) && !st.failed() {
			t := now
			s.State, s.Started, s.Ended = wire.MachineSetupStateDone, &t, &t
		}
	}
}

// compared is a done compare step as the control plane judges it (#5359): the digits the
// host shows against the ones it made. They differ, or the host showed none: the machine is
// not the one that was made, so the step fails, and the machine is kept for the person to see.
func (st *storedSetup) compared(r wire.MachineSetupReport) wire.MachineSetupReport {
	shown := deref(r.Code)
	if shown == st.Expect {
		detail := "Both show " + shown
		r.Detail = &detail
		return r
	}
	problem := "The machine that joined shows the digits " + shown + ", not " + st.Expect + ", so it is not the one that was made. It is stopped and kept: look at it, then delete it."
	if shown == "" {
		problem = "The machine that joined showed no digits to compare with " + st.Expect + ", so it is not known to be the one that was made. It is stopped and kept: look at it, then delete it."
	}
	r.State, r.Error = wire.MachineSetupStateFailed, &problem
	return r
}

// madeMachineJoined is a machine the control plane made, as it enrols: steps 1 and 2 are done
// (step 1 is, by now, when the control plane that made it went away before it said so), and
// the digits it must show at step 4 are kept. It makes the set-up when there is none.
func (s *Service) madeMachineJoined(ctx context.Context, host, digits string) {
	var took time.Duration
	var finished bool
	_, err := s.changeMachineSetup(ctx, host, true, func(st *storedSetup) error {
		st.Expect = digits
		now := time.Now()
		for _, id := range []wire.MachineSetupStepID{wire.MachineSetupStepIDCreate, wire.MachineSetupStepIDJoin} {
			step := st.step(id)
			if step.State == wire.MachineSetupStateDone {
				continue
			}
			detail := "Joined the control plane"
			if id == wire.MachineSetupStepIDCreate {
				detail = "Made"
			}
			// A failed step (the wait for it ran out) is done after all: it joined.
			if step.State == wire.MachineSetupStateFailed {
				step.State, step.Error = wire.MachineSetupStateRunning, ""
			}
			d, done, err := st.report(wire.MachineSetupReport{Step: id, State: wire.MachineSetupStateDone, Detail: &detail}, now)
			if err != nil {
				return err
			}
			if id == wire.MachineSetupStepIDJoin {
				took, finished = d, done
			}
		}
		return nil
	})
	if err != nil {
		s.log("machine " + host + ": its set-up could not be kept as it joined: " + err.Error())
		return
	}
	if finished {
		s.noteSetupTime(ctx, wire.MachineSetupStepIDJoin, took)
	}
	s.setupChanged(ctx, host)
}

// madeStep is the providers' SetupStep hook (#5765): a step of a machine the control plane is
// making (1, create machine, and 2 until it joins), before the machine is a host.
func (s *Service) madeStep(host string, report wire.MachineSetupReport) {
	if err := s.setupStep(context.Background(), host, report); err != nil {
		s.log("machine " + host + ": set-up step " + string(report.Step) + " could not be kept: " + err.Error())
	}
}

// madeFor is the providers' MadeFor hook (#6075): the workspace a made machine is, kept on its
// set-up before it can join, so its join links it with no wait in memory.
func (s *Service) madeFor(host, provider, workspace string) {
	_, err := s.changeMachineSetup(context.Background(), host, true, func(st *storedSetup) error {
		st.Provider, st.Workspace = provider, workspace
		return nil
	})
	if err != nil {
		s.log("machine " + host + ": the workspace it was made as could not be kept: " + err.Error())
	}
}

// madeWorkspace is the workspace a made machine was made as (madeFor), or nil.
func (s *Service) madeWorkspace(ctx context.Context, host string) *codeWorkspace {
	stored, _, err := s.readMachineSetup(ctx, host)
	if err != nil || stored == nil || stored.Provider == "" || stored.Workspace == "" {
		return nil
	}
	return &codeWorkspace{Provider: stored.Provider, Workspace: stored.Workspace}
}

// retry makes the first failed step, and every one after it, wait again.
func (st *storedSetup) retry() bool {
	for i := range st.Steps {
		if st.Steps[i].State != wire.MachineSetupStateFailed {
			continue
		}
		for j := i; j < len(st.Steps); j++ {
			st.Steps[j] = storedSetupStep{ID: st.Steps[j].ID, State: wire.MachineSetupStateWaiting}
		}
		st.Ready = nil
		return true
	}
	return false
}

// medianOf is the middle of ds, or zero for none.
func medianOf(ds []float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]float64(nil), ds...)
	sort.Float64s(sorted)
	return time.Duration(sorted[len(sorted)/2] * float64(time.Second))
}

// wire is the set-up as clients see it at now, with a running step marked slower when it is
// past 3 times its median (at least 2 minutes).
func (st *storedSetup) wire(now time.Time, times map[wire.MachineSetupStepID][]float64) *wire.MachineSetup {
	out := &wire.MachineSetup{StartedAt: wire.DateOf(st.Started), Steps: []wire.MachineSetupStep{}}
	if st.Ready != nil {
		out.ReadyAt = optional(wire.DateOf(*st.Ready))
	}
	for _, s := range st.Steps {
		row := wire.MachineSetupStep{ID: s.ID, State: s.State, Detail: optional(s.Detail), Error: optional(s.Error), FromPool: optional(s.FromPool),
			Hash: optional(s.Hash), Warning: optional(s.Warning)}
		if s.Started != nil {
			row.StartedAt = optional(wire.DateOf(*s.Started))
		}
		if s.Ended != nil {
			row.EndedAt = optional(wire.DateOf(*s.Ended))
		}
		if s.State == wire.MachineSetupStateRunning && s.Started != nil {
			limit := slowFactor * medianOf(times[s.ID])
			if limit < slowFloor {
				limit = slowFloor
			}
			// With no past runs there is no median to be slower than.
			if len(times[s.ID]) > 0 && now.Sub(*s.Started) > limit {
				row.Slower = optional(true)
			}
		}
		out.Steps = append(out.Steps, row)
	}
	return out
}

func (s *Service) readMachineSetup(ctx context.Context, host string) (*storedSetup, string, error) {
	object, err := s.cfg.Store.Get(ctx, machineSetupKey(host))
	if err != nil || object == nil {
		return nil, "", err
	}
	var stored storedSetup
	if json.Unmarshal(object.Data, &stored) != nil || !stored.readable() {
		return nil, object.ETag, nil
	}
	return &stored, object.ETag, nil
}

// changeMachineSetup reads a machine's set-up (made now when it has none and create is true),
// changes it and writes it, again once when another write came between.
func (s *Service) changeMachineSetup(ctx context.Context, host string, create bool, change func(*storedSetup) error) (*storedSetup, error) {
	for try := 0; ; try++ {
		stored, tag, err := s.readMachineSetup(ctx, host)
		if err != nil {
			return nil, err
		}
		when := store.Absent
		if tag != "" {
			when = store.Matching(tag)
		}
		if stored == nil {
			if !create {
				return nil, nil
			}
			stored = newStoredSetup(time.Now())
			if scriptOnly, _ := ctx.Value(scriptOnlyKey{}).(bool); scriptOnly {
				stored.Steps, stored.ScriptOnly = nil, true
			}
		}
		if err := change(stored); err != nil {
			return nil, err
		}
		for i := range stored.Steps {
			step := &stored.Steps[i]
			step.Detail, step.Error = cutWords(step.Detail), cutWords(step.Error)
		}
		data, err := json.Marshal(stored)
		if err != nil {
			return nil, err
		}
		if len(data) > machineSetupMost {
			return nil, fail(invalidParams, "A machine's set-up is %d KiB at most.", machineSetupMost>>10)
		}
		if _, err = s.cfg.Store.Put(ctx, machineSetupKey(host), data, when); err == nil {
			return stored, nil
		}
		if !isConflict(err) || try > 0 {
			return nil, err
		}
	}
}

// cutWords is a step's words, at most setupWordsMost of them.
func cutWords(words string) string {
	if len(words) <= setupWordsMost {
		return words
	}
	return strings.ToValidUTF8(words[:setupWordsMost-len("…")], "") + "…"
}

// ended says the set-up is ready or failed, so nothing runs it until Try Again or a claim.
func (st *storedSetup) ended() bool { return st.Ready != nil || st.failed() }

// changed is the last time that the set-up says: a step began or ended, or it was ready.
func (st *storedSetup) changed() time.Time {
	last := st.Started
	for _, step := range st.Steps {
		for _, at := range []*time.Time{step.Started, step.Ended} {
			if at != nil && at.After(last) {
				last = *at
			}
		}
	}
	if st.Ready != nil && st.Ready.After(last) {
		last = *st.Ready
	}
	return last
}

// sweepMachineSetups deletes, at most most, the set-ups that ended endedSetupKept ago, and
// those of machines that are gone (#6968). full says it stopped at most.
func sweepMachineSetups(ctx context.Context, rs *records.Records, now time.Time, most int) (int, bool, error) {
	return sweepMachineObjects(ctx, rs, machineSetupPrefix, most, func(object *store.Object, gone bool) ([]byte, bool) {
		var stored storedSetup
		if json.Unmarshal(object.Data, &stored) != nil || len(stored.Steps) == 0 {
			return nil, gone
		}
		quiet := now.Sub(stored.changed())
		return nil, gone && quiet >= unjoinedSetupKept || stored.ended() && quiet >= endedSetupKept
	})
}

// sweepMachineObjects goes over the objects of one a machine under prefix. sweep says of each,
// with whether its machine has no record in the store, to delete it or what to write in its
// place (nil for neither). full says it stopped at most.
func sweepMachineObjects(ctx context.Context, rs *records.Records, prefix string, most int,
	sweep func(object *store.Object, gone bool) (write []byte, remove bool)) (swept int, full bool, err error) {
	s := rs.Store
	keys, err := s.List(ctx, prefix)
	if err != nil || len(keys) == 0 {
		return 0, false, err
	}
	// From the store and not only from these records, as a machine can have joined at another
	// copy. A removed one's tombstone counts until it goes. An object's name is in lower case.
	machines, err := s.List(ctx, records.MachinesPrefix)
	if err != nil {
		return 0, false, err
	}
	known := map[string]bool{}
	for _, k := range machines {
		known[strings.ToLower(strings.TrimPrefix(k.Key, records.MachinesPrefix))] = true
	}
	for _, h := range rs.Hosts() {
		known[strings.ToLower(h.ID)+".json"] = true
	}
	for _, k := range keys {
		if swept >= most {
			return swept, true, nil
		}
		object, err := s.Get(ctx, k.Key)
		if err != nil {
			return swept, false, err
		}
		if object == nil {
			continue
		}
		write, remove := sweep(object, !known[strings.TrimPrefix(k.Key, prefix)])
		switch {
		case remove:
			err = s.Delete(ctx, k.Key)
		case write != nil:
			_, err = s.Put(ctx, k.Key, write, store.Matching(object.ETag))
		default:
			continue
		}
		// Another write came between: it is looked at again in the next hour.
		if err != nil && !isConflict(err) {
			return swept, false, err
		}
		swept++
	}
	return swept, false, nil
}

// BeginSetup starts a machine's set-up, its eight steps waiting. It does nothing when the
// machine has one already.
func (s *Service) BeginSetup(ctx context.Context, host string) error {
	_, err := s.changeMachineSetup(ctx, host, true, func(*storedSetup) error { return nil })
	if err == nil {
		s.setupChanged(ctx, host)
	}
	return err
}

// scriptOnlyKey marks a change of a set-up that, made now, has only the steps its host says.
type scriptOnlyKey struct{}

// setupStep is machine/setupStep: a step the installer on the machine says. A machine whose
// person pasted the installer has no set-up yet: its first report makes one. Your set-up or root
// set-up said first, by a machine that no installer set up, makes one of those steps alone (#7155).
func (s *Service) setupStep(ctx context.Context, host string, report wire.MachineSetupReport) error {
	if report.Step == wire.MachineSetupStepIDUser || report.Step == wire.MachineSetupStepIDRoot {
		ctx = context.WithValue(ctx, scriptOnlyKey{}, true)
	}
	var took time.Duration
	var finished bool
	_, err := s.changeMachineSetup(ctx, host, true, func(st *storedSetup) (err error) {
		took, finished, err = st.report(report, time.Now())
		return err
	})
	if err != nil {
		if _, ok := err.(*RPCError); ok {
			return err
		}
		return fail(storeUnavailable, "The machine's set-up could not be kept: %v", err)
	}
	if finished {
		s.noteSetupTime(ctx, report.Step, took)
		if report.Step == wire.MachineSetupStepIDCompare && s.madeAndCompared(ctx, host) {
			s.lockJoins.compared(ctx, host)
			if !s.unclaimedPoolMachine(ctx, host) {
				s.setupWanted(ctx, host)
			}
		}
	}
	s.setupChanged(ctx, host)
	return nil
}

// madeAndCompared says whether host is a machine the control plane made whose compare step
// is done (#5776): it showed the digits made from its join token, so it is the one that was
// made (#5359). A signer adds it to the lock with no person to compare digits again.
func (s *Service) madeAndCompared(ctx context.Context, host string) bool {
	stored, _, err := s.readMachineSetup(ctx, host)
	if err != nil || stored == nil || stored.Expect == "" {
		return false
	}
	return stored.step(wire.MachineSetupStepIDCompare).State == wire.MachineSetupStateDone
}

// retrySetup is machines/retrySetup.
func (s *Service) retrySetup(ctx context.Context, host string) error {
	retried, root := false, false
	stored, err := s.changeMachineSetup(ctx, host, false, func(st *storedSetup) error {
		if failed := st.failedStep(); failed != nil {
			if failed.ID == wire.MachineSetupStepIDUser {
				// Your set-up runs on the machine (#7155): its client runs it again there, with setup/run.
				return fail(invalidParams, "Your set-up runs on the machine: run it again with Run Again on its page.")
			}
			root = failed.ID == wire.MachineSetupStepIDRoot
		}
		retried = st.retry()
		if !retried {
			return fail(invalidParams, "No step of this machine's set-up has failed.")
		}
		return nil
	})
	if err != nil {
		if _, ok := err.(*RPCError); ok {
			return err
		}
		return fail(storeUnavailable, "The machine's set-up could not be kept: %v", err)
	}
	if stored == nil {
		return fail(noSuchHost, "This machine has no set-up.")
	}
	if root {
		// The machine reads root.sh's result again (#7159): the person may have run the copied
		// root command there. Then it runs the steps after it, which never began.
		s.rootAgain(ctx, host)
	}
	s.setupChanged(ctx, host)
	return nil
}

// rootAgainPatience is how long Try Again waits for a machine to take setup/rootAgain: it answers
// at once, and reports the step as it reads it.
const rootAgainPatience = 10 * time.Second

// rootAgain asks host to read root.sh's result again and run the steps after it (#7159). A
// machine that does not take it keeps its Root set-up failed, and says why.
func (s *Service) rootAgain(ctx context.Context, host string) {
	answer := s.router.Ask(host, wire.CallSetupRootAgain.Name, wire.SetupRootAgain{Rest: true}, rootAgainPatience)
	problem := ""
	switch {
	case answer == nil:
		problem = "The machine did not answer, so root.sh’s result was not read again. Try again when it is online."
	case len(answer.Error) > 0:
		var refused struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(answer.Error, &refused)
		problem = refused.Message
		if problem == "" {
			problem = "The machine did not read root.sh’s result again."
		}
	default:
		return
	}
	_, _ = s.changeMachineSetup(ctx, host, false, func(st *storedSetup) error {
		if step := st.step(wire.MachineSetupStepIDRoot); step != nil && step.State == wire.MachineSetupStateWaiting {
			now := time.Now()
			step.State, step.Error, step.Ended = wire.MachineSetupStateFailed, problem, &now
		}
		return nil
	})
}

// machineSetup is a machine's set-up for its row, or none.
func (s *Service) machineSetup(ctx context.Context, host string) *wire.MachineSetup {
	stored, _, err := s.readMachineSetup(ctx, host)
	if err != nil || stored == nil {
		return nil
	}
	return stored.wire(time.Now(), s.readSetupTimes(ctx, stored))
}

// setupChanged tells the clients, as any change of a machine is told.
func (s *Service) setupChanged(ctx context.Context, host string) {
	if s.records.Host(host) == nil {
		// A machine being made has not joined yet (#5765): its row comes with its join.
		return
	}
	described := map[string]any{}
	for k, v := range s.router.Described(host) {
		described[k] = v
	}
	if setup := s.machineSetup(ctx, host); setup != nil {
		described["setup"] = setup
	}
	s.router.BroadcastHost(host, "control/hostChanged", described)
}

// unclaimedPoolMachine says that host is a warm pool's machine that no person claimed yet: it
// gets the person's setup.sh at the claim, not before (#7155).
func (s *Service) unclaimedPoolMachine(ctx context.Context, host string) bool {
	if s.pools == nil {
		return false
	}
	stored, _, err := s.readMachineSetup(ctx, host)
	if err != nil || stored == nil || stored.claimed() {
		return false
	}
	p, err := s.pools.poolOf(ctx, s.records.Host(host), s.madeWorkspace(ctx, host))
	return err == nil && p != nil
}

// setupWanted tells the machine that carries the provider that made host that host is ready for
// the person's setup.sh (#7155, setup/wanted). It carries no text: the sender sends one only
// with an approval of scope made, which the target takes once.
func (s *Service) setupWanted(ctx context.Context, host string) {
	if s.providers == nil {
		return
	}
	made := s.madeWorkspace(ctx, host)
	if made == nil {
		return
	}
	sender := s.providers.CarrierOf(made.Provider)
	if sender == "" || sender == host {
		return
	}
	if h := s.records.Host(sender); h == nil || !wire.Has(hostCapabilities(*h), wire.CapHostMachineSetup2) {
		return
	}
	if !s.router.Tell(sender, wire.NotifySetupWanted.Name, wire.SetupWanted{Host: host}) {
		s.log("machine " + host + ": the machine with its person's scripts is offline, so setup.sh is not sent")
	}
}
