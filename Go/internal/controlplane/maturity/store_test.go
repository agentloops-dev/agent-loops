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
		if _, err := m.Evaluate(ctx, id, "pending", 1, false, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := s.List(ctx, "v1/maturity/")
	if err != nil || len(keys) != MaxRecords {
		t.Fatalf("records = %d, err = %v", len(keys), err)
	}
	if _, err := m.Evaluate(ctx, "feature-00", "beta", 1, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	keys, _ = s.List(ctx, "v1/maturity/")
	if len(keys) != MaxRecords {
		t.Fatalf("same feature key was not replaced: records = %d", len(keys))
	}
	if _, err := m.Evaluate(ctx, "feature-84", "pending", 1, false, time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unregistered feature error = %v", err)
	}
}

func TestOversizedUpdateLeavesOldRecord(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	now := time.Now().UTC()
	m, _ := New(s, []string{"voice/1"})
	if _, err := m.Evaluate(ctx, "voice/1", "beta", 1, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "voice/1", Completion{ID: "source-1", Actor: ActorMember, Success: true, Completed: true, Eligible: true}, now); err != nil {
		t.Fatal(err)
	}
	_, err := m.update(ctx, "voice/1", "", now.Add(time.Minute), func(r *Record) error { r.Rating = string(make([]byte, MaxBytes)); return nil })
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized update error = %v", err)
	}
	r, err := m.Read(ctx, "voice/1")
	if err != nil || r.Rating != "beta" || r.CompletionID != "source-1" {
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
	first := Completion{ID: "completion-0", Actor: ActorMember, Success: true, Completed: true, Eligible: true}
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
			_, err := m.Complete(ctx, "chat/1", Completion{ID: id, Actor: ActorMember, Success: true, Completed: true, Eligible: true}, now)
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
	if _, err := a.Evaluate(ctx, "chat/1", "beta", 1, false, time.Now()); err != nil {
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
	_, err = m.Complete(ctx, "chat/1", Completion{ID: "refresh-1", Actor: ActorMember, Success: true, Completed: true, Eligible: true, BackgroundRefresh: true}, time.Now())
	if !errors.Is(err, ErrNotCompleted) {
		t.Fatalf("background refresh error = %v", err)
	}
	r, err := m.Read(ctx, "chat/1")
	if err != nil || r != nil {
		t.Fatalf("refresh created maturity record: %#v, err = %v", r, err)
	}
	_, err = m.Complete(ctx, "chat/1", Completion{ID: "old-completion", Actor: ActorMember, Success: true, Completed: true}, time.Now())
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
		if _, err := m.AddBetaSession(ctx, "chat/1", fmt.Sprintf("session-%d", i), now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.AddBetaSession(ctx, "chat/1", "session-0", now, now); err != nil {
		t.Fatalf("duplicate beta session: %v", err)
	}
	if _, err := m.AddBetaSession(ctx, "chat/1", "session-3", now, now); !errors.Is(err, ErrEvidenceFull) {
		t.Fatalf("fourth beta session error = %v", err)
	}
	if _, err := m.SetGAEvidence(ctx, "chat/1", "person-1", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetGAEvidence(ctx, "chat/1", "person-1", now.Add(24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetGAEvidence(ctx, "chat/1", "person-2", now.Add(48*time.Hour), now); !errors.Is(err, ErrEvidenceFull) {
		t.Fatalf("second GA person error = %v", err)
	}
	for _, client := range []string{"mac", "remote", "web"} {
		if _, err := m.MarkClient(ctx, "chat/1", client, now); err != nil {
			t.Fatal(err)
		}
	}
	r, err := m.Read(ctx, "chat/1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Beta[0].Session != "session-0" || r.Beta[0].Date.Location() != time.UTC || r.GA.Dates[1].Location() != time.UTC || !r.Clients.Mac || !r.Clients.Remote || !r.Clients.Web {
		t.Fatalf("evidence not normalized/stored in fixed slots: %#v", r)
	}
}

func TestEncodedRecordWithinLimit(t *testing.T) {
	r := Record{FeatureID: "feature", Rating: "beta", Actor: ActorOwner, Beta: [3]BetaProof{{Session: "opaque-session-reference", Date: time.Now().UTC()}}, GA: GAProof{Person: "opaque-person-reference"}, Clients: ClientProof{Mac: true, Remote: true, Web: true}}
	b, err := json.Marshal(r)
	if err != nil || len(b) > MaxBytes {
		t.Fatalf("encoded record bytes = %d, err = %v", len(b), err)
	}
}
