package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

func has(t *testing.T, st store.Store, key string) bool {
	t.Helper()
	o, err := st.Get(context.Background(), key)
	must(t, err)
	return o != nil
}

func put(t *testing.T, st store.Store, key string, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	_, err := st.Put(context.Background(), key, data, store.Always)
	must(t, err)
}

// fill puts n records under prefix.
func fill(t *testing.T, st store.Store, prefix string, n int) {
	t.Helper()
	for i := range n {
		put(t, st, fmt.Sprintf("%sx%d.json", prefix, i), map[string]any{})
	}
}

// #6782 to #6784, #6786: the sweep deletes what is past its age, and keeps the rest; a
// block, however old, and an account stay.
func TestTheRecordsSweepAgesOutInvitesAllowsAndTheWaitlist(t *testing.T) {
	ctx, st := context.Background(), store.NewMemory()
	start := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	now := start
	admin := Admin{Store: st, Domain: domain, Now: func() time.Time { return now }}
	s := &Service{cfg: Config{Store: st, Now: admin.Now, SignUpsPerAddress: 100, SignUpsPerHour: 100, Log: func(string) {}}}

	link, err := admin.Invite(ctx, 0)
	must(t, err)
	invite := inviteKey(link[strings.LastIndex(link, "=")+1:])
	put(t, st, inviteKey("long"), Invite{Expires: start.Add(400 * 24 * time.Hour)})
	must(t, admin.Allow(ctx, "ana"))
	must(t, admin.Allow(ctx, "kept"))
	waiter, late := gitHubSignIn(7, "sam"), SignIn{Provider: "apple", Subject: "a.1"}
	if res := s.wait(ctx, "addr", waiter, whyNoInvite, false, false); res.place != 1 {
		t.Fatalf("%+v", res)
	}
	orphan := waitlistKey(fmt.Sprintf("a%020d-abc", start.UnixNano()))
	put(t, st, orphan, waitlistLink{})
	_, err = admin.Block(ctx, "github:99", "spam")
	must(t, err)
	acc, _, err := admin.accounts().make(ctx, gitHubSignIn(5, "ana"))
	must(t, err)
	// Unreadable, or with no time: kept.
	put(t, st, allowKey("odd"), map[string]any{})

	// At 14 days the invite is expired; the rest stay.
	now = start.Add(InviteLife)
	must(t, sweepRecords(ctx, st, now))
	if has(t, st, invite) || !has(t, st, allowKey("ana")) || !has(t, st, waitingKey(waiter)) || !has(t, st, orphan) {
		t.Fatal("14 days")
	}
	var long Invite
	o, _ := st.Get(ctx, inviteKey("long"))
	if must(t, json.Unmarshal(o.Data, &long)); !long.Expires.Equal(now.Add(recordLife)) {
		t.Fatal(long.Expires)
	}
	// Allowed again, so its 90 days start again; another person joins the waitlist.
	must(t, admin.Allow(ctx, "kept"))
	s.wait(ctx, "addr", late, whyNoInvite, false, false)

	now = start.Add(recordLife)
	must(t, sweepRecords(ctx, st, now))
	if !has(t, st, allowKey("ana")) || !has(t, st, waitingKey(waiter)) {
		t.Fatal("at 90 days they are still there")
	}
	now = now.Add(time.Second)
	must(t, sweepRecords(ctx, st, now))
	if has(t, st, allowKey("ana")) || has(t, st, waitingKey(waiter)) || has(t, st, orphan) {
		t.Fatal("past 90 days")
	}
	if list, _ := admin.Waitlist(ctx); len(list) != 1 || list[0].SignIn != late {
		t.Fatalf("%+v", list)
	}
	// Someone who aged out joins again, at the back.
	if res := s.wait(ctx, "addr", waiter, whyNoInvite, false, false); res.place != 2 {
		t.Fatalf("%+v", res)
	}
	now = now.Add(1000 * 24 * time.Hour)
	must(t, sweepRecords(ctx, st, now))
	if got, _ := admin.accounts().get(ctx, acc.ID); got == nil || !has(t, st, blockedKey(gitHubSignIn(99, ""))) || !has(t, st, allowKey("odd")) {
		t.Fatal("an account, a block or a record with no time went")
	}
	if has(t, st, allowKey("kept")) || has(t, st, inviteKey("long")) || has(t, st, waitingKey(late)) {
		t.Fatal("the rest are still there")
	}
}

// #6782 to #6784: a new record past a list's count, or over the largest size, is refused;
// one that is there is replaced.
func TestTheListsRefuseOverTheirBounds(t *testing.T) {
	ctx, st := context.Background(), store.NewMemory()
	admin := Admin{Store: st, Domain: domain, Keeper: testKeeper}
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "most") {
			t.Fatalf("%s: %v", what, err)
		}
	}
	if _, err := admin.Invite(ctx, recordLife+time.Hour); err == nil {
		t.Fatal("an invite of more than 90 days was made")
	}
	fill(t, st, invitesFolder, invitesMost)
	_, err := admin.Invite(ctx, 0)
	refused("invite", err)

	must(t, admin.Allow(ctx, "ana"))
	fill(t, st, allowFolder, allowMost-1)
	must(t, admin.Allow(ctx, "ana"))
	refused("allow", admin.Allow(ctx, "bo"))

	// Emails do not count against the sign-ins.
	fill(t, st, blockedEmailFolder, blockedMost)
	_, err = admin.Block(ctx, "github:1", strings.Repeat("x", recordMost))
	refused("a large block", err)
	_, err = admin.Block(ctx, "github:1", "")
	must(t, err)
	refused("email", putEmailBlock(ctx, st, "mark", []byte("{}")))
	fill(t, st, blockedFolder, blockedMost-1)
	_, err = admin.Block(ctx, "github:1", "again")
	must(t, err)
	_, err = admin.Block(ctx, "github:2", "")
	refused("block", err)
}
