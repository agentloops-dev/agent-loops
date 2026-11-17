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
