package agentsd

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Ports WorktreeTests and WorktreeNameTests: a start in a new worktree, listing, the
// checks before removing, removing, and build output cleared.

func repo(t *testing.T) string {
	t.Helper()
	dir := canonical(t.TempDir())
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"},
		{"commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestWorktreeNames(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 16, 9, 5, 0, 0, time.UTC)
	for prompt, want := range map[string]string{
		"Please fix the login bug on the dashboard": "fix-login-bug-dashboard",
		"Café crème":                        "cafe-creme",
		"?!":                                "agent-1016-0905",
		strings.Repeat("x", 40):             strings.Repeat("x", 32),
		"one two three four five six seven": "one-two-three-four",
	} {
		if got := worktreeName(prompt, false, at); got != want {
			t.Errorf("%q: %q, want %q", prompt, got, want)
		}
	}
	if got := worktreeName("fix the bug", true, at); got != "fix-the-bug" {
		t.Error(got)
	}
	if folderForBranch("agents/feat/x y") != "feat-x-y" || folderForBranch("..") != "branch" {
		t.Error(folderForBranch("agents/feat/x y"))
	}
	if nextName("a", map[string]bool{"a": true, "a-2": true}) != "a-3" {
		t.Error("next")
	}
}

func TestAStartInANewWorktreeAndItsRemoval(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	c.call(t, 1, "folders/add", map[string]any{"folder": store.FolderURL(dir)})
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 2, "agents/start", map[string]any{"runtimeID": "claude", "cwd": store.FolderURL(dir),
		"prompt": "Please fix the login bug", "worktree": map[string]any{"new": map[string]any{}}}), &id)
	a := d.Store.Agent(id)
	root := filepath.Join(dir, ".agents", "worktrees", "fix-login-bug")
	if a.Worktree == nil || store.FolderPath(a.Cwd) != root || *a.Worktree.Branch != "agents/fix-login-bug" ||
		*a.Worktree.Base != "main" || !a.Worktree.MadeByApp || store.FolderPath(a.Worktree.Folder) != dir {
		t.Fatalf("%+v %+v", a.Cwd, a.Worktree)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude")); !strings.Contains(string(b), "/.agents/worktrees/") {
		t.Fatalf("%s", b)
	}
	waitFor(t, "finished", func() bool { return d.Store.Agent(id).State == wire.AgentStateFinished })
	if k := kinds(d, id); k[0] != "runtimeNote" {
		t.Fatal(k)
	}
	waitFor(t, "a starting point", func() bool { return d.Store.Agent(id).StartingPoint != nil })
	var list wire.WorktreesListResponse
	mustUnmarshal(t, c.call(t, 3, "worktrees/list", map[string]any{"folder": store.FolderURL(dir)}), &list)
	if !list.IsRepository || !list.CanMakeNew || len(list.Worktrees) != 2 {
		t.Fatalf("%+v", list)
	}
	wt := list.Worktrees[1]
	if !wt.MadeByApp || wt.IsFolder || len(wt.Agents) != 1 || wt.Agents[0] != id || wt.Status == nil || *wt.Status.Unmerged != 0 {
		t.Fatalf("%+v", wt)
	}
	req := map[string]any{"folder": store.FolderURL(dir), "root": store.FolderURL(root)}
	var check wire.RemovalCheck
	mustUnmarshal(t, c.call(t, 4, "worktrees/check", req), &check)
	if len(check.BlockedBy) != 1 || check.Unmerged {
		t.Fatalf("%+v", check)
	}
	if e := c.callError(t, 5, "worktrees/remove", req); e.Code != wire.FailureWorktreeInUse || !strings.HasPrefix(e.Message, "Still in use by ") {
		t.Fatalf("%+v", e)
	}
	// Untracked before the archive, and the archive removes it all the same (#3877).
	must(t, os.WriteFile(filepath.Join(root, "new.txt"), []byte("x"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	c.call(t, 6, "agents/archive", map[string]any{"agentID": id})
	d.sweeps.Wait()
	if isDir(root) {
		t.Fatal("the archive kept the worktree")
	}
	if out, _ := git(dir, "branch", "--list", "agents/fix-login-bug"); out != "" {
		t.Fatal(out)
	}
	// worktrees/remove still asks before it loses changes in a worktree no agent has.
	loose := filepath.Join(dir, ".agents", "worktrees", "loose")
	if _, err := git(dir, "worktree", "add", "-q", "-b", "agents/loose", loose, "HEAD"); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(loose, "new.txt"), []byte("x"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	req["root"] = store.FolderURL(loose)
	if e := c.callError(t, 7, "worktrees/remove", req); !strings.Contains(e.Message, "would lose 1 uncommitted change. Confirm") {
		t.Fatal(e.Message)
	}
	req["confirmed"] = true
	var removed wire.WorktreeRemoved
	mustUnmarshal(t, c.call(t, 8, "worktrees/remove", req), &removed)
	if !removed.RemovedBranch || isDir(loose) {
		t.Fatalf("%+v", removed)
	}
}

func TestAWorktreeOutsideARepositoryIsRefused(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	if e := c.callError(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": store.FolderURL(dir), "prompt": "x",
		"worktree": map[string]any{"new": map[string]any{}}}); e.Code != wire.FailureWorktreeFailed || !strings.HasSuffix(e.Message, "is not in a git repository, so it has no worktrees.") {
		t.Fatalf("%+v", e)
	}
}

func TestBuildOutputIsCleared(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	must(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, errOnly(git(dir, "add", ".gitignore")))
	must(t, errOnly(git(dir, "commit", "-q", "-m", "ignore")))
	wt, _, err := d.prepareWorktree(dir, wire.WorktreeChoice{Named: &wire.WorktreeChoiceNamed{Value: "Clean me"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	root := store.FolderPath(wt.Root)
	if filepath.Base(root) != "clean-me" {
		t.Fatal(root)
	}
	must(t, os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755))                              //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(root, "node_modules", "x", "big"), make([]byte, 10000), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, os.MkdirAll(filepath.Join(root, "build"), 0o755))                                          //nolint:gosec // G301: the mode this folder always had; only its error is new; not ignored: kept
	var out wire.BuildOutputRemoved
	mustUnmarshal(t, c.call(t, 1, "worktrees/removeBuildOutput", map[string]any{"folder": store.FolderURL(dir), "root": wt.Root}), &out)
	if len(out.Removed) != 1 || !strings.HasSuffix(out.Removed[0], "node_modules") || out.Freed.Bytes == 0 || !isDir(filepath.Join(root, "build")) {
		t.Fatalf("%+v", out)
	}
}

// A plain name that is not there yet makes a new worktree with that name (#5185):
// the lead skill starts lanes in fix-github-issue-<n>. A name with a slash is a
// branch, so one that is not there is still refused.
func TestAHelperNamesANewWorktree(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	choice, err := d.helperWorktree("fix-github-issue-4706", dir)
	if err != nil || choice == nil || choice.Named == nil || choice.Named.Value != "fix-github-issue-4706" {
		t.Fatalf("%+v %v", choice, err)
	}
	if choice, err := d.helperWorktree("New", dir); err != nil || choice.New == nil {
		t.Fatalf("%+v %v", choice, err)
	}
	for _, refused := range []string{"agents/no-such-branch", "Fix Issue"} {
		if _, err := d.helperWorktree(refused, dir); err == nil || !strings.Contains(err.Error(), "there is no worktree called") {
			t.Fatalf("%s: %v", refused, err)
		}
	}
}

// A worktree is also found by its branch, with or without agents/ and origin/ (#6033):
// leads gave agents/<name> and origin/agents/<name> for the worktree <name>. A branch
// that two worktrees have is refused, naming both.
func TestAHelperFindsAWorktreeByItsBranch(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	lane := filepath.Join(canonical(t.TempDir()), "work-github-issue-5287")
	run("worktree", "add", "-q", "-b", "agents/fix-github-issue-5287", lane)
	run("branch", "loose") // checked out nowhere
	for _, written := range []string{"work-github-issue-5287", "agents/fix-github-issue-5287", "fix-github-issue-5287",
		"origin/agents/fix-github-issue-5287", "origin/fix-github-issue-5287", "refs/heads/agents/fix-github-issue-5287"} {
		choice, err := d.helperWorktree(written, dir)
		if err != nil || choice == nil || choice.Existing == nil || store.FolderPath(choice.Existing.Value) != lane {
			t.Fatalf("%s: %+v %v", written, choice, err)
		}
	}
	// A branch checked out nowhere still makes a worktree on it; a new name still names one.
	if choice, err := d.helperWorktree("loose", dir); err != nil || choice.Branch == nil || choice.Branch.Value != "loose" {
		t.Fatalf("loose: %+v %v", choice, err)
	}
	if choice, err := d.helperWorktree("fix-github-issue-6033", dir); err != nil || choice.Named == nil || choice.Named.Value != "fix-github-issue-6033" {
		t.Fatalf("new name: %+v %v", choice, err)
	}
	if _, err := d.helperWorktree("origin/agents/no-such-branch", dir); err == nil || !strings.Contains(err.Error(), "there is no worktree called") {
		t.Fatalf("no such branch: %v", err)
	}

	// Two worktrees on one branch (git worktree add --force): refused, naming both.
	twin := filepath.Join(canonical(t.TempDir()), "twin")
	run("worktree", "add", "-q", "--force", twin, "agents/fix-github-issue-5287")
	_, err := d.helperWorktree("agents/fix-github-issue-5287", dir)
	if err == nil || !strings.Contains(err.Error(), "more than one worktree") ||
		!strings.Contains(err.Error(), "work-github-issue-5287") || !strings.Contains(err.Error(), "twin") {
		t.Fatalf("shared branch: %v", err)
	}
	// Its name still finds each.
	if choice, err := d.helperWorktree("twin", dir); err != nil || choice.Existing == nil || store.FolderPath(choice.Existing.Value) != twin {
		t.Fatalf("twin: %+v %v", choice, err)
	}
}

// Only git's own "not a git repository" is the folder being in none: any other failure is
// git's words, never the false "not in a git repository" (#6106).
func TestARepositoryGitCannotReadIsNotCalledNone(t *testing.T) {
	t.Parallel()

	if _, err := findRepository(t.TempDir()); !errors.Is(err, errNotARepository) {
		t.Fatalf("a plain folder: %v", err)
	}
	dir := repo(t)
	if r, err := findRepository(dir); err != nil || canonical(r.toplevel) != canonical(dir) {
		t.Fatalf("a repository: %+v %v", r, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := findRepository(dir)
	if err == nil || errors.Is(err, errNotARepository) {
		t.Fatalf("a config git cannot read: %v", err)
	}
	// A git the system cannot start says why, not only that it failed.
	if _, err := git(filepath.Join(dir, "gone"), "status"); err == nil || !strings.Contains(err.Error(), "git status could not start: ") {
		t.Fatalf("a git that did not start: %v", err)
	}
	if words := repositoryWords(err, "not in one", "Git said:"); !strings.HasPrefix(words, "Git said: ") || !strings.Contains(words, "config") {
		t.Fatal(words)
	}
}

// A send's facts say "no branch" only when git says HEAD is detached, and a patch's git that
// cannot start says why (#6341): a git that did not run to its end is not a fact.
func TestASendSaysNoBranchOnlyWhenGitSaysSo(t *testing.T) {
	t.Parallel()

	d := &Daemon{}
	dir := repo(t)
	if f, err := d.sendFacts(wire.SendFactsRequest{Folder: store.FolderURL(dir)}); err != nil || f.Current == nil || *f.Current != "main" {
		t.Fatalf("on main: %+v %v", f, err)
	}
	run(t, dir, "git", "checkout", "-q", "--detach")
	if f, err := d.sendFacts(wire.SendFactsRequest{Folder: store.FolderURL(dir)}); err != nil || f.Current != nil {
		t.Fatalf("detached: %+v %v", f, err)
	}
	if err := gitTo(filepath.Join(dir, "gone"), io.Discard, "diff"); err == nil || !strings.Contains(err.Error(), "git diff could not start: ") {
		t.Fatalf("a git that did not start: %v", err)
	}
}

// A new worktree's base is the branch, or the commit when detached, and nil when git
// cannot read it: never "", which made the note say "from ." (#6098).
func TestANewWorktreesBaseIsNeverEmpty(t *testing.T) {
	t.Parallel()

	dir := repo(t)
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := worktreeBase(dir, head); got == nil || *got != "main" {
		t.Fatalf("on main: %v", got)
	}
	run(t, dir, "git", "checkout", "-q", "--detach")
	if got := worktreeBase(dir, head); got == nil || *got != head {
		t.Fatalf("detached: %v", got)
	}
	if got := worktreeBase(filepath.Join(dir, "gone"), head); got != nil {
		t.Fatalf("git failed: %q", *got)
	}
}
