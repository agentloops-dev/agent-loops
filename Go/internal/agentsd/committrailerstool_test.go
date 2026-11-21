package agentsd

import (
	"strings"
	"testing"
)

// commit_trailers (#6362) is offered, gives the calling session's own block from git/trailers,
// with Refs where no PR is named, and refuses a pr that is not a number.
func TestCommitTrailersToolGivesTheCallersBlock(t *testing.T) {
	t.Parallel()

	var listed bool
	for _, raw := range offeredTools(false, false, false, false) {
		listed = listed || strings.Contains(string(raw), `"commit_trailers"`)
	}
	if !listed {
		t.Fatal("commit_trailers is not offered")
	}
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	a := seeded(t, d, dir, nil)
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)

	text, isErr := m.tool(2, "commit_trailers", map[string]any{})
	if isErr || !strings.Contains(text, "Session: "+string(a.ID)) {
		t.Fatalf("%v %q", isErr, text)
	}
	for _, line := range strings.Split(text, "\n") {
		if key, _, ok := strings.Cut(line, ": "); !ok || key == "" || key == "Fixes" || key == "Approved-by" {
			t.Errorf("not a trailer, or one that needs a PR: %q", line)
		}
	}

	if text, isErr := m.tool(3, "commit_trailers", map[string]any{"pr": "six"}); !isErr || !strings.Contains(text, "pull request's number") {
		t.Errorf("%v %q", isErr, text)
	}
}
