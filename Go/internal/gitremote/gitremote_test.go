package gitremote

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	out := "fork\thttps://github.com/me/app.git (fetch)\n" +
		"fork\thttps://github.com/me/app.git (push)\n" +
		"origin\tgit@codeberg.org:team/app.git (fetch)\n" +
		"origin\tgit@codeberg.org:team/app.git (push)\n" +
		"origin\tgit@mirror.example.com:team/app.git (push)\n" +
		"bad line\n"
	if got, want := Parse(out), []string{"git@codeberg.org:team/app.git", "https://github.com/me/app.git"}; !slices.Equal(got, want) {
		t.Fatalf("Parse: %q, want %q", got, want)
	}
	if got := Parse(""); len(got) != 0 {
		t.Fatalf("no remotes: %q", got)
	}
}

func TestURLs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil { //nolint:gosec // git on the test's own temporary folder
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("remote", "add", "upstream", "https://gitlab.com/team/app.git")
	git("remote", "add", "origin", "short:team/app.git")
	git("config", "url.https://code.example.org/.insteadOf", "short:")
	want := []string{"https://code.example.org/team/app.git", "https://gitlab.com/team/app.git"}
	if got := URLs(context.Background(), dir); !slices.Equal(got, want) {
		t.Fatalf("URLs: %q, want %q", got, want)
	}
	if got := URLs(context.Background(), t.TempDir()); len(got) != 0 {
		t.Fatalf("not a repository: %q", got)
	}
}

// A read that fails is an error, not "no remote" (#6064): a folder with a Forgejo remote
// must not look like a folder with none when git is slow.
func TestReadTellsAFailedReadFromNoRemote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil { //nolint:gosec // git on the test's own temporary folder
		t.Fatalf("git init: %v %s", err, out)
	}
	if got, err := Read(context.Background(), dir); err != nil || len(got) != 0 {
		t.Fatalf("a repository with no remote: %q %v", got, err)
	}
	if got, err := Read(context.Background(), t.TempDir()); err != nil || len(got) != 0 {
		t.Fatalf("not a repository: %q %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := Read(ctx, dir); err == nil {
		t.Fatalf("a read with no time left: %q, want an error", got)
	}
}
