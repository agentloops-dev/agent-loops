package hosted

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// scrape reads the metrics as from remote.
func scrape(s *Service, remote string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	s.Metrics().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// #2226 FR-042: the metrics answer loopback and MetricsFrom only, never through the
// service's own port, and count accounts, sign-ups, refusals and requests.
func TestMetrics(t *testing.T) {
	st := store.NewMemory()
	s, newBrowser := startWith(t, Config{Store: st, SignUp: SignUpOpen,
		MetricsFrom: []netip.Prefix{netip.MustParsePrefix("172.30.62.1/32")}})
	b := newBrowser()
	b.signIn("good")
	acc, _ := s.accounts.bySignIn(context.Background(), gitHubSignIn(42, ""))
	if err := (Admin{Store: st}).Suspend(context.Background(), acc.ID, "spam"); err != nil {
		t.Fatal(err)
	}
	// The sweep stops its copy, as within a minute.
	s.sweepAccounts(context.Background())
	if res, _ := b.get("https://" + acc.ID + "." + domain + "/readyz"); res.StatusCode != http.StatusForbidden {
		t.Fatal(res.StatusCode)
	}
	if _, err := (Admin{Store: st}).Block(context.Background(), "7", "spam"); err != nil {
		t.Fatal(err)
	}
	newBrowser().signIn("u.7.bot.400")

	for _, remote := range []string{"127.0.0.1:4000", "[::1]:4000", "172.30.62.1:4000"} {
		if code, _ := scrape(s, remote); code != http.StatusOK {
			t.Fatal(remote, code)
		}
	}
	for _, remote := range []string{"172.30.62.250:4000", "203.0.113.5:4000", "nonsense"} {
		if code, _ := scrape(s, remote); code != http.StatusForbidden {
			t.Fatal(remote, code)
		}
	}
	_, text := scrape(s, "127.0.0.1:4000")
	for _, want := range []string{
		`agents_hosted_accounts{plan="free",state="suspended"} 1`,
		`agents_hosted_accounts{plan="pro",state="active"} 0`,
		"agents_hosted_waitlist 0",
		"agents_hosted_copies_running 0",
		`agents_hosted_signups_total{kind="account"} 1`,
		`agents_hosted_refusals_total{kind="blocked"} 1`,
		`agents_hosted_refusals_total{kind="suspended"} 1`,
		`agents_hosted_requests_total{status="303"}`,
		`agents_hosted_requests_total{status="403"}`,
		"# TYPE agents_hosted_signups_total counter",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %s in\n%s", want, text)
		}
	}
	// Not through the service's own port, which Caddy passes.
	if res, _ := b.get("https://" + domain + "/metrics"); res.StatusCode != http.StatusNotFound {
		t.Fatal(res.StatusCode)
	}
}

// A limit's refusal is counted by the limit.
func TestMetricsCountLimits(t *testing.T) {
	var s Service
	l := limited(plans[PlanFree])
	l.onRefuse = s.metrics.refused
	ctx := context.Background()
	for _, key := range []string{"v1/machines/a.json", "v1/machines/b.json", "v1/machines/c.json"} {
		_, _ = l.Put(ctx, key, []byte(`{}`), store.Absent)
	}
	if s.metrics.refusals["machines"] != 1 {
		t.Fatal(s.metrics.refusals)
	}
}

// #3123: N+2 backups with --keep N leave the newest N, and nothing else in the folder goes.
func TestPruneBackupsKeepsTheNewest(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	a := Admin{Store: st}
	if _, _, err := a.accounts().make(ctx, gitHubSignIn(1, "ana")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	other := filepath.Join(root, "notes")
	if err := os.MkdirAll(filepath.Join(other, "abcdefghij"), 0o700); err != nil {
		t.Fatal(err)
	}
	const keep = 3
	days := []string{"2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04", "2026-10-05"}
	for _, day := range days {
		dir := filepath.Join(root, day)
		if _, err := a.Backup(ctx, dir, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := a.PruneBackups(ctx, dir, keep); err != nil {
			t.Fatal(err)
		}
	}
	var left []string
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if want := append(append([]string{}, days[len(days)-keep:]...), "notes"); strings.Join(left, " ") != strings.Join(want, " ") {
		t.Fatal(left)
	}
	// A backup named before the newest keeps them, and is never deleted itself.
	again := filepath.Join(root, "2026-09-30")
	if _, err := a.Backup(ctx, again, nil); err != nil {
		t.Fatal(err)
	}
	if gone, err := a.PruneBackups(ctx, again, keep); err != nil || len(gone) != 0 {
		t.Fatal(gone, err)
	}
	if _, err := os.Stat(again); err != nil {
		t.Fatal("deleted the backup just made:", err)
	}
	if _, err := a.PruneBackups(ctx, again, 0); err == nil {
		t.Fatal("kept no backup")
	}
}

// #6787: a copy goes at 90 days, but never the newest of an account that is still here, also
// past the count; a deleted account's goes, and one that fails does not stop the others.
func TestPruneBackupsByAgeKeepsEachAccountsNewest(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := store.NewMemory()
	a := Admin{Store: st, Now: func() time.Time { return now }}
	ana, _, _ := a.accounts().make(ctx, gitHubSignIn(1, "ana"))
	bo, _, _ := a.accounts().make(ctx, gitHubSignIn(2, "bo"))
	cy, _, _ := a.accounts().make(ctx, gitHubSignIn(3, "cy"))
	root := t.TempDir()
	first, second := filepath.Join(root, "2026-01-01"), filepath.Join(root, "2026-01-02")
	for _, dir := range []string{first, second} {
		if _, err := a.Backup(ctx, dir, nil); err != nil {
			t.Fatal(err)
		}
	}
	// What a cut-off backup left, and a folder of the operator's own.
	for _, f := range []string{"2026-01-03/" + ana.ID + "/.records-1", "mine/" + ana.ID + "/notes.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.accounts().remove(ctx, cy); err != nil {
		t.Fatal(err)
	}
	there := func(dir, id string) bool {
		_, err := os.Stat(filepath.Join(dir, id))
		return err == nil
	}
	now = now.Add(89 * 24 * time.Hour)
	last := filepath.Join(root, "2026-04-01")
	done, err := a.Backup(ctx, last, []string{"nosuchacct", ana.ID})
	if err == nil || len(done) != 1 || done[0].ID != ana.ID || done[0].Bytes == 0 {
		t.Fatal(done, err)
	}
	if gone, err := a.PruneBackups(ctx, last, 7); err != nil || len(gone) != 0 {
		t.Fatal(gone, err)
	}
	now = now.Add(2 * 24 * time.Hour)
	if _, err := a.PruneBackups(ctx, last, 7); err != nil {
		t.Fatal(err)
	}
	if there(root, "2026-01-01") || there(second, ana.ID) || there(second, cy.ID) || there(root, "2026-01-03") {
		t.Fatal("a copy past 90 days stayed")
	}
	// The account with no newer copy keeps this one, also past the count.
	if _, err := a.PruneBackups(ctx, last, 1); err != nil {
		t.Fatal(err)
	}
	if !there(second, bo.ID) || !there(last, ana.ID) || !there(root, "mine") {
		t.Fatal("the newest copy of an account, or what is not a backup, went")
	}
}

// #2226 FR-041: a backup of one account restores only that account's records, and only
// once it is suspended; a deleted account comes back whole.
func TestBackupAndRestoreOneAccount(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	now := time.Now()
	a := Admin{Store: st, Now: func() time.Time { return now }}
	later := func() { now = now.Add(2 * time.Minute) }
	ana, bo, dir := backUpTwoAccounts(t, a, st)

	// Both change after the backup.
	for _, id := range []string{ana.ID, bo.ID} {
		p := accountStore(st, id)
		_, _ = p.Put(ctx, "v1/hosts/a.json", []byte("changed"), store.Always)
		_ = p.Delete(ctx, "v1/clients/b.json")
		_, _ = p.Put(ctx, "v1/hosts/new.json", []byte("new"), store.Absent)
	}
	restoreOnlyOnceSuspended(t, a, dir, ana.ID, later)
	recordIs(t, st, ana.ID, "v1/hosts/a.json", ana.ID+"v1/hosts/a.json")
	recordIs(t, st, ana.ID, "v1/clients/b.json", ana.ID+"v1/clients/b.json")
	recordIs(t, st, ana.ID, "v1/hosts/new.json", "")
	// The other account is as it was.
	recordIs(t, st, bo.ID, "v1/hosts/a.json", "changed")
	recordIs(t, st, bo.ID, "v1/clients/b.json", "")
	recordIs(t, st, bo.ID, "v1/hosts/new.json", "new")
	if acc, _ := a.accounts().get(ctx, ana.ID); acc == nil || acc.Suspended == nil {
		t.Fatal("the restore changed the account's record")
	}

	again := deletedAccountComesBack(t, a, st, dir, bo)
	operatorDeletesSuspendedOnly(t, a, st, again, later)
}

// backUpTwoAccounts makes two accounts with three records each and backs them up to a
// new folder, which it gives.
func backUpTwoAccounts(t *testing.T, a Admin, st store.Store) (ana, bo *Account, dir string) {
	t.Helper()
	ctx := context.Background()
	ana, _, _ = a.accounts().make(ctx, gitHubSignIn(1, "ana"))
	bo, _, _ = a.accounts().make(ctx, gitHubSignIn(2, "bo"))
	for _, id := range []string{ana.ID, bo.ID} {
		for _, k := range []string{"v1/hosts/a.json", "v1/clients/b.json", "v1/keys/control.sealed"} {
			if _, err := accountStore(st, id).Put(ctx, k, []byte(id+k), store.Absent); err != nil {
				t.Fatal(err)
			}
		}
	}
	dir = t.TempDir()
	done, err := a.Backup(ctx, dir, nil)
	if err != nil || len(done) != 2 {
		t.Fatal(done, err)
	}
	if info, err := os.Stat(filepath.Join(dir, ana.ID, "records.jsonl")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}
	return ana, bo, dir
}

// restoreOnlyOnceSuspended restores id: refused while it runs and just after its
// suspension, done once later has moved the clock on.
func restoreOnlyOnceSuspended(t *testing.T, a Admin, dir, id string, later func()) {
	t.Helper()
	ctx := context.Background()
	if _, err := a.Restore(ctx, dir, id); err == nil || !strings.Contains(err.Error(), "suspend it first") {
		t.Fatal("restored a running account:", err)
	}
	if err := a.Suspend(ctx, id, "restoring"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(ctx, dir, id); err == nil || !strings.Contains(err.Error(), "try again in a minute") {
		t.Fatal("restored over a copy that may still run:", err)
	}
	later()
	if n, err := a.Restore(ctx, dir, id); err != nil || n != 3 {
		t.Fatal(n, err)
	}
}

// recordIs checks an account's record holds want, or is not there when want is "".
func recordIs(t *testing.T, st store.Store, id, key, want string) {
	t.Helper()
	o, _ := accountStore(st, id).Get(context.Background(), key)
	if want == "" && o != nil || want != "" && (o == nil || string(o.Data) != want) {
		t.Fatalf("%s %s: %v", id, key, o)
	}
}

// deletedAccountComesBack restores a deleted account, record and link; and not when its
// user has another account, which it gives.
func deletedAccountComesBack(t *testing.T, a Admin, st store.Store, dir string, bo *Account) *Account {
	t.Helper()
	ctx := context.Background()
	// A deleted account comes back, record and link; not when its user has another.
	if err := a.accounts().remove(ctx, bo); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(ctx, dir, bo.ID); err != nil {
		t.Fatal(err)
	}
	if acc, _ := a.accounts().bySignIn(ctx, gitHubSignIn(2, "")); acc == nil || acc.ID != bo.ID {
		t.Fatal("the deleted account did not come back", acc)
	}
	recordIs(t, st, bo.ID, "v1/hosts/a.json", bo.ID+"v1/hosts/a.json")
	_ = a.accounts().remove(ctx, bo)
	again, _, _ := a.accounts().make(ctx, gitHubSignIn(2, "bo"))
	if _, err := a.Restore(ctx, dir, bo.ID); err == nil || !strings.Contains(err.Error(), again.ID) {
		t.Fatal("restored over the user's new account:", err)
	}
	if _, err := a.Restore(ctx, dir, "zzzzzzzzzz"); err == nil {
		t.Fatal("restored an account with no backup")
	}
	return again
}

// operatorDeletesSuspendedOnly deletes an account: refused while it runs, and then all
// its records and its sign-in go.
func operatorDeletesSuspendedOnly(t *testing.T, a Admin, st store.Store, acc *Account, later func()) {
	t.Helper()
	ctx := context.Background()
	if err := a.Delete(ctx, acc.ID); err == nil || !strings.Contains(err.Error(), "suspend it first") {
		t.Fatal(err)
	}
	_ = a.Suspend(ctx, acc.ID, "deleting")
	later()
	_, _ = accountStore(st, acc.ID).Put(ctx, "v1/hosts/x.json", []byte("x"), store.Absent)
	if err := a.Delete(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if keys, _ := accountStore(st, acc.ID).List(ctx, ""); len(keys) != 0 {
		t.Fatal("records left:", keys)
	}
	if got, _ := a.accounts().bySignIn(ctx, gitHubSignIn(2, "")); got != nil {
		t.Fatal("the account is still there")
	}
}

// #3106 P20261031-6: a scrape reads no store while the sweep's counts are fresh; the
// sweep counts again, and a scrape counts when the sweep is late.
func TestMetricsReadNoStorePerScrape(t *testing.T) {
	ctx := context.Background()
	st := &countingStore{Store: store.NewMemory()}
	clk := &clock{now: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	s, _ := startWith(t, Config{Store: st, SignUp: SignUpOpen, Now: clk.Now})
	for i := range 3 {
		if _, _, err := s.accounts.make(ctx, gitHubSignIn(int64(600 + i), "u")); err != nil {
			t.Fatal(err)
		}
	}
	accounts := func(text string, n int) {
		t.Helper()
		if want := fmt.Sprintf(`agents_hosted_accounts{plan="free",state="active"} %d`, n); !strings.Contains(text, want) {
			t.Fatalf("no %s in\n%s", want, text)
		}
	}
	s.sweepCounts(ctx)
	gets, lists := st.gets.Load(), st.lists.Load()
	for range 4 {
		_, text := scrape(s, "127.0.0.1:4000")
		accounts(text, 3)
	}
	if st.gets.Load() != gets || st.lists.Load() != lists {
		t.Fatalf("scrapes read the store: %d gets, %d lists", st.gets.Load()-gets, st.lists.Load()-lists)
	}

	if _, _, err := s.accounts.make(ctx, gitHubSignIn(700, "u")); err != nil {
		t.Fatal(err)
	}
	s.sweepCounts(ctx)
	_, text := scrape(s, "127.0.0.1:4000")
	accounts(text, 4)

	// No sweep for countsLife: the scrape counts itself.
	if _, _, err := s.accounts.make(ctx, gitHubSignIn(701, "u")); err != nil {
		t.Fatal(err)
	}
	clk.add(countsLife)
	_, text = scrape(s, "127.0.0.1:4000")
	accounts(text, 5)
}

// #7094: an account's record has a largest size, and one at it can still change back.
func TestAnAccountsRecordHasALargestSize(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	accs := &accounts{store: st, now: time.Now}
	acc, _, err := accs.make(ctx, gitHubSignIn(7, "ana"))
	if err != nil {
		t.Fatal(err)
	}
	admin := Admin{Store: st}
	if err := admin.Suspend(ctx, acc.ID, strings.Repeat("x", accountRecordMost)); err == nil {
		t.Fatal("a record past the most was written")
	}
	if err := admin.Suspend(ctx, acc.ID, strings.Repeat("x", recordMost)); err != nil {
		t.Fatal(err)
	}
	if err := admin.Unsuspend(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
}
