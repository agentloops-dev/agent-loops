package agentsd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func (f *fakeForgeGitHub) MakeRepository(_ context.Context, owner, name, _ string) (githubmcp.Repository, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if owner == "" {
		owner = "alexec"
	}
	full := owner + "/" + name
	if f.cannotMake {
		if slices.Contains(f.byPerson, full) {
			return githubmcp.Repository{FullName: full, CloneURL: f.bare, Private: true, Login: "alexec", UserID: 1142830}, false, nil
		}
		return githubmcp.Repository{}, false, &githubmcp.CannotMakeError{FullName: full,
			Err: &githubmcp.GitHubError{Kind: githubmcp.Failed, Message: "GitHub: Resource not accessible by integration", Refused: true}}
	}
	made := !slices.Contains(f.made, full)
	if made {
		f.made = append(f.made, full)
	}
	return githubmcp.Repository{FullName: full, CloneURL: f.bare, Login: "alexec", UserID: 1142830}, made, nil
}

func (f *fakeForgeGitHub) MakePublicRepository(_ context.Context, owner, name, _ string, template bool) (githubmcp.Repository, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if owner == "" {
		owner = "alexec"
	}
	full := owner + "/" + name
	f.public = append(f.public, fmt.Sprintf("%s template=%v", full, template))
	if slices.Contains(f.private, full) {
		return githubmcp.Repository{FullName: full, CloneURL: f.bare, Private: true, Login: "alexec", UserID: 1142830}, false, nil
	}
	made := !slices.Contains(f.made, full)
	if made {
		f.made = append(f.made, full)
	}
	return githubmcp.Repository{FullName: full, CloneURL: f.bare, Login: "alexec", UserID: 1142830}, made, nil
}

// #3708: make_app_repository makes the repository, pushes the starter with its values as the
// first commit on main, clones it and adds it as a folder; called again, it skips each step;
// and it refuses a starter's missing values before it asks GitHub for anything.
func TestMakeAppRepositoryPushesTheStarterAndAddsTheFolder(t *testing.T) {
	d, _, gh, _ := forgeDaemon(t)
	parent := t.TempDir()
	t.Setenv("AGENTS_CLONE_PARENT", parent)
	bare := filepath.Join(t.TempDir(), "tide-log.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "--initial-branch=trunk", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	gh.bare = "file://" + bare
	starterTemplate(t, "agentloops-dev/web-service-starter", webTemplate)
	a := seeded(t, d, gitFolder(t, "git@github.com:alexec/agents.git"), func(a *wire.Agent) { a.State, a.EndedReason = wire.AgentStateRunning, nil })
	if !appToolServed("make_app_repository") {
		t.Fatal("make_app_repository is not served")
	}

	if _, err := d.makeAppRepository(a.ID, ciArgs("name", "tide-log", "starter", "web-service", "values", map[string]string{"APP_NAME": "Tide Log"})); err == nil ||
		!strings.Contains(err.Error(), "GCP_PROJECT") || strings.Contains(err.Error(), "GITHUB_REPO") || len(gh.made) != 0 {
		t.Fatalf("missing values: %v, made %v", err, gh.made)
	}
	if _, err := d.makeAppRepository(a.ID, ciArgs("name", "../x", "starter", "web-service")); err == nil {
		t.Fatal("../x was taken as a name")
	}

	values := map[string]string{"APP_NAME": "Tide Log", "SERVICE": "tidelog", "GCP_PROJECT": "tidelog-1", "GCP_REGION": "us-central1"}
	got, err := d.makeAppRepository(a.ID, ciArgs("name", "tide-log", "starter", "web-service", "values", values))
	if err != nil || !strings.Contains(got, "Made alexec/tide-log on GitHub, private") || !strings.Contains(got, "pushed the web-service starter") ||
		!strings.Contains(got, "added it as a folder") || strings.Contains(got, "ghu_secret") {
		t.Fatalf("%q %v", got, err)
	}
	dest := filepath.Join(parent, "tide-log")
	if log, err := exec.Command("git", "-C", bare, "log", "--format=%an <%ae> %s", "main").CombinedOutput(); err != nil ||
		strings.TrimSpace(string(log)) != "alexec <1142830+alexec@users.noreply.github.com> The starter" {
		t.Fatalf("pushed: %s %v", log, err)
	}
	deploy, _ := os.ReadFile(filepath.Join(dest, ".github/workflows/deploy.yml"))
	if !strings.Contains(string(deploy), "tidelog") || strings.Contains(string(deploy), "__") {
		t.Errorf("deploy.yml is not filled:\n%s", deploy)
	}
	if _, err := os.Stat(filepath.Join(dest, ".github/README.md")); err == nil {
		t.Error("the template's own README is in the new app")
	}
	if fi, err := os.Stat(filepath.Join(dest, "setup-gcp.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Errorf("setup-gcp.sh cannot be run: %v", err)
	}
	if cfg, _ := os.ReadFile(filepath.Join(dest, ".git/config")); strings.Contains(string(cfg), "extraheader") {
		t.Errorf("the token's header is in the clone's config:\n%s", cfg)
	}
	if !slices.ContainsFunc(d.Store.Folders(), func(f wire.Folder) bool { return store.FolderPath(f.Folder) == canonical(dest) }) {
		t.Fatalf("%s is not a folder", dest)
	}

	again, err := d.makeAppRepository(a.ID, ciArgs("name", "tide-log", "starter", "web-service", "values", values))
	if err != nil || !strings.Contains(again, "was on GitHub already") || !strings.Contains(again, "was cloned already") {
		t.Fatalf("again: %q %v", again, err)
	}
	if n, _ := exec.Command("git", "-C", bare, "rev-list", "--count", "main").Output(); strings.TrimSpace(string(n)) != "1" {
		t.Errorf("again pushed: %s commits", n)
	}

	// The folder gone but the repository there: it is cloned again, with what is in it.
	os.RemoveAll(dest)
	kept, err := d.makeAppRepository(a.ID, ciArgs("name", "tide-log", "starter", "web-service", "values", values))
	if err != nil || !strings.Contains(kept, "kept what was in it") {
		t.Fatalf("kept: %q %v", kept, err)
	}
}

// #5563: the GitHub App cannot make a repository, so make_app_repository says the person's
// step (github.com/new from the template, private, then the App installed on it) and makes
// nothing; once the person made it from the template, the call fills in the starter's values
// over the template's files as one commit, clones it and adds it as a folder, and a third call
// pushes nothing more.
func TestMakeAppRepositoryHandsTheRepositoryToThePerson(t *testing.T) {
	d, _, gh, _ := forgeDaemon(t)
	parent := t.TempDir()
	t.Setenv("AGENTS_CLONE_PARENT", parent)
	starterTemplate(t, "agentloops-dev/web-service-starter", webTemplate)
	gh.cannotMake = true
	a := seeded(t, d, gitFolder(t, "git@github.com:alexec/agents.git"), func(a *wire.Agent) { a.State, a.EndedReason = wire.AgentStateRunning, nil })
	values := map[string]string{"APP_NAME": "Tide Log", "SERVICE": "tidelog", "GCP_PROJECT": "tidelog-1", "GCP_REGION": "us-central1"}
	args := ciArgs("name", "tide-log", "starter", "web-service", "values", values)

	_, err := d.makeAppRepository(a.ID, args)
	for _, want := range []string{
		"Nothing was made", "hand_to_person", "browser_navigate",
		"https://github.com/new?description=Tide+Log&name=tide-log&owner=alexec&template_name=web-service-starter&template_owner=agentloops-dev&visibility=private",
		"https://github.com/apps/agent-loops-dev/installations/new", "make_app_repository again",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("refused: no %q in %v", want, err)
		}
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("something was cloned: %v", entries)
	}

	// The person makes it on github.com from the template: GitHub copies the template's
	// files, placeholders and its own files too, as one commit.
	bare := filepath.Join(t.TempDir(), "tide-log.git")
	work := t.TempDir()
	for p, body := range webTemplate {
		full := filepath.Join(work, filepath.FromSlash(p))
		must(t, os.MkdirAll(filepath.Dir(full), 0o755))  //nolint:gosec // G301: the mode this folder always had; only its error is new
		must(t, os.WriteFile(full, []byte(body), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	}
	for _, args := range [][]string{
		{"init", "-q", "--bare", "--initial-branch=main", bare},
		{"-C", work, "init", "-q", "--initial-branch=main"},
		{"-C", work, "add", "-A"},
		{"-C", work, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Initial commit"},
		{"-C", work, "push", "-q", bare, "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gh.bare = "file://" + bare
	gh.byPerson = append(gh.byPerson, "alexec/tide-log")

	got, err := d.makeAppRepository(a.ID, args)
	if err != nil || !strings.Contains(got, "was on GitHub already") || !strings.Contains(got, "filled in the web-service starter's values") ||
		!strings.Contains(got, "added it as a folder") {
		t.Fatalf("%q %v", got, err)
	}
	if log, err := exec.Command("git", "-C", bare, "log", "--format=%an %s", "main").CombinedOutput(); err != nil ||
		strings.TrimSpace(string(log)) != "alexec Fill in the starter\nt Initial commit" {
		t.Fatalf("pushed: %s %v", log, err)
	}
	dest := filepath.Join(parent, "tide-log")
	for p := range webTemplate {
		b, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(p)))
		switch {
		case p == ".starterignore" || strings.HasPrefix(p, ".github/README") || strings.HasPrefix(p, ".github/template") || p == ".github/workflows/template.yml":
			if err == nil {
				t.Errorf("the template's own %s is in the new app", p)
			}
		case p == "icon.bin":
		case err != nil || strings.Contains(string(b), "__"):
			t.Errorf("%s is not filled: %q %v", p, b, err)
		}
	}
	if !slices.ContainsFunc(d.Store.Folders(), func(f wire.Folder) bool { return store.FolderPath(f.Folder) == canonical(dest) }) {
		t.Fatalf("%s is not a folder", dest)
	}

	// The folder gone, the repository filled already: cloned again, nothing pushed.
	os.RemoveAll(dest)
	kept, err := d.makeAppRepository(a.ID, args)
	if err != nil || !strings.Contains(kept, "kept what was in it") {
		t.Fatalf("kept: %q %v", kept, err)
	}
	if n, _ := exec.Command("git", "-C", bare, "rev-list", "--count", "main").Output(); strings.TrimSpace(string(n)) != "2" {
		t.Errorf("again pushed: %s commits", n)
	}
}
