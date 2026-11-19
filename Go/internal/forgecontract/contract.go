// Package forgecontract is the one contract that every forge server passes (#6191, epic
// #6183): the eight servers (githubmcp, gitlabmcp, forgejomcp, bitbucketmcp, azuredevopsmcp,
// linearmcp, jiramcp, azureboardsmcp) give the same events for the same story, so a lane that
// waits on pr.merged, pr.blocked or checks.passed does not wait for ever on one forge.
//
// The contract is a scripted sequence of steps (open a pull request, a check fails, a check
// passes, a conflict, a review, a comment, a merge, a close; for a tracker, an issue's steps)
// and the events each step must raise, with pr.blocked's reasons. Each server's package runs
// it in its own contract_test.go, against its own fake, with a Server that says how to poll
// and how to make the fake do a step. A server that cannot give an event says so in Missing,
// with the bug that tracks it; the contract fails when an event is neither given nor named
// there. With -tags live, each package's contract_live_test.go runs Live against a real
// account when its credentials are set, and skips when they are not.
package forgecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/mcpevents"
)

// Kind is what a server is: a code forge (pull requests and their checks) or a tracker
// (issues only, as Linear, Jira and Azure Boards).
type Kind int

const (
	CodeForge Kind = iota
	Tracker
)

func (k Kind) String() string {
	if k == Tracker {
		return "tracker"
	}
	return "code forge"
}

// Expect is one event a step must raise; for pr.blocked, its reasons, in order.
type Expect struct {
	Event   string
	Reasons []string
}

// Step is one step of the sequence: what the forge does, and the events it must raise.
// Refused is a step where the forge refuses every call: each poll must be an error, never an
// empty list (#6163).
type Step struct {
	Name    string
	Does    string
	Expects []Expect
	Refused bool
}

// The steps' names, which a Server's Do switches on.
const (
	Open          = "open"
	CheckFails    = "check fails"
	CheckPasses   = "check passes"
	Conflict      = "conflict"
	ChangesAsked  = "changes requested"
	Comment       = "comment"
	Merge         = "merge"
	Close         = "close"
	Refused       = "refused"
	IssueOpen     = "issue open"
	IssueComment  = "issue comment"
	IssueAssign   = "issue assign"
	IssueLabel    = "issue label"
	IssueClose    = "issue close"
	IssueRefused  = "issue refused"
	blockedEvent  = "pr.blocked"
	checksFailed  = "checks.failed"
	checksPassed  = "checks.passed"
	prOpened      = "pr.opened"
	prConflicted  = "pr.conflicted"
	prReviewed    = "pr.reviewed"
	prCommented   = "pr.commented"
	prMerged      = "pr.merged"
	prClosed      = "pr.closed"
	issueOpened   = "issue.opened"
	issueComment  = "issue.commented"
	issueAssigned = "issue.assigned"
	issueLabelled = "issue.labelled"
	issueClosed   = "issue.closed"
)

// CodeForgeSteps are a code forge's sequence. The world is cumulative: each step starts from
// the one before. Pull request 1 is the one the story is about; pull request 2 is only for
// Close. Every person in it is a member of the project, not a bot, and no branch is a fork's.
var CodeForgeSteps = []Step{
	{Name: Open, Does: "Pull request 1 opens, ready (not a draft), from a branch of the project to its default branch, its description closing issue #1. No check has run, nobody asked for a review, auto-merge is off.",
		Expects: []Expect{{Event: prOpened}}},
	{Name: CheckFails, Does: "A required check (CI) on pull request 1's head finishes failed.",
		Expects: []Expect{{Event: checksFailed}, {Event: blockedEvent, Reasons: []string{mcpevents.ReasonRequiredCheckFailed}}}},
	{Name: CheckPasses, Does: "A new commit is pushed to pull request 1, and every required check on the new head finishes passed.",
		Expects: []Expect{{Event: checksPassed}}},
	{Name: Conflict, Does: "The default branch moves so that pull request 1 has a merge conflict with it; the forge knows it cannot merge for that.",
		Expects: []Expect{{Event: prConflicted}, {Event: blockedEvent, Reasons: []string{mcpevents.ReasonConflict}}}},
	{Name: ChangesAsked, Does: "A new commit settles the conflict (no check has finished on it yet), and a member of the project reviews pull request 1, asking for changes.",
		Expects: []Expect{{Event: prReviewed}, {Event: blockedEvent, Reasons: []string{mcpevents.ReasonChangesRequested}}}},
	{Name: Comment, Does: "A member of the project comments on pull request 1 (a comment on the pull request, not on a line of its diff).",
		Expects: []Expect{{Event: prCommented}}},
	{Name: Merge, Does: "Pull request 1 merges.",
		Expects: []Expect{{Event: prMerged}}},
	{Name: Close, Does: "Pull request 2, open since before the sequence began, closes without a merge.",
		Expects: []Expect{{Event: prClosed}}},
	{Name: Refused, Does: "The forge refuses every call: a rate limit (429 with its reset) or a token at its end (401).", Refused: true},
}

// TrackerSteps are a tracker's sequence, cumulative as CodeForgeSteps, about issue 1.
var TrackerSteps = []Step{
	{Name: IssueOpen, Does: "Issue 1 opens.", Expects: []Expect{{Event: issueOpened}}},
	{Name: IssueComment, Does: "A member comments on issue 1.", Expects: []Expect{{Event: issueComment}}},
	{Name: IssueAssign, Does: "Issue 1 is assigned to the account the server is connected as.", Expects: []Expect{{Event: issueAssigned}}},
	{Name: IssueLabel, Does: "Issue 1 gets the label bug.", Expects: []Expect{{Event: issueLabelled}}},
	{Name: IssueClose, Does: "Issue 1 closes.", Expects: []Expect{{Event: issueClosed}}},
	{Name: IssueRefused, Does: "The tracker refuses every call: a rate limit (429) or a token at its end (401).", Refused: true},
}

// Steps are k's sequence.
func Steps(k Kind) []Step {
	if k == Tracker {
		return TrackerSteps
	}
	return CodeForgeSteps
}

// Events are every event k's sequence expects, in the order the steps first raise them.
func Events(k Kind) []string {
	var out []string
	for _, s := range Steps(k) {
		for _, e := range s.Expects {
			if !slices.Contains(out, e.Event) {
				out = append(out, e.Event)
			}
		}
	}
	return out
}

// Poll is a server's events/poll, as each package's Events.Poll is.
type Poll func(ctx context.Context, raw json.RawMessage) (map[string]any, *mcpevents.Error)

// Server is one forge server under the contract.
type Server struct {
	// Name is the server's, as gitlab.
	Name string
	Kind Kind
	// Offered are the events the server's events/list gives (its EventNames).
	Offered []string
	// Missing are the contract's events the server does not give, each with why and the bug
	// that tracks it ("#6300: Bitbucket has no review states"). A key pr.blocked/<reason>, as
	// pr.blocked/required_check_failed, is a reason of pr.blocked the server does not read.
	Missing map[string]string
	// Args are the arguments of a poll of event: which pull requests or issues it is about.
	Args func(event string) map[string]any
	// Poll polls the server once.
	Poll Poll
	// Do makes the forge do step, from the world the step before left. It also moves the
	// server's clock on, so what it does is after the last poll. Nil in a live run that only
	// reads.
	Do func(t *testing.T, step Step)
	// Settle, when set, moves the server's clock past any wait it makes before it trusts what
	// it read (as Forgejo, which sees a conflict twice, a while apart, before it raises it).
	// Each step polls, settles and polls again; the two polls together raise what the step
	// expects, once.
	Settle func()
}

var bugRef = regexp.MustCompile(`#[0-9]+`)

// Run runs the contract against s: what s declares, then the sequence.
func Run(t *testing.T, s Server) {
	t.Helper()
	t.Run("declares", func(t *testing.T) { Declares(t, s) })
	t.Run("sequence", func(t *testing.T) { Sequence(t, s) })
}

// Declares checks that each of the contract's events is offered by s or named in its Missing
// with a bug, and not both.
func Declares(t *testing.T, s Server) {
	t.Helper()
	for _, name := range Events(s.Kind) {
		why, missing := s.Missing[name]
		offered := slices.Contains(s.Offered, name)
		switch {
		case offered && missing:
			t.Errorf("%s offers %s, and Missing says it does not (%s): take it out of Missing", s.Name, name, why)
		case !offered && !missing:
			t.Errorf("%s does not offer %s, a %s's contract event, and its Missing says nothing of it: give it, or say why with its bug", s.Name, name, s.Kind)
		case missing && !bugRef.MatchString(why):
			t.Errorf("%s's Missing for %s names no bug (#NNNN): %q", s.Name, name, why)
		}
	}
	for name, why := range s.Missing {
		if reason, ok := strings.CutPrefix(name, blockedEvent+"/"); ok {
			switch {
			case !slices.Contains(mcpevents.BlockedReasons, reason):
				t.Errorf("%s's Missing names %s, and %s is no reason of pr.blocked", s.Name, name, reason)
			case !slices.Contains(s.Offered, blockedEvent):
				t.Errorf("%s's Missing names %s, but it does not offer pr.blocked at all", s.Name, name)
			case !bugRef.MatchString(why):
				t.Errorf("%s's Missing for %s names no bug (#NNNN): %q", s.Name, name, why)
			}
			continue
		}
		if !slices.Contains(Events(s.Kind), name) {
			t.Errorf("%s's Missing names %s, which is no %s's contract event", s.Name, name, s.Kind)
		}
	}
}

// expected is what step must raise of what s gives: the events s offers, and of pr.blocked
// the reasons s reads.
func (s Server) expected(step Step, watched []string) []string {
	var want []string
	for _, e := range step.Expects {
		if !slices.Contains(watched, e.Event) {
			continue
		}
		var reasons []string
		for _, r := range e.Reasons {
			if _, missing := s.Missing[blockedEvent+"/"+r]; !missing {
				reasons = append(reasons, r)
			}
		}
		if len(e.Reasons) > 0 && len(reasons) == 0 {
			continue
		}
		want = append(want, describeExpect(Expect{Event: e.Event, Reasons: reasons}))
	}
	return want
}

// watched are the contract's events s offers.
func (s Server) watched() []string {
	var out []string
	for _, name := range Events(s.Kind) {
		if slices.Contains(s.Offered, name) {
			out = append(out, name)
		}
	}
	return out
}

// cursors keeps one cursor per event across polls.
type cursors map[string]any

// raised is one event a poll gave: its name and data.
type raised struct {
	Event string
	Data  map[string]any
}

func (s Server) poll(ctx context.Context, name string, c cursors) ([]raised, error) {
	var args map[string]any
	if s.Args != nil {
		args = s.Args(name)
	}
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args, "cursor": c[name]})
	result, rpcErr := s.Poll(ctx, raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	c[name] = result["cursor"]
	list, _ := result["events"].([]any)
	var out []raised
	for _, e := range list {
		m, _ := e.(map[string]any)
		data, _ := m["data"].(map[string]any)
		out = append(out, raised{Event: name, Data: data})
	}
	return out, nil
}

// Sequence runs s's steps: a first poll of each event s offers, then each step and a poll of
// every one. Each step must raise what it expects of the events s offers, pr.blocked with the
// same reasons, and nothing else; a refused step must make every poll an error.
func Sequence(t *testing.T, s Server) {
	t.Helper()
	ctx := context.Background()
	watched := s.watched()
	c := cursors{}
	for _, name := range watched {
		if _, err := s.poll(ctx, name, c); err != nil {
			t.Fatalf("%s: the first poll of %s: %v", s.Name, name, err)
		}
	}
	for _, step := range Steps(s.Kind) {
		s.Do(t, step)
		if step.Refused {
			s.refused(ctx, t, step, watched, c)
			continue
		}
		got := s.raisedBy(ctx, t, step, watched, c)
		want := s.expected(step, watched)
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s, step %q (%s):\n  raised %v\n  want   %v\n(an event or reason the server now gives comes out of its Missing)", s.Name, step.Name, step.Does, got, want)
		}
	}
}

// refused checks that each poll of a refused step is an error. It polls with copies of the
// cursors, so the step after starts where the step before left.
func (s Server) refused(ctx context.Context, t *testing.T, step Step, watched []string, c cursors) {
	t.Helper()
	for _, name := range watched {
		if got, err := s.poll(ctx, name, cursors{name: c[name]}); err == nil {
			t.Errorf("%s, step %q: a poll of %s gave %d events and no error; a refused call must be an error, not an empty list (#6163)", s.Name, step.Name, name, len(got))
		}
	}
}

// raisedBy polls every watched event after step, and again after Settle when s has one, and
// gives what the polls raised.
func (s Server) raisedBy(ctx context.Context, t *testing.T, step Step, watched []string, c cursors) []string {
	t.Helper()
	var got []string
	rounds := 1
	if s.Settle != nil {
		rounds = 2
	}
	for round := range rounds {
		if round > 0 {
			s.Settle()
		}
		for _, name := range watched {
			events, err := s.poll(ctx, name, c)
			if err != nil {
				t.Errorf("%s, step %q: poll %s: %v", s.Name, step.Name, name, err)
			}
			for _, e := range events {
				got = append(got, describe(e))
			}
		}
	}
	return got
}

func describe(e raised) string {
	if e.Event != blockedEvent {
		return e.Event
	}
	var reasons []string
	list, _ := e.Data["reasons"].([]any)
	for _, r := range list {
		reasons = append(reasons, fmt.Sprint(r))
	}
	return e.Event + "(" + strings.Join(reasons, ",") + ")"
}

func describeExpect(e Expect) string {
	if e.Event != blockedEvent {
		return e.Event
	}
	return e.Event + "(" + strings.Join(e.Reasons, ",") + ")"
}

// Live runs the contract's reads against a real account (-tags live): what s declares, and two
// polls of every contract event s offers, which must answer, not fail, so a changed field or a
// token at its end shows. When s has a Do, it runs the whole sequence on the account instead.
func Live(t *testing.T, s Server) {
	t.Helper()
	Declares(t, s)
	if s.Do != nil {
		Sequence(t, s)
		return
	}
	ctx := context.Background()
	c := cursors{}
	for _, name := range s.watched() {
		for i := range 2 {
			if _, err := s.poll(ctx, name, c); err != nil {
				t.Errorf("%s: poll %d of %s on the real account: %v", s.Name, i+1, name, err)
			}
		}
	}
}

// Env is the values of the environment variables names, for a live run; it skips t when one
// is not set, so the live mode runs only where an account's credentials are.
func Env(t *testing.T, names ...string) []string {
	t.Helper()
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = os.Getenv(name)
		if out[i] == "" {
			t.Skipf("%s is not set: no real account for the forge contract's live mode", name)
		}
	}
	return out
}
