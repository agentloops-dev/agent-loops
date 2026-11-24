package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/maturity"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestAuthenticatedMaturityCompletion(t *testing.T) {
	t.Parallel()
	shared := store.NewMemory()
	a := startPlane(t, store.NewPrefixed(shared, "organisations/a"), controllink.NewPrivateKey())
	_, owner := a.client("Owner")
	otherPerson := a.invite(owner, "Bea", wire.UserRoleMember)
	member := otherPerson.window

	yes, no := true, false
	request := maturityCompletionRequest{FeatureID: "session-chat", ID: "completion-1", SessionID: "session-owner-1", BuildID: "build-a1", Success: &yes, Completed: &yes, Eligible: &yes, BackgroundRefresh: &no}
	var recorded maturity.Record
	must(t, json.Unmarshal(call(t, owner, "", "maturity/complete", request), &recorded))
	if recorded.Actor != maturity.ActorAlex || recorded.Successes != 1 {
		t.Fatalf("owner completion = %+v", recorded)
	}
	if recorded.CompletionID == request.ID || len(recorded.CompletionID) != 64 {
		t.Fatalf("raw source identity was stored: %q", recorded.CompletionID)
	}
	if recorded.Beta[0].Session != hashMaturityReference(request.SessionID) || recorded.Beta[0].Build != hashMaturityReference(request.BuildID) {
		t.Fatalf("owner gate evidence = %+v", recorded.Beta[0])
	}
	must(t, json.Unmarshal(call(t, owner, "", "maturity/complete", request), &recorded))
	if recorded.Successes != 1 {
		t.Fatalf("retry incremented the outcome: %+v", recorded)
	}
	if got, err := a.s.methods.maturity.Read(context.Background(), "session-chat"); err != nil || got == nil {
		t.Fatalf("organisation A record = %+v, err = %v", got, err)
	}
	other, err := maturity.New(store.NewPrefixed(shared, "organisations/b"), maturity.RegisteredFeatureIDs())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := other.Read(context.Background(), "session-chat"); err != nil || got != nil {
		t.Fatalf("organisation B saw A's record: %+v, err = %v", got, err)
	}

	request.ID, request.SessionID = "completion-2", "session-member-1"
	must(t, json.Unmarshal(call(t, member, "", "maturity/complete", request), &recorded))
	if recorded.Actor != maturity.ActorOther || recorded.GA.Person != hashMaturityReference(otherPerson.id) || recorded.GA.Sessions[0] != hashMaturityReference(request.SessionID) {
		t.Fatalf("authenticated member evidence = %+v", recorded.GA)
	}
	request.ID, request.SessionID = "completion-3", "session-member-2"
	request.Eligible = &no
	if got := callError(t, member, "", "maturity/complete", request); !strings.Contains(got, "eligible") {
		t.Fatalf("already-applied retry = %s", got)
	}
	request.Eligible = &yes
	request.BackgroundRefresh = &yes
	if got := callError(t, member, "", "maturity/complete", request); !strings.Contains(got, "eligible") {
		t.Fatalf("background refresh = %s", got)
	}
	request.BackgroundRefresh, request.Completed = &no, &no
	if got := callError(t, member, "", "maturity/complete", request); !strings.Contains(got, "eligible") {
		t.Fatalf("incomplete source operation = %s", got)
	}
	request.Completed = &yes
	request.FeatureID = "unknown"
	if got := callError(t, member, "", "maturity/complete", request); !strings.Contains(got, "eligible") {
		t.Fatalf("unknown feature = %s", got)
	}
	if _, err := a.s.methods.Handle("maturity/complete", mustJSON(t, request), Caller{}); err == nil {
		t.Fatal("unauthenticated completion was accepted")
	}

	// Unknown fields cannot smuggle an actor or customer content into the write API.
	params, _ := json.Marshal(map[string]any{"featureId": "session-chat", "completionId": "completion-4", "sessionId": "session-4", "buildId": "build-a1", "success": true,
		"completed": true, "eligible": true, "backgroundRefresh": false, "actor": "alex", "content": "customer text"})
	if _, err := a.s.methods.Handle("maturity/complete", params, Caller{Person: a.s.localOwner()}); err == nil {
		t.Fatal("caller supplied actor or content was accepted")
	}
}

func TestMaturityActorComesFromAuthenticatedPerson(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		person records.Person
		owner  string
		want   maturity.Actor
	}{
		{person: records.Person{ID: "owner", Kind: records.KindPerson}, owner: "owner", want: maturity.ActorAlex},
		{person: records.Person{ID: "member", Kind: records.KindPerson}, owner: "owner", want: maturity.ActorOther},
		{person: records.Person{ID: "agent", Kind: records.KindBot}, owner: "owner", want: maturity.ActorAgent},
	} {
		if got := maturityActor(&test.person, test.owner); got != test.want {
			t.Errorf("maturityActor(%+v) = %q, want %q", test.person, got, test.want)
		}
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
