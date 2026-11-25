package agentsd

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/procgroup"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A send's seed (#7119): the machine that receives a folder gets the commits from the folder's
// origin, so the send carries only what the origin does not have. It asks nobody: git gets no
// password and SSH no prompt, and when the machine cannot reach the origin the send carries
// everything, as before.
//
// A folder that is there fetches its own origin, into its remote-tracking branches only. For
// a new folder the commits go to a repository of their own in Seeds, with no branch and no
// files, and folders/receive starts the folder from it. So a seed makes no folder, moves no
// branch and changes no file: receive's checks are the same with it and without.

const (
	// seedLimit is the longest that one seed runs.
	seedLimit = 10 * time.Minute
	// seedStall is how long a sender waits for a seed that gets nothing more: it is slow.
	seedStall = 90 * time.Second
	// seedAlone is how long a seed runs on when its sender asks no more.
	seedAlone = time.Minute
	// seedsAtOnce bounds the seeds this machine holds, those under way and those not yet used.
	seedsAtOnce = 4
	// seedAnswer is how long one folders/receiveSeed waits for the seed to end.
	seedAnswer = 2 * time.Second
	// mostRemoteHeads bounds the remote-tracking branches that a machine names, the newest first.
	mostRemoteHeads = 2000
)

type seedBook struct {
	seedMu sync.Mutex
	seeds  map[wire.UUID]*seedJob
}

// seedJob is one seed. staged is the repository in Seeds for a new folder, "" for a folder
// that is there.
type seedJob struct {
	path, staged string
	cancel       context.CancelFunc
	// ended closes once the seed has ended, dropped once its commits are used or not wanted.
	ended, dropped chan struct{}

	mu    sync.Mutex
	asked time.Time
	state wire.ReceiveSeed
}

func (j *seedJob) now() wire.ReceiveSeed {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

func (d *Daemon) seedsFolder() string { return filepath.Join(d.Store.Loc.Root, "Seeds") }

// withoutCredential is a remote's address with no credential in it: no user and no query in an
// HTTP address, where the user can be a token, and no password in an SSH one.
func withoutCredential(address string) string {
	scheme, rest, ok := strings.Cut(address, "://")
	if !ok {
		return address
	}
	ssh := strings.Contains(strings.ToLower(scheme), "ssh")
	if !ssh {
		if i := strings.IndexAny(rest, "?#"); i >= 0 {
			rest = rest[:i]
		}
	}
	host, path, slash := strings.Cut(rest, "/")
	if at := strings.LastIndex(host, "@"); at >= 0 {
		user, _, _ := strings.Cut(host[:at], ":")
		host = host[at+1:]
		if ssh && user != "" {
			host = user + "@" + host
		}
	}
	out := scheme + "://" + host
	if slash {
		out += "/" + path
	}
	return out
}

// seedOrigin is the address a seed fetches: an HTTPS or SSH one, with no credential in it.
func seedOrigin(origin string) (gitRemote, bool) {
	r, ok := parseRemote(withoutCredential(strings.TrimSpace(origin)))
	if scheme, _, url := strings.Cut(r.url, "://"); url && scheme != "https" && scheme != "ssh" {
		return gitRemote{}, false // parseRemote reads it as host:path
	}
	if !ok || strings.HasPrefix(r.host, "-") || strings.Contains(r.path, "@") || strings.Contains(r.url, "::") ||
		strings.ContainsFunc(r.url, func(c rune) bool { return c < ' ' || c == 0x7f }) {
		return gitRemote{}, false
	}
	return r, true
}

// receiveSeed is folders/receiveSeed: it starts the seed r names, or says how it stands.
func (d *Daemon) receiveSeed(r wire.ReceiveSeedRequest) (wire.ReceiveSeed, error) {
	if r.Stop != nil && *r.Stop {
		d.dropSeed(r.Seed)
		return wire.ReceiveSeed{Finished: true, Heads: []string{}}, nil
	}
	path, err := receiveFolder(r.Folder)
	if err != nil {
		return wire.ReceiveSeed{}, err
	}
	if file, err := d.incomingFile(r.Seed); err != nil || filepath.Base(file) != string(r.Seed) {
		return wire.ReceiveSeed{}, Invalid("That is not a seed.")
	}
	remote, ok := seedOrigin(r.Origin)
	if !ok {
		return wire.ReceiveSeed{}, Invalid("That origin is not an HTTPS or SSH address.")
	}
	d.seedMu.Lock()
	j := d.seeds[r.Seed]
	if j != nil && j.path != path {
		d.seedMu.Unlock()
		return wire.ReceiveSeed{}, Invalid("That seed is for another folder.")
	}
	if j == nil {
		if len(d.seeds) >= seedsAtOnce {
			d.seedMu.Unlock()
			return noSeed("this machine is getting other folders"), nil
		}
		j = &seedJob{path: path, ended: make(chan struct{}), dropped: make(chan struct{}), asked: time.Now(),
			state: wire.ReceiveSeed{Heads: []string{}}}
		var ctx context.Context
		ctx, j.cancel = context.WithTimeout(d.hostCtx(), seedLimit)
		if d.seeds == nil {
			d.seeds = map[wire.UUID]*seedJob{}
		}
		d.seeds[r.Seed] = j
		d.goJob("seed of "+path, func() { d.runSeed(ctx, r.Seed, j, remote) })
	}
	d.seedMu.Unlock()
	j.mu.Lock()
	j.asked = time.Now()
	j.mu.Unlock()
	select {
	case <-j.ended:
	case <-d.hostCtx().Done():
	case <-time.After(seedAnswer):
	}
	return j.now(), nil
}

func noSeed(why string) wire.ReceiveSeed {
	return wire.ReceiveSeed{Finished: true, Heads: []string{}, Problem: &why}
}

// runSeed gets a seed's commits, and then holds them until they are used or not wanted.
func (d *Daemon) runSeed(ctx context.Context, id wire.UUID, j *seedJob, remote gitRemote) {
	defer j.cancel()
	staging := filepath.Join(d.seedsFolder(), string(id))
	defer os.RemoveAll(staging)
	alone := make(chan struct{})
	d.goJob("seed's sender", func() {
		tick := time.NewTicker(seedAlone / 4)
		defer tick.Stop()
		for {
			select {
			case <-alone:
				return
			case <-tick.C:
				j.mu.Lock()
				gone := time.Since(j.asked) > seedAlone
				j.mu.Unlock()
				if gone {
					j.cancel()
				}
			}
		}
	})
	heads, staged, err := d.getSeed(ctx, j, remote, staging)
	close(alone)
	if ctx.Err() != nil {
		err = errors.New("it took too long")
	}
	j.mu.Lock()
	j.state.Finished = true
	if err != nil {
		why := err.Error()
		j.state.Problem = &why
	} else {
		j.state.Heads, j.staged = heads, staged
	}
	j.mu.Unlock()
	close(j.ended)
	if err != nil {
		log.Printf("seed of %q: %v", j.path, err)
		_ = os.RemoveAll(staging) // the deferred one tries again
	}
	select {
	case <-j.dropped:
		return
	case <-d.hostCtx().Done():
		return
	case <-time.After(transferLimit):
	}
	if d.forgetSeed(id) == nil {
		// A receive uses it now.
		select {
		case <-j.dropped:
		case <-d.hostCtx().Done():
		}
	}
}

// forgetSeed takes a seed out of the book; nil when it is not there.
func (d *Daemon) forgetSeed(id wire.UUID) *seedJob {
	d.seedMu.Lock()
	defer d.seedMu.Unlock()
	j := d.seeds[id]
	delete(d.seeds, id)
	return j
}

// dropSeed ends a seed and lets its commits go.
func (d *Daemon) dropSeed(id wire.UUID) {
	if j := d.forgetSeed(id); j != nil {
		j.cancel()
		close(j.dropped)
	}
}

// takeSeed is the repository a new folder at path starts from, "" when there is none, and
// what to call once it was used. The seed is gone from the book, so it is used once.
func (d *Daemon) takeSeed(id wire.UUID, path string) (staged string, used func()) {
	j := d.forgetSeed(id)
	if j == nil {
		return "", func() {}
	}
	j.cancel()
	select {
	case <-j.ended:
	case <-d.hostCtx().Done():
		return "", func() { close(j.dropped) }
	}
	if s := j.now(); s.Problem == nil && j.path == path {
		staged = j.staged
	}
	return staged, func() { close(j.dropped) }
}

// getSeed gets the commits of remote for the folder at j.path: into it when it is there, else
// into a repository in staging, which it answers with.
func (d *Daemon) getSeed(ctx context.Context, j *seedJob, remote gitRemote, staging string) (heads []string, staged string, err error) {
	state, err := d.receiveState(wire.ReceiveStateRequest{Folder: j.path})
	if err != nil {
		return nil, "", err
	}
	if state.Repository {
		have, err := git(j.path, "remote", "get-url", "origin")
		if err != nil {
			return nil, "", errors.New("the folder has no origin")
		}
		if r, ok := parseRemote(have); !ok || r.identity() != remote.identity() {
			return nil, "", errors.New("the folder has another origin")
		}
		if err := seedFetch(ctx, j, j.path, remote, "--no-tags"); err != nil {
			return nil, "", err
		}
		return folderHeads(j.path), "", nil
	}
	if _, err := os.Lstat(j.path); err == nil {
		if entries, err := os.ReadDir(j.path); err != nil || len(entries) > 0 {
			return nil, "", errors.New("the folder is not a Git repository")
		}
	}
	staged = filepath.Join(staging, "repository")
	if err := os.MkdirAll(staged, 0o700); err != nil {
		return nil, "", err
	}
	if _, err := git(staged, "init", "-q"); err != nil {
		return nil, "", err
	}
	if _, err := git(staged, "remote", "add", "origin", remote.url); err != nil {
		return nil, "", err
	}
	if err := seedFetch(ctx, j, staged, remote); err != nil {
		return nil, "", err
	}
	if heads = folderHeads(staged); len(heads) == 0 {
		return nil, "", errors.New("the origin has no commit")
	}
	return heads, staged, nil
}

// folderHeads are the commits that a send can leave out: those of the branches of the
// repository at path, and of its newest remote-tracking branches (#7119). A shallow one has
// not the history of its remote-tracking branches.
func folderHeads(path string) []string {
	heads := []string{}
	if out, err := git(path, "for-each-ref", "--format=%(objectname)", "refs/heads"); err == nil && out != "" {
		heads = strings.Split(out, "\n")
	}
	if shallow, err := git(path, "rev-parse", "--is-shallow-repository"); err != nil || shallow != "false" {
		return heads
	}
	out, err := git(path, "for-each-ref", "--sort=-committerdate", "--count="+strconv.Itoa(mostRemoteHeads),
		"--format=%(objecttype) %(objectname)", "refs/remotes")
	if err != nil {
		return heads
	}
	have := map[string]bool{}
	for _, h := range heads {
		have[h] = true
	}
	for _, l := range strings.Split(out, "\n") {
		if h, ok := strings.CutPrefix(l, "commit "); ok && !have[h] {
			have[h] = true
			heads = append(heads, h)
		}
	}
	return heads
}

// seedFetch fetches every branch of origin into dir's remote-tracking branches. Nothing asks
// for a password, and only HTTPS and SSH are used.
func seedFetch(ctx context.Context, j *seedJob, dir string, remote gitRemote, more ...string) error {
	ssh := os.Getenv("GIT_SSH_COMMAND")
	if ssh == "" {
		if set, err := git(dir, "config", "core.sshCommand"); err == nil && set != "" {
			ssh = set
		} else if program := os.Getenv("GIT_SSH"); program != "" {
			ssh = "'" + strings.ReplaceAll(program, "'", `'\''`) + "'"
		} else {
			ssh = "ssh"
		}
	}
	args := append([]string{"-c", "credential.interactive=false", "-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=30",
		"-c", "gc.auto=0", "-c", "maintenance.auto=false",
		"fetch", "--progress", "--no-prune", "--no-recurse-submodules", "--no-write-fetch-head"}, more...)
	args = append(args, "origin", "+refs/heads/*:refs/remotes/origin/*")
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git itself, with no shell; the address is the repository's own origin
	cmd.Dir = dir
	// The credential helpers that could open a window, and the programs that ask, are off.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GCM_INTERACTIVE=never",
		"SSH_ASKPASS_REQUIRE=never", "GIT_ALLOW_PROTOCOL=https:ssh",
		"GIT_SSH_COMMAND="+ssh+" -o BatchMode=yes -o ConnectTimeout=15")
	said := &seedProgress{job: j}
	cmd.Stderr = said
	// git's own children (ssh, the HTTPS helper) end with it.
	procgroup.Own(cmd)
	cmd.Cancel = func() error { procgroup.End(cmd); return nil }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	procgroup.End(cmd)
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("git is not installed")
	}
	// Not git's own words: they can have the address of the folder's own origin, and a
	// credential in it.
	where := "Could not clone " + remote.host + "/" + remote.path
	if why := cloneFailure(remote, said.words.String()); !strings.HasPrefix(why, where) {
		return errors.New(why)
	}
	return errors.New(where + ".")
}

// received is git's progress as it gets objects: the bytes. Its percentage is of objects, not
// of bytes, so it says no total.
var received = regexp.MustCompile(`^Receiving objects: +\d+% \(\d+/\d+\), ([\d.]+) (bytes|KiB|MiB|GiB)`)

// seedProgress reads git's progress for a seed, and keeps its other words.
type seedProgress struct {
	job  *seedJob
	line []byte
	// words is what git said that is no progress, at most seedWords of it.
	words bytes.Buffer
}

const seedWords = 16 << 10

func (p *seedProgress) Write(b []byte) (int, error) {
	for _, c := range b {
		if c != '\r' && c != '\n' {
			if len(p.line) < 1024 {
				p.line = append(p.line, c)
			}
			continue
		}
		if done, ok := seedBytes(string(p.line)); ok {
			p.job.mu.Lock()
			p.job.state.Done = done
			p.job.mu.Unlock()
		} else if c == '\n' && p.words.Len() < seedWords {
			p.words.Write(append(p.line, '\n'))
		}
		p.line = p.line[:0]
	}
	return len(b), nil
}

// seedBytes are the bytes in a line of git's progress.
func seedBytes(line string) (int64, bool) {
	m := received.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	size, _ := strconv.ParseFloat(m[1], 64)
	return int64(size * map[string]float64{"bytes": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30}[m[2]]), true
}

// receiveStartFromSeed makes the new folder's repository from the seed's: moved when both are
// on one disk, else copied.
func receiveStartFromSeed(path, staged string) error {
	if os.Rename(filepath.Join(staged, ".git"), filepath.Join(path, ".git")) == nil {
		return nil
	}
	if _, err := git(path, "init", "-q"); err != nil {
		return err
	}
	_, err := git(path, "fetch", "-q", "--no-tags", "--", staged, "+refs/remotes/origin/*:refs/remotes/origin/*")
	return err
}

// seedThere has machine seed the folder of a send from its origin, and answers with the
// commits that it then has and the seed; no seed when the send goes on as it would without
// one. said, when not nil, hears the bytes the machine got and about how many there are.
func (d *Daemon) seedThere(me *wire.Agent, machine string, pack wire.SendPackRequest, receive wire.ReceiveRequest,
	said func(done, total int64)) ([]string, *wire.UUID) {
	if receive.Origin == nil || !d.controlHas(wire.CapFoldersSendSeed1) {
		return nil, nil
	}
	remote, ok := seedOrigin(*receive.Origin)
	if !ok || !seedHelps(store.FolderPath(pack.Folder), pack.Branches, pack.LeavingOut) {
		return nil, nil
	}
	seed := wire.ReceiveSeedRequest{Seed: store.NewUUID(), Folder: receive.Folder, Origin: remote.url}
	var got func(int64)
	if said != nil {
		// About as many as the branches' commits are here: the origin can have more.
		total := sizeLacked(store.FolderPath(pack.Folder), pack.Branches, pack.LeavingOut)
		got = func(done int64) { said(done, max(done, total)) }
	}
	return d.waitSeed(seed, got, func(r wire.ReceiveSeedRequest) (wire.ReceiveSeed, error) {
		var out wire.MachineFolderSent
		if err := d.askSend(me, wire.MachineFolderSend{Host: machine, Seed: &r}, &out); err != nil {
			return wire.ReceiveSeed{}, err
		}
		if out.Seed == nil {
			return wire.ReceiveSeed{}, errors.New("the control plane's answer could not be read")
		}
		return *out.Seed, nil
	})
}

// waitSeed asks for a seed until it ends. It gives up on one that gets nothing more.
func (d *Daemon) waitSeed(seed wire.ReceiveSeedRequest, said func(done int64),
	ask func(wire.ReceiveSeedRequest) (wire.ReceiveSeed, error)) ([]string, *wire.UUID) {
	var last wire.ReceiveSeed
	moved := time.Now()
	for begun := time.Now(); ; {
		s, err := ask(seed)
		switch {
		case err != nil:
			// An older machine does not know the step.
			_, why := errMessage(err)
			log.Printf("send: no seed from the origin: %s", why)
		case s.Finished && s.Problem != nil:
			log.Printf("send: no seed from the origin: %s", *s.Problem)
		case s.Finished:
			return s.Heads, &seed.Seed
		case d.hostCtx().Err() != nil || time.Since(begun) > seedLimit || time.Since(moved) > seedStall && s.Done == last.Done:
			log.Printf("send: the seed from the origin is slow")
			d.stopSeed(seed, ask)
		default:
			if s.Done != last.Done {
				moved, last = time.Now(), s
				if said != nil {
					said(s.Done)
				}
			}
			continue
		}
		return nil, nil
	}
}

// stopSeed ends a seed that the send does not use. One it cannot reach ends by itself.
func (d *Daemon) stopSeed(seed wire.ReceiveSeedRequest, ask func(wire.ReceiveSeedRequest) (wire.ReceiveSeed, error)) {
	seed.Stop = new(true)
	if _, err := ask(seed); err != nil {
		log.Printf("send: could not stop a seed: %v", err)
	}
}

// seedHelps says whether the origin has commits of branches that the other machine, which has
// heads, lacks, as far as the repository at path knows.
func seedHelps(path string, branches, heads []string) bool {
	args := append([]string{"rev-list", "--count"}, lacked(path, branches, heads)...)
	lacks, err := git(path, args...)
	if err != nil || lacks == "0" {
		return err != nil
	}
	if tracked, err := git(path, "for-each-ref", "--count=1", "refs/remotes/origin"); err != nil || tracked == "" {
		return true // such as a copy that a send made
	}
	unpushed, err := git(path, append(args, "--remotes=origin")...)
	return err != nil || unpushed != lacks
}

// lacked are rev-list's arguments for the commits of branches that a machine with heads lacks.
func lacked(path string, branches, heads []string) []string {
	args := []string{}
	for _, b := range branches {
		args = append(args, "refs/heads/"+b)
	}
	return append(append(args, "--not"), knownCommits(path, heads)...)
}

// sizeLacked is about the bytes of what a machine with heads lacks of branches, 0 when git
// does not say.
func sizeLacked(path string, branches, heads []string) int64 {
	out, err := git(path, append([]string{"rev-list", "--disk-usage", "--objects"}, lacked(path, branches, heads)...)...)
	if err != nil {
		return 0
	}
	size, _ := strconv.ParseInt(out, 10, 64)
	return size
}

// knownCommits are those of commits that the repository at path has too.
func knownCommits(path string, commits []string) []string {
	if len(commits) == 0 {
		return nil
	}
	// A partial clone does not get from its origin what it is only asked about.
	out, err := gitRawEnv(path, []string{"GIT_NO_LAZY_FETCH=1"}, []byte(strings.Join(commits, "\n")+"\n"), "cat-file", "--batch-check")
	if err != nil {
		return nil
	}
	var known []string
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[1] == "commit" {
			known = append(known, f[0])
		}
	}
	return known
}
