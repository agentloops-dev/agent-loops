package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/maturity"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
)

// maturityCompletionRequest carries source operation state, not an actor or content.
type maturityCompletionRequest struct {
	FeatureID         string `json:"featureId"`
	ID                string `json:"completionId"`
	SessionID         string `json:"sessionId"`
	BuildID           string `json:"buildId"`
	Success           *bool  `json:"success"`
	Completed         *bool  `json:"completed"`
	Eligible          *bool  `json:"eligible"`
	BackgroundRefresh *bool  `json:"backgroundRefresh"`
}

func (m *Methods) completeMaturity(ctx context.Context, params json.RawMessage, from Caller) (any, error) {
	var request maturityCompletionRequest
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, fail(invalidParams, "The maturity completion is not valid.")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fail(invalidParams, "The maturity completion is not valid.")
	}
	if from.Person == "" || m.maturity == nil {
		return nil, fail(notPermitted, "An authenticated person is required.")
	}
	person := m.records.Person(m.callerPerson(from))
	if person == nil || person.IsRemoved() || person.IsSuspended() {
		return nil, fail(notPermitted, "An active person is required.")
	}
	actor := maturityActor(person, m.s.localOwner())
	if request.Success == nil || request.Completed == nil || request.Eligible == nil || request.BackgroundRefresh == nil ||
		!validMaturityCompletionID(request.ID) || !validMaturityCompletionID(request.BuildID) ||
		(actor != maturity.ActorAgent && !validMaturityCompletionID(request.SessionID)) {
		return nil, fail(invalidParams, "The source completion state is not valid.")
	}
	completion := maturity.Completion{
		ID: hashMaturityReference(request.ID), Actor: actor, Success: *request.Success, Completed: *request.Completed,
		Eligible: *request.Eligible, BackgroundRefresh: *request.BackgroundRefresh, Build: hashMaturityReference(request.BuildID),
	}
	if actor != maturity.ActorAgent {
		completion.Session = hashMaturityReference(request.SessionID)
	}
	if actor == maturity.ActorOther {
		completion.ActorRef = hashMaturityReference(person.ID)
	}
	_, err := m.maturity.Complete(ctx, request.FeatureID, completion, time.Now())
	if errors.Is(err, maturity.ErrInvalid) || errors.Is(err, maturity.ErrNotCompleted) {
		return nil, fail(invalidParams, "The maturity completion is not eligible.")
	}
	if err != nil {
		return nil, err
	}
	// Not the record: what it holds about others is a report reader's, and any person may call this.
	return map[string]any{}, nil
}

func hashMaturityReference(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func validMaturityCompletionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func maturityActor(person *records.Person, owner string) maturity.Actor {
	if person == nil {
		return ""
	}
	if person.Kind == records.KindBot {
		return maturity.ActorAgent
	}
	if person.ID == owner {
		return maturity.ActorAlex
	}
	return maturity.ActorOther
}
