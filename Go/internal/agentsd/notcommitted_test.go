package agentsd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestNotCommittedIsWhatGitIgnoresInAgents(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	folder := store.FolderURL(dir)
	var r wire.NotCommittedResponse
	mustUnmarshal(t, c.call(t, 2, "folders/notCommitted", map[string]any{"folder": folder}), &r)
	if r.IsRepository || len(r.Paths) != 0 {
		t.Fatalf("not a repository: %+v", r)
	}
	run(t, dir, "git", "init", "-q")
	must(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".agents/worktrees/\n*.local.json\nbuild/\n"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, os.MkdirAll(filepath.Join(dir, ".agents", "worktrees", "a", "src"), 0o755))                                  //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(dir, ".agents", "worktrees", "a", "src", "x.go"), []byte("x"), 0o644))            //nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, os.WriteFile(filepath.Join(dir, ".agents", "settings.local.json"), []byte("{}"), 0o644))                     //nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, os.WriteFile(filepath.Join(dir, ".agents", "kept.md"), []byte("kept"), 0o644))                               //nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, os.MkdirAll(filepath.Join(dir, "build"), 0o755))                                                             //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(dir, "build", "out"), []byte("out"), 0o644))                                      //nolint:gosec // G306: the mode this file always had; only its error is new; ignored, but not in .agents
	r = wire.NotCommittedResponse{}
	mustUnmarshal(t, c.call(t, 3, "folders/notCommitted", map[string]any{"folder": folder}), &r)
	if want := []string{".agents/settings.local.json", ".agents/worktrees/"}; !r.IsRepository || !reflect.DeepEqual(r.Paths, want) || r.More != 0 {
		t.Fatalf("%+v", r)
	}
	if e := c.callError(t, 4, "folders/notCommitted", map[string]any{"folder": store.FolderURL(filepath.Join(dir, "gone"))}); e.Code != wire.FailureFolderGone {
		t.Fatalf("%+v", e)
	}
}
