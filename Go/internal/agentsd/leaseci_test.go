package agentsd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The ci lease (#5678): in line while the folder's repository has a CI job queued, with the
// reason; granted, the whole line, once none is. A queued run with no jobs (a ghost, #5676)
// does not count, and is not read again. A folder not on GitHub is never held.
func TestTheCILeaseWaitsWhileAJobIsQueued(t *testing.T) {
	fakeRuntimes(t)
	now := time.Now().UTC().Truncate(time.Second)
	var queued atomic.Bool
	queued.Store(true)
	var ghostReads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/alexec/agents/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("status") {
		case "queued":
			runs := fmt.Sprintf(`{"id":1,"created_at":%q}`, now.Add(-48*time.Hour).Format(time.RFC3339))
			if queued.Load() {
				runs += fmt.Sprintf(`,{"id":2,"created_at":%q}`, now.Add(-14*time.Minute).Format(time.RFC3339))
			}
			fmt.Fprintf(w, `{"workflow_runs":[%s]}`, runs)
		case "in_progress":
			fmt.Fprintf(w, `{"workflow_runs":[{"id":3,"created_at":%q}]}`, now.Format(time.RFC3339))
		}
	})
	mux.HandleFunc("/repos/alexec/agents/actions/runs/1/jobs", func(w http.ResponseWriter, _ *http.Request) {
		ghostReads.Add(1)
		fmt.Fprint(w, `{"jobs":[]}`)
	})
	mux.HandleFunc("/repos/alexec/agents/actions/runs/2/jobs", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"jobs":[{"status":"queued","created_at":%q,"labels":["xcode-27"]}]}`,
			now.Add(-14*time.Minute).Format(time.RFC3339))
	})
	mux.HandleFunc("/repos/alexec/agents/actions/runs/3/jobs", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"jobs":[{"status":"in_progress","created_at":%q,"labels":["ubuntu-latest"]}]}`, now.Format(time.RFC3339))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.ciAPI = srv.URL
	dir := gitFolder(t, "git@github.com:alexec/agents.git")
	a, b := seeded(t, d, dir, nil), seeded(t, d, dir, nil)
	ma, mb := appClient(t, d, a.ID), appClient(t, d, b.ID)
	for _, m := range []*mcpClient{ma, mb} {
		m.post("initialize", map[string]any{}, 1)
	}
	const name = "ci:github:alexec/agents"

	inLine := ". You are 1st in line. You will get a lease.granted event when it is yours. End your turn, and you will be started again with it."
	if text, _ := ma.tool(2, "lease_resource", map[string]any{"name": "ci"}); text != "CI of alexec/agents has 1 job queued (xcode-27), the oldest for 14 min"+inLine {
		t.Fatal(text)
	}
	if got := capacityWaitOf(d, name); got != "CI of alexec/agents has jobs queued" {
		t.Fatalf("snapshot %q", got)
	}
	// The ghost is read once, then left out.
	d.ciqMu.Lock()
	d.ciq[name] = ciQueue{folder: d.ciq[name].folder}
	d.ciqMu.Unlock()
	if text, _ := mb.tool(2, "lease_resource", map[string]any{"name": "ci"}); !strings.Contains(text, "2nd in line") {
		t.Fatal(text)
	}
	if n := ghostReads.Load(); n != 1 {
		t.Fatalf("the ghost was read %d times", n)
	}

	// No job queued: the next look grants the whole line.
	queued.Store(false)
	d.serveCILines(context.Background())
	if !holds(d, name, a.ID) || !holds(d, name, b.ID) {
		t.Fatal("the line was not granted when no job was queued")
	}
	if text, _ := ma.tool(3, "release_resource", map[string]any{"name": "ci"}); text != "Released CI of alexec/agents." {
		t.Fatal(text)
	}

	// A folder not on GitHub leases plain ci, which nothing holds.
	other := seeded(t, d, gitFolder(t, ""), nil)
	mo := appClient(t, d, other.ID)
	mo.post("initialize", map[string]any{}, 1)
	if text, _ := mo.tool(2, "lease_resource", map[string]any{"name": "ci"}); !strings.HasPrefix(text, "You hold CI until ") {
		t.Fatal(text)
	}
}
