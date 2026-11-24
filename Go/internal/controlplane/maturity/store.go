// Package maturity stores the bounded evidence used to rate a feature (#6905).
// The caller must pass an organisation-scoped store, such as store.NewPrefixed.
package maturity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

const (
	MaxRecords = 84
	MaxBytes   = 4 * 1024
	maxRetries = 32
)

var featureID = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)

var (
	ErrInvalid      = errors.New("invalid maturity record")
	ErrTooLarge     = errors.New("maturity record exceeds 4 KiB")
	ErrNotCompleted = errors.New("source operation is not an eligible completion")
	ErrEvidenceFull = errors.New("maturity evidence slots are full")
)

// Record is the current aggregate for one registered feature. Its evidence fields
// have fixed capacity. CompletionID is the source operation's current completion
// identity, not an attempt log; source integrations must call this at completion.
type Record struct {
	FeatureID    string       `json:"featureId"`
	Rating       string       `json:"rating"`
	RuleVersion  uint16       `json:"ruleVersion"`
	StartedAt    time.Time    `json:"startedAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
	Successes    uint64       `json:"successes"`
	Failures     uint64       `json:"failures"`
	Blocked      bool         `json:"blocked"`
	Actor        Actor        `json:"actor"`
	CompletionID string       `json:"completionId,omitempty"`
	Beta         [3]BetaProof `json:"beta"`
	GA           GAProof      `json:"ga"`
	Clients      ClientProof  `json:"clients"`
}

// Actor is derived by the authenticated source handler, never from event content.
type Actor string

const (
	ActorAgent  Actor = "agent"
	ActorOwner  Actor = "owner"
	ActorMember Actor = "member"
)

// Completion is a source operation's final, authenticated outcome. The source
// handler sets Actor from its authenticated caller and Eligible from the existing
// operation's completion state; a replay that the source has already applied is
// ineligible. Complete also remembers the current identity for retry safety.
type Completion struct {
	ID                string
	Actor             Actor
	Success           bool
	Completed         bool
	Eligible          bool
	BackgroundRefresh bool
}

// BetaProof is one fixed beta session reference and UTC date.
type BetaProof struct {
	Session string    `json:"session,omitempty"`
	Date    time.Time `json:"date,omitempty"`
}

// GAProof stores one independent person's opaque reference and two UTC dates.
type GAProof struct {
	Person string       `json:"person,omitempty"`
	Dates  [2]time.Time `json:"dates"`
}

// ClientProof is the fixed checklist for the three supported clients.
type ClientProof struct {
	Mac    bool `json:"mac"`
	Remote bool `json:"remote"`
	Web    bool `json:"web"`
}

// Records is the maturity store for one organisation. Its registry is the
// authoritative feature catalogue; limiting it to 84 IDs bounds record count.
type Records struct {
	inner store.Store
	known map[string]struct{}
}

// New binds the store to the registered feature IDs for this organisation. The
// backing store must already be scoped to that organisation.
func New(s store.Store, registered []string) (*Records, error) {
	if s == nil || len(registered) > MaxRecords {
		return nil, ErrInvalid
	}
	r := &Records{inner: s, known: make(map[string]struct{}, len(registered))}
	for _, id := range registered {
		if !validFeatureID(id) {
			return nil, ErrInvalid
		}
		if _, exists := r.known[id]; exists {
			return nil, ErrInvalid
		}
		r.known[id] = struct{}{}
	}
	return r, nil
}

// Key is the only object key for a feature in this organisation's scoped store.
func Key(id string) string { return "v1/maturity/" + id + ".json" }

// Read returns one feature's record. Pass an organisation-scoped store to keep
// identifiers and evidence private to that organisation.
func (s *Records) Read(ctx context.Context, id string) (*Record, error) {
	if !s.registered(id) {
		return nil, ErrInvalid
	}
	o, err := s.inner.Get(ctx, Key(id))
	if err != nil || o == nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(o.Data, &r); err != nil {
		return nil, fmt.Errorf("read maturity record: %w", err)
	}
	if r.FeatureID != id {
		return nil, ErrInvalid
	}
	return &r, nil
}

// Evaluate updates the current rating, rule version, and blocker state.
func (s *Records) Evaluate(ctx context.Context, id, rating string, ruleVersion uint16, blocked bool, now time.Time) (*Record, error) {
	switch rating {
	case "unmeasured", "pending", "pilot", "alpha", "beta", "ga":
	default:
		return nil, ErrInvalid
	}
	return s.update(ctx, id, "", now, func(r *Record) error {
		r.Rating, r.RuleVersion, r.Blocked = rating, ruleVersion, blocked
		return nil
	})
}

// AddBetaSession stores one distinct opaque session reference and UTC date in a
// fixed slot. A repeated session does not consume another slot.
func (s *Records) AddBetaSession(ctx context.Context, id, session string, date, now time.Time) (*Record, error) {
	if !validReference(session) || date.IsZero() {
		return nil, ErrInvalid
	}
	return s.update(ctx, id, "", now, func(r *Record) error {
		for _, proof := range r.Beta {
			if proof.Session == session {
				return nil
			}
		}
		for i, proof := range r.Beta {
			if proof.Session == "" {
				r.Beta[i] = BetaProof{Session: session, Date: utcDate(date)}
				return nil
			}
		}
		return ErrEvidenceFull
	})
}

// SetGAEvidence stores evidence for one independent person on two distinct UTC
// dates. It never stores a person's name or customer content.
func (s *Records) SetGAEvidence(ctx context.Context, id, person string, date, now time.Time) (*Record, error) {
	if !validReference(person) || date.IsZero() {
		return nil, ErrInvalid
	}
	return s.update(ctx, id, "", now, func(r *Record) error {
		if r.GA.Person != "" && r.GA.Person != person {
			return ErrEvidenceFull
		}
		r.GA.Person = person
		day := utcDate(date)
		if r.GA.Dates[0].IsZero() {
			r.GA.Dates[0] = day
			return nil
		}
		if r.GA.Dates[0].Equal(day) || r.GA.Dates[1].Equal(day) {
			return nil
		}
		if r.GA.Dates[1].IsZero() {
			r.GA.Dates[1] = day
			return nil
		}
		return ErrEvidenceFull
	})
}

// MarkClient records one item in the fixed supported-client checklist.
func (s *Records) MarkClient(ctx context.Context, id, client string, now time.Time) (*Record, error) {
	return s.update(ctx, id, "", now, func(r *Record) error {
		switch client {
		case "mac":
			r.Clients.Mac = true
		case "remote":
			r.Clients.Remote = true
		case "web":
			r.Clients.Web = true
		default:
			return ErrInvalid
		}
		return nil
	})
}

// update atomically replaces one feature record. It is private so callers must use
// the outcome and evidence methods, which enforce the record's bounded contract.
func (s *Records) update(ctx context.Context, id, completionID string, now time.Time, change func(*Record) error) (*Record, error) {
	if !s.registered(id) || len(completionID) > 128 || change == nil {
		return nil, ErrInvalid
	}
	key := Key(id)
	for attempt := 0; attempt < maxRetries; attempt++ {
		old, err := s.inner.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		var r Record
		condition := store.Absent
		if old != nil {
			condition = store.Matching(old.ETag)
			if err := json.Unmarshal(old.Data, &r); err != nil {
				return nil, fmt.Errorf("read maturity record: %w", err)
			}
			if r.FeatureID != id {
				return nil, ErrInvalid
			}
		}
		if completionID != "" && r.CompletionID == completionID {
			return &r, nil
		}
		if r.FeatureID == "" {
			r.FeatureID = id
			r.StartedAt = now.UTC()
		}
		if err := change(&r); err != nil {
			return nil, err
		}
		if completionID != "" {
			r.CompletionID = completionID
		}
		r.UpdatedAt = now.UTC()
		data, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		if len(data) > MaxBytes {
			return nil, ErrTooLarge
		}
		if _, err := s.inner.Put(ctx, key, data, condition); err == nil {
			return &r, nil
		} else if !store.IsConflict(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("update maturity record after %d conflicts: %w", maxRetries, &store.ConflictError{Key: key})
}

// Complete applies one eligible source completion to its feature aggregate.
// Background refreshes and operations that are not complete cannot count.
func (s *Records) Complete(ctx context.Context, id string, completion Completion, now time.Time) (*Record, error) {
	if !completion.Completed || !completion.Eligible || completion.BackgroundRefresh || completion.ID == "" || !validActor(completion.Actor) {
		return nil, ErrNotCompleted
	}
	return s.update(ctx, id, completion.ID, now, func(r *Record) error {
		r.Actor = completion.Actor
		return addOutcome(r, completion.Success, completion.ID)
	})
}

func (s *Records) registered(id string) bool {
	if s == nil || !validFeatureID(id) {
		return false
	}
	_, ok := s.known[id]
	return ok
}

// AddOutcome increments a fixed-width counter once per current source completion.
// Counters saturate instead of wrapping.
func addOutcome(r *Record, success bool, completionID string) error {
	if r == nil || completionID == "" {
		return ErrInvalid
	}
	if r.CompletionID == completionID {
		return nil
	}
	if success {
		if r.Successes < ^uint64(0) {
			r.Successes++
		}
	} else if r.Failures < ^uint64(0) {
		r.Failures++
	}
	r.CompletionID = completionID
	return nil
}

func validReference(value string) bool { return value != "" && len(value) <= 128 }

func utcDate(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func validFeatureID(id string) bool { return featureID.MatchString(id) }

func validActor(actor Actor) bool {
	switch actor {
	case ActorAgent, ActorOwner, ActorMember:
		return true
	default:
		return false
	}
}
