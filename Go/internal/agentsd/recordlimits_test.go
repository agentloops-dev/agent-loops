package agentsd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6908: the largest record of this machine's stores, on the writes that each store's own tests
// do not make: the real migrations, a restore, and a change of a record that is there.

// A removed connector that is too large to carry over loses no copy, whatever the sweep does.
func TestRemovedConnectorsTooLargeToCarryOverAreNotLost(t *testing.T) {
	t.Skip("#7181")
	t.Parallel()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	path := filepath.Join(home, ".agents", "mcp.removed.json")
	writeFile(t, path, `{"mcpServers": {"mine": {"command": "a"}, "big": {"command": "`+strings.Repeat("x", 256<<10)+`"}}}`)
	d.sweepSmallStores(nowFunc())
	b, err := os.ReadFile(path)
	must(t, err)
	if !strings.Contains(string(b), `"mine"`) {
		t.Fatalf("the sweep left %.200s", b)
	}
}

// The old file's deploys come over, less one that is too large.
func TestADeployTooLargeToBringOverLeavesTheOthers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seen := deployRecord{Deploy: wire.Deploy{State: wire.DeployStateSucceeded}, Environments: []string{}, Seen: wire.DateOf(nowFunc())}
	big := seen
	big.Environments = []string{strings.Repeat("e", 16<<10)}
	data, err := diskjson.Marshal(map[string]map[string]deployRecord{"deploys": {"kept": seen, "big": big}})
	must(t, err)
	writeFile(t, filepath.Join(root, "lineage-deploys.json"), string(data))
	d, _ := start(t, root)
	defer d.Shutdown()
	d.keepDeploy("new", seen)
	m, err := d.deployStore.Read()
	must(t, err)
	if _, there := m["kept"]; !there || len(m) != 2 {
		t.Fatalf("%d deploys, the old one there: %v", len(m), there)
	}
}

// The old lists come over, less an entry that is too large.
func TestAnElsewhereEntryTooLargeToCarryLeavesTheOthers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	entry := func(id wire.UUID, folder string) string {
		return `{"machine":"pc","session":"` + string(id) + `","starter":"B","folder":"` + folder + `","there":"/b","title":"watch","at":"2026-11-06T11:00:00.000Z"}`
	}
	writeFile(t, filepath.Join(root, "elsewhere.json"),
		`{"started":[`+entry(there, "/a")+`,`+entry(store.NewUUID(), strings.Repeat("f", 64<<10))+`]}`)
	d, _ := start(t, root)
	defer d.Shutdown()
	if s := d.startedThereBy("B"); len(s) != 1 || s[0].Title != "watch" {
		t.Fatalf("%d carried over", len(s))
	}
}

// The old marks come over within the largest, and so does an old operation.
func TestTheOldFilesComeOverWithinTheLargest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "review-dismissals.json"), `{"/a":["https://x/`+strings.Repeat("u", 8<<10)+`","https://x/1"]}`)
	writeFile(t, filepath.Join(root, "secret-ops-pending.json"),
		`[{"name":"`+strings.Repeat("n", 64<<10)+`","what":"read","at":"2026-11-24T11:00:00.000Z"}]`)
	d, _ := start(t, root)
	defer d.Shutdown()
	for name, most := range map[string]int{"review-dismissals.json": 8 << 10, "secret-ops-pending.json": 16 << 10} {
		b, err := os.ReadFile(filepath.Join(root, name))
		must(t, err)
		var m map[string]json.RawMessage
		must(t, json.Unmarshal(b, &m))
		if len(m) == 0 {
			t.Errorf("%s came over with none", name)
		}
		for k, v := range m {
			if len(k)+len(v) > most {
				t.Errorf("%s has an entry of %d bytes, and the most is %d", name, len(k)+len(v), most)
			}
		}
	}
}

// A run that grows past its largest keeps its earlier form on the disk.
func TestARunThatGrowsTooLargeKeepsItsEarlierForm(t *testing.T) {
	t.Skip("#7184")
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.workflowMu.Lock()
	defer d.workflowMu.Unlock()
	run := workflowRun{ID: store.NewUUID(), WorkflowID: "w", Folder: "file:///x/", StartedAt: now()}
	must(t, d.saveRunsLocked([]workflowRun{run}))
	run.Folder += wire.URL(strings.Repeat("x", workflowRunBytes))
	must(t, d.saveRunsLocked([]workflowRun{run}))
	if runs, err := d.runsFile.Read(); err != nil || len(runs) != 1 {
		t.Fatalf("%d runs on the disk (%v)", len(runs), err)
	}
}

// An answer within its largest is on the disk when it is told, with a request at its largest too.
func TestALargeRequestsAnswerIsOnTheDisk(t *testing.T) {
	t.Skip("#7185")
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	asker := seeded(t, d, t.TempDir(), nil)
	msg := strings.Repeat("x", requestMaxNew/2-2<<10) // a request says it twice
	req := wire.ElicitationRequest{ID: store.NewUUID(), AgentID: asker.ID, Message: &msg, AskedAt: now()}
	must(t, d.formRefused(req))
	d.openFormRequest(req)
	answer := strings.Repeat("<", requestMaxAnswer)
	if !d.endRequest(req.ID, wire.RequestStateAnswered, &answer, "", nil) {
		t.Fatal("not answered")
	}
	b, err := d.requestRecords().Get(string(req.ID))
	must(t, err)
	var kept wire.Request
	must(t, json.Unmarshal(b, &kept))
	if kept.State != wire.RequestStateAnswered {
		t.Fatalf("answered to the caller, and %s on the disk", kept.State)
	}
}

// Dismiss of a pull request that cannot be kept is refused, and it stays in the list.
func TestADismissalTooLargeToKeepIsRefused(t *testing.T) {
	t.Skip("#7186")
	t.Parallel()
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	url := wire.URL("https://x/" + strings.Repeat("u", 8<<10))
	d.reviewMu.Lock()
	d.reviews = map[string][]wire.ReviewPull{dir: {{URL: url}}}
	d.reviewMu.Unlock()
	d.dismissReview(dir, url)
	marks, err := d.dismissed.Read()
	must(t, err)
	if len(marks) == 0 && len(d.reviewsFor(dir)) == 0 {
		t.Fatal("it is gone from the list, and no dismissal is kept: it comes back")
	}
}

// An app tool call with many arguments, or a long one's name, is kept within its largest.
func TestAnAppToolCallIsKeptWhateverItsArguments(t *testing.T) {
	t.Skip("#7187")
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	agent := store.NewUUID()
	args := map[string]json.RawMessage{}
	for i := range 600 {
		args[fmt.Sprint("a", i)] = json.RawMessage(`"` + strings.Repeat("x", appToolAnswerMost) + `"`)
	}
	d.keepAppToolCall(agent, "notify", args, "ok", nil, time.Now())
	if b, _ := os.ReadFile(filepath.Join(d.Store.Loc.Agent(string(agent)), "app-tools.jsonl")); len(b) == 0 {
		t.Fatal("the call is not kept")
	}
}

// A cut argument says that it is cut.
func TestACutAppToolArgumentSaysSo(t *testing.T) {
	t.Skip("#7187")
	t.Parallel()
	v, _ := json.Marshal(strings.Repeat("x", appToolAnswerMost+1))
	var got string
	must(t, json.Unmarshal(cutAppToolArgument(v), &got))
	if strings.Trim(got, "x") == "" {
		t.Fatal("nothing says that it is cut")
	}
}

// A transcript that is restored, imported or moved here has no entry over the largest.
func TestARestoredTranscriptIsWithinTheLargestRecord(t *testing.T) {
	t.Skip("#7183")
	shared := t.TempDir()
	a, ca := start(t, t.TempDir())
	dir := folder(t, ca, 1)
	ca.call(t, 2, "backups/configure", map[string]any{"every": "off", "keep": 2, "folder": shared})
	agent := seeded(t, a, dir, nil)
	line, err := diskjson.Marshal(wire.TranscriptEntry{ID: store.NewUUID(), At: now(), Kind: wire.TranscriptEntryKind{
		AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: strings.Repeat("x", 5<<20)}}})
	must(t, err)
	writeFile(t, a.Store.Loc.Transcript(string(agent.ID)), string(line)+"\n")
	made, err := a.makeBackup()
	must(t, err)
	b, cb := start(t, t.TempDir())
	cb.call(t, 2, "backups/configure", map[string]any{"every": "off", "keep": 2, "folder": shared})
	cb.call(t, 3, "backups/restore", map[string]any{"id": made.ID})
	entries, _, err := b.Store.Transcript(agent.ID, 0)
	must(t, err)
	if len(entries) != 1 || entries[0].Kind.AgentMessage != nil {
		t.Fatalf("%d entries; one of 5 MiB is in the store, and the most is 4 MiB", len(entries))
	}
}

// The display settings have a largest size.
func TestTheDisplaySettingsHaveALargestSize(t *testing.T) {
	t.Skip("#7189")
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	_, err := d.setPreferences(map[string]wire.JSONValue{"k": wire.JSONValue("1")})
	must(t, err)
	if _, err := d.setPreferences(map[string]wire.JSONValue{"k": wire.JSONValue(`"` + strings.Repeat("x", 8<<20) + `"`)}); err == nil {
		t.Fatal("a setting of 8 MiB was taken")
	}
	if got := string(d.preferences()["k"]); got != "1" {
		t.Fatalf("the earlier value is %.20s", got)
	}
}
