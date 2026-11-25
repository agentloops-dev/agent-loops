package agentsd

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/releasesig"
	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
	"github.com/agentloops-dev/agent-loops/Go/wire"
	"golang.org/x/sys/unix"
)

// Agents updates itself on a Mac (#2178, Enterprise T7). The control plane names its
// release in machine/release at each join. When it is newer than this copy, and the person
// has not turned updating off (selfUpdate/set, self-update.json in the root), the host:
//
//  1. downloads the release's Agent-Loops-<version>.zip and SHA256SUMS, and checks the sum;
//  2. unpacks it beside this copy, and checks the new copy's signature (the same team and
//     identifier as this one), its notarisation and its version;
//  3. waits until no turn runs, then swaps the new copy and this one in one step, and keeps
//     this one beside it, with a mark (#2710);
//  4. restarts the launch agents, which run what is in the bundle now.
//
// The old copy stays until the new one has stayed up selfUpdateHealthy. A new copy that
// starts selfUpdateTries times without staying up is swapped back for the old one, and its
// release is not taken again (settleSelfUpdates).
//
// It never asks for an administrator (#2178, hosts never escalate): a copy in a folder the
// person cannot change, such as /Applications for a standard user, says so and stays.
// A development build, or a copy with no team, never updates itself.
//
// Agent Loops was Agents, and Agents Host before that, with the same bundle ID (#4224,
// #5297). A release's copy that runs from an Agents.app or an Agents Host.app takes the new
// name once (takeNewName), so an earlier install moves across with no step from the person,
// and no second app stays beside it.
//
// A Linux machine updates itself by the same steps (#5788, epic #5785), with these
// differences (selfupdate_linux.go):
//
//  1. it checks SHA256SUMS against SHA256SUMS.sig (releasesig), whatever the source,
//     before it downloads the release's agentsd-linux-<arch>-<version>.tar.gz, and checks
//     each program in it against its .sha256 too;
//  2. it puts the new agentsd at bin/agentsd-<sha256>, beside this one; the new copy says
//     its version itself (--version);
//  3. the "app" it swaps is bin/current, the link that systemd and the supervisor start:
//     a link to the new agentsd takes its place in one step, and the MCP servers go in
//     just before;
//  4. the host ends with ExitRestart, and its service or its supervisor starts
//     bin/current again.

const selfUpdateFile = "self-update.json"

// The app's name (#5297), and its names before it: Agents (#4225) and Agents Host.
const appName = "Agent Loops.app"

var oldAppNames = []string{"Agents.app", "Agents Host.app"}

// An update's folder beside the app is updateFolderPrefix and a random end; Agents Host's
// were oldUpdateFolderPrefix.
const (
	updateFolderPrefix    = ".Agents.update-"
	oldUpdateFolderPrefix = ".Agents Host.update-"
)

// selfUpdateLimit bounds the download: the release's zip is about 100 MB.
const selfUpdateLimit = 1 << 30

// selfUpdateMark is the mark an update leaves in its folder beside the app just before the
// swap (#2710): which release replaced which, where the old copy is, and how many times the
// new copy has started.
const selfUpdateMark = "update.json"

// A new copy has selfUpdateTries starts to stay up selfUpdateHealthy; then the old copy
// goes back. launchd starts a host that ends again after about ten seconds.
const (
	selfUpdateTries   = 3
	selfUpdateHealthy = 5 * time.Minute
)

type selfUpdateMarkFile struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Old    string `json:"old"` // the old copy, in the update's folder
	Starts int    `json:"starts"`
}

// releaseDownloads is where the public releases' files are, by tag (#1064: the releases'
// repository, not the source's). A self-update takes them from the control plane first
// (#5786), and from here when it does not answer or has no such file.
// AGENTS_RELEASE_DOWNLOADS replaces both, for a test or a mirror. It changes only where the
// files come from: a Linux machine checks their signature all the same.
const releaseDownloads = "https://github.com/agentloops-dev/agents-releases/releases/download"

// selfUpdater is one Mac host's updater; nil on a host that is not in an app.
type selfUpdater struct {
	file    string
	app     string // the app this host runs from (Agent Loops.app)
	running string // this copy's version
	tell    func(wire.SelfUpdate)
	turns   func() int
	ctx     context.Context

	// What the Mac does, replaced by a test.
	fetch func(ctx context.Context, url, pin string, w io.Writer, limit int64) error
	// asset is the release's file for this platform (releaseZip on a Mac).
	asset func(version string) string
	// open makes the new copy from a checked asset in work (unpackZip on a Mac); it is
	// what takes the app's place.
	open func(version, work, file string) (string, error)
	// place, when set, puts what goes beside the new copy just before the swap (a Linux
	// machine's MCP servers).
	place func(work string) error
	// signed, when set, checks SHA256SUMS against its signature before anything else of
	// the release is fetched (a Linux machine; a Mac checks the app's own signature).
	signed func(sums, sig []byte) error
	// control is where this Mac's control plane answers, one address after another, as
	// {url, pin}; nil or empty when it has none.
	control func() []releaseSource
	// public is where the public releases are (releaseDownloads; a test says another).
	public   string
	unpack   func(zip, dir string) error
	verify   func(next, running string) error
	version  func(app string) (string, error)
	writable func(app string) error
	swap     func(a, b string) error
	restart  func() error
	pause    time.Duration

	mu     sync.Mutex
	status wire.SelfUpdate
	// busy is a release being downloaded, checked or waited on; another named meanwhile
	// waits for it to end.
	busy bool
	// rolledBack is a release that did not stay up here, so it is not taken again.
	rolledBack string
}

type selfUpdateState struct {
	updater *selfUpdater
	// restart is an update put in place on Linux: the host ends with ExitRestart, and its
	// service or supervisor starts bin/current again (#5788).
	restart atomic.Bool
}

// ExitRestart is the status a Linux host ends with to be started again from bin/current
// (#5788): systemd's Restart=always starts it whatever the status, and the supervisor
// starts itself again from bin/current on this one, so it runs the new copy too.
const ExitRestart = 75

// RestartWanted is whether the host stopped to run the copy a self-update put in place.
func (d *Daemon) RestartWanted() bool { return d.restart.Load() }

type selfUpdateSettings struct {
	AutoUpdate bool   `json:"autoUpdate"`
	RolledBack string `json:"rolledBack,omitempty"`
}

func readSelfUpdateSettings(path string) selfUpdateSettings {
	s := selfUpdateSettings{AutoUpdate: true}
	if b, err := os.ReadFile(path); err == nil {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) == nil {
			// A value that is missing or of the wrong kind leaves its default.
			_ = json.Unmarshal(m["autoUpdate"], &s.AutoUpdate)
			_ = json.Unmarshal(m["rolledBack"], &s.RolledBack)
		}
	}
	return s
}

func writeSelfUpdateSettings(path string, s selfUpdateSettings) error {
	b, _ := json.Marshal(s)
	return store.WriteAtomically(path, b, 0o600)
}

func newSelfUpdater(file, app, running string) *selfUpdater {
	u := &selfUpdater{file: file, app: app, running: running, pause: 15 * time.Second,
		fetch: fetchTo, asset: releaseZip, public: releaseDownloads, writable: folderWritable, swap: exchangePaths, ctx: context.Background()}
	u.open = u.unpackZip
	settings := readSelfUpdateSettings(file)
	u.rolledBack = settings.RolledBack
	u.status = wire.SelfUpdate{AutoUpdate: settings.AutoUpdate, State: wire.SelfUpdateStateUpToDate, Running: running}
	if why := u.cannot(); why != "" {
		u.status.State, u.status.Message = wire.SelfUpdateStateCannot, &why
	} else if !u.status.AutoUpdate {
		u.status.State = wire.SelfUpdateStateOff
	}
	return u
}

// cannot is why this copy never updates itself, or "".
func (u *selfUpdater) cannot() string {
	if _, ok := releaseVersion(u.running); !ok {
		return "This copy of Agent Loops is a development build, so it does not update itself."
	}
	if strings.Contains(u.app, "/AppTranslocation/") {
		return "macOS runs this copy of Agent Loops from a temporary place. Move it to Applications in your home folder, then open it again, so it can update itself."
	}
	return ""
}

func (u *selfUpdater) Status() wire.SelfUpdate {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status
}

func (u *selfUpdater) set(change func(*wire.SelfUpdate)) {
	u.mu.Lock()
	change(&u.status)
	s := u.status
	u.mu.Unlock()
	if u.tell != nil {
		u.tell(s)
	}
}

// Set is selfUpdate/set: kept, and a release named before is taken up at once.
func (u *selfUpdater) Set(r wire.SelfUpdateSet) (wire.SelfUpdate, error) {
	settings := readSelfUpdateSettings(u.file)
	settings.AutoUpdate = r.AutoUpdate
	if err := writeSelfUpdateSettings(u.file, settings); err != nil {
		return u.Status(), err
	}
	u.mu.Lock()
	release := u.status.Release
	u.mu.Unlock()
	u.set(func(s *wire.SelfUpdate) {
		s.AutoUpdate = r.AutoUpdate
		if s.State == wire.SelfUpdateStateOff && r.AutoUpdate {
			s.State = wire.SelfUpdateStateUpToDate
		}
	})
	if release != nil {
		u.Named(*release)
	} else if !r.AutoUpdate {
		u.set(func(s *wire.SelfUpdate) {
			if s.State == wire.SelfUpdateStateUpToDate {
				s.State = wire.SelfUpdateStateOff
			}
		})
	}
	return u.Status(), nil
}

// Named is machine/release: the release the control plane names. A newer one is taken up,
// unless updating is off or this copy cannot update itself.
func (u *selfUpdater) Named(version string) {
	u.mu.Lock()
	u.status.Release = &version
	busy, on, state := u.busy, u.status.AutoUpdate, u.status.State
	u.mu.Unlock()
	if busy || state == wire.SelfUpdateStateCannot || state == wire.SelfUpdateStateRestarting {
		u.set(func(*wire.SelfUpdate) {})
		return
	}
	if !on {
		u.set(func(s *wire.SelfUpdate) { s.State, s.Message = wire.SelfUpdateStateOff, nil })
		return
	}
	if !newerRelease(version, u.running) {
		u.set(func(s *wire.SelfUpdate) { s.State, s.Message = wire.SelfUpdateStateUpToDate, nil })
		return
	}
	if version == u.rolledBack {
		why := "Agent Loops " + version + " did not stay running here, so this machine went back to " + u.running +
			". It waits for the next release."
		u.set(func(s *wire.SelfUpdate) { s.State, s.Message = wire.SelfUpdateStateFailed, &why })
		return
	}
	if err := u.writable(u.app); err != nil {
		why := "Agent Loops is in a folder you can change only as an administrator (" + filepath.Dir(u.app) +
			"). Move it to Applications in your home folder, then open it again, so it can update itself."
		u.set(func(s *wire.SelfUpdate) { s.State, s.Message = wire.SelfUpdateStateCannot, &why })
		return
	}
	u.mu.Lock()
	u.busy = true
	u.mu.Unlock()
	safe.Go("self-update", func() {
		err := u.update(version)
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
		if err != nil {
			log.Printf("self-update: %s: %v", version, err)
			why := err.Error()
			u.set(func(s *wire.SelfUpdate) {
				if s.State == wire.SelfUpdateStateOff {
					return
				}
				s.State, s.Message = wire.SelfUpdateStateFailed, &why
			})
		}
	})
}

// errTurnedOff is updating turned off while a release waited: nothing was changed.
var errTurnedOff = errors.New("turned off")

func (u *selfUpdater) update(version string) error {
	u.set(func(s *wire.SelfUpdate) { s.State, s.Message = wire.SelfUpdateStateDownloading, nil })
	work, err := os.MkdirTemp(filepath.Dir(u.app), updateFolderPrefix)
	if err != nil {
		return fmt.Errorf("Agent Loops could not make a folder beside itself for the update: %w", err)
	}
	swapped := false
	defer func() {
		// After the swap the folder holds the old copy, which may still be running: the
		// new copy takes it away once it has stayed up (settleSelfUpdates).
		if !swapped {
			os.RemoveAll(work)
		}
	}()
	next, err := u.download(version, work)
	if err != nil {
		return err
	}
	if v, err := u.version(next); err != nil || v != version {
		return fmt.Errorf("The download is not Agent Loops %s (it says %q), so it was not installed.", version, v)
	}
	if err := u.verify(next, u.app); err != nil {
		return fmt.Errorf("The download's signature or notarisation did not check out, so it was not installed: %v", err)
	}
	u.set(func(s *wire.SelfUpdate) { s.State = wire.SelfUpdateStateWaiting })
	if err := u.betweenTurns(); err != nil {
		return err
	}
	if u.place != nil {
		if err := u.place(work); err != nil {
			return err
		}
	}
	if err := u.putInPlace(version, work, next); err != nil {
		return err
	}
	swapped = true
	log.Printf("self-update: Agent Loops %s is in place of %s", version, u.running)
	u.set(func(s *wire.SelfUpdate) { s.State, s.Message = wire.SelfUpdateStateRestarting, nil })
	// A turn that started since is not cut: the restart waits for it too.
	if err := u.betweenTurns(); err != nil && !errors.Is(err, errTurnedOff) {
		return err
	}
	if err := u.restart(); err != nil {
		return fmt.Errorf("Agent Loops %s is installed. Quit and open Agent Loops to finish: %v", version, err)
	}
	return nil
}

// putInPlace swaps the new copy and this one in one step, so the app is never missing; the
// old copy is then where the new one was. Where the folder cannot swap, it takes two
// renames, and a start that finds no app puts the old copy back. The mark is written
// first either way.
func (u *selfUpdater) putInPlace(version, work, next string) error {
	mark := selfUpdateMarkFile{From: u.running, To: version, Old: strings.TrimPrefix(next, work+string(filepath.Separator))}
	if err := writeSelfUpdateMark(work, mark); err != nil {
		return fmt.Errorf("Agent Loops could not keep a note of the update: %w", err)
	}
	err := u.swap(u.app, next)
	if err == nil {
		return nil
	}
	log.Printf("self-update: no swap in one step (%v): two renames", err)
	mark.Old = "old.app"
	if err := writeSelfUpdateMark(work, mark); err != nil {
		return fmt.Errorf("Agent Loops could not keep a note of the update: %w", err)
	}
	old := filepath.Join(work, mark.Old)
	if err := os.Rename(u.app, old); err != nil {
		return fmt.Errorf("Agent Loops could not move itself aside: %w", err)
	}
	if err := os.Rename(next, u.app); err != nil {
		if back := os.Rename(old, u.app); back != nil {
			return fmt.Errorf("Agent Loops could not put the new copy in its place (%w), nor itself back from %s: %v", err, old, back) //nolint:staticcheck // ST1005: it starts with the app's name
		}
		return fmt.Errorf("Agent Loops could not put the new copy in its place: %w", err)
	}
	return nil
}

func writeSelfUpdateMark(work string, m selfUpdateMarkFile) error {
	b, _ := json.Marshal(m)
	return store.WriteAtomically(filepath.Join(work, selfUpdateMark), b, 0o600)
}

// betweenTurns waits until no turn runs; it ends early when the host stops or updating is
// turned off before the swap.
func (u *selfUpdater) betweenTurns() error {
	for {
		u.mu.Lock()
		state, on := u.status.State, u.status.AutoUpdate
		u.mu.Unlock()
		if !on && state == wire.SelfUpdateStateWaiting {
			u.set(func(s *wire.SelfUpdate) { s.State = wire.SelfUpdateStateOff })
			return errTurnedOff
		}
		if u.turns() == 0 {
			return nil
		}
		select {
		case <-u.ctx.Done():
			return u.ctx.Err()
		case <-time.After(u.pause):
		}
	}
}

// download fetches the release's file and its sum, checks the one against the other, and
// opens it in work. It is the new copy: on a Mac the new Agent Loops.app, which goes where
// this copy is, whatever that is named.
func (u *selfUpdater) download(version, work string) (string, error) {
	var lastErr, unsigned error
	for _, src := range u.sources(version) {
		file, err := u.downloadFrom(src, version, work)
		if err == nil {
			return u.open(version, work, file)
		}
		var refused *refusedDownload
		if errors.As(err, &refused) {
			return "", err
		}
		log.Printf("self-update: %s did not give release %s: %v", src.base, version, err)
		lastErr = err
		if errors.Is(err, errUnsigned) {
			unsigned = err
		}
	}
	// A release that was there with no signature says more than a source that was not.
	return "", cmp.Or(unsigned, lastErr)
}

// releaseSource is where a release's files are: a base address that ends in a slash, and
// the pin of its certificate, "" for none.
type releaseSource struct{ base, pin string }

// sources are the places to take a release from, in order: the control plane (every
// address it has), then the public releases. AGENTS_RELEASE_DOWNLOADS is the only one when
// it is set.
func (u *selfUpdater) sources(version string) []releaseSource {
	if given := os.Getenv("AGENTS_RELEASE_DOWNLOADS"); given != "" {
		return []releaseSource{{base: strings.TrimSuffix(given, "/") + "/v" + version + "/"}}
	}
	var out []releaseSource
	if u.control != nil {
		for _, c := range u.control() {
			out = append(out, releaseSource{base: strings.TrimSuffix(c.base, "/") + "/v1/releases/" + version + "/", pin: c.pin})
		}
	}
	return append(out, releaseSource{base: strings.TrimSuffix(u.public, "/") + "/v" + version + "/"})
}

// refusedDownload is a file that does not match its checksum, or checksums that do not
// match their signature: no other source is tried.
type refusedDownload struct{ version, why string }

func (e *refusedDownload) Error() string {
	return "The download of release " + e.version + " does not match its " + e.why + ", so it was not installed."
}

// errUnsigned is a source with a release's checksums and no signature of them, such as a
// control plane that is older than signed releases: the next source is tried.
var errUnsigned = errors.New("no signature")

// checkSigned checks a source's checksums against the signature from the same source.
func (u *selfUpdater) checkSigned(src releaseSource, version, sums string) error {
	if u.signed == nil {
		return nil
	}
	var sig bytes.Buffer
	if err := u.fetch(u.ctx, src.base+releasesig.SigName, src.pin, &sig, releasesig.Limit); err != nil {
		return fmt.Errorf("Agent Loops could not download release %s's signature, so it was not installed: %w (%w)", version, errUnsigned, err) //nolint:staticcheck // ST1005: the person reads it as the update's message
	}
	if err := u.signed([]byte(sums), sig.Bytes()); err != nil {
		log.Printf("self-update: %s: release %s: %v", src.base, version, err)
		return &refusedDownload{version, "signature"}
	}
	return nil
}

// downloadFrom fetches the release's file and its sum from one source, and checks the one
// against the other. It is the file's path.
func (u *selfUpdater) downloadFrom(src releaseSource, version, work string) (string, error) {
	name := u.asset(version)
	var sums strings.Builder
	if err := u.fetch(u.ctx, src.base+"SHA256SUMS", src.pin, &sums, 1<<16); err != nil {
		return "", fmt.Errorf("Agent Loops could not download release %s's checksums: %w", version, err)
	}
	if err := u.checkSigned(src, version, sums.String()); err != nil {
		return "", err
	}
	want := sumFor(sums.String(), name)
	if want == "" {
		return "", fmt.Errorf("Release %s lists no checksum for %s.", version, name)
	}
	zip := filepath.Join(work, name)
	f, err := os.Create(zip)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = u.fetch(u.ctx, src.base+name, src.pin, io.MultiWriter(f, h), selfUpdateLimit)
	f.Close()
	if err != nil {
		return "", fmt.Errorf("Agent Loops could not download release %s: %w", version, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(zip)
		return "", &refusedDownload{version, "checksum"}
	}
	return zip, nil
}

// unpackZip unpacks a checked zip in work. It is the new Agent Loops.app, which goes where
// this copy is, whatever that is named.
func (u *selfUpdater) unpackZip(version, work, zip string) (string, error) {
	out := filepath.Join(work, "new")
	if err := u.unpack(zip, out); err != nil {
		return "", fmt.Errorf("Agent Loops could not unpack release %s: %w", version, err)
	}
	os.Remove(zip)
	next := filepath.Join(out, appName)
	if fi, err := os.Stat(next); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("Release %s holds no %s.", version, appName)
	}
	return next, nil
}

// releaseZip is the release's zip of Agent Loops.app. A release also carries
// Agents-<version>.zip (Agents.app) and Agents-Host-<version>.zip (Agents Host.app), for
// the copies that named their zip so before (#5297, #4225).
func releaseZip(version string) string { return "Agent-Loops-" + version + ".zip" }

// sumFor is the sum SHA256SUMS gives a file ("<hex>  <name>"), or "".
func sumFor(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

var selfUpdateHTTP = controllink.HTTPClient("")

// fetchTo copies a URL's body to w: at most limit bytes, or an error.
func fetchTo(ctx context.Context, url, pin string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := selfUpdateHTTP
	if pin != "" {
		client = controllink.HTTPClient(pin)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%s is larger than %d bytes", url, limit)
	}
	return nil
}

// folderWritable is nil when this user can move the app within its folder, with no
// administrator.
func folderWritable(app string) error {
	if err := unix.Access(filepath.Dir(app), unix.W_OK); err != nil {
		return err
	}
	return unix.Access(app, unix.W_OK)
}

// settleSelfUpdates is what a start does with what an update left beside the app (#2710).
// A folder with no mark is a download that a stop cut short: it goes. A folder with a mark
// holds the old copy:
//   - with no app in place (a cut between two renames), the old copy goes back;
//   - when this is the new copy, the start is counted, and the folder is kept until this
//     copy has stayed up; a start past selfUpdateTries swaps the old copy back and keeps its
//     release from being taken again;
//   - otherwise (the old copy runs again, or another copy was put there), it goes.
//
// keep is the folders to take away once this copy has stayed up selfUpdateHealthy. back is
// the old copy put back: this host restarts, to run it.
func settleSelfUpdates(app, running, settingsFile string, swap func(a, b string) error) (keep []string, back bool) {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(app), updateFolderPrefix+"*"))
	old, _ := filepath.Glob(filepath.Join(filepath.Dir(app), oldUpdateFolderPrefix+"*"))
	for _, work := range append(matches, old...) {
		var mark selfUpdateMarkFile
		b, err := os.ReadFile(filepath.Join(work, selfUpdateMark))
		if err != nil || json.Unmarshal(b, &mark) != nil || mark.Old == "" {
			os.RemoveAll(work)
			continue
		}
		old := filepath.Join(work, mark.Old)
		if _, err := os.Stat(app); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(old, app); err != nil {
				log.Printf("self-update: no app at %s, and the old copy did not go back: %v", app, err)
				continue
			}
			log.Printf("self-update: no app at %s: Agent Loops %s went back", app, mark.From)
			os.RemoveAll(work)
			back = true
			continue
		}
		if mark.To != running {
			os.RemoveAll(work)
			continue
		}
		mark.Starts++
		if mark.Starts <= selfUpdateTries {
			if err := writeSelfUpdateMark(work, mark); err != nil {
				log.Printf("self-update: %v", err)
			}
			keep = append(keep, work)
			continue
		}
		if err := swapBack(app, old, swap); err != nil {
			log.Printf("self-update: Agent Loops %s did not stay up, and %s did not go back: %v", mark.To, mark.From, err)
			keep = append(keep, work)
			continue
		}
		log.Printf("self-update: Agent Loops %s started %d times without staying up: %s went back", mark.To, selfUpdateTries, mark.From)
		settings := readSelfUpdateSettings(settingsFile)
		settings.RolledBack = mark.To
		if err := writeSelfUpdateSettings(settingsFile, settings); err != nil {
			log.Printf("self-update: %v", err)
		}
		_ = os.RemoveAll(work)
		back = true
	}
	return keep, back
}

// swapBack puts the old copy in the app's place: in one step, or with two renames.
func swapBack(app, old string, swap func(a, b string) error) error {
	if swap(app, old) == nil {
		return nil
	}
	aside := old + ".failed"
	if err := os.Rename(app, aside); err != nil {
		return err
	}
	if err := os.Rename(old, app); err != nil {
		if back := os.Rename(aside, app); back != nil {
			return fmt.Errorf("%w; and could not put %s back: %v", err, aside, back)
		}
		return err
	}
	return nil
}

// releaseVersion is X.Y.Z as three numbers; not ok for a development build (a suffix).
func releaseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newerRelease is whether named is a release later than running. An older one is never
// taken: an update never goes back.
func newerRelease(named, running string) bool {
	a, ok1 := releaseVersion(named)
	b, ok2 := releaseVersion(running)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// enclosingApp is the outermost-first .app the executable is in: Agent Loops.app for
// Contents/Helpers/agentsd. "" when it is in none.
func enclosingApp(executable string) string {
	for dir := filepath.Dir(executable); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
	}
	return ""
}

// takeNewName is where a release's copy of the app runs from once it has Agent Loops' name
// (#5297, #4225): an Agents.app or an Agents Host.app becomes an Agent Loops.app beside it,
// once, so the old bundle is gone. It stays where it is when something else has the name
// there, or the rename fails. moved is whether it was renamed: what runs from the old path
// then restarts, from the new one.
func takeNewName(app string, rename func(a, b string) error) (now string, moved bool) {
	if !slices.Contains(oldAppNames, filepath.Base(app)) {
		return app, false
	}
	next := filepath.Join(filepath.Dir(app), appName)
	if _, err := os.Lstat(next); err == nil {
		log.Printf("self-update: %s is there already, so %s keeps its name", next, app)
		return app, false
	}
	if err := rename(app, next); err != nil {
		log.Printf("self-update: %s did not take the name %s: %v", app, appName, err)
		return app, false
	}
	log.Printf("self-update: %s is now %s", app, next)
	return next, true
}

// toldRelease is machine/release.
func (d *Daemon) toldRelease(params json.RawMessage) {
	var r wire.MachineRelease
	if err := json.Unmarshal(params, &r); err != nil {
		log.Printf("self-update: %s: %v", wire.NotifyMachineRelease.Name, err)
		return
	}
	if u := d.updater; u != nil {
		u.Named(r.Version)
	}
}

// turnsRunning is how many agents are in a turn: one waiting on the person is too.
// Only a self-update asks (serveSelfUpdate).
func (d *Daemon) turnsRunning() int {
	n := 0
	for _, a := range d.Store.Agents() {
		if d.turnIsRunning(a.ID) {
			n++
		}
	}
	return n
}

// serveSelfUpdate serves selfUpdate/status and selfUpdate/set for an updater.
func (d *Daemon) serveSelfUpdate(u *selfUpdater) {
	u.ctx = d.ctx
	u.turns = d.turnsRunning
	u.tell = func(s wire.SelfUpdate) { Broadcast(d, wire.NotifySelfUpdateChanged, s) }
	d.updater = u
	On(d, wire.CallSelfUpdateStatus, func(context.Context, *Client, wire.Empty) (wire.SelfUpdate, error) {
		return u.Status(), nil
	})
	On(d, wire.CallSelfUpdateSet, func(_ context.Context, _ *Client, r wire.SelfUpdateSet) (wire.SelfUpdate, error) {
		return u.Set(r)
	})
}

// controlSources are the addresses of the control plane this host joined, from its root's
// membership, or none when it has not joined one (#5786, #5788).
func controlSources(root string) []releaseSource {
	member, err := controllink.LoadMembership(root)
	if err != nil || member == nil {
		return nil
	}
	var out []releaseSource
	for _, e := range member.EndpointsToDial() {
		if !strings.HasPrefix(e.URL, "https://") && !strings.HasPrefix(e.URL, "http://") {
			continue
		}
		src := releaseSource{base: e.URL}
		if e.Pin != nil {
			src.pin = *e.Pin
		}
		out = append(out, src)
	}
	return out
}
