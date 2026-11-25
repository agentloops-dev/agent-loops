package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const day = 24 * time.Hour

func putFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sweepFake is a running fake with these cookies, and these days since each site's last use.
func sweepFake(t *testing.T, store *fakeStore, idle map[string]int) (*fake, time.Time) {
	t.Helper()
	f, now := newFake(t), time.Now()
	withStore(t, f, store)
	mustStart(t, f.m)
	uses := siteUses{}
	for site, days := range idle {
		uses[site] = &siteUse{Used: now.Add(-time.Duration(days) * day)}
	}
	for _, c := range store.jar {
		if u := uses[SiteOf(c.Domain)]; u != nil {
			u.Expires = max(u.Expires, c.Expires)
		}
	}
	writeSites(f.m.Profile, uses)
	return f, now
}

func domains(s *fakeStore) []string {
	out := []string{}
	for _, c := range s.jar {
		out = append(out, c.Domain)
	}
	return out
}

func TestASiteNotUsedFor90DaysGoes(t *testing.T) {
	store := &fakeStore{jar: jar(".old.com", "old.com", ".used.com", ".open.com", ".renewed.com", ".new.com")}
	f, now := sweepFake(t, store, map[string]int{"old.com": 90, "used.com": 89, "open.com": 200, "renewed.com": 200, "visited.com": 200, "storage.com": 100})
	store.jar[4].Expires = float64(now.Add(day).Unix())
	store.open = []string{"https://www.open.com/inbox", "about:blank"}
	f.m.running.visits = siteUses{}
	f.m.running.visits.opened("https://visited.com:8443/x", now)
	uses := readSites(f.m.Profile)
	uses["storage.com"].Origins = []string{"https://app.storage.com"}
	writeSites(f.m.Profile, uses)

	f.m.sweepRunning(now)
	if got, want := domains(store), []string{".used.com", ".open.com", ".renewed.com", ".new.com"}; !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if got := strings.Join(store.cleared, " "); !strings.Contains(got, "https://old.com") || !strings.Contains(got, "https://app.storage.com") ||
		strings.Contains(got, "visited") {
		t.Fatal(got)
	}
	uses = readSites(f.m.Profile)
	for _, site := range []string{"open.com", "renewed.com", "visited.com", "new.com"} {
		if uses[site] == nil || now.Sub(uses[site].Used) > time.Minute {
			t.Error(site, uses[site])
		}
	}
	if uses["old.com"] != nil || uses["storage.com"] != nil {
		t.Error(uses)
	}
	// Once an hour.
	store.jar = jar(".old.com")
	store.cleared = nil
	f.m.sweepRunning(now.Add(30 * time.Minute))
	if store.cleared != nil {
		t.Fatal(store.cleared)
	}
}

func TestASiteThatCouldNotBeClearedIsTriedAgain(t *testing.T) {
	store := &fakeStore{jar: jar(".old.com"), refuse: errors.New("no tab")}
	f, now := sweepFake(t, store, map[string]int{"old.com": 91})
	f.m.sweepRunning(now)
	if len(store.jar) != 1 || readSites(f.m.Profile)["old.com"] == nil {
		t.Fatal("it was let go of")
	}
	store.refuse = nil
	f.m.sweepRunning(now.Add(sweepEvery))
	if len(store.jar) != 0 || readSites(f.m.Profile)["old.com"] != nil {
		t.Fatal("it was not tried again")
	}
}

func TestAPageOpenedSinceTheSweepChoseIsKept(t *testing.T) {
	store := &fakeStore{jar: jar(".old.com")}
	f, now := sweepFake(t, store, nil)
	p := f.m.running
	p.visits = siteUses{}
	p.visits.opened("https://old.com/", now)
	if err := f.m.clearIdle(context.Background(), p, "old.com", nil, store.jar); err != nil || len(store.jar) != 1 {
		t.Fatal(err, store.jar)
	}
}

func TestOverACountOrASizeTheOldestGoAndA30DaySiteStays(t *testing.T) {
	now := time.Now()
	uses := siteUses{}
	for i := range sitesKept + 5 {
		uses[fmt.Sprintf("s%03d.com", i)] = &siteUse{Used: now.Add(-40*day + time.Duration(i)*time.Second)}
	}
	if got := uses.expired(0, nil, now); len(got) != 5 || got[0] != "s000.com" || got[4] != "s004.com" {
		t.Fatal(got)
	}
	for _, u := range uses {
		u.Used = now.Add(-29 * day)
	}
	if got := uses.expired(siteBytes*2, map[string]int64{"s000.com": siteBytes}, now); got != nil {
		t.Fatal("a site in use went", got)
	}
	// Size: only as many as it takes, and only those that hold some.
	few := siteUses{"a.com": {Used: now.Add(-80 * day)}, "b.com": {Used: now.Add(-70 * day)}, "c.com": {Used: now.Add(-60 * day)}}
	if got := few.expired(siteBytes+10, map[string]int64{"b.com": 10, "c.com": 10}, now); !reflect.DeepEqual(got, []string{"b.com"}) {
		t.Fatal(got)
	}
}

func TestTheFileOfLastUsesIsBounded(t *testing.T) {
	profile, now := filepath.Join(t.TempDir(), "profile"), time.Now()
	uses := siteUses{}
	for i := range siteEntries + 10 {
		uses[fmt.Sprintf("s%04d.com", i)] = &siteUse{Used: now.Add(time.Duration(i) * time.Second)}
	}
	uses.opened("https://more.com", now)
	if uses["more.com"] != nil {
		t.Fatal("over the count")
	}
	for i := range 2 * siteOrigins {
		uses.opened(fmt.Sprintf("https://h%d.s0999.com/", i), now.Add(time.Hour))
	}
	writeSites(profile, uses)
	got := readSites(profile)
	if len(got) != siteEntries || got["s0009.com"] != nil || len(got["s0999.com"].Origins) != siteOrigins {
		t.Fatal(len(got))
	}
	for _, none := range []string{"about:blank", "chrome://settings", "file:///x", "https://" + strings.Repeat("a", 300) + ".com"} {
		if site, _ := siteOfURL(none); site != "" {
			t.Error(none, site)
		}
	}
	putFile(t, sitesFile(profile), 10)
	if len(readSites(profile)) != 0 {
		t.Fatal("a file that cannot be read")
	}
}

func TestTheDatabasesOnDiskNameTheirSites(t *testing.T) {
	profile := t.TempDir()
	db := filepath.Join(profile, "Default", "IndexedDB")
	putFile(t, filepath.Join(db, "https_mail.example.co.uk_0.indexeddb.leveldb", "1.log"), 100)
	putFile(t, filepath.Join(db, "https_example.co.uk_8443.indexeddb.blob", "1"), 11)
	putFile(t, filepath.Join(db, "chrome-extension_abc_0.indexeddb.leveldb", "1.log"), 5)
	got := storedSites(profile)
	if len(got) != 1 || got["example.co.uk"].bytes != 111 ||
		!reflect.DeepEqual(got["example.co.uk"].origins, []string{"https://example.co.uk:8443", "https://mail.example.co.uk"}) {
		t.Fatalf("%+v", got)
	}
}

func TestAProfileNotUsedFor90DaysGoes(t *testing.T) {
	f, now := newFake(t), time.Now()
	for _, p := range []string{f.m.Profile, f.m.ChromeProfile()} {
		putFile(t, filepath.Join(p, "Default", "Cookies"), 1)
		writeSites(p, siteUses{"a.com": {Used: now}})
	}
	old := now.Add(-91 * day)
	age := func(p string) {
		for _, path := range []string{filepath.Join(p, "Default", "Cookies"), filepath.Join(p, "Default"), p, sitesFile(p)} {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	age(f.m.Profile)
	there := func(p string) bool { _, err := os.Stat(p); return err == nil }

	// A browser that this machine did not start has it open.
	lock := filepath.Join(f.m.Profile, "SingletonLock")
	if err := os.Symlink(fmt.Sprintf("host-%d", os.Getpid()), lock); err != nil {
		t.Fatal(err)
	}
	age(f.m.Profile)
	f.m.Sweep(now)
	if !there(f.m.Profile) {
		t.Fatal("a profile that is open went")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	age(f.m.Profile)
	mustStart(t, f.m)
	f.m.Sweep(now.Add(91 * day))
	if !there(f.m.Profile) {
		t.Fatal("the running browser's profile went")
	}
	f.m.Stop()
	age(f.m.Profile)
	putFile(t, filepath.Join(f.m.ChromeProfile(), "Local State"), 1)
	f.m.Sweep(now)
	if there(f.m.Profile) || there(sitesFile(f.m.Profile)) || !there(f.m.ChromeProfile()) {
		t.Fatal("not just the idle profile went")
	}
}

func TestAnOverdueSiteStartsAStoppedBrowserOnceADay(t *testing.T) {
	store := &fakeStore{jar: jar(".old.com")}
	f, now := sweepFake(t, store, map[string]int{"old.com": 89})
	f.m.Stop()
	f.m.Sweep(now)
	if f.m.Running() {
		t.Fatal("started with nothing overdue")
	}
	f.m.Sweep(now.Add(2 * day))
	if !f.m.Running() {
		t.Fatal("not started")
	}
	f.m.sweepRunning(now.Add(2 * day))
	if len(store.jar) != 0 {
		t.Fatal(store.jar)
	}
	f.m.Stop()
	writeSites(f.m.Profile, siteUses{"older.com": {Used: now.Add(-100 * day)}})
	f.m.Sweep(now.Add(2*day + time.Hour))
	if f.m.Running() {
		t.Fatal("started twice in a day")
	}
}

func TestVisitsAreKeptWhenItStopsAndClearForgetsThem(t *testing.T) {
	store := &fakeStore{jar: jar(".a.com")}
	f, now := sweepFake(t, store, nil)
	f.m.running.visits = siteUses{}
	f.m.running.visits.opened("https://app.b.com/", now)
	f.m.Stop()
	if readSites(f.m.Profile)["b.com"] == nil {
		t.Fatal("the visits went with the browser")
	}
	if err := f.m.Clear(context.Background(), "b.com"); err != nil || len(store.jar) != 1 ||
		!reflect.DeepEqual(store.cleared, []string{"https://app.b.com"}) || readSites(f.m.Profile)["b.com"] != nil {
		t.Fatal(err, store.jar, store.cleared)
	}
}

func TestChromesDownloadsGoPastTheirLimit(t *testing.T) {
	profile := t.TempDir()
	model := filepath.Join(profile, "OptGuideOnDeviceModel", "weights.bin")
	kept := []string{filepath.Join(profile, "Default", "Cookies"), filepath.Join(profile, "Profile 1", "Cookies"), filepath.Join(profile, "Local State")}
	for _, p := range append(kept, model) {
		putFile(t, p, 100)
	}
	dropDownloads(profile, 100)
	if _, err := os.Stat(model); err != nil {
		t.Fatal("within the limit", err)
	}
	dropDownloads(profile, 99)
	if _, err := os.Stat(model); !os.IsNotExist(err) {
		t.Fatal("over the limit")
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
}

// TestARealSweep sweeps a real browser on a scratch profile: the one old site goes, cookies
// and storage, the other stays, and the pages that open are seen.
func TestARealSweep(t *testing.T) {
	exe, how := os.Getenv("AGENTS_TEST_GOOGLE_CHROME"), mode{chrome: true, headed: true}
	if exe == "" {
		t.Skip("set AGENTS_TEST_GOOGLE_CHROME to Google Chrome to start a real one")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "in=v; Max-Age=8640000; HttpOnly")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<script>localStorage.t = localStorage.t || "kept"</script>`))
	}))
	defer srv.Close()
	old, other := srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)

	f := newFake(t)
	f.m.chrome = func() string { return exe }
	f.m.launch = func(exe, profile string, _ []string, _ mode) (*process, error) {
		return launchChromium(exe, profile, os.Environ(), how)
	}
	mustStart(t, f.m)
	p, profile := f.m.running, f.m.ChromeProfile()
	ctx := p.store.(cdpStore).ctx
	stored := func(url string) string {
		t.Helper()
		if err := chromedp.Do(ctx, chromedp.Navigate(url)); err != nil {
			t.Fatal(err)
		}
		v, err := chromedp.Run(ctx, chromedp.Evaluate[string](`localStorage.t + performance.timeOrigin*0`))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	set := func(url, value string) {
		t.Helper()
		stored(url)
		if _, err := chromedp.Run(ctx, chromedp.Evaluate[string](`localStorage.t = "`+value+`"`)); err != nil {
			t.Fatal(err)
		}
	}
	set(old, "old")
	set(other, "other")
	if err := chromedp.Do(ctx, chromedp.Navigate("about:blank")); err != nil {
		t.Fatal(err)
	}
	seen := func() bool {
		f.m.sitesMu.Lock()
		defer f.m.sitesMu.Unlock()
		return p.visits["127.0.0.1"] != nil && p.visits["localhost"] != nil
	}
	for i := 0; i < 50 && !seen(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !seen() {
		t.Fatalf("the pages were not seen: %v", p.visits)
	}
	now := time.Now()
	f.m.sweepRunning(now)
	uses := readSites(profile)
	uses["127.0.0.1"].Used = now.Add(-91 * day)
	writeSites(profile, uses)
	if err := f.m.sweepSites(context.Background(), p, now); err != nil {
		t.Fatal(err)
	}
	var left []*network.Cookie
	left, err := p.store.cookies(context.Background())
	if err != nil || len(left) != 1 || left[0].Domain != "localhost" {
		t.Fatal(err, left)
	}
	if got := stored(old); !strings.HasPrefix(got, "kept") {
		t.Fatal("the old site's storage stayed", got)
	}
	if got := stored(other); !strings.HasPrefix(got, "other") {
		t.Fatal("the other site's storage went", got)
	}
}
