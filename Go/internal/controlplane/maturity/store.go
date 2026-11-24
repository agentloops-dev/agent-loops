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
	Blocker      BlockerProof `json:"blocker"`
	Actor        Actor        `json:"actor"`
	CompletionID string       `json:"completionId,omitempty"`
	Agent        SuccessProof `json:"agent"`
	Beta         [3]BetaProof `json:"beta"`
	GA           GAProof      `json:"ga"`
	Clients      ClientProof  `json:"clients"`
}

// Actor is derived by the authenticated source handler, never from event content.
type Actor string

const (
	ActorAgent Actor = "agent"
	ActorAlex  Actor = "alex"
	ActorOther Actor = "other"
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
	ActorRef          string
	Session           string
	Build             string
}

// BetaProof is one fixed beta session reference and UTC date.
type BetaProof struct {
	Session string    `json:"session,omitempty"`
	Date    time.Time `json:"date,omitempty"`
	Build   string    `json:"build,omitempty"`
}

// GAProof stores one independent person's opaque reference and two UTC dates.
type GAProof struct {
	Person   string       `json:"person,omitempty"`
	Dates    [2]time.Time `json:"dates"`
	Sessions [2]string    `json:"sessions"`
	Source   [2]string    `json:"source"`
	Builds   [2]string    `json:"builds"`
}

// SuccessProof is the fixed alpha evidence for one successful agent task.
type SuccessProof struct {
	Source string    `json:"source,omitempty"`
	Build  string    `json:"build,omitempty"`
	Date   time.Time `json:"date,omitempty"`
}

// WalkProof retains the source build and date for a supported-client walk.
type WalkProof struct {
	Build string    `json:"build,omitempty"`
	Date  time.Time `json:"date,omitempty"`
}

// BlockerProof retains one blocking path and the fixed-build recovery evidence.
type BlockerProof struct {
	Path               string    `json:"path,omitempty"`
	FixedBuild         string    `json:"fixedBuild,omitempty"`
	RecoveryActor      string    `json:"recoveryActor,omitempty"`
	RecoveryCompletion string    `json:"recoveryCompletion,omitempty"`
	RecoveredAt        time.Time `json:"recoveredAt,omitempty"`
}

// ClientProof is the fixed checklist for the three supported clients.
type ClientProof struct {
	Mac        bool      `json:"mac"`
	Remote     bool      `json:"remote"`
	Web        bool      `json:"web"`
	Configured bool      `json:"configured"`
	Supported  ClientSet `json:"supported"`
	MacWalk    WalkProof `json:"macWalk"`
	RemoteWalk WalkProof `json:"remoteWalk"`
	WebWalk    WalkProof `json:"webWalk"`
}

// ClientSet records which clients support this feature under its tier.
type ClientSet struct {
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

// Evaluate applies the current rule version to the retained gate evidence.
func (s *Records) Evaluate(ctx context.Context, id string, ruleVersion uint16, now time.Time) (*Record, error) {
	return s.update(ctx, id, "", now, func(r *Record) error {
		r.RuleVersion = ruleVersion
		return nil
	})
}

// RecordClientWalk stores the build and date of a successful supported-client walk.
func (s *Records) RecordClientWalk(ctx context.Context, id, client, build string, date, now time.Time) (*Record, error) {
	if !validReference(build) || date.IsZero() {
		return nil, ErrInvalid
	}
	return s.update(ctx, id, "", now, func(r *Record) error {
		proof := WalkProof{Build: build, Date: utcDate(date)}
		switch client {
		case "mac":
			if !r.Clients.Configured || !r.Clients.Supported.Mac {
				return ErrInvalid
			}
			r.Clients.Mac, r.Clients.MacWalk = true, proof
		case "remote":
			if !r.Clients.Configured || !r.Clients.Supported.Remote {
				return ErrInvalid
			}
			r.Clients.Remote, r.Clients.RemoteWalk = true, proof
		case "web":
			if !r.Clients.Configured || !r.Clients.Supported.Web {
				return ErrInvalid
			}
			r.Clients.Web, r.Clients.WebWalk = true, proof
		default:
			return ErrInvalid
		}
		return nil
	})
}

// SetSupportedClients records the fixed client tier before the GA walks.
func (s *Records) SetSupportedClients(ctx context.Context, id string, clients ClientSet, now time.Time) (*Record, error) {
	if !clients.Mac && !clients.Remote && !clients.Web {
		return nil, ErrInvalid
	}
	return s.update(ctx, id, "", now, func(r *Record) error {
		r.Clients.Configured = true
		r.Clients.Supported = clients
		return nil
	})
}

// RecordBlocker suspends the current rating until the affected person succeeds
// on the fixed build for the same feature path.
func (s *Records) RecordBlocker(ctx context.Context, id, path string, now time.Time) (*Record, error) {
	if !validReference(path) {
		return nil, ErrInvalid
	}
	return s.update(ctx, id, "", now, func(r *Record) error {
		r.Blocked = true
		r.Blocker = BlockerProof{Path: path}
		return nil
	})
}

// RecoverBlocker stores a successful retry on the fixed build for the same path.
func (s *Records) RecoverBlocker(ctx context.Context, id, path string, completion Completion, now time.Time) (*Record, error) {
	if !validReference(path) || !completion.Completed || !completion.Eligible || completion.BackgroundRefresh || !completion.Success || !validActor(completion.Actor) || !validReference(completion.ActorRef) || !validReference(completion.ID) || !validReference(completion.Build) {
		return nil, ErrInvalid
	}
	return s.update(ctx, id, "", now, func(r *Record) error {
		if !r.Blocked || r.Blocker.Path != path {
			return ErrInvalid
		}
		r.Blocked = false
		r.Blocker.FixedBuild = completion.Build
		r.Blocker.RecoveryActor = completion.ActorRef
		r.Blocker.RecoveryCompletion = completion.ID
		r.Blocker.RecoveredAt = utcDate(now)
		return nil
	})
}

// RatingFor evaluates the fixed gate evidence in order. A blocker always
// suspends the rating; incomplete evidence remains pending.
func RatingFor(r Record) string {
	if r.Blocked {
		return "pending"
	}
	if r.Agent.Source == "" || r.Agent.Build == "" || r.Agent.Date.IsZero() {
		return "pending"
	}
	rating := "alpha"
	betaDays := map[string]bool{}
	betaCount := 0
	for _, proof := range r.Beta {
		if proof.Session != "" && !proof.Date.IsZero() && proof.Build != "" {
			betaCount++
			betaDays[proof.Date.Format("2006-01-02")] = true
		}
	}
	if betaCount < 3 || len(betaDays) < 2 {
		return rating
	}
	rating = "beta"
	if r.GA.Person == "" || r.GA.Dates[0].IsZero() || r.GA.Dates[1].IsZero() || r.GA.Dates[0].Equal(r.GA.Dates[1]) || r.GA.Sessions[0] == "" || r.GA.Sessions[1] == "" || r.GA.Sessions[0] == r.GA.Sessions[1] || r.GA.Source[0] == "" || r.GA.Source[1] == "" || r.GA.Builds[0] == "" || r.GA.Builds[1] == "" {
		return rating
	}
	if !r.Clients.Configured || (r.Clients.Supported.Mac && !validWalk(r.Clients.MacWalk)) || (r.Clients.Supported.Remote && !validWalk(r.Clients.RemoteWalk)) || (r.Clients.Supported.Web && !validWalk(r.Clients.WebWalk)) {
		return rating
	}
	return "ga"
}

func validWalk(proof WalkProof) bool { return proof.Build != "" && !proof.Date.IsZero() }

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
		r.Rating = RatingFor(r)
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
	if !completion.Completed || !completion.Eligible || completion.BackgroundRefresh || completion.ID == "" || !validActor(completion.Actor) || !validReference(completion.Build) {
		return nil, ErrNotCompleted
	}
	return s.update(ctx, id, completion.ID, now, func(r *Record) error {
		r.Actor = completion.Actor
		if err := addCompletionEvidence(r, completion, now); err != nil {
			return err
		}
		return addOutcome(r, completion.Success, completion.ID)
	})
}

func addCompletionEvidence(r *Record, completion Completion, now time.Time) error {
	if !completion.Success {
		return nil
	}
	switch completion.Actor {
	case ActorAgent:
		r.Agent = SuccessProof{Source: completion.ID, Build: completion.Build, Date: utcDate(now)}
	case ActorAlex:
		return addAlexEvidence(r, completion, now)
	case ActorOther:
		return addOtherEvidence(r, completion, now)
	}
	return nil
}

func addAlexEvidence(r *Record, completion Completion, now time.Time) error {
	if !validReference(completion.Session) {
		return ErrInvalid
	}
	for _, proof := range r.Beta {
		if proof.Session == completion.Session {
			return nil
		}
	}
	for i, proof := range r.Beta {
		if proof.Session == "" {
			r.Beta[i] = BetaProof{Session: completion.Session, Date: utcDate(now), Build: completion.Build}
			return nil
		}
	}
	return ErrEvidenceFull
}

func addOtherEvidence(r *Record, completion Completion, now time.Time) error {
	if !validReference(completion.ActorRef) || !validReference(completion.Session) {
		return ErrInvalid
	}
	if r.GA.Person != "" && r.GA.Person != completion.ActorRef {
		return ErrEvidenceFull
	}
	r.GA.Person = completion.ActorRef
	day := utcDate(now)
	for _, existing := range r.GA.Dates {
		if existing.Equal(day) {
			return nil
		}
	}
	for i, existing := range r.GA.Dates {
		if existing.IsZero() {
			r.GA.Dates[i], r.GA.Sessions[i], r.GA.Source[i], r.GA.Builds[i] = day, completion.Session, completion.ID, completion.Build
			return nil
		}
	}
	return nil
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
	case ActorAgent, ActorAlex, ActorOther:
		return true
	default:
		return false
	}
}
