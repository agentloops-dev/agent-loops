package hosted

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/hosted/billing"
)

// ask is what an account's address answers to a request, with its refusal.
func ask(s *Service, id string) (int, string, string) {
	req := httptest.NewRequest(http.MethodGet, "https://"+id+"."+domain+"/readyz", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w.Code, w.Header().Get("Agents-Refusal"), w.Body.String()
}

func account(t *testing.T, s *Service, id string) *Account {
	t.Helper()
	acc, err := s.accounts.get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func sweepIdle(t *testing.T, s *Service, now *clock) {
	t.Helper()
	if err := s.sweepIdleAccounts(context.Background(), now.Now()); err != nil {
		t.Fatal(err)
	}
}

// #6785: a free account unused for 90 days is paused, and its address says so with the
// delete date; 90 days later it is deleted, and its address says gone for 90 days, then
// nothing.
func TestAnIdleAccountIsPausedThenDeleted(t *testing.T) {
	s, st, now, ids := idleService(t, 1)
	id := ids[0]
	if got := account(t, s, id).LastUsed; got != dayOf(now.Now()) {
		t.Fatalf("a new account's last use is %q", got)
	}
	now.add(idleAfter - day)
	sweepIdle(t, s, now)
	if account(t, s, id).Suspended != nil {
		t.Fatal("paused before 90 days")
	}
	now.add(day)
	sweepIdle(t, s, now)
	acc := account(t, s, id)
	if !idlePaused(acc) || acc.Suspended.Deletes == nil || !acc.Suspended.Deletes.Equal(now.Now().Add(pausedFor)) {
		t.Fatalf("not paused for idle: %+v", acc.Suspended)
	}
	if s.running() != 0 {
		t.Fatal("its copy still runs")
	}
	code, refusal, body := ask(s, id)
	if code != http.StatusForbidden || refusal != "suspended" ||
		!strings.Contains(body, "nobody used it for 90 days") || !strings.Contains(body, shownDay(*acc.Suspended.Deletes)) {
		t.Fatalf("paused: %d %q %q", code, refusal, body)
	}
	if len(body) > 200 {
		t.Fatalf("the line is %d characters", len(body))
	}

	now.add(pausedFor - time.Hour)
	sweepIdle(t, s, now)
	if account(t, s, id) == nil {
		t.Fatal("deleted before its date")
	}
	now.add(time.Hour)
	sweepIdle(t, s, now)
	if account(t, s, id) != nil {
		t.Fatal("not deleted on its date")
	}
	if keys, _ := st.List(context.Background(), accountsPrefix+id+"/"); len(keys) != 0 {
		t.Fatalf("its store is left: %d records", len(keys))
	}
	if o, _ := st.Get(context.Background(), signInKey(gitHubSignIn(1000, ""))); o != nil {
		t.Fatal("its sign-in link is left")
	}
	g, err := s.gone(context.Background(), id)
	if err != nil || g == nil || g.Why != goneIdle || g.Deleted != dayOf(now.Now()) {
		t.Fatalf("tombstone %+v %v", g, err)
	}
	code, refusal, body = ask(s, id)
	if code != http.StatusGone || refusal != "gone" || !strings.Contains(body, "connect them to another control plane") {
		t.Fatalf("gone: %d %q %q", code, refusal, body)
	}
	if len(body) > 200 {
		t.Fatalf("the line is %d characters", len(body))
	}

	now.add(goneFor + day)
	sweepIdle(t, s, now)
	if code, refusal, _ := ask(s, id); code != http.StatusNotFound || refusal != "" {
		t.Fatalf("after the tombstone: %d %q", code, refusal)
	}
}

// An account from before LastUsed starts its clock on the day the sweep first sees it,
// not on its sign-up.
func TestAnOldAccountsClockStartsAtTheSweep(t *testing.T) {
	s, _, now, ids := idleService(t, 1)
	if _, err := s.accounts.update(context.Background(), ids[0], func(a *Account) { a.LastUsed = "" }); err != nil {
		t.Fatal(err)
	}
	now.add(400 * day)
	sweepIdle(t, s, now)
	acc := account(t, s, ids[0])
	if acc.Suspended != nil || acc.LastUsed != dayOf(now.Now()) {
		t.Fatalf("%+v", acc)
	}
}

// A paid account is never paused, and one paid while paused runs again. The operator's
// suspension is neither lifted nor deleted.
func TestPaidAndSuspendedAccountsStay(t *testing.T) {
	s, _, now, ids := idleService(t, 3)
	ctx := context.Background()
	pro, paused, suspended := ids[0], ids[1], ids[2]
	if _, err := s.accounts.update(ctx, pro, func(a *Account) { a.Plan = PlanPro }); err != nil {
		t.Fatal(err)
	}
	if err := (Admin{Store: s.cfg.Store, Now: now.Now}).Suspend(ctx, suspended, "abuse"); err != nil {
		t.Fatal(err)
	}
	now.add(idleAfter)
	sweepIdle(t, s, now)
	if account(t, s, pro).Suspended != nil {
		t.Fatal("a paid account was paused")
	}
	if !idlePaused(account(t, s, paused)) {
		t.Fatal("a free account was not paused")
	}
	if _, err := s.accounts.update(ctx, paused, func(a *Account) { a.Plan = PlanPro }); err != nil {
		t.Fatal(err)
	}
	now.add(pausedFor + idleAfter)
	sweepIdle(t, s, now)
	if acc := account(t, s, paused); acc == nil || acc.Suspended != nil || acc.LastUsed != dayOf(now.Now()) {
		t.Fatalf("paid while paused: %+v", acc)
	}
	if acc := account(t, s, suspended); acc == nil || acc.Suspended == nil || acc.Suspended.Idle {
		t.Fatalf("the operator's suspension: %+v", acc)
	}
}

// IdleAccounts off does nothing to accounts; pause pauses and never deletes.
func TestIdleAccountsModes(t *testing.T) {
	for _, mode := range []string{IdleOff, IdlePause} {
		s, _, now, ids := idleService(t, 1)
		s.cfg.IdleAccounts = mode
		now.add(idleAfter)
		sweepIdle(t, s, now)
		if got := idlePaused(account(t, s, ids[0])); got != (mode == IdlePause) {
			t.Fatalf("%s: paused %v", mode, got)
		}
		now.add(pausedFor)
		sweepIdle(t, s, now)
		if account(t, s, ids[0]) == nil {
			t.Fatalf("%s: deleted", mode)
		}
	}
	if _, err := New(context.Background(), Config{Store: store.NewMemory(), Keeper: testKeeper, Domain: domain, IdleAccounts: "later"}); err == nil {
		t.Fatal("an unknown mode was taken")
	}
}

// One sweep deletes at most idleDeletesMost accounts; the next takes the rest.
func TestTheIdleSweepDeletesTenAtMost(t *testing.T) {
	s, _, now, ids := idleService(t, idleDeletesMost+2)
	now.add(idleAfter)
	sweepIdle(t, s, now)
	now.add(pausedFor)
	sweepIdle(t, s, now)
	n, _ := s.accounts.count(context.Background())
	if n != 2 {
		t.Fatalf("%d left of %d after one sweep, want 2", n, len(ids))
	}
	sweepIdle(t, s, now)
	if n, _ := s.accounts.count(context.Background()); n != 0 {
		t.Fatalf("%d left after two sweeps", n)
	}
}

// The owner's sign-in lifts an idle pause and is a use; it lifts no operator's suspension.
func TestASignInKeepsAPausedAccount(t *testing.T) {
	s, newBrowser := startWith(t, Config{Store: store.NewMemory(), SignUp: SignUpOpen})
	b := newBrowser()
	b.signIn("good")
	ctx := context.Background()
	acc, err := s.accounts.bySignIn(ctx, gitHubSignIn(42, ""))
	if err != nil || acc == nil {
		t.Fatal("no account", err)
	}
	deletes := time.Now().Add(pausedFor)
	if _, err := s.accounts.update(ctx, acc.ID, func(a *Account) {
		a.LastUsed = "2020-01-01"
		a.Suspended = &Suspension{Reason: idleReason, At: time.Now(), Idle: true, Deletes: &deletes}
	}); err != nil {
		t.Fatal(err)
	}
	b.signIn("good")
	if got := account(t, s, acc.ID); got.Suspended != nil || got.LastUsed != dayOf(time.Now()) {
		t.Fatalf("after a sign-in: %+v", got)
	}
	if _, err := s.accounts.update(ctx, acc.ID, func(a *Account) {
		a.Suspended = &Suspension{Reason: "abuse", At: time.Now()}
	}); err != nil {
		t.Fatal(err)
	}
	b.signIn("good")
	if got := account(t, s, acc.ID); got.Suspended == nil {
		t.Fatal("a sign-in lifted the operator's suspension")
	}
}

// Unsuspend lifts an idle pause too, and is a use today.
func TestUnsuspendIsAUse(t *testing.T) {
	s, _, now, ids := idleService(t, 1)
	now.add(idleAfter)
	sweepIdle(t, s, now)
	if err := (Admin{Store: s.cfg.Store, Now: now.Now}).Unsuspend(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	if acc := account(t, s, ids[0]); acc.Suspended != nil || acc.LastUsed != dayOf(now.Now()) {
		t.Fatalf("%+v", acc)
	}
}

// A use is written once a day for each account, however many sockets come.
func TestAUseIsWrittenOnceADay(t *testing.T) {
	s, _, now, ids := idleService(t, 1)
	ctx := context.Background()
	id := ids[0]
	old := func() {
		if _, err := s.accounts.update(ctx, id, func(a *Account) { a.LastUsed = "2020-01-01" }); err != nil {
			t.Fatal(err)
		}
	}
	old()
	s.markUsed(ctx, id)
	if got := account(t, s, id).LastUsed; got != dayOf(now.Now()) {
		t.Fatalf("first use: %q", got)
	}
	old()
	s.markUsed(ctx, id)
	if got := account(t, s, id).LastUsed; got != "2020-01-01" {
		t.Fatalf("written twice in a day: %q", got)
	}
	now.add(day)
	s.markUsed(ctx, id)
	if got := account(t, s, id).LastUsed; got != dayOf(now.Now()) {
		t.Fatalf("next day: %q", got)
	}
}

// The idle clock starts on the day a paid plan ends.
func TestAPlansEndIsAUse(t *testing.T) {
	s, _, now, ids := idleService(t, 1)
	ctx := context.Background()
	if _, err := s.accounts.update(ctx, ids[0], func(a *Account) {
		a.Plan, a.StripeSubscription, a.LastUsed = PlanPro, "sub_1", "2020-01-01"
	}); err != nil {
		t.Fatal(err)
	}
	now.add(400 * day)
	var e billing.Event
	e.Type = billing.SubscriptionDeleted
	e.Data.Object = billing.Object{ID: "sub_1", Metadata: map[string]string{"account": ids[0]}}
	if err := s.actOn(ctx, e); err != nil {
		t.Fatal(err)
	}
	if acc := account(t, s, ids[0]); acc.Plan != PlanFree || acc.LastUsed != dayOf(now.Now()) {
		t.Fatalf("%+v", acc)
	}
}

// Delete Account leaves a tombstone too, and a restore takes it away.
func TestDeleteAccountLeavesATombstone(t *testing.T) {
	s, _, now, ids := idleService(t, 1)
	ctx := context.Background()
	acc := account(t, s, ids[0])
	if err := s.removeAccount(ctx, acc, goneDeleted); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.gone(ctx, ids[0]); g == nil || g.Why != goneDeleted {
		t.Fatalf("%+v", g)
	}
	data, _ := json.Marshal(acc)
	if err := (Admin{Store: s.cfg.Store, Now: now.Now}).relinkDeleted(ctx, acc.ID, *acc, data); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.gone(ctx, ids[0]); g != nil {
		t.Fatal("a restored account still says gone")
	}
}

// The tombstones are bounded by count too: the oldest past goneMost go.
func TestTombstonesAreBounded(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range goneMost + 5 {
		id := string(rune('a'+i%26)) + strings.Repeat("0", 5) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)) + "00"
		if err := writeGone(ctx, st, id, goneIdle, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sweepGone(ctx, st, now.Add(day)); err != nil {
		t.Fatal(err)
	}
	if keys, _ := st.List(ctx, goneFolder); len(keys) != goneMost {
		t.Fatalf("%d tombstones, want %d", len(keys), goneMost)
	}
}
