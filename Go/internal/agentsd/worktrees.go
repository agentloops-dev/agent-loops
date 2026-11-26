package agentsd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Worktrees, as DaemonCore+Worktrees makes, lists and removes them: the app's own under
// <folder>/.agents/worktrees/<name> on a branch agents/<name>, kept out of git's view of
// the folder by a line in the repository's info/exclude.

const branchPrefix = "agents/"

var fillerWords = map[string]bool{"a": true, "an": true, "the": true, "to": true, "of": true, "on": true, "in": true,
	"for": true, "and": true, "or": true, "with": true, "please": true, "can": true, "could": true, "you": true,
	"would": true, "we": true, "i": true, "me": true, "my": true, "this": true, "that": true, "it": true, "is": true, "be": true}

// worktreeName is WorktreeName.from(prompt:): a few words of the prompt, folded and joined.
func worktreeName(prompt string, keepingFiller bool, at time.Time) string {
	var folded strings.Builder
	for _, r := range norm.NFD.String(prompt) {
		if !unicode.Is(unicode.Mn, r) {
			folded.WriteRune(unicode.ToLower(r))
		}
	}
	words := strings.FieldsFunc(folded.String(), func(r rune) bool {
		return r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	var kept []string
	for _, w := range words {
		if keepingFiller || !fillerWords[w] {
			kept = append(kept, w)
		}
	}
	if !keepingFiller && len(kept) > 4 {
		kept = kept[:4]
	}
	name := ""
	for _, w := range kept {
		longer := w
		if name != "" {
			longer = name + "-" + w
		}
		if len(longer) > 32 {
			break
		}
		name = longer
	}
	if name == "" && len(kept) > 0 {
		name = kept[0][:min(32, len(kept[0]))]
	}
	if name != "" {
		return name
	}
	return "agent-" + at.Format("0102-1504")
}

// folderForBranch is WorktreeName.folder(forBranch:).
func folderForBranch(branch string) string {
	bare := strings.TrimPrefix(branch, branchPrefix)
	var b strings.Builder
	for _, r := range bare {
		next := '-'
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_.", r)) {
			next = r
		}
		if next == '-' && strings.HasSuffix(b.String(), "-") {
			continue
		}
		b.WriteRune(next)
	}
	if t := strings.Trim(b.String(), "-."); t != "" {
		return t
	}
	return "branch"
}

func nextName(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	for n := 2; ; n++ {
		if c := name + "-" + strconv.Itoa(n); !taken[c] {
			return c
		}
	}
}

func worktreeFailed(msg string) error { return &Error{Code: wire.FailureWorktreeFailed, Message: msg} }

type worktreeState struct {
	reservedMu sync.Mutex
	reserved   map[string]bool
	sizeMu     sync.Mutex
	sizes      map[string]sizedAt
	sizing     map[string]bool
	// sweepMu takes one worktree sweep at a time (worktreesweep.go).
	sweepMu sync.Mutex
	sweeps  sync.WaitGroup
}

type sizedAt struct {
	size wire.WorktreeSize
	at   time.Time
}

// ensureExcluded keeps the worktrees folder out of the repository's view of the folder.
func ensureExcluded(commonDir string) error {
	if err := excludeLine(commonDir, "# Worktrees the Agent Loops app makes for its agents.", "/.agents/worktrees/"); err != nil {
		return worktreeFailed("Could not keep the worktrees out of the folder: " + err.Error())
	}
	return nil
}

// excludeLine adds line, after its comment, to the repository's info/exclude when it is not
// there yet. The file is shared by the checkout and all its worktrees, and never committed.
func excludeLine(commonDir, comment, line string) error {
	path := filepath.Join(commonDir, "info", "exclude")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%s could not be read, so it was left as it is", path)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	text := string(b)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += comment + "\n" + line + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: the mode this folder always had; only its error is new
		return err
	}
	// store-bounds: not a store: the repository's info/exclude, a file of git.
	return store.WriteAtomically(path, []byte(text), mode)
}

func workingFolder(root, prefix string) string {
	if prefix == "" {
		return root
	}
	if inside := filepath.Join(root, prefix); isDir(inside) {
		return inside
	}
	return root
}

// prepareWorktree is DaemonCore.prepareWorktree: the worktree a start asked for, made or
// found, and the folder the agent works in.
func (d *Daemon) prepareWorktree(folder string, choice wire.WorktreeChoice, prompt string) (*wire.AgentWorktree, string, error) {
	return d.prepareWorktreeFrom(folder, folder, choice, prompt)
}

// prepareWorktreeFrom is prepareWorktree with a new worktree made from what from has
// checked out: the folder for a start, or the worktree an agent moves out of
// (053), so its commits come along.
func (d *Daemon) prepareWorktreeFrom(folder, from string, choice wire.WorktreeChoice, prompt string) (*wire.AgentWorktree, string, error) {
	repo, err := findRepository(folder)
	if err != nil {
		return nil, "", worktreeFailed(repositoryWords(err,
			folder+" is not in a git repository, so it has no worktrees.",
			"No worktree was made: git could not read the repository of "+folder+", so try again. Git said:"))
	}
	folderURL := store.FolderURL(folder)
	var wt *wire.AgentWorktree
	var cwd string
	switch {
	case choice.New != nil || choice.Named != nil:
		wt, cwd, err = d.prepareNewWorktree(repo, folder, from, choice, prompt, folderURL)
	case choice.Branch != nil:
		wt, cwd, err = d.prepareBranchWorktree(repo, folder, choice.Branch.Value, folderURL)
	case choice.Existing != nil:
		wt, cwd, err = prepareExistingWorktree(repo, folder, choice, folderURL)
	default:
		return nil, "", Invalid("A worktree choice names new, named, branch or existing.")
	}
	if err == nil {
		// Its commit-msg hook (#6372): also in a worktree made before the hook was.
		installTrailerHookAt(wt)
	}
	return wt, cwd, err
}

// prepareNewWorktree makes a new worktree, on a new branch, from what from has checked out.
// A named choice keeps its name; a new one takes a name from the prompt.
func (d *Daemon) prepareNewWorktree(repo repository, folder, from string, choice wire.WorktreeChoice, prompt string, folderURL wire.URL) (*wire.AgentWorktree, string, error) {
	wanted := ""
	if choice.Named != nil {
		wanted = worktreeName(choice.Named.Value, true, time.Now())
	} else {
		wanted = worktreeName(prompt, false, time.Now())
	}
	head, err := git(from, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return nil, "", worktreeFailed("There is no commit here yet to base a worktree on. Commit something first.")
	}
	base := worktreeBase(from, head)
	if err := ensureExcluded(repo.commonDir); err != nil {
		return nil, "", err
	}
	for attempt := 0; attempt < 20; attempt++ {
		name := d.reserveName(repo, wanted)
		root := filepath.Join(repo.worktreesFolder(), name)
		branch := branchPrefix + name
		if _, err := git(folder, "show-ref", "--verify", "-q", "refs/heads/"+branch); err == nil {
			d.release(name)
			d.reservedMu.Lock()
			d.reserved[name] = true // taken by a branch: try the next
			d.reservedMu.Unlock()
			continue
		}
		_, err := git(from, "worktree", "add", "-b", branch, root, "HEAD")
		d.release(name)
		if err != nil {
			if strings.Contains(err.Error(), "already exists") {
				d.reservedMu.Lock()
				d.reserved[name] = true
				d.reservedMu.Unlock()
				continue
			}
			return nil, "", worktreeFailed("Could not make a worktree: " + err.Error())
		}
		b := branch
		wt := &wire.AgentWorktree{Name: name, Root: store.FolderURL(canonical(root)), Branch: &b, Folder: folderURL, Base: base, MadeByApp: true}
		return wt, workingFolder(canonical(root), repo.prefix), nil
	}
	return nil, "", worktreeFailed("Could not find a free name for a worktree like " + wanted + ".")
}

// prepareBranchWorktree makes a worktree on branch b: a local branch, or a new local one
// that tracks a remote's.
func (d *Daemon) prepareBranchWorktree(repo repository, folder, b string, folderURL wire.URL) (*wire.AgentWorktree, string, error) {
	list, _ := worktreeList(folder)
	for _, e := range list {
		if e.branch == b {
			return nil, "", worktreeFailed(b + " is already checked out in " + e.root + ". Choose that worktree instead.")
		}
	}
	local := false
	if _, err := git(folder, "show-ref", "--verify", "-q", "refs/heads/"+b); err == nil {
		local = true
	}
	remote := ""
	if !local {
		remotes, _ := git(folder, "remote")
		for _, r := range strings.Fields(remotes) {
			if _, err := git(folder, "show-ref", "--verify", "-q", "refs/remotes/"+r+"/"+b); err == nil {
				remote = r
				break
			}
		}
		if remote == "" {
			return nil, "", worktreeFailed("There is no branch called " + b + " here.")
		}
	}
	if err := ensureExcluded(repo.commonDir); err != nil {
		return nil, "", err
	}
	name := d.reserveName(repo, folderForBranch(b))
	defer d.release(name)
	root := filepath.Join(repo.worktreesFolder(), name)
	var err error
	if local {
		_, err = git(folder, "worktree", "add", root, b)
	} else {
		_, err = git(folder, "worktree", "add", "--track", "-b", b, root, remote+"/"+b)
	}
	if err != nil {
		return nil, "", worktreeFailed("Could not make a worktree on " + b + ": " + err.Error())
	}
	wt := &wire.AgentWorktree{Name: name, Root: store.FolderURL(canonical(root)), Branch: &b, Folder: folderURL, MadeByApp: true}
	return wt, workingFolder(canonical(root), repo.prefix), nil
}

// prepareExistingWorktree is the worktree of the folder's repository that the choice names.
func prepareExistingWorktree(repo repository, folder string, choice wire.WorktreeChoice, folderURL wire.URL) (*wire.AgentWorktree, string, error) {
	root := canonical(store.FolderPath(choice.Existing.Value))
	list, _ := worktreeList(folder)
	for _, e := range list {
		if canonical(e.root) != root {
			continue
		}
		if e.prunable || !isDir(root) {
			return nil, "", &Error{Code: wire.FailureWorktreeMissing, Message: "The worktree " + filepath.Base(root) + " is not there any more."}
		}
		var branch *string
		if e.branch != "" {
			b := e.branch
			branch = &b
		}
		made := strings.HasPrefix(root, canonical(repo.worktreesFolder())+"/") && branch != nil
		wt := &wire.AgentWorktree{Name: filepath.Base(root), Root: store.FolderURL(root), Branch: branch, Folder: folderURL, MadeByApp: made}
		return wt, workingFolder(root, repo.prefix), nil
	}
	return nil, "", &Error{Code: wire.FailureNotAWorktree, Message: root + " is not a worktree of " + folder + "'s repository."}
}

// reserveName is WorktreeName.next over the worktrees folder and the names being made.
func (d *Daemon) reserveName(repo repository, wanted string) string {
	d.reservedMu.Lock()
	defer d.reservedMu.Unlock()
	if d.reserved == nil {
		d.reserved = map[string]bool{}
	}
	taken := map[string]bool{}
	for k := range d.reserved {
		taken[k] = true
	}
	entries, _ := os.ReadDir(repo.worktreesFolder())
	for _, e := range entries {
		taken[e.Name()] = true
	}
	name := nextName(wanted, taken)
	d.reserved[name] = true
	return name
}

func (d *Daemon) release(name string) {
	d.reservedMu.Lock()
	delete(d.reserved, name)
	d.reservedMu.Unlock()
}

// undoWorktree takes back a worktree made for a start that then failed.
func undoWorktree(choice wire.WorktreeChoice, wt *wire.AgentWorktree) {
	if wt == nil || choice.Existing != nil {
		return
	}
	folder := store.FolderPath(wt.Folder)
	if _, err := git(folder, "worktree", "remove", "--force", store.FolderPath(wt.Root)); err != nil {
		log.Printf("worktrees: could not undo %s: %v", store.FolderPath(wt.Root), err)
	}
	if (choice.New != nil || choice.Named != nil) && wt.Branch != nil {
		if _, err := git(folder, "branch", "-D", *wt.Branch); err != nil {
			log.Printf("worktrees: could not delete branch %s: %v", *wt.Branch, err)
		}
	}
}

// worktreeBase is what a new worktree is made from: the branch from has checked out, or
// head, its commit, when from is detached. A failed read gives nil, never "", so a note
// says nothing of the base rather than "from ." (#6098); git is asked again once first,
// as a read can fail on a busy machine.
func worktreeBase(from, head string) *string {
	for attempt := 0; attempt < 2; attempt++ {
		branch, err := git(from, "rev-parse", "--abbrev-ref", "HEAD")
		switch {
		case err != nil || branch == "":
			continue
		case branch == "HEAD":
			if head == "" {
				return nil
			}
			return &head
		default:
			return &branch
		}
	}
	return nil
}

func worktreeNote(wt wire.AgentWorktree) string {
	on := ", detached"
	if wt.Branch != nil {
		on = " on " + *wt.Branch
	}
	from := ""
	if wt.MadeByApp && wt.Base != nil {
		from = ", from " + *wt.Base
	}
	return "Working in worktree " + wt.Name + on + from + "."
}

// takeStartingPoint is DaemonCore.takeStartingPoint: the commit the agent started from,
// for its changes; kept quietly.
func (d *Daemon) takeStartingPoint(id wire.UUID, cwd string) {
	out, err := git(cwd, "rev-parse", "--show-toplevel", "HEAD")
	lines := strings.Split(out, "\n")
	if err != nil || len(lines) != 2 || len(lines[1]) < 40 {
		return
	}
	d.agentMu.Lock()
	defer d.agentMu.Unlock()
	if a := d.Store.Agent(id); a != nil && a.StartingPoint == nil {
		a.StartingPoint = &wire.StartingPoint{Repository: store.FolderURL(lines[0]), Commit: lines[1]}
		if err := d.Store.Save(*a); err != nil {
			log.Printf("worktrees: could not save the starting point of %s: %v", id, err)
		}
	}
}

// MARK: listing (worktrees/list, worktrees/all)

func (d *Daemon) agentsIn(root string, nested []string) []wire.UUID {
	var out []wire.Agent
	for _, a := range d.Store.Agents() {
		if a.State == wire.AgentStateArchived {
			continue
		}
		cwd := canonical(store.FolderPath(a.Cwd))
		if !inside(root, cwd) {
			continue
		}
		deeper := false
		for _, n := range nested {
			deeper = deeper || (n != root && inside(root, n) && inside(n, cwd))
		}
		if !deeper {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	ids := []wire.UUID{}
	for _, a := range out {
		ids = append(ids, a.ID)
	}
	return ids
}

func porcelainCount(dir string) int64 {
	out, err := git(dir, "status", "--porcelain")
	if err != nil || out == "" {
		return 0
	}
	return int64(strings.Count(out, "\n") + 1)
}

func (d *Daemon) listWorktrees(folder string) wire.WorktreesListResponse {
	empty := wire.WorktreesListResponse{Worktrees: []wire.WorktreeSummary{}, Branches: []wire.BranchSummary{}}
	repo, ok := repositoryOf(folder)
	if !ok {
		return empty
	}
	list, err := worktreeList(folder)
	if err != nil {
		return empty
	}
	resp := empty
	resp.IsRepository = true
	top := canonical(repo.toplevel)
	hasCommit := false
	var roots []string
	for _, e := range list {
		roots = append(roots, canonical(e.root))
		if canonical(e.root) == top {
			hasCommit = e.head != "" && strings.Trim(e.head, "0") != ""
		}
	}
	if !hasCommit {
		_, err := git(folder, "rev-parse", "--verify", "-q", "HEAD")
		hasCommit = err == nil
	}
	base := ""
	checkedOut := map[string]bool{}
	var wanted []string
	for _, e := range list {
		root := canonical(e.root)
		s := wire.WorktreeSummary{Name: filepath.Base(root), Root: store.FolderURL(root), IsFolder: root == top,
			Exists: !e.prunable && isDir(root), Agents: d.agentsIn(root, roots)}
		if e.branch != "" {
			b := e.branch
			s.Branch = &b
			checkedOut[b] = true
		}
		if root == top {
			base = e.branch
			if base == "" {
				base = e.head
			}
		}
		s.MadeByApp = strings.HasPrefix(root, canonical(repo.worktreesFolder())+"/") && s.Branch != nil
		if s.Exists && !s.IsFolder {
			if size, ok := d.cachedSize(root); ok {
				s.Size = &size
			}
			wanted = append(wanted, root)
		}
		resp.Worktrees = append(resp.Worktrees, s)
	}
	worktreesFillStatus(resp.Worktrees, base)
	d.wantSizes(wanted)
	if hasCommit {
		resp.CanMakeNew = true
		resp.Branches = branches(folder, checkedOut)
	} else {
		why := "There is no commit here yet to base a worktree on."
		resp.WhyNot = &why
	}
	return resp
}

// worktreesFillStatus gives a status to at most 12 worktrees that are there: the folder,
// those with agents, then the rest.
func worktreesFillStatus(worktrees []wire.WorktreeSummary, base string) {
	order := make([]int, len(worktrees))
	for i := range order {
		order[i] = i
	}
	rank := func(s wire.WorktreeSummary) int {
		switch {
		case s.IsFolder:
			return 0
		case len(s.Agents) > 0:
			return 1
		}
		return 2
	}
	sort.SliceStable(order, func(i, j int) bool { return rank(worktrees[order[i]]) < rank(worktrees[order[j]]) })
	for n, i := range order {
		s := &worktrees[i]
		if n >= 12 || !s.Exists {
			continue
		}
		st := worktreeStatusOf(s, base)
		s.Status = &st
	}
}

// worktreeStatusOf is a worktree's uncommitted changes, how far it is ahead of and behind
// its upstream, and its commits not in base.
func worktreeStatusOf(s *wire.WorktreeSummary, base string) wire.WorktreeStatus {
	root := store.FolderPath(s.Root)
	st := wire.WorktreeStatus{Uncommitted: porcelainCount(root)}
	if out, err := git(root, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		if f := strings.Fields(out); len(f) == 2 {
			ahead, _ := strconv.ParseInt(f[0], 10, 64)
			behind, _ := strconv.ParseInt(f[1], 10, 64)
			st.Ahead, st.Behind = &ahead, &behind
		}
	}
	if !s.IsFolder && s.Branch != nil && base != "" && *s.Branch != base {
		if out, err := git(root, "rev-list", "--count", base+".."+*s.Branch); err == nil {
			n, _ := strconv.ParseInt(out, 10, 64)
			st.Unmerged = &n
		}
	}
	return st
}

// branches is GitWorktrees.branches less those checked out: local first, then remote ones a
// local name does not shadow, 200 at most.
func branches(dir string, checkedOut map[string]bool) []wire.BranchSummary {
	out := []wire.BranchSummary{}
	refs, err := git(dir, "for-each-ref", "--sort=-committerdate", "--count=1000", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return out
	}
	remotesText, _ := git(dir, "remote")
	remotes := strings.Fields(remotesText)
	local := map[string]bool{}
	var remote []wire.BranchSummary
	for _, r := range strings.Split(refs, "\n") {
		switch {
		case strings.HasPrefix(r, "refs/heads/"):
			name := strings.TrimPrefix(r, "refs/heads/")
			local[name] = true
			if !checkedOut[name] {
				out = append(out, wire.BranchSummary{Name: name})
			}
		case strings.HasPrefix(r, "refs/remotes/"):
			rest := strings.TrimPrefix(r, "refs/remotes/")
			for _, rm := range remotes {
				if name, ok := strings.CutPrefix(rest, rm+"/"); ok && name != "HEAD" {
					rmc := rm
					remote = append(remote, wire.BranchSummary{Name: name, Remote: &rmc})
				}
			}
		}
	}
	for _, b := range remote {
		if !local[b.Name] && !checkedOut[b.Name] {
			out = append(out, b)
		}
	}
	return out[:min(len(out), 200)]
}

// MARK: sizes (worktrees/sized)

func (d *Daemon) cachedSize(root string) (wire.WorktreeSize, bool) {
	d.sizeMu.Lock()
	defer d.sizeMu.Unlock()
	s, ok := d.sizes[root]
	return s.size, ok
}

// wantSizes measures, one after another and off the caller, the roots not measured in
// ten minutes, and says worktrees/sized.
func (d *Daemon) wantSizes(roots []string) {
	d.sizeMu.Lock()
	if d.sizes == nil {
		d.sizes, d.sizing = map[string]sizedAt{}, map[string]bool{}
	}
	var todo []string
	for _, r := range roots {
		if s, ok := d.sizes[r]; (!ok || time.Since(s.at) > 10*time.Minute) && !d.sizing[r] {
			d.sizing[r] = true
			todo = append(todo, r)
		}
	}
	for r, s := range d.sizes {
		if time.Since(s.at) > 20*time.Minute {
			delete(d.sizes, r)
		}
	}
	d.sizeMu.Unlock()
	if len(todo) == 0 {
		return
	}
	d.goJob("worktree sizes", func() {
		var notes []wire.WorktreeSizeNote
		for _, r := range todo {
			if !isDir(r) {
				continue
			}
			size := measure(r, time.Now().Add(10*time.Second))
			d.sizeMu.Lock()
			d.sizes[r] = sizedAt{size, time.Now()}
			delete(d.sizing, r)
			d.sizeMu.Unlock()
			notes = append(notes, wire.WorktreeSizeNote{Root: store.FolderURL(r), Size: size})
		}
		d.sizeMu.Lock()
		for _, r := range todo {
			delete(d.sizing, r)
		}
		d.sizeMu.Unlock()
		if len(notes) > 0 {
			Broadcast(d, wire.NotifyWorktreesSized, wire.WorktreesSized{Sizes: notes})
		}
	})
}

// measure is the bytes of the regular files under root, within a budget of a million
// files and a deadline; past either, partial.
func measure(root string, deadline time.Time) wire.WorktreeSize {
	var size wire.WorktreeSize
	n := 0
	// The callback returns only nil or SkipAll, so the walk has no error to give.
	_ = filepath.WalkDir(root, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n++
		if n%512 == 0 && time.Now().After(deadline) || n > 1_000_000 {
			size.Partial = true
			return fs.SkipAll
		}
		if e.Type().IsRegular() {
			if fi, err := e.Info(); err == nil {
				size.Bytes += allocated(fi)
			}
		}
		return nil
	})
	return size
}

// MARK: removing (worktrees/check, worktrees/remove, worktrees/removeBuildOutput)

type removalFacts struct {
	repo        repository
	entry       worktreeEntry
	root        string
	madeByApp   bool
	blockedBy   []wire.UUID
	uncommitted int64
	unmerged    bool
	base        string
}

func (d *Daemon) removalFacts(r wire.WorktreeRemovalRequest) (removalFacts, error) {
	folder := store.FolderPath(r.Folder)
	repo, err := findRepository(folder)
	if err != nil {
		if !errors.Is(err, errNotARepository) {
			return removalFacts{}, worktreeFailed(repositoryWords(err, "", "Nothing was removed: git could not read the repository of "+folder+", so try again. Git said:"))
		}
		return removalFacts{}, &Error{Code: wire.FailureNotAWorktree, Message: folder + " is not in a git repository."}
	}
	root := canonical(store.FolderPath(r.Root))
	list, _ := worktreeList(folder)
	var f removalFacts
	found := false
	for _, e := range list {
		if canonical(e.root) == root && root != canonical(repo.toplevel) {
			f.entry, found = e, true
		}
	}
	if !found {
		return removalFacts{}, &Error{Code: wire.FailureNotAWorktree, Message: root + " is not a worktree of " + folder + "'s repository."}
	}
	f.repo, f.root = repo, root
	f.madeByApp = strings.HasPrefix(root, canonical(repo.worktreesFolder())+"/") && f.entry.branch != ""
	f.blockedBy = d.agentsIn(root, nil)
	if isDir(root) && !f.entry.prunable {
		f.uncommitted = porcelainCount(root)
	}
	if strings.HasPrefix(f.entry.branch, branchPrefix) {
		d.removalFactsBase(&f, folder, list)
	}
	return f, nil
}

// removalFactsBase finds the base of a worktree the app made, and whether its branch has
// commits not in that base. The base is an agent's in the folder, else what the folder has
// checked out.
func (d *Daemon) removalFactsBase(f *removalFacts, folder string, list []worktreeEntry) {
	for _, a := range d.Store.Agents() {
		if a.Worktree != nil && a.Worktree.Base != nil && folderPath(&a) == folder {
			f.base = *a.Worktree.Base
			break
		}
	}
	if f.base == "" {
		for _, e := range list {
			if canonical(e.root) == canonical(f.repo.toplevel) {
				f.base = e.branch
				if f.base == "" {
					f.base = e.head
				}
			}
		}
	}
	if f.base != "" {
		_, err := git(folder, "merge-base", "--is-ancestor", f.entry.branch, f.base)
		f.unmerged = err != nil
	}
}

func (d *Daemon) blockedWords(ids []wire.UUID) string {
	var names []string
	for _, id := range ids {
		if a := d.Store.Agent(id); a != nil && a.Title != nil {
			names = append(names, *a.Title)
		} else {
			names = append(names, "an agent")
		}
	}
	return "Still in use by " + strings.Join(names, ", ") + ". Archive them first."
}

func (d *Daemon) removeWorktree(r wire.WorktreeRemovalRequest) (wire.WorktreeRemoved, error) {
	f, err := d.removalFacts(r)
	if err != nil {
		return wire.WorktreeRemoved{}, err
	}
	name := filepath.Base(f.root)
	if !f.madeByApp {
		return wire.WorktreeRemoved{}, &Error{Code: wire.FailureNotAWorktree, Message: name + " was not made by this app, so it is not this app's to remove."}
	}
	if len(f.blockedBy) > 0 {
		return wire.WorktreeRemoved{}, &Error{Code: wire.FailureWorktreeInUse, Message: d.blockedWords(f.blockedBy)}
	}
	var lost []string
	if f.uncommitted > 0 {
		s := "s"
		if f.uncommitted == 1 {
			s = ""
		}
		lost = append(lost, strconv.FormatInt(f.uncommitted, 10)+" uncommitted change"+s)
	}
	if f.unmerged {
		lost = append(lost, "commits not in "+f.base)
	}
	if len(lost) > 0 && !r.Confirmed {
		return wire.WorktreeRemoved{}, worktreeFailed("Removing " + name + " would lose " + strings.Join(lost, " and ") + ". Confirm to remove it anyway.")
	}
	folder := store.FolderPath(r.Folder)
	if isDir(f.root) && !f.entry.prunable {
		args := []string{"worktree", "remove"}
		if f.uncommitted > 0 {
			args = append(args, "--force")
		}
		if _, err := git(folder, append(args, f.root)...); err != nil {
			return wire.WorktreeRemoved{}, worktreeFailed("Could not remove " + name + ": " + err.Error())
		}
	} else if _, err := git(folder, "worktree", "prune"); err != nil {
		return wire.WorktreeRemoved{}, worktreeFailed("Could not remove " + name + ": " + err.Error())
	}
	removed := false
	if strings.HasPrefix(f.entry.branch, branchPrefix) {
		flag := "-d"
		if f.unmerged {
			flag = "-D"
		}
		if _, err := git(folder, "branch", flag, f.entry.branch); err != nil {
			return wire.WorktreeRemoved{}, worktreeFailed("Could not remove " + name + ": " + err.Error())
		}
		removed = true
	}
	d.sizeMu.Lock()
	delete(d.sizes, f.root)
	d.sizeMu.Unlock()
	d.noteFolders()
	d.folderChanged(folder)
	return wire.WorktreeRemoved{RemovedBranch: removed}, nil
}

// noteFolders is DaemonCore.noteFolder for every agent: missingFolder says whose folder
// went, and whether its branch is still there to make it again.
func (d *Daemon) noteFolders() {
	for _, a := range d.Store.Agents() {
		d.noteFolder(a.ID)
	}
}

func (d *Daemon) noteFolder(id wire.UUID) {
	a := d.Store.Agent(id)
	if a == nil || a.State == wire.AgentStateArchived {
		return
	}
	var missing *wire.MissingFolder
	if !isDir(store.FolderPath(a.Cwd)) {
		kept := false
		if a.Worktree != nil && a.Worktree.Branch != nil {
			_, err := git(store.FolderPath(a.Worktree.Folder), "show-ref", "--verify", "-q", "refs/heads/"+*a.Worktree.Branch)
			kept = err == nil
		}
		missing = &wire.MissingFolder{BranchKept: kept}
	}
	if (missing == nil) != (a.MissingFolder == nil) || (missing != nil && *missing != *a.MissingFolder) {
		d.change(id, func(a *wire.Agent) { a.MissingFolder = missing })
	}
}

var buildOutputFolders = map[string]bool{"build": true, ".build": true, "DerivedData": true, "node_modules": true, "target": true, "dist": true}

func (d *Daemon) removeBuildOutput(r wire.WorktreeRemovalRequest) (wire.BuildOutputRemoved, error) {
	f, err := d.removalFacts(r)
	if err != nil {
		return wire.BuildOutputRemoved{}, err
	}
	name := filepath.Base(f.root)
	if !f.madeByApp {
		return wire.BuildOutputRemoved{}, &Error{Code: wire.FailureNotAWorktree, Message: name + " was not made by this app, so it is not this app's to clean."}
	}
	if len(f.blockedBy) > 0 {
		return wire.BuildOutputRemoved{}, &Error{Code: wire.FailureWorktreeInUse, Message: d.blockedWords(f.blockedBy)}
	}
	if !isDir(f.root) {
		return wire.BuildOutputRemoved{}, &Error{Code: wire.FailureWorktreeMissing, Message: "The worktree " + name + " is not there any more."}
	}
	candidates := buildOutputCandidates(f.root)
	out := wire.BuildOutputRemoved{Removed: []string{}}
	if len(candidates) > 0 {
		removeIgnoredBuildOutput(f.root, candidates, &out)
	}
	d.sizeMu.Lock()
	delete(d.sizes, f.root)
	d.sizeMu.Unlock()
	d.wantSizes([]string{f.root})
	return out, nil
}

// buildOutputCandidates is each build output folder at the top of root, or one level
// down in a folder that is not hidden and not build output itself. It skips symlinks.
func buildOutputCandidates(root string) []string {
	var candidates []string
	top, _ := os.ReadDir(root)
	consider := func(dir string, e fs.DirEntry) {
		if e.IsDir() && e.Type()&fs.ModeSymlink == 0 && buildOutputFolders[e.Name()] {
			candidates = append(candidates, filepath.Join(dir, e.Name()))
		}
	}
	for _, e := range top {
		consider(root, e)
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !buildOutputFolders[e.Name()] {
			sub, _ := os.ReadDir(filepath.Join(root, e.Name()))
			for _, s := range sub {
				consider(filepath.Join(root, e.Name()), s)
			}
		}
	}
	return candidates
}

// removeIgnoredBuildOutput removes each candidate that git ignores and tracks no file in,
// and adds it and its size to out.
func removeIgnoredBuildOutput(root string, candidates []string, out *wire.BuildOutputRemoved) {
	ignored, err := git(root, append([]string{"check-ignore", "--"}, candidates...)...)
	if err != nil {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, p := range strings.Split(ignored, "\n") {
		if p == "" {
			continue
		}
		if tracked, _ := git(root, "ls-files", "--", p); tracked != "" {
			continue
		}
		size := measure(p, deadline)
		if os.RemoveAll(p) == nil {
			out.Removed = append(out.Removed, p)
			out.Freed.Bytes += size.Bytes
			out.Freed.Partial = out.Freed.Partial || size.Partial
		}
	}
}

// recreateWorktree is DaemonCore.recreateWorktree: made again from its branch, where it was.
func (d *Daemon) recreateWorktree(id wire.UUID) (wire.Agent, error) {
	a := d.Store.Agent(id)
	if a == nil {
		return wire.Agent{}, noSuchAgent()
	}
	if isDir(store.FolderPath(a.Cwd)) {
		d.noteFolder(id)
		return *d.Store.Agent(id), nil
	}
	if a.Worktree == nil || a.Worktree.Branch == nil {
		return wire.Agent{}, &Error{Code: wire.FailureNotAWorktree, Message: "This agent was not in a worktree, so there is nothing to make again."}
	}
	folder := store.FolderPath(a.Worktree.Folder)
	if !isDir(folder) {
		return wire.Agent{}, &Error{Code: wire.FailureFolderGone, Message: "The folder isn't there either (" + shortPath(folder) + ")."}
	}
	branch := *a.Worktree.Branch
	if _, err := git(folder, "show-ref", "--verify", "-q", "refs/heads/"+branch); err != nil {
		d.noteFolder(id)
		return wire.Agent{}, &Error{Code: wire.FailureFolderGone, Message: "Its branch " + branch + " is not there any more, so the worktree cannot be made again."}
	}
	_, _ = git(folder, "worktree", "prune") // tidy-up only: the add below says when it matters
	if _, err := git(folder, "worktree", "add", store.FolderPath(a.Worktree.Root), branch); err != nil {
		return wire.Agent{}, &Error{Code: wire.FailureFolderGone, Message: "The worktree could not be made again: " + err.Error()}
	}
	installTrailerHookAt(a.Worktree)
	d.noteFolder(id)
	return *d.Store.Agent(id), nil
}

func (d *Daemon) registerWorktrees() {
	On(d, wire.CallWorktreesList, func(ctx context.Context, c *Client, r wire.WorktreesListRequest) (wire.WorktreesListResponse, error) {
		return d.listWorktrees(store.FolderPath(r.Folder)), nil
	})
	On(d, wire.CallWorktreesAll, func(ctx context.Context, c *Client, _ wire.Empty) ([]wire.FolderWorktrees, error) {
		out := []wire.FolderWorktrees{}
		seen := map[string]bool{}
		for _, p := range d.folders(false) {
			folder := store.FolderPath(p.Folder.Folder)
			if !isDir(folder) {
				continue
			}
			var wts []wire.WorktreeSummary
			for _, w := range d.listWorktrees(folder).Worktrees {
				if !w.IsFolder && !seen[string(w.Root)] {
					seen[string(w.Root)] = true
					wts = append(wts, w)
				}
			}
			if len(wts) > 0 {
				out = append(out, wire.FolderWorktrees{Folder: p.Folder.Folder, Worktrees: wts})
			}
		}
		return out, nil
	})
	On(d, wire.CallWorktreesCheck, func(ctx context.Context, c *Client, r wire.WorktreeRemovalRequest) (wire.RemovalCheck, error) {
		f, err := d.removalFacts(r)
		if err != nil {
			return wire.RemovalCheck{}, err
		}
		return wire.RemovalCheck{BlockedBy: f.blockedBy, Uncommitted: f.uncommitted, Unmerged: f.unmerged}, nil
	})
	On(d, wire.CallWorktreesRemove, func(ctx context.Context, c *Client, r wire.WorktreeRemovalRequest) (wire.WorktreeRemoved, error) {
		return d.removeWorktree(r)
	})
	On(d, wire.CallWorktreesRemoveBuildOutput, func(ctx context.Context, c *Client, r wire.WorktreeRemovalRequest) (wire.BuildOutputRemoved, error) {
		return d.removeBuildOutput(r)
	})
	On(d, wire.CallAgentsRecreateWorktree, func(ctx context.Context, c *Client, r wire.AgentRequest) (wire.Agent, error) {
		return d.recreateWorktree(r.AgentID)
	})
}
