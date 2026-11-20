package agentsd

import (
	"os"
	"path/filepath"
	"testing"
)

// ownTree (#6345): a branch with no commit yet has no head, and that is not a failure; a
// folder with a commit always has its head, never "".
func TestOwnTreeHasTheHeadOrNoCommitYet(t *testing.T) {
	t.Parallel()

	src := repo(t)
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head, tree, err := ownTree(src)
	if err != nil || head != sendGit(t, src, nil, "rev-parse", "HEAD") || tree == "" {
		t.Fatalf("with a commit: %q %q %v", head, tree, err)
	}

	unborn := canonical(t.TempDir())
	sendGit(t, unborn, nil, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(unborn, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head, tree, err = ownTree(unborn)
	if err != nil || head != "" || tree == "" {
		t.Fatalf("with no commit: %q %q %v", head, tree, err)
	}
}

// gitSaidNo is git's own exit status only: a git that gave no answer (it could not start,
// or a signal stopped it) said nothing (#6345).
func TestGitSaidNoIsGitsOwnExitStatus(t *testing.T) {
	t.Parallel()

	dir := repo(t)
	_, err := git(dir, "rev-parse", "-q", "--verify", "refs/heads/none")
	if !gitSaidNo(err, 1) {
		t.Fatalf("a ref that is not there: %v", err)
	}
	if gitSaidNo(nil, 1) || gitSaidNo(&gitError{message: "git rev-parse failed (signal: killed)."}, 1) {
		t.Fatal("no error, or no answer, is not git saying no")
	}
}
