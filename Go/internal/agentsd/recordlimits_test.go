package agentsd

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6908: the largest record of this machine's stores, on the writes that each store's own tests
// do not make: the real migrations, a restore, and a change of a record that is there.

// A removed connector that is too large to carry over loses no copy, whatever the sweep does.
func TestRemovedConnectorsTooLargeToCarryOverAreNotLost(t *testing.T) {
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
	// A second sweep and a new removal leave the copies as they are.
	d.sweepSmallStores(nowFunc())
	must(t, d.keepRemovedConnector("new", skillcatalog.Object()))
	got, err := d.removedConnectors.Read()
	must(t, err)
	if len(got) != 3 || string(got["mine"].Entry) != `{"command":"a"}` || len(got["big"].Entry) < 256<<10 || got["big"].RemovedAt == 0 {
		t.Fatalf("%d copies, mine %s, big of %d bytes", len(got), got["mine"].Entry, len(got["big"].Entry))
	}
}

// More removed connectors than the count all come over, and none is added to them.
func TestTooManyRemovedConnectorsToCarryOverAreNotLost(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	var old []string
	for i := range 101 {
		old = append(old, fmt.Sprintf(`"c%d": {"command": "a"}`, i))
	}
	writeFile(t, d.removedConnectorsPath(), `{"mcpServers": {`+strings.Join(old, ",")+`}}`)
	d.sweepSmallStores(nowFunc())
	if err := d.keepRemovedConnector("new", skillcatalog.Object()); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("one more when full: %v", err)
	}
	got, err := d.removedConnectors.Read()
	must(t, err)
	if len(got) != 101 || string(got["c100"].Entry) != `{"command":"a"}` {
		t.Fatalf("%d copies", len(got))
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
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.workflowMu.Lock()
	defer d.workflowMu.Unlock()
	run := workflowRun{ID: store.NewUUID(), WorkflowID: "w", Folder: "file:///x/", StartedAt: now()}
	must(t, d.saveRunsLocked([]workflowRun{run}))
	run.Folder += wire.URL(strings.Repeat("x", workflowRunBytes))
	agent := store.NewUUID()
	run.AgentID = &agent
	must(t, d.saveRunsLocked([]workflowRun{run}))
	if runs, err := d.runsFile.Read(); err != nil || len(runs) != 1 {
		t.Fatalf("%d runs on the disk (%v)", len(runs), err)
	} else if kept := runs[string(run.ID)]; kept.Folder != "file:///x/" || kept.AgentID == nil || *kept.AgentID != agent {
		t.Fatalf("the earlier form is %+v", kept)
	}
}

// A run of an old workflows.json with a trigger that is too large comes over with it cut.
func TestAnOldRunThatIsTooLargeComesOverWithItsTriggerCut(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.workflowMu.Lock()
	defer d.workflowMu.Unlock()
	agent := store.NewUUID()
	run := workflowRun{ID: store.NewUUID(), WorkflowID: "w", Folder: "file:///x/", AgentID: &agent, StartedAt: now(),
		Trigger: wire.WorkflowTrigger{Unrecognised: &wire.WorkflowTriggerStoredUnrecognised{Name: "folder.idle",
			Keys: map[string]wire.JSONValue{"k": wire.JSONValue(`"` + strings.Repeat("x", workflowRunBytes) + `"`)}}}}
	must(t, writeStore(d.workflowsPath(), workflowRecords{States: []workflowState{}, Runs: []workflowRun{run},
		ApprovalsBegan: d.recordsLocked().ApprovalsBegan}))
	d.workflowRecs = nil
	d.recordsLocked()
	d.saveRecordsLocked(false)
	d.workflowRecs = nil // a restart
	runs := d.recordsLocked().Runs
	if len(runs) != 1 || runs[0].AgentID == nil || *runs[0].AgentID != agent || !ranOnIdle(runs[0].Trigger) {
		t.Fatalf("%d runs after a restart", len(runs))
	}
	if _, cut := runs[0].Trigger.Unrecognised.Keys["cut"]; !cut {
		t.Fatal("nothing says that it is cut")
	}
}

// An answer within its largest is on the disk when it is told, with a request at its largest too.
func TestALargeRequestsAnswerIsOnTheDisk(t *testing.T) {
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
	t.Parallel()
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	url := wire.URL("https://x/" + strings.Repeat("u", 8<<10))
	d.reviewMu.Lock()
	d.reviews = map[string][]wire.ReviewPull{dir: {{URL: url}}}
	d.reviewMu.Unlock()
	if err := d.dismissReview(dir, url); err == nil {
		t.Error("the dismissal was taken")
	}
	if err := d.openReview(dir, url); err == nil {
		t.Error("the opened mark was taken")
	}
	marks, err := d.dismissed.Read()
	must(t, err)
	if len(marks) == 0 && len(d.reviewsFor(dir)) == 0 {
		t.Fatal("it is gone from the list, and no dismissal is kept: it comes back")
	}
}

// An app tool call with many arguments, or a long one's name, is kept within its largest.
func TestAnAppToolCallIsKeptWhateverItsArguments(t *testing.T) {
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

// #7189: the other settings files have a largest size too, and one over it from before can shrink.
func TestTheSettingsFilesHaveALargestSize(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	big := strings.Repeat("x", 300<<10)
	name := "Sam"
	_, err := d.setPerson(wire.PersonSettings{Name: &name})
	must(t, err)
	if _, err := d.setPerson(wire.PersonSettings{Name: &big}); err == nil {
		t.Error("a name of 300 KiB was taken")
	}
	if p := readPerson(d.personFile()); p.Name == nil || *p.Name != name {
		t.Error("the earlier name is gone")
	}
	must(t, d.writeSetting("auto.json", map[string]string{"a": "1"}))
	if err := d.writeSetting("auto.json", map[string]string{"a": big}); !errors.Is(err, store.ErrLimit) {
		t.Errorf("a setting file of 300 KiB: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(d.SettingsDir, "auto.json")); string(b) != `{"a":"1"}` {
		t.Errorf("auto.json is %.40s", b)
	}
	if err := d.writeRuntimeChoices(wire.JSONValue(`{"sandbox":{"defaults":{"` + big + `":"on"}}}`)); err == nil {
		t.Error("the sandbox of the user settings, of 300 KiB, was taken")
	}
	category := "mode"
	options := []wire.ConfigOption{{ID: "m", Type: "select", Category: &category}}
	d.rememberMode("claude", "m", wire.JSONValue(`"plan"`), options)
	d.rememberMode("claude", "m", wire.JSONValue(`"`+big+`"`), options)
	if got := string(d.modes()["claude"]); got != `"plan"` {
		t.Errorf("the mode is %.40s", got)
	}
	// Many settings, and many small ones, are too much; what is there already can be made smaller.
	change := map[string]wire.JSONValue{}
	for i := range 1001 {
		change[fmt.Sprint("k", i)] = wire.JSONValue("1")
	}
	if _, err := d.setPreferences(change); err == nil {
		t.Error("1001 settings were taken")
	}
	writeFile(t, d.displayFile(), `{"a":"`+big+`","b":1}`)
	_, err = d.setPreferences(map[string]wire.JSONValue{"b": wire.JSONValue("2")})
	must(t, err)
	if _, err := d.setPreferences(map[string]wire.JSONValue{"c": wire.JSONValue("2")}); err == nil {
		t.Error("a full file grew")
	}
	_, err = d.setPreferences(map[string]wire.JSONValue{"a": wire.JSONValue("null")})
	must(t, err)
	if got := d.preferences(); len(got) != 1 || string(got["b"]) != "2" {
		t.Errorf("%d settings", len(got))
	}
}

// #7203: a machine's user that is too large is kept without its labels, or not taken at all, and
// memory is as the disk is.
func TestAHostUserTooLargeIsTheSameInMemoryAndOnTheDisk(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	tell := func(u wire.MachineUser) wire.MachineUser {
		b, err := json.Marshal(u)
		must(t, err)
		d.toldHostUser(b)
		var kept wire.MachineUser
		b, err = os.ReadFile(d.hostUserPath())
		must(t, err)
		must(t, json.Unmarshal(b, &kept))
		return kept
	}
	big := strings.Repeat("x", hostUserMostBytes)
	tell(wire.MachineUser{User: "p1", Kind: "person", Name: "p", Labels: []string{}})
	if kept := tell(wire.MachineUser{User: "b1", Kind: "bot", Name: "b", Labels: []string{big}, Bots: []string{big}}); kept.User != "b1" || kept.Kind != "bot" || !d.isBot() {
		t.Errorf("kept %s, a bot in memory: %v", kept.User, d.isBot())
	}
	kept := tell(wire.MachineUser{User: "p2", Kind: "person", Name: big, Labels: []string{}})
	if got := d.user(); got.User != kept.User {
		t.Errorf("%s in memory, %s on the disk", got.User, kept.User)
	}
}

// #7203: a lock whose head cannot be pinned is not taken: not on the disk and not in memory.
func TestALockWhoseHeadCannotBePinnedIsNotTaken(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	key := newLockKey(t)
	// An entry's own limit holds its name, but the pin writes each < in six bytes.
	person := strings.Repeat("<", lockPinMostBytes/2)
	body, err := json.Marshal(wire.LockBody{V: machinelock.Version, Kind: wire.LockEntryKindRoot, Person: person, At: wire.DateOf(time.Now()),
		Key: machinelock.PublicKey(key), Signer: machinelock.PublicKey(key)})
	must(t, err)
	body = []byte(strings.ReplaceAll(string(body), `\u003c`, "<"))
	sum := sha256.Sum256(append([]byte("agents-lock/1\n"), body...))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	must(t, err)
	entry := wire.LockEntry{Body: body, Sig: append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)}
	if d.TakeLock(wire.Lock{Person: person, Entries: []wire.LockEntry{entry}}) {
		t.Fatal("it was taken")
	}
	if fileExists(d.lockFilePath()) || fileExists(d.lockPinPath()) {
		t.Error("it is on the disk")
	}
	if state, pinned := d.heldLock(); pinned || state != nil {
		t.Error("it is in memory")
	}
}

// #7203: a lock that the disk did not take is written when it comes again.
func TestALockThatWasNotKeptIsKeptWhenItComesAgain(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	mac, phone := newLockKey(t), newLockKey(t)
	root := lockEntry(t, mac, wire.LockEntryKindRoot, mac, nil)
	add := lockEntry(t, mac, wire.LockEntryKindAddClient, phone, &root)
	first, both := wire.Lock{Person: "sam", Entries: []wire.LockEntry{root}}, wire.Lock{Person: "sam", Entries: []wire.LockEntry{root, add}}
	d.TakeLock(first)
	old, err := os.ReadFile(d.lockFilePath())
	must(t, err)
	d.TakeLock(both)
	must(t, store.WriteAtomically(d.lockFilePath(), old, 0o600)) // as if the write had failed
	d.TakeLock(both)
	d.lockMu.Lock()
	d.lockHeld, d.lockRead = nil, false
	d.lockMu.Unlock()
	if state, _ := d.heldLock(); state == nil || state.Count() != 2 {
		t.Fatal("the lock after a restart is not the one that was pinned")
	}
}

// #7203: a score that cannot be kept is not told as kept.
func TestAnAssessmentsScoreThatCannotBeKeptIsRefused(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	agent := seeded(t, d, t.TempDir(), nil)
	writeFile(t, d.assessmentsPath(), "not a folder")
	if _, err := d.assessment(agent.ID); err == nil {
		t.Fatal("a score that is not kept was told as a success")
	}
}

// #7203: a scope file is cut at a whole character, and says so.
func TestAScopeFileThatIsCutSaysSo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := assessmentFiles{dir: dir, mu: &sync.Mutex{}}
	path := filepath.Join(dir, "scope-a.txt")
	for pad := range 3 {
		writeFile(t, path, strings.Repeat("x", pad)+strings.Repeat("€", assessmentMaxBytes/2))
		must(t, in.Cut("scope-a.txt"))
		b, err := os.ReadFile(path)
		must(t, err)
		if len(b) > assessmentMaxBytes || !utf8.Valid(b) || !strings.Contains(string(b), "cut") {
			t.Fatalf("%d bytes, whole characters: %v, ends in %.60q", len(b), utf8.Valid(b), b[max(0, len(b)-60):])
		}
	}
}

// A restored record of an app tool call is within the largest, and so is a moved session's
// transcript after it: said.json of a cut transcript is made again.
func TestARestoredConversationIsBoundedWithWhatPointsAtIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{}
	put := func(name, text string) {
		files[name] = filepath.Join(dir, name)
		writeFile(t, files[name], text)
	}
	put("transcript.jsonl", `{"id":"A","at":"2026-11-24T11:00:00.000Z","kind":{"t":"`+strings.Repeat("x", 5<<20)+`"}}`+"\n")
	put("said.json", `{"offset":1,"size":5242880}`)
	put("app-tools.jsonl", `{"at":"2026-11-24T11:00:00.000Z","method":"people/notify","ok":true,"answer":"`+strings.Repeat("x", 2<<20)+`"}`+"\n")
	must(t, boundConversation(files))
	if _, ok := files["said.json"]; ok {
		t.Error("said.json points into the transcript as it was")
	}
	b, err := os.ReadFile(files["app-tools.jsonl"])
	must(t, err)
	var call appToolCall
	must(t, diskjson.Unmarshal(b, &call))
	if len(b) > 1<<20 || call.Method != "people/notify" || !call.Ok || !strings.Contains(answerOfCall(call), "too large") {
		t.Fatalf("%d bytes, %+v", len(b), call)
	}
}

// A form's answer that the disk refuses is not told: the request stays open.
func TestAFormsAnswerThatIsNotKeptIsNotTold(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	asker := seeded(t, d, t.TempDir(), nil)
	msg := "which"
	req := wire.ElicitationRequest{ID: store.NewUUID(), AgentID: asker.ID, Message: &msg, AskedAt: now()}
	d.openFormRequest(req)
	must(t, os.RemoveAll(d.requestsDir()))
	writeFile(t, d.requestsDir(), "") // a file where the folder was
	answer := "one"
	if ended, err := d.answerFormRequest(req.ID, &answer, nil); err == nil || ended {
		t.Fatalf("told: %v %v", ended, err)
	}
	if r := d.request(req.ID); r == nil || r.State != wire.RequestStateOpen || r.Answer != nil {
		t.Fatalf("%+v", r)
	}
	must(t, os.Remove(d.requestsDir()))
	if ended, err := d.answerFormRequest(req.ID, &answer, nil); err != nil || !ended {
		t.Fatalf("not answered again: %v %v", ended, err)
	}
}
