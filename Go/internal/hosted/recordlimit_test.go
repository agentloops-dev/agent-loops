package hosted

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #6908: a record of the service's lists at its largest is taken, and one a byte over it is
// refused with the earlier one as it was.
func TestRecordLimitOfTheLists(t *testing.T) {
	ctx, st := context.Background(), store.NewMemory()
	must(t, putRecord(ctx, st, "k", make([]byte, recordMost), store.Always))
	if err := putRecord(ctx, st, "k", make([]byte, recordMost+1), store.Always); err == nil || !strings.Contains(err.Error(), "4096") {
		t.Fatal(err)
	}
	if o, err := st.Get(ctx, "k"); err != nil || len(o.Data) != recordMost {
		t.Fatal(errors.Join(err, errors.New("a refused write changed the record")))
	}
}

// An invite over the largest, from before there was one, does not stop the others' sweep.
func TestAnOldInviteOverTheLargestDoesNotStopTheSweep(t *testing.T) {
	ctx, st := context.Background(), store.NewMemory()
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	put(t, st, inviteKey("long"), Invite{Expires: now.Add(400 * 24 * time.Hour),
		Used: &Used{By: &SignIn{Provider: "github", Subject: strings.Repeat("x", recordMost)}}})
	put(t, st, inviteKey("recent"), Invite{Expires: now.Add(400 * 24 * time.Hour),
		Used: &Used{By: &SignIn{Provider: "github", Subject: strings.Repeat("x", recordMost)}, At: now.Add(-time.Hour)}})
	put(t, st, allowKey("old"), map[string]any{"at": now.Add(-2 * recordLife)})
	err := sweepRecords(ctx, st, now)
	if err != nil || has(t, st, inviteKey("long")) || !has(t, st, inviteKey("recent")) {
		t.Fatalf("the used invite goes %v after its use: %v", recordLife, err)
	}
	if has(t, st, allowKey("old")) {
		t.Fatalf("a login allowed 180 days ago is still there: %v", err)
	}
}
