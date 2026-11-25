package browser

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// The bounds of the kept profiles (#6710, docs/explanation/data-stores.md). Chromium has its
// own: at most 3,300 cookies of 4 KB, the one used longest ago out first; cookies of 400 days;
// browsing history of 90 days; and the HTTP cache (diskCacheBytes). It has none by a site's
// last use, so agentsd writes that down beside each profile and clears a site, with its
// sign-in, through the running browser. Nothing here edits a profile that a browser has open.
const (
	// siteAge is how long a site stays after its last use: a page of it opened, or a cookie
	// of it renewed (a sign-in by a redirect opens no page).
	siteAge = store.MaxAge
	// A site used in this time never goes for the count or the size, as Chromium spares its
	// cookies: a site in use is not signed out. So a profile can be over a limit for a time.
	siteSpared = 30 * 24 * time.Hour
	// sitesKept is the most sites a profile keeps.
	sitesKept = 200
	// siteBytes is the most its data holds: all that is in Default.
	siteBytes = 256 << 20
	// downloadBytes is the most that Chrome's own downloads (models, lists) hold before a
	// start: past it they all go, and Chrome fetches again what it needs.
	downloadBytes = 1 << 30
	// The file of last uses: its sites, and each site's origins.
	siteEntries   = 1000
	siteOrigins   = 20
	maxSiteHost   = 253
	maxSiteOrigin = 300
	sweepEvery    = time.Hour
	// wakeEvery is how often a sweep starts the browser to clear a site past its age.
	wakeEvery = 24 * time.Hour
)

// siteUse is what is kept of one site of a profile.
type siteUse struct {
	// Used is its last use.
	Used time.Time `json:"used"`
	// Expires is the latest expiry of its cookies, in seconds: a later one is a renewal.
	Expires float64 `json:"expires,omitempty"`
	// Origins are its pages' own, whose storage goes with it.
	Origins []string `json:"origins,omitempty"`
}

func (u *siteUse) add(origin string) {
	if !slices.Contains(u.Origins, origin) && len(u.Origins) < siteOrigins && len(origin) <= maxSiteOrigin {
		u.Origins = append(u.Origins, origin)
	}
}

type siteUses map[string]*siteUse

// at is the site's entry; a site not seen before counts as used now.
func (u siteUses) at(site string, now time.Time) *siteUse {
	if u[site] == nil {
		u[site] = &siteUse{Used: now}
	}
	return u[site]
}

// opened is a use of a page's site, when it has one.
func (u siteUses) opened(page string, now time.Time) {
	site, origin := siteOfURL(page)
	if site == "" || u[site] == nil && len(u) >= siteEntries {
		return
	}
	e := u.at(site, now)
	e.Used = now
	e.add(origin)
}

func siteOfURL(page string) (site, origin string) {
	p, err := url.Parse(page)
	if err != nil || p.Scheme != "https" && p.Scheme != "http" || p.Hostname() == "" || len(p.Hostname()) > maxSiteHost {
		return "", ""
	}
	return SiteOf(p.Hostname()), p.Scheme + "://" + strings.ToLower(p.Host)
}

// sitesFile is a profile's last uses, beside it.
func sitesFile(profile string) string { return profile + "-sites.json" }

// readSites is a profile's last uses; a file that cannot be read is none.
func readSites(profile string) siteUses {
	uses := siteUses{}
	b, err := os.ReadFile(sitesFile(profile))
	if err == nil {
		err = json.Unmarshal(b, &uses)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("browser: couldn't read the sites' last uses: %v", err)
		return siteUses{}
	}
	for site, u := range uses {
		if u == nil {
			delete(uses, site)
		}
	}
	return uses
}

func writeSites(profile string, uses siteUses) {
	if over := len(uses) - siteEntries; over > 0 {
		for _, site := range uses.oldest()[:over] {
			delete(uses, site)
		}
	}
	b, err := json.Marshal(uses)
	if err == nil {
		// store-bounds: kind=key-value; count=1000; size=7KiB a site; age=90d; applied=on write (count), and the hourly sweep
		err = store.WriteAtomically(sitesFile(profile), b, 0o600)
	}
	if err != nil {
		log.Printf("browser: couldn't keep the sites' last uses: %v", err)
	}
}

// oldest is every site, the one used longest ago first.
func (u siteUses) oldest() []string {
	sites := make([]string, 0, len(u))
	for s := range u {
		sites = append(sites, s)
	}
	slices.SortFunc(sites, func(a, b string) int { return cmp.Or(u[a].Used.Compare(u[b].Used), cmp.Compare(a, b)) })
	return sites
}

// expired is the sites to clear: each past its age, then, over the count or the size, the
// ones used longest ago, but none used in siteSpared. held is the bytes of the site data, and
// sizes each site's own: for size, only a site with a size goes, as clearing another frees nothing.
func (u siteUses) expired(held int64, sizes map[string]int64, now time.Time) []string {
	var out []string
	left := len(u)
	for _, s := range u.oldest() {
		idle := now.Sub(u[s].Used)
		if idle >= siteAge || idle >= siteSpared && (left > sitesKept || held > siteBytes && sizes[s] > 0) {
			out = append(out, s)
			left--
			held -= sizes[s]
		}
	}
	return out
}

// follow writes down each page that p opens, until it ends.
func (m *Machine) follow(p *process) {
	p.watch(func(page string) {
		m.sitesMu.Lock()
		defer m.sitesMu.Unlock()
		if p.visits == nil {
			p.visits = siteUses{}
		}
		p.visits.opened(page, time.Now())
	})
}

// take adds p's visits to uses. Under sitesMu.
func (p *process) take(uses siteUses) {
	for site, v := range p.visits {
		e := uses.at(site, v.Used)
		e.Used = v.Used
		for _, o := range v.Origins {
			e.add(o)
		}
	}
	p.visits = nil
}

// keepVisits writes down what p opened since the last sweep.
func (m *Machine) keepVisits(p *process) {
	m.sitesMu.Lock()
	defer m.sitesMu.Unlock()
	if len(p.visits) == 0 {
		return
	}
	uses := readSites(m.profileOf(p))
	p.take(uses)
	writeSites(m.profileOf(p), uses)
}

// sweepRunning holds the running browser's profile to its bounds, once an hour.
func (m *Machine) sweepRunning(now time.Time) {
	m.mu.Lock()
	p := m.running
	due := p != nil && p.store != nil && now.Sub(p.swept) >= sweepEvery
	if due {
		p.swept = now
	}
	m.mu.Unlock()
	if !due {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.sweepSites(ctx, p, now); err != nil {
		log.Printf("browser: couldn't sweep the sites: %v", err)
	}
}

// sweepSites brings p's sites under their bounds, through the browser.
func (m *Machine) sweepSites(ctx context.Context, p *process, now time.Time) error {
	profile := m.profileOf(p)
	jar, err := p.store.cookies(ctx)
	if err != nil {
		return err
	}
	pages, err := p.store.pages(ctx)
	if err != nil {
		return err
	}
	stored := storedSites(profile)
	held := bytesUnder(filepath.Join(profile, "Default"))

	m.sitesMu.Lock()
	uses := readSites(profile)
	p.take(uses)
	uses.fold(jar, pages, stored, now)
	sizes := map[string]int64{}
	for site, s := range stored {
		sizes[site] = s.bytes
	}
	gone := uses.expired(held, sizes, now)
	writeSites(profile, uses)
	m.sitesMu.Unlock()

	var errs []error
	for _, site := range gone {
		if err := m.clearIdle(ctx, p, site, uses[site].Origins, jar); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", site, err))
		}
	}
	if len(gone) > 0 {
		log.Printf("browser: cleared %d sites past their limits", len(gone)-len(errs))
	}
	if n := len(uses) - len(gone); n > sitesKept || held > siteBytes {
		log.Printf("browser: %d sites and %d MB of site data, over a limit; a site used in the last 30 days stays", n, held>>20)
	}
	return errors.Join(errs...)
}

// fold brings the uses up to date with what the profile holds now.
func (u siteUses) fold(jar []*network.Cookie, pages []string, stored map[string]storedSite, now time.Time) {
	latest := map[string]float64{}
	for _, c := range jar {
		site := SiteOf(c.Domain)
		latest[site] = max(latest[site], c.Expires)
	}
	for site, expires := range latest {
		e := u.at(site, now)
		if expires > e.Expires {
			e.Used, e.Expires = now, expires
		}
	}
	for site, s := range stored {
		e := u.at(site, now)
		for _, o := range s.origins {
			e.add(o)
		}
	}
	for _, page := range pages {
		u.opened(page, now)
	}
	for _, e := range u {
		if e.Used.After(now) {
			e.Used = now
		}
	}
}

// clearIdle clears one site, unless a page of it opened since the sweep chose it. A site whose
// storage was not cleared keeps its entry, so the next sweep tries again.
func (m *Machine) clearIdle(ctx context.Context, p *process, site string, origins []string, jar []*network.Cookie) error {
	m.sitesMu.Lock()
	used := p.visits[site] != nil
	m.sitesMu.Unlock()
	if used {
		return nil
	}
	var old []*network.Cookie
	for _, c := range jar {
		if SiteOf(c.Domain) == site {
			old = append(old, c)
			origins = append(origins, "https://"+strings.TrimPrefix(c.Domain, "."), "http://"+strings.TrimPrefix(c.Domain, "."))
		}
	}
	slices.Sort(origins)
	for _, o := range slices.Compact(origins) {
		if err := p.store.clearOrigin(ctx, o); err != nil {
			return err
		}
	}
	if len(old) > 0 {
		if err := p.store.expire(ctx, old); err != nil {
			return err
		}
	}
	m.forgetSite(m.profileOf(p), site)
	return nil
}

// forgetSite lets go of a site's last use, or, with "", of every site's.
func (m *Machine) forgetSite(profile, site string) {
	m.sitesMu.Lock()
	defer m.sitesMu.Unlock()
	uses := readSites(profile)
	if site == "" {
		clear(uses)
	}
	delete(uses, site)
	writeSites(profile, uses)
}

// knownOrigins is the origins written down for a site, or, with "", for every site.
func (m *Machine) knownOrigins(p *process, site string) map[string]bool {
	m.sitesMu.Lock()
	defer m.sitesMu.Unlock()
	uses := readSites(m.profileOf(p))
	p.take(uses)
	out := map[string]bool{}
	for s, u := range uses {
		if site == "" || s == site {
			for _, o := range u.Origins {
				out[o] = true
			}
		}
	}
	return out
}

// Sweep holds the profiles that no browser has open to their bounds, hourly: one not used for
// siteAge goes whole. When the profile in use has a site past its age and the browser is
// stopped, it starts the browser, at most once a day, whose own sweep then clears the site.
func (m *Machine) Sweep(now time.Time) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	for _, profile := range []string{m.Profile, m.ChromeProfile()} {
		m.sweepIdleProfile(profile, now)
	}
	wake := m.running == nil && now.Sub(m.woken) >= wakeEvery && m.Installed() && m.overdue(now)
	if wake {
		m.woken = now
	}
	m.mu.Unlock()
	if wake {
		if _, err := m.Endpoint(nil); err != nil {
			log.Printf("browser: couldn't start it to clear old sites: %v", err)
		}
	}
}

// overdue is whether a site of the profile that the next start uses is past its age.
func (m *Machine) overdue(now time.Time) bool {
	profile := m.Profile
	if m.chromeExe() != "" {
		profile = m.ChromeProfile()
	}
	m.sitesMu.Lock()
	defer m.sitesMu.Unlock()
	for _, u := range readSites(profile) {
		if now.Sub(u.Used) >= siteAge {
			return true
		}
	}
	return false
}

// sweepIdleProfile removes a profile whose browser has not run for siteAge. Under mu, so none
// starts meanwhile.
func (m *Machine) sweepIdleProfile(profile string, now time.Time) {
	if m.running != nil && m.profileOf(m.running) == profile || heldOpen(profile) {
		return
	}
	var last time.Time
	for _, path := range []string{profile, sitesFile(profile), filepath.Join(profile, "Local State"),
		filepath.Join(profile, "Default"), filepath.Join(profile, "Default", "Cookies"), filepath.Join(profile, "Default", "Preferences")} {
		if fi, err := os.Stat(path); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime()
		}
	}
	if last.IsZero() || now.Sub(last) < siteAge {
		return
	}
	if err := os.RemoveAll(profile); err != nil {
		log.Printf("browser: couldn't remove the profile not used since %v: %v", last, err)
		return
	}
	_ = os.Remove(sitesFile(profile))
	log.Printf("browser: removed the profile %s, not used since %v", profile, last)
}

// heldOpen is whether a browser has the profile open: SingletonLock names it ("<host>-<pid>").
// A lock that cannot be read counts as held.
func heldOpen(profile string) bool {
	lock, err := os.Readlink(filepath.Join(profile, "SingletonLock"))
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	pid, err := strconv.Atoi(lock[strings.LastIndex(lock, "-")+1:])
	if err != nil || pid <= 1 {
		return true
	}
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// storedSite is a site's databases on disk.
type storedSite struct {
	origins []string
	bytes   int64
}

// storedSites is each site with databases in the profile, by the names of their folders
// (https_github.com_0.indexeddb.leveldb). It opens none of them.
func storedSites(profile string) map[string]storedSite {
	out := map[string]storedSite{}
	dir := filepath.Join(profile, "Default", "IndexedDB")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name, _, ok := strings.Cut(e.Name(), ".indexeddb.")
		scheme, rest, ok2 := strings.Cut(name, "_")
		i := strings.LastIndex(rest, "_")
		if !ok || !ok2 || i <= 0 {
			continue
		}
		origin := scheme + "://" + rest[:i]
		if port := rest[i+1:]; port != "0" {
			origin += ":" + port
		}
		site, origin := siteOfURL(origin)
		if site == "" {
			continue
		}
		s := out[site]
		s.origins = append(s.origins, origin)
		s.bytes += bytesUnder(filepath.Join(dir, e.Name()))
		out[site] = s
	}
	return out
}

// bytesUnder is the bytes of the files in a folder.
func bytesUnder(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// dropDownloads empties Chrome's own downloads before it starts, when they pass limit: every
// folder of the profile but the ones with a person's data. Chromium is not running then.
func dropDownloads(profile string, limit int64) {
	entries, err := os.ReadDir(profile)
	if err != nil {
		return
	}
	var dirs []string
	var held int64
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || n == "Default" || strings.HasSuffix(n, "Profile") || strings.HasPrefix(n, "Profile ") ||
			n == "NativeMessagingHosts" || n == "External Extensions" {
			continue
		}
		dirs = append(dirs, filepath.Join(profile, n))
		held += bytesUnder(filepath.Join(profile, n))
	}
	if held <= limit {
		return
	}
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("browser: couldn't empty %s: %v", dir, err)
		}
	}
	log.Printf("browser: emptied %d MB of Chrome's own downloads", held>>20)
}

// watchPages calls opened with the address of each page and frame of the browser at endpoint,
// as it opens and as it changes, until the browser goes away.
func watchPages(endpoint string, opened func(string)) {
	alloc, cancelAlloc := remote.NewAllocator(context.Background(), endpoint, remote.NoModifyURL)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	// The first call connects, so the events are asked for after it and before discovery.
	if _, err := chromedp.CallBrowser(ctx, target.GetTargets, target.GetTargetsParams{}); err != nil {
		log.Printf("browser: couldn't follow the sites' uses: %v", err)
		return
	}
	created := chromedp.BrowserEvents(ctx, target.TargetCreated)
	changed := chromedp.BrowserEvents(ctx, target.TargetInfoChanged)
	if _, err := chromedp.CallBrowser(ctx, target.SetDiscoverTargets, target.SetDiscoverTargetsParams{Discover: true}); err != nil {
		log.Printf("browser: couldn't follow the sites' uses: %v", err)
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev, err := range created {
			if err != nil {
				return
			}
			if ev.TargetInfo != nil {
				opened(ev.TargetInfo.URL)
			}
		}
	}()
	for ev, err := range changed {
		if err != nil {
			break
		}
		if ev.TargetInfo != nil {
			opened(ev.TargetInfo.URL)
		}
	}
	cancel()
	<-done
}

// pages is the address of every page and frame open now.
func (s cdpStore) pages(ctx context.Context) ([]string, error) {
	c, done := s.bound(ctx)
	defer done()
	res, err := chromedp.CallBrowser(c, target.GetTargets, target.GetTargetsParams{})
	var out []string
	for _, i := range res.TargetInfos {
		if i != nil {
			out = append(out, i.URL)
		}
	}
	return out, err
}

// expire removes just these cookies: Chromium drops one that is set with an expiry in the
// past, and no other is touched meanwhile.
func (s cdpStore) expire(ctx context.Context, cs []*network.Cookie) error {
	params := cookieParams(cs)
	for _, p := range params {
		p.Expires = cdp.TimeSinceEpoch(1)
	}
	return s.set(ctx, params)
}
