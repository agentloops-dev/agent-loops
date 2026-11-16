package agentsd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestConflictRiskRequestsAreReadWhateverTheForge(t *testing.T) {
	soon := true
	head, base := "feat", "main"
	checks := "passing"
	gh := wire.ForgeItem{Node: wire.LineageNode{Kind: "pr", Ref: "github:acme/app#12"}, Checks: &checks, AutoMerge: &soon, BaseRef: &base, HeadRef: &head}
	if p, ok := conflictRiskPR("github", gh); !ok || p.Number != 12 || p.FetchRef != "refs/pull/12/head" || !p.LandsSoon {
		t.Errorf("github: %+v", p)
	}
	gl := wire.ForgeItem{Node: wire.LineageNode{Kind: "pr", Ref: "gitlab:g/app!7"}, HeadRef: &head, Labels: []wire.ForgeLabel{{Name: "approved"}}}
	if p, ok := conflictRiskPR("gitlab", gl); !ok || p.Number != 7 || p.FetchRef != "refs/merge-requests/7/head" || !p.LandsSoon {
		t.Errorf("gitlab: %+v", p)
	}
	bb := wire.ForgeItem{Node: wire.LineageNode{Kind: "pr", Ref: "bitbucket:ws/app#3"}, HeadRef: &head}
	if p, ok := conflictRiskPR("bitbucket", bb); !ok || p.FetchRef != "refs/heads/feat" || p.LandsSoon {
		t.Errorf("bitbucket: %+v", p)
	}
	if _, ok := conflictRiskPR("azuredevops", wire.ForgeItem{Node: wire.LineageNode{Ref: "azuredevops:o/p/r#4"}}); ok {
		t.Error("a request with no branch to fetch was taken")
	}
}

func TestConflictRiskAnalysisIsListedAndAnswersWhereThereIsNoForge(t *testing.T) {
	var listed bool
	for _, raw := range offeredTools(false, false, false, false) {
		listed = listed || strings.Contains(string(raw), `"conflict_risk_analysis"`)
	}
	if !listed {
		t.Fatal("conflict_risk_analysis is not offered")
	}
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	a := seeded(t, d, dir, nil)
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	text, isErr := m.tool(2, "conflict_risk_analysis", map[string]any{})
	if !isErr || !strings.Contains(text, "not on a forge") {
		t.Errorf("%v %q", isErr, text)
	}
}

// #5661: the tool, called from a lane's worktree of a GitHub folder whose default branch is
// main, gives advice. The agent's cwd is a file URL; git ran in that URL as a folder and every
// call failed, which read as "the default branch is not in this repository".
func TestConflictRiskAnalysisFromAWorktreeGivesAdvice(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "false") // the requests' heads are not fetched from github.com
	d, _, _, _ := forgeDaemon(t)
	var folder string
	for _, f := range d.folderPaths() {
		if out, _ := git(f, "remote", "get-url", "origin"); out == "git@github.com:alexec/agents.git" {
			folder = f
		}
	}
	if folder == "" {
		t.Fatal("no folder on alexec/agents")
	}
	g := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append( //nolint:gosec // git on the test's own temporary folders
			[]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(folder, "a.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g(folder, "add", ".")
	g(folder, "commit", "-qm", "base")
	g(folder, "update-ref", "refs/remotes/origin/main", "HEAD")
	wt := filepath.Join(t.TempDir(), "lane")
	g(folder, "worktree", "add", "-q", "-b", "agents/lane", wt, "origin/main")
	if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("lane\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	branch := "agents/lane"
	a := seeded(t, d, wt, func(a *wire.Agent) {
		a.Worktree = &wire.AgentWorktree{Name: "lane", Root: store.FolderURL(wt), Branch: &branch, MadeByApp: true, Folder: store.FolderURL(folder)}
	})
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	text, isErr := m.tool(2, "conflict_risk_analysis", map[string]any{})
	if isErr || strings.Contains(text, "default branch") || !strings.Contains(text, `"main":"`+g(folder, "rev-parse", "origin/main")+`"`) {
		t.Fatalf("%v %q", isErr, text)
	}
}
