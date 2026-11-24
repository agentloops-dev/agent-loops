package agentsd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Ports DaemonOptionsTests: a new session's form is given the runtime's options and
// commands, kept for the next form, which has them at once and hears of a change after.

func TestAFormIsGivenWhatTheRuntimeOffers(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := filepath.Join(t.TempDir(), "offers")
	must(t, os.Mkdir(dir, 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	var got wire.OptionsResponse
	mustUnmarshal(t, c.call(t, 1, "agents/options", map[string]any{"runtimeID": "claude", "cwd": "file://" + dir, "mcpServers": []any{}}), &got)
	// Claude's model, then the app's Use Chrome beside it (#1837).
	if got.DraftID == "" || len(got.Options) != 2 || got.Options[0].ID != "model" || got.Options[1].ID != "chrome" ||
		len(got.Commands) != 1 || got.Commands[0].Name != "review" {
		t.Fatalf("%+v", got)
	}
	b, _ := os.ReadFile(d.optionCachePath())
	if !strings.Contains(string(b), "claude\\t"+dir+"\\t") || !strings.Contains(string(b), `"savedAt"`) {
		t.Fatalf("%s", b)
	}
	// What a workflow's form reads, with no session.
	var remembered []wire.ConfigOption
	mustUnmarshal(t, c.call(t, 2, "options/remembered", map[string]any{"runtimeID": "claude", "cwd": "file://" + dir}), &remembered)
	if len(remembered) != 2 {
		t.Fatalf("%+v", remembered)
	}
	// The next form has them at once; the runtime offers the same, so nothing more is said.
	must(t, os.WriteFile(d.optionCachePath(), []byte(strings.Replace(string(b), `"Opus"`, `"Old Opus"`, 1)), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	got = wire.OptionsResponse{}
	mustUnmarshal(t, c.call(t, 3, "agents/options", map[string]any{"runtimeID": "claude", "cwd": "file://" + dir, "mcpServers": []any{}}), &got)
	if !strings.Contains(string(mustJSON(got.Options)), "Old Opus") {
		t.Fatalf("%+v", got)
	}
	c.until(t, "the change", func(m string, p json.RawMessage) bool {
		var n wire.DraftOptionsNotification
		return m == "agents/draftOptions" && json.Unmarshal(p, &n) == nil && n.DraftID == got.DraftID && n.Failure == nil && !strings.Contains(string(p), "Old Opus")
	})
	c.call(t, 4, "agents/discardDraft", map[string]any{"draftID": got.DraftID})
	if d.closeDraft(got.DraftID) {
		t.Fatal("still open")
	}
	if e := c.callError(t, 5, "agents/options", map[string]any{"runtimeID": "nope", "cwd": "file://" + dir, "mcpServers": []any{}}); e.Code != CodeRuntimeNotFound {
		t.Fatalf("%+v", e)
	}
}
