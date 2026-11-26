package agentsd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6372: the commit-msg hook of an app worktree.

func TestRewriteTrailersWritesTheBlockAsTheLastParagraph(t *testing.T) {
	t.Parallel()

	block := []string{"Runtime: claude-code", "Session: s1", "Refs: #7"}
	for _, c := range []struct {
		name, msg string
		off       []string
		want      string
	}{
		{"a subject alone", "Fix it\n", nil,
			"Fix it\n\nRuntime: claude-code\nSession: s1\nRefs: #7\n"},
		{"a body, no newline at the end", "Fix it\n\nWhy it broke.", nil,
			"Fix it\n\nWhy it broke.\n\nRuntime: claude-code\nSession: s1\nRefs: #7\n"},
		{"a person's trailer stays, an off one goes", "Fix it\n\nSigned-off-by: P <p@example.com>\nCost: $3.10\n", []string{"Cost"},
			"Fix it\n\nSigned-off-by: P <p@example.com>\nRuntime: claude-code\nSession: s1\nRefs: #7\n"},
		{"a typed single trailer is the block's, a line already there is not doubled", "Fix it\n\nSession: old\nRefs: #7\n", nil,
			"Fix it\n\nRefs: #7\nRuntime: claude-code\nSession: s1\n"},
		{"Fixes off takes out Refs", "Fix it\n\nRefs: #9\n", []string{"Fixes"},
			"Fix it\n\nRuntime: claude-code\nSession: s1\nRefs: #7\n"},
		{"the runtime's Co-authored-by stays, once, also with an old layer's Co-authored-by off (#7174)",
			"Fix it\n\nGenerated with a runtime\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n", []string{"Co-authored-by"},
			"Fix it\n\nGenerated with a runtime\n\nCo-Authored-By: Claude <noreply@anthropic.com>\nRuntime: claude-code\nSession: s1\nRefs: #7\n"},
		{"a last paragraph of prose is not a block", "Fix it\n\nThis: is a sentence with a colon\nand more words.\n", nil,
			"Fix it\n\nThis: is a sentence with a colon\nand more words.\n\nRuntime: claude-code\nSession: s1\nRefs: #7\n"},
		{"the editor's comments stay after the block", "Fix it\n\n# Please enter the commit message.\n#\n# On branch x\n", nil,
			"Fix it\n\nRuntime: claude-code\nSession: s1\nRefs: #7\n\n# Please enter the commit message.\n#\n# On branch x\n"},
		{"commit -v's scissors and diff stay after it", "Fix it\n# ------------------------ >8 ------------------------\ndiff --git a/x b/x\n", nil,
			"Fix it\n\nRuntime: claude-code\nSession: s1\nRefs: #7\n\n# ------------------------ >8 ------------------------\ndiff --git a/x b/x\n"},
		{"an empty message stays empty, for git to refuse", "\n# Please enter the commit message.\n", nil,
			"\n# Please enter the commit message.\n"},
	} {
		if got := rewriteTrailers(c.msg, "#", block, c.off); got != c.want {
			t.Errorf("%s:\n%q\nwant\n%q", c.name, got, c.want)
		}
	}
	// A rule that turns every trailer off takes the whole block out.
	if got := rewriteTrailers("Fix it\n\nRuntime: x\nCost: $1\n", "#", nil, []string{"Runtime", "Cost"}); got != "Fix it\n" {
		t.Errorf("%q", got)
	}
	// The hook never adds a Co-authored-by, also when the daemon's lines have one.
	if got := rewriteTrailers("Fix it\n", "#", []string{"Co-authored-by: X <x@example.com>"}, nil); got != "Fix it\n" {
		t.Errorf("%q", got)
	}
	// The same block again changes nothing.
	once := rewriteTrailers("Fix it\n", "#", block, nil)
	if twice := rewriteTrailers(once, "#", block, nil); twice != once {
		t.Errorf("%q", twice)
	}
}

func gitWithEnv(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFileMode(t *testing.T, path string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, b, mode); err != nil {
		t.Fatal(err)
	}
}

func lastMessage(t *testing.T, dir string) string {
	t.Helper()
	out, err := git(dir, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTheHookChainsToTheFoldersOwnAndLeavesTheMainCheckout(t *testing.T) {
	t.Parallel()

	dir := repo(t)
	root := filepath.Join(dir, ".agents", "worktrees", "w")
	if _, err := git(dir, "worktree", "add", "-q", "-b", "agents/w", root, "HEAD"); err != nil {
		t.Fatal(err)
	}
	// The folder's own commit-msg, in the repository's hooks folder (as lefthook writes it).
	own := filepath.Join(dir, ".git", "hooks", "commit-msg")
	writeFileMode(t, own, []byte("#!/bin/sh\nprintf '\\nFolder-hook: ran\\n' >> \"$1\"\n"), 0o755)
	if err := installTrailerHook(root); err != nil {
		t.Fatal(err)
	}
	if err := installTrailerHook(root); err != nil { // twice is the same
		t.Fatal(err)
	}
	if err := installTrailerHook(dir); err == nil {
		t.Fatal("the main checkout got a hook")
	}
	if v, _ := git(dir, "config", "--get", "core.hooksPath"); v != "" {
		t.Fatalf("the main checkout's hooksPath changed: %q", v)
	}
	if v, _ := git(root, "config", "--get", "core.hooksPath"); !strings.HasSuffix(v, "/worktrees/w/"+trailerHooksDir) {
		t.Fatalf("%q", v)
	}

	// A fake agents-trailers on PATH stands in for agentsd.
	bin := t.TempDir()
	writeFileMode(t, filepath.Join(bin, trailersCommandName), []byte("#!/bin/sh\nprintf 'Runtime: fake\\n' >> \"$1\"\n"), 0o755)
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")

	// A person's commit: the folder's hook runs, and no block.
	if out, err := gitWithEnv(t, root, []string{path, secretGrantEnv + "="}, "commit", "-q", "--allow-empty", "-m", "by hand"); err != nil {
		t.Fatal(err, out)
	}
	if m := lastMessage(t, root); m != "by hand\n\nFolder-hook: ran" {
		t.Fatalf("%q", m)
	}
	// A session's commit: the folder's hook, then the block.
	session := []string{path, secretGrantEnv + "=g", secretURLEnv + "=http://127.0.0.1:1" + secretCommandPath}
	if out, err := gitWithEnv(t, root, session, "commit", "-q", "--allow-empty", "-m", "by an agent"); err != nil {
		t.Fatal(err, out)
	}
	if m := lastMessage(t, root); m != "by an agent\n\nFolder-hook: ran\nRuntime: fake" {
		t.Fatalf("%q", m)
	}
	// --no-verify skips both.
	if out, err := gitWithEnv(t, root, session, "commit", "-q", "--no-verify", "--allow-empty", "-m", "skipped"); err != nil {
		t.Fatal(err, out)
	}
	if m := lastMessage(t, root); m != "skipped" {
		t.Fatalf("%q", m)
	}
	// The main checkout runs only its own.
	if out, err := gitWithEnv(t, dir, session, "commit", "-q", "--allow-empty", "-m", "main"); err != nil {
		t.Fatal(err, out)
	}
	if m := lastMessage(t, dir); m != "main\n\nFolder-hook: ran" {
		t.Fatalf("%q", m)
	}

	// A folder's hook that refuses the commit still refuses it.
	writeFileMode(t, own, []byte("#!/bin/sh\necho 'no, says the folder' >&2\nexit 1\n"), 0o755)
	if out, err := gitWithEnv(t, root, session, "commit", "-q", "--allow-empty", "-m", "refused"); err == nil || !strings.Contains(out, "no, says the folder") {
		t.Fatal("the folder's refusal did not hold", out)
	}

	// core.hooksPath that the folder sets (as Husky does), also after the install: it runs.
	husky := t.TempDir()
	writeFileMode(t, filepath.Join(husky, "commit-msg"), []byte("#!/bin/sh\nprintf '\\nHusky: ran\\n' >> \"$1\"\n"), 0o755)
	writeFileMode(t, filepath.Join(husky, "pre-commit"), []byte("#!/bin/sh\ntouch \"$HUSKY_MARK\"\n"), 0o755)
	if _, err := git(dir, "config", "core.hooksPath", husky); err != nil {
		t.Fatal(err)
	}
	if err := installTrailerHook(root); err != nil { // as the next session's start does (#6529)
		t.Fatal(err)
	}
	mark := filepath.Join(t.TempDir(), "pre-commit-ran")
	if out, err := gitWithEnv(t, root, append(session, "HUSKY_MARK="+mark), "commit", "-q", "--allow-empty", "-m", "husky"); err != nil {
		t.Fatal(err, out)
	}
	if m := lastMessage(t, root); m != "husky\n\nHusky: ran\nRuntime: fake" {
		t.Fatalf("%q", m)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("the folder's pre-commit did not run")
	}

	// When agents-trailers fails, the commit goes in as it is.
	writeFileMode(t, filepath.Join(bin, trailersCommandName), []byte("#!/bin/sh\nexit 3\n"), 0o755)
	if out, err := gitWithEnv(t, root, append(session, "HUSKY_MARK="+mark), "commit", "-q", "--allow-empty", "-m", "as it is"); err != nil {
		t.Fatal(err, out)
	}
	if m := lastMessage(t, root); m != "as it is\n\nHusky: ran" {
		t.Fatalf("%q", m)
	}
}

// #6529: the worktree's folder links commit-msg and the names the folder's own hooks folder
// has, not every hook's name, so a fetch or a rebase starts no hook for each ref.
func TestTheHookLinksOnlyTheNamesTheFolderHas(t *testing.T) {
	t.Parallel()

	dir := repo(t)
	root := filepath.Join(dir, ".agents", "worktrees", "w")
	if _, err := git(dir, "worktree", "add", "-q", "-b", "agents/w", root, "HEAD"); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o750); err != nil {
		t.Fatal(err)
	}
	gitDir, err := git(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(gitDir, trailerHooksDir)
	linked := func() []string {
		t.Helper()
		var names []string
		for _, name := range gitHookNames {
			if target, err := os.Readlink(filepath.Join(mine, name)); err == nil && target == trailerHookScript {
				names = append(names, name)
			}
		}
		return names
	}

	// An install from before #6529 linked every name; the next install takes them out.
	if err := writeTrailerHooks(mine, map[string]bool{"reference-transaction": true, "pre-push": true, "commit-msg": true}); err != nil {
		t.Fatal(err)
	}
	writeFileMode(t, filepath.Join(hooks, "pre-push.sample"), []byte("#!/bin/sh\n"), 0o755)
	writeFileMode(t, filepath.Join(hooks, "post-merge"), []byte("#!/bin/sh\n"), 0o644) // not executable: git skips it
	if err := installTrailerHook(root); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(linked(), " "); got != "commit-msg" {
		t.Fatalf("linked %q, want commit-msg alone", got)
	}

	// A hook the folder adds gets its link at the next install, and its hook runs.
	mark := filepath.Join(t.TempDir(), "ran")
	writeFileMode(t, filepath.Join(hooks, "reference-transaction"), []byte("#!/bin/sh\ntouch \"$MARK\"\n"), 0o755)
	if err := installTrailerHook(root); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(linked(), " "); got != "commit-msg reference-transaction" {
		t.Fatalf("linked %q", got)
	}
	if out, err := gitWithEnv(t, root, []string{"MARK=" + mark}, "update-ref", "refs/heads/x", "HEAD"); err != nil {
		t.Fatal(err, out)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("the folder's reference-transaction did not run")
	}

	// And it goes when the folder's hook goes.
	if err := os.Remove(filepath.Join(hooks, "reference-transaction")); err != nil {
		t.Fatal(err)
	}
	if err := installTrailerHook(root); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(linked(), " "); got != "commit-msg" {
		t.Fatalf("linked %q", got)
	}
}

func TestAgentsTrailersWritesTheSessionsBlock(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	c.call(t, 1, "folders/add", map[string]any{"folder": store.FolderURL(dir)})
	var id wire.UUID
	if err := json.Unmarshal(c.call(t, 2, "agents/start", map[string]any{"runtimeID": "claude", "cwd": store.FolderURL(dir),
		"prompt": "Write the block", "worktree": map[string]any{"new": map[string]any{}}}), &id); err != nil {
		t.Fatal(err)
	}
	a := d.Store.Agent(id)
	if v, _ := git(store.FolderPath(a.Cwd), "config", "--worktree", "--get", "core.hooksPath"); !strings.HasSuffix(v, trailerHooksDir) {
		t.Fatalf("the new worktree has no hook: %q", v)
	}

	grant := ""
	waitFor(t, "the session's grant", func() bool {
		d.credMu.Lock()
		defer d.credMu.Unlock()
		for g, agent := range d.commandGrants {
			if agent == id {
				grant = g
			}
		}
		return grant != ""
	})
	port, err := d.startAppTools()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		secretURLEnv:   "http://127.0.0.1:" + strconv.Itoa(port) + secretCommandPath,
		secretGrantEnv: grant,
	}
	msg := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	writeFileMode(t, msg, []byte("Fix it\n\nSigned-off-by: P <p@example.com>\nCost: $9.99\n"), 0o644)
	var errOut bytes.Buffer
	if code := TrailersCommand([]string{msg}, func(k string) string { return env[k] }, &errOut); code != 0 || errOut.Len() > 0 {
		t.Fatal(code, errOut.String())
	}
	b, _ := os.ReadFile(msg)
	got := string(b)
	// Cost is off by default, so the typed one goes; the person's trailer stays.
	if !strings.Contains(got, "Signed-off-by: P <p@example.com>\n") || strings.Contains(got, "Cost:") ||
		!strings.Contains(got, "Session: "+string(id)+"\n") || !strings.Contains(got, "Managed-by: Agent Loops") {
		t.Fatalf("%q", got)
	}

	// A grant agentsd does not know, and an agentsd that does not answer: one line, the
	// message as it was, and never a refusal.
	for _, e := range []map[string]string{
		{secretURLEnv: env[secretURLEnv], secretGrantEnv: "not-a-grant"},
		{secretURLEnv: "http://127.0.0.1:1" + secretCommandPath, secretGrantEnv: grant},
	} {
		errOut.Reset()
		writeFileMode(t, msg, []byte("Fix it\n"), 0o644)
		if code := TrailersCommand([]string{msg}, func(k string) string { return e[k] }, &errOut); code != 0 {
			t.Fatal(code)
		}
		if b, _ := os.ReadFile(msg); string(b) != "Fix it\n" || strings.Count(errOut.String(), "\n") != 1 {
			t.Fatalf("%q %q", b, errOut.String())
		}
	}
	// No session: nothing, and nothing said.
	errOut.Reset()
	if code := TrailersCommand([]string{msg}, func(string) string { return "" }, &errOut); code != 0 || errOut.Len() > 0 {
		t.Fatal(code, errOut.String())
	}
}
