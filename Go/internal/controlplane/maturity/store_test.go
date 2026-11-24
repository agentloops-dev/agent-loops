package maturity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

func TestRecordBounds(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	ids := make([]string, MaxRecords)
	for i := 0; i < MaxRecords; i++ {
		ids[i] = fmt.Sprintf("feature-%02d", i)
	}
	m, err := New(s, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := m.Evaluate(ctx, id, 1, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := s.List(ctx, "v1/maturity/")
	if err != nil || len(keys) != MaxRecords {
		t.Fatalf("records = %d, err = %v", len(keys), err)
	}
	if _, err := m.Evaluate(ctx, "feature-00", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	keys, _ = s.List(ctx, "v1/maturity/")
	if len(keys) != MaxRecords {
		t.Fatalf("same feature key was not replaced: records = %d", len(keys))
	}
	if _, err := m.Evaluate(ctx, "feature-84", 1, time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unregistered feature error = %v", err)
	}
}

func TestRegisteredFeatureIDsAreBoundedAndCopied(t *testing.T) {
	ids := RegisteredFeatureIDs()
	if len(ids) != MaxRecords {
		t.Fatalf("registered features = %d, want %d", len(ids), MaxRecords)
	}
	if _, err := New(store.NewMemory(), ids); err != nil {
		t.Fatalf("registered feature catalogue: %v", err)
	}
	ids[0] = "not-registered"
	if RegisteredFeatureIDs()[0] == "not-registered" {
		t.Fatal("registered feature catalogue was mutated")
	}
}

func TestOversizedUpdateLeavesOldRecord(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	now := time.Now().UTC()
	m, _ := New(s, []string{"voice/1"})
	if _, err := m.Evaluate(ctx, "voice/1", 1, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "voice/1", Completion{ID: "source-1", Actor: ActorOther, ActorRef: "person-1", Session: "session-1", Build: "build-1", Success: true, Completed: true, Eligible: true}, now); err != nil {
		t.Fatal(err)
	}
	_, err := m.update(ctx, "voice/1", "", now.Add(time.Minute), func(r *Record) error { r.Agent.Build = string(make([]byte, MaxBytes)); return nil })
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized update error = %v", err)
	}
	r, err := m.Read(ctx, "voice/1")
	if err != nil || r.Rating != "pending" || r.CompletionID != "source-1" {
		t.Fatalf("old record = %#v, err = %v", r, err)
	}
}

func TestSaturationAndRetry(t *testing.T) {
	r := Record{Successes: math.MaxUint64, Failures: math.MaxUint64 - 1}
	if err := addOutcome(&r, true, "source-1"); err != nil {
		t.Fatal(err)
	}
	if err := addOutcome(&r, true, "source-1"); err != nil {
		t.Fatal(err)
	}
	if err := addOutcome(&r, false, "source-2"); err != nil {
		t.Fatal(err)
	}
	if r.Successes != math.MaxUint64 || r.Failures != math.MaxUint64 {
		t.Fatalf("counters = %d/%d", r.Successes, r.Failures)
	}
}

func TestConcurrentUpdatesAndRetry(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	m, _ := New(s, []string{"chat/1"})
	now := time.Now().UTC()
	const workers = 24
	var wg sync.WaitGroup
	first := Completion{ID: "completion-0", Actor: ActorOther, ActorRef: "person-1", Session: "session-0", Build: "build-1", Success: true, Completed: true, Eligible: true}
	if _, err := m.Complete(ctx, "chat/1", first, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "chat/1", first, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, workers-1)
	for i := 1; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("completion-%d", i)
			_, err := m.Complete(ctx, "chat/1", Completion{ID: id, Actor: ActorOther, ActorRef: "person-1", Session: id, Build: "build-1", Success: true, Completed: true, Eligible: true}, now)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	// The source operation's identity and state reject an older retry after later
	// completions. No attempt history is needed in the maturity record.
	first.Eligible = false
	if _, err := m.Complete(ctx, "chat/1", first, now.Add(time.Minute)); !errors.Is(err, ErrNotCompleted) {
		t.Fatalf("older completion retry error = %v", err)
	}
	r, err := m.Read(ctx, "chat/1")
	if err != nil {
		t.Fatal(err)
	}
	if err != nil || r.Successes != workers || r.CompletionID == first.ID {
		t.Fatalf("success count = %d, err = %v", r.Successes, err)
	}
}

func TestOrganisationScope(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemory()
	a, err := New(store.NewPrefixed(inner, "organisations/org-opaque-a"), []string{"chat/1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(store.NewPrefixed(inner, "organisations/org-opaque-b"), []string{"chat/1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Evaluate(ctx, "chat/1", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r, err := b.Read(ctx, "chat/1"); err != nil || r != nil {
		t.Fatalf("other organisation read = %#v, err = %v", r, err)
	}
}

func TestBackgroundRefreshCannotCount(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	m, err := New(s, []string{"chat/1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Complete(ctx, "chat/1", Completion{ID: "refresh-1", Actor: ActorOther, ActorRef: "person-1", Session: "session-1", Build: "build-1", Success: true, Completed: true, Eligible: true, BackgroundRefresh: true}, time.Now())
	if !errors.Is(err, ErrNotCompleted) {
		t.Fatalf("background refresh error = %v", err)
	}
	r, err := m.Read(ctx, "chat/1")
	if err != nil || r != nil {
		t.Fatalf("refresh created maturity record: %#v, err = %v", r, err)
	}
	_, err = m.Complete(ctx, "chat/1", Completion{ID: "old-completion", Actor: ActorOther, ActorRef: "person-1", Session: "session-1", Build: "build-1", Success: true, Completed: true}, time.Now())
	if !errors.Is(err, ErrNotCompleted) {
		t.Fatalf("ineligible source retry error = %v", err)
	}
}

func TestEvidenceSlotsStayBounded(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	m, err := New(s, []string{"chat/1"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 23, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	for i := 0; i < 3; i++ {
		if _, err := m.Complete(ctx, "chat/1", Completion{ID: fmt.Sprintf("beta-%d", i), Actor: ActorAlex, Session: fmt.Sprintf("session-%d", i), Build: "build-1", Success: true, Completed: true, Eligible: true}, now.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Complete(ctx, "chat/1", Completion{ID: "beta-0", Actor: ActorAlex, Session: "session-0", Build: "build-1", Success: true, Completed: true, Eligible: true}, now); err != nil {
		t.Fatalf("duplicate beta session: %v", err)
	}
	if _, err := m.Complete(ctx, "chat/1", Completion{ID: "beta-3", Actor: ActorAlex, Session: "session-3", Build: "build-1", Success: true, Completed: true, Eligible: true}, now.Add(4*24*time.Hour)); !errors.Is(err, ErrEvidenceFull) {
		t.Fatalf("fourth beta session error = %v", err)
	}
	if _, err := m.Complete(ctx, "chat/1", Completion{ID: "ga-1", Actor: ActorOther, ActorRef: "person-1", Session: "ga-session-1", Build: "build-1", Success: true, Completed: true, Eligible: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "chat/1", Completion{ID: "ga-2", Actor: ActorOther, ActorRef: "person-1", Session: "ga-session-2", Build: "build-1", Success: true, Completed: true, Eligible: true}, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "chat/1", Completion{ID: "ga-3", Actor: ActorOther, ActorRef: "person-2", Session: "ga-session-3", Build: "build-1", Success: true, Completed: true, Eligible: true}, now.Add(48*time.Hour)); !errors.Is(err, ErrEvidenceFull) {
		t.Fatalf("second GA person error = %v", err)
	}
	if _, err := m.SetSupportedClients(ctx, "chat/1", ClientSet{Mac: true, Remote: true, Web: true}, now); err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"mac", "remote", "web"} {
		if _, err := m.RecordClientWalk(ctx, "chat/1", client, "build-1", now, now); err != nil {
			t.Fatal(err)
		}
	}
	r, err := m.Read(ctx, "chat/1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Beta[0].Session != "session-0" || r.Beta[0].Date.Location() != time.UTC || r.GA.Dates[1].Location() != time.UTC || r.Clients.MacWalk.Build == "" || r.Clients.RemoteWalk.Build == "" || r.Clients.WebWalk.Build == "" {
		t.Fatalf("evidence not normalized/stored in fixed slots: %#v", r)
	}
}

func TestEncodedRecordWithinLimit(t *testing.T) {
	r := Record{FeatureID: "feature", Rating: "beta", Actor: ActorAlex, Agent: SuccessProof{Source: "source", Build: "build", Date: time.Now().UTC()}, Beta: [3]BetaProof{{Session: "opaque-session-reference", Date: time.Now().UTC(), Build: "build"}}, GA: GAProof{Person: "opaque-person-reference"}, Clients: ClientProof{Mac: true, Remote: true, Web: true, MacWalk: WalkProof{Build: "build"}, RemoteWalk: WalkProof{Build: "build"}, WebWalk: WalkProof{Build: "build"}}}
	b, err := json.Marshal(r)
	if err != nil || len(b) > MaxBytes {
		t.Fatalf("encoded record bytes = %d, err = %v", len(b), err)
	}
}

func TestRatingGatesAndMissingEvidence(t *testing.T) {
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r := Record{StartedAt: day, Agent: SuccessProof{Source: "agent-completion", Build: "build-a", Date: day}}
	if got := RatingFor(r); got != "alpha" {
		t.Fatalf("alpha rating = %q", got)
	}
	r.Beta = [3]BetaProof{
		{Session: "alex-1", Date: day, Build: "build-a"},
		{Session: "alex-2", Date: day, Build: "build-a"},
		{Session: "alex-3", Date: day.Add(24 * time.Hour), Build: "build-a"},
	}
	if got := RatingFor(r); got != "beta" {
		t.Fatalf("beta rating = %q", got)
	}
	r.GA = GAProof{Person: "person-1", Dates: [2]time.Time{day, day.Add(24 * time.Hour)}, Sessions: [2]string{"session-1", "session-2"}, Source: [2]string{"completion-1", "completion-2"}, Builds: [2]string{"build-a", "build-a"}}
	if got := RatingFor(r); got != "beta" {
		t.Fatalf("rating without client walks = %q", got)
	}
	r.Clients = ClientProof{Configured: true, Supported: ClientSet{Mac: true, Remote: true, Web: true}, Mac: true, Remote: true, Web: true, MacWalk: WalkProof{Build: "build-a", Date: day}, RemoteWalk: WalkProof{Build: "build-a", Date: day}, WebWalk: WalkProof{Build: "build-a", Date: day}}
	if got := RatingFor(r); got != "ga" {
		t.Fatalf("GA rating = %q", got)
	}
	r.GA.Dates[1] = day
	if got := RatingFor(r); got != "beta" {
		t.Fatalf("same-day GA evidence rating = %q", got)
	}
}

func TestRatingDistinguishesUnmeasuredMeasuredZeroAndIncompleteEvidence(t *testing.T) {
	if got := RatingFor(Record{}); got != "unmeasured" {
		t.Fatalf("empty record rating = %q", got)
	}
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	measuredZero := Record{StartedAt: day}
	if got := RatingFor(measuredZero); got != "pending" {
		t.Fatalf("measured zero rating = %q", got)
	}
	saturatedWithoutAgentProof := Record{StartedAt: day, Successes: math.MaxUint64, Failures: math.MaxUint64}
	if got := RatingFor(saturatedWithoutAgentProof); got != "pending" {
		t.Fatalf("saturated counters without agent proof rating = %q", got)
	}
	alexOnly := Record{StartedAt: day, Beta: [3]BetaProof{{Session: "alex-1", Date: day, Build: "build-1"}}}
	if got := RatingFor(alexOnly); got != "pending" {
		t.Fatalf("Alex evidence without an agent success rating = %q", got)
	}
	missingAgentDate := Record{StartedAt: day, Agent: SuccessProof{Source: "completion-1", Build: "build-1"}}
	if got := RatingFor(missingAgentDate); got != "pending" {
		t.Fatalf("agent proof without date rating = %q", got)
	}
	missingAgentSource := Record{StartedAt: day, Agent: SuccessProof{Build: "build-1", Date: day}}
	if got := RatingFor(missingAgentSource); got != "pending" {
		t.Fatalf("agent proof without source rating = %q", got)
	}
	missingBetaDate := Record{
		StartedAt: day,
		Agent:     SuccessProof{Source: "completion-1", Build: "build-1", Date: day},
		Beta: [3]BetaProof{
			{Session: "session-1", Date: day, Build: "build-1"},
			{Session: "session-2", Build: "build-1"},
			{Session: "session-3", Date: day.Add(24 * time.Hour), Build: "build-1"},
		},
	}
	if got := RatingFor(missingBetaDate); got != "alpha" {
		t.Fatalf("beta proof missing date rating = %q", got)
	}
}

func TestEvaluateRecomputesRatingWhenRuleVersionChanges(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemory()
	m, err := New(inner, []string{"chat/1"})
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	record := Record{FeatureID: "chat/1", Rating: "ga", RuleVersion: 1, StartedAt: day, UpdatedAt: day}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inner.Put(ctx, Key("chat/1"), data, store.Absent); err != nil {
		t.Fatal(err)
	}
	got, err := m.Evaluate(ctx, "chat/1", 2, day.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.RuleVersion != 2 || got.Rating != "pending" {
		t.Fatalf("reevaluated record = version %d, rating %q", got.RuleVersion, got.Rating)
	}
}

func TestGARequiresOnlyConfiguredSupportedClientWalks(t *testing.T) {
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r := Record{
		StartedAt: day,
		Agent:     SuccessProof{Source: "agent-1", Build: "build-1", Date: day},
		Beta: [3]BetaProof{
			{Session: "alex-1", Date: day, Build: "build-1"},
			{Session: "alex-2", Date: day, Build: "build-1"},
			{Session: "alex-3", Date: day.Add(24 * time.Hour), Build: "build-1"},
		},
		GA: GAProof{
			Person: "person-1", Dates: [2]time.Time{day, day.Add(24 * time.Hour)},
			Sessions: [2]string{"other-1", "other-2"}, Source: [2]string{"source-1", "source-2"},
			Builds: [2]string{"build-1", "build-1"},
		},
		Clients: ClientProof{Configured: true, Supported: ClientSet{Mac: true, Web: true},
			Mac: true, Web: true, MacWalk: WalkProof{Build: "build-1", Date: day}, WebWalk: WalkProof{Build: "build-1", Date: day}},
	}
	if got := RatingFor(r); got != "ga" {
		t.Fatalf("GA with supported Mac and web walks = %q", got)
	}
	r.Clients.Supported.Remote = true
	if got := RatingFor(r); got != "beta" {
		t.Fatalf("GA without supported Remote walk = %q", got)
	}
}

func TestReadsPreviousClientChecklistShape(t *testing.T) {
	var r Record
	if err := json.Unmarshal([]byte(`{"featureId":"chat","clients":{"mac":true,"remote":false,"web":true}}`), &r); err != nil {
		t.Fatalf("read previous record shape: %v", err)
	}
	if !r.Clients.Mac || r.Clients.Remote || !r.Clients.Web {
		t.Fatalf("client checklist = %#v", r.Clients)
	}
}

func TestBlockerSuspendsUntilSamePathFixedBuildRecovery(t *testing.T) {
	ctx := context.Background()
	m, err := New(store.NewMemory(), []string{"feature"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := m.Evaluate(ctx, "feature", 1, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "feature", Completion{ID: "agent-1", Actor: ActorAgent, Build: "build-1", Success: true, Completed: true, Eligible: true}, now); err != nil {
		t.Fatal(err)
	}
	r, err := m.Read(ctx, "feature")
	if err != nil || r.Rating != "alpha" {
		t.Fatalf("rating before blocker = %#v, err=%v", r, err)
	}
	if _, err := m.RecordBlocker(ctx, "feature", "settings/save", now); err != nil {
		t.Fatal(err)
	}
	r, err = m.Read(ctx, "feature")
	if err != nil || !r.Blocked || RatingFor(*r) != "pending" {
		t.Fatalf("blocked record = %#v, err=%v", r, err)
	}
	recovery := Completion{ID: "completion-1", Actor: ActorAlex, ActorRef: "alex", Session: "session-1", Build: "build-fixed", Success: true, Completed: true, Eligible: true}
	if _, err := m.RecoverBlocker(ctx, "feature", "other/path", recovery, now.Add(time.Hour)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong-path recovery error = %v", err)
	}
	if _, err := m.RecoverBlocker(ctx, "feature", "settings/save", recovery, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, err = m.Read(ctx, "feature")
	if err != nil || r.Blocked || r.Rating != "alpha" || r.Blocker.FixedBuild != "build-fixed" || r.Blocker.RecoveryActor != "alex" || r.Blocker.RecoveryCompletion != "completion-1" || r.Blocker.RecoveredAt.IsZero() {
		t.Fatalf("recovered evidence = %#v, err=%v", r, err)
	}
}
