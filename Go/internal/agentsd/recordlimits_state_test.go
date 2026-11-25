package agentsd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/mcpcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/envfile"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7246: the largest record of the daemon's state files, at the limit and over it.

func overLimit(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, store.ErrLimit) {
		t.Fatalf("over the limit: %v", err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return string(b)
}

func TestAttentionLeavesOutWhatIsOverItsLimits(t *testing.T) {
	t.Parallel()
	id := func(size int) wire.NeedID {
		s := strings.Repeat("n", size)
		return wire.NeedID{Notice: &s}
	}
	entry := func(size int) int {
		b, err := diskjson.Marshal(id(size))
		must(t, err)
		return len(b)
	}
	fits := attentionMostBytes - entry(0)
	if got := boundAttention([]wire.NeedID{id(fits), id(fits + 1)}); len(got) != 1 || len(*got[0].Notice) != fits {
		t.Errorf("%d entries", len(got))
	}
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.attentionMu.Lock()
	defer d.attentionMu.Unlock()
	for i := range attentionMost + 1 {
		n := id(i + 1)
		d.raisedAt[token(n)], d.raisedNeed[token(n)] = wire.Date(float64(i)), n
	}
	d.writeAttentionIfMoved()
	r := readAttentionRecords(d.attentionFile())
	if len(r.Raised) != attentionMost || r.Raised[0].At != wire.Date(float64(attentionMost)) {
		t.Errorf("%d raised, the first at %v", len(r.Raised), r.Raised[0].At)
	}
}

func TestBackgroundKeepsTheNewestWithTheirLinesCut(t *testing.T) {
	t.Parallel()
	h := backgroundHandover{Agents: map[wire.UUID][]adoptedProcess{}}
	for i := range backgroundMost {
		h.Agents["a"] = append(h.Agents["a"], adoptedProcess{Pid: i, Line: strings.Repeat("x", backgroundLineMost), Started: int64(i)})
	}
	if got := boundBackground(h).Agents["a"]; len(got) != backgroundMost || len(got[0].Line) != backgroundLineMost {
		t.Fatalf("at the limit: %d", len(got))
	}
	h.Agents["b"] = []adoptedProcess{{Pid: 1, Line: strings.Repeat("x", backgroundLineMost+1), Started: backgroundMost}}
	got := boundBackground(h)
	if len(got.Agents["a"]) != backgroundMost-1 || got.Agents["a"][backgroundMost-2].Started != 1 {
		t.Errorf("%d of a", len(got.Agents["a"]))
	}
	if b := got.Agents["b"]; len(b) != 1 || b[0].Line != strings.Repeat("x", backgroundLineMost)+"…" {
		t.Errorf("%d of b", len(b))
	}
}

func TestAnAttachmentOverItsLimitIsRefused(t *testing.T) {
	clock := atTime(t, time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC))
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	root := t.TempDir()
	_, err := d.writeAttachment(root, "big", make([]byte, attachmentMost+1))
	overLimit(t, err)
	if fileExists(d.attachmentFoldersPath()) {
		t.Error("the folder is listed")
	}
	path, err := d.writeAttachment(root, "fits", make([]byte, attachmentMost))
	must(t, err)

	// A file past 90 days goes, whatever names it.
	old := clock.now().Add(-store.MaxAge - time.Hour)
	must(t, os.Chtimes(path, old, old))
	d.sweepAttachments()
	if fileExists(path) {
		t.Error("it was kept past 90 days")
	}

	folders := make([]string, attachmentFoldersMost-1)
	for i := range folders {
		folders[i] = fmt.Sprint("/f", i)
	}
	must(t, d.writeAttachmentFolders(folders))
	path, err = d.writeAttachment(root, "last", []byte("x"))
	must(t, err)
	was := readText(t, d.attachmentFoldersPath())
	other := t.TempDir()
	_, err = d.writeAttachment(other, "over", []byte("x"))
	overLimit(t, err)
	if hasAttachments(other) || readText(t, d.attachmentFoldersPath()) != was {
		t.Error("the refused write left something")
	}
	if _, err := d.writeAttachment(root, "again", []byte("x")); err != nil || !fileExists(path) {
		t.Errorf("a listed folder: %v", err)
	}
	d.attachmentFoldersMu.Lock()
	defer d.attachmentFoldersMu.Unlock()
	must(t, d.writeAttachmentFolders(nil))
	must(t, d.noteAttachmentFolderLocked("/"+strings.Repeat("x", attachmentFolderMostBytes-3)))
	overLimit(t, d.noteAttachmentFolderLocked("/"+strings.Repeat("x", attachmentFolderMostBytes-2)))
}

// A guarded file too large to keep waits: the copy approved before applies, and Keep says why.
func TestAGuardedFileOverItsLimitIsNotApproved(t *testing.T) {
	t.Parallel()
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	path := filepath.Join(dir, folderConfig)
	text := func(size int) string { return `{"a":"` + strings.Repeat("x", size-8) + `"}` }
	writeFile(t, path, text(guardedMostBytes))
	if got, ok := d.guardedContent(dir, folderConfig); !ok || len(got) != guardedMostBytes {
		t.Fatalf("at the limit: %d", len(got))
	}
	was := readText(t, d.guardedFile())
	overLimit(t, func() error {
		writeFile(t, path, text(guardedMostBytes+1))
		return d.approveGuarded(dir, folderConfig)
	}())
	if readText(t, d.guardedFile()) != was {
		t.Error("the refusal changed the record")
	}
	if got, ok := d.guardedContent(dir, folderConfig); !ok || len(got) != guardedMostBytes {
		t.Fatalf("the copy before does not apply: %d", len(got))
	}
	digest, _ := diskRead(dir, folderConfig)
	r := wire.GuardedChangeRequest{Folder: store.FolderURL(dir), Path: folderConfig, Digest: digest}
	_, err := d.keepOrUndo(r, true)
	var e *Error
	if !errors.As(err, &e) || e.Code != wire.FailureCouldNotSave {
		t.Fatalf("Keep: %v", err)
	}
	must(t, errOnly(d.keepOrUndo(r, false)))
	if got := readText(t, path); len(got) != guardedMostBytes {
		t.Errorf("Undo left %d bytes", len(got))
	}
	// One first seen over the limit is not approved.
	other := t.TempDir()
	writeFile(t, filepath.Join(other, folderConfig), text(guardedMostBytes+1))
	if _, ok := d.guardedContent(other, folderConfig); ok {
		d.guardedMu.Lock()
		s, _ := d.stateOf(other, folderConfig)
		approved := s.ApprovedDigest
		d.guardedMu.Unlock()
		if approved != nil {
			t.Error("a first copy over the limit was approved")
		}
	}
	must(t, setFolderConfig(dir, "a", "small"))
	overLimit(t, setFolderConfig(dir, "a", strings.Repeat("x", guardedMostBytes)))
}

func TestLeasesOverTheirLimitsAreRefused(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	a := seeded(t, d, t.TempDir(), nil)
	lease := func(name string) error {
		_, err := d.leaseResource(a.ID, map[string]json.RawMessage{"name": mustJSON(name)})
		return err
	}
	must(t, lease(strings.Repeat("n", leaseNameMost)))
	if err := lease(strings.Repeat("n", leaseNameMost+1)); err == nil {
		t.Error("a name over the limit was leased")
	}
	d.leaseMu.Lock()
	for i := range leaseEntriesMost - 2 {
		name := wire.ResourceName(fmt.Sprint("r", i))
		d.book.Entries[name] = &leaseEntry{Kind: "named", DisplayName: string(name), Rules: standardRules,
			Leases: []wire.Lease{{Resource: name, DisplayName: string(name), Holder: "X", GrantedAt: now(), ExpiresAt: wire.DateOf(time.Now().Add(time.Hour))}}}
	}
	d.leaseMu.Unlock()
	must(t, lease("last"))
	was := readText(t, d.leasesFile())
	if err := lease("one more"); err == nil || readText(t, d.leasesFile()) != was {
		t.Errorf("a book that is full: %v", err)
	}
	must(t, lease("last")) // a holder extends

	d.leaseMu.Lock()
	defer d.leaseMu.Unlock()
	e := d.book.Entries["r0"]
	for i := range leaseLineMost {
		if why := d.leaseOverLimitLocked(a.ID, "r0", "r0", true); why != "" {
			t.Fatalf("%d in line: %s", i, why)
		}
		e.Line = append(e.Line, waiter{AgentID: wire.UUID(fmt.Sprint(i))})
	}
	if why := d.leaseOverLimitLocked(a.ID, "r0", "r0", true); why != leaseLineFull {
		t.Errorf("a full line: %q", why)
	}

	at := time.Now()
	for i := range leaseNoticesMost + 1 {
		d.leaveNotice(leaseNotice{Resource: wire.ResourceName(fmt.Sprint(i)), At: wire.DateOf(at)}, a.ID)
	}
	if list := d.book.Notices[string(a.ID)]; len(list) != leaseNoticesMost || list[0].Resource != "1" {
		t.Errorf("%d notices", len(list))
	}
	for i := range leaseNoticeAgents {
		d.leaveNotice(leaseNotice{At: wire.DateOf(at.Add(-time.Duration(i+1) * time.Minute))}, wire.UUID(fmt.Sprint(i)))
	}
	d.pruneNotices(at)
	if _, oldest := d.book.Notices[fmt.Sprint(leaseNoticeAgents-1)]; oldest || len(d.book.Notices) != leaseNoticeAgents {
		t.Errorf("%d agents with notices", len(d.book.Notices))
	}
	d.pruneNotices(at.Add(store.MaxAge + time.Second))
	if len(d.book.Notices) != 0 {
		t.Errorf("%d agents with notices past 90 days", len(d.book.Notices))
	}
}

func TestADeclaredResourceOverItsLimitIsRefused(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.leaseMu.Lock()
	defer d.leaseMu.Unlock()
	d.loadLeases()
	declare := func(name string, size int, replacing *string) error {
		res := wire.DeclaredResource{Name: wire.ResourceName(name), DisplayName: name, Holders: 1, Description: "d"}
		b, err := diskjson.Marshal(res)
		must(t, err)
		res.Description += strings.Repeat("d", max(0, size-len(b)))
		_, err = d.declareResourceLocked(wire.DeclareResourceRequest{Resource: res, Replacing: replacing})
		return err
	}
	must(t, declare("a", declaredMostBytes, nil))
	was := readText(t, d.resourcesFile())
	a := "a"
	if declare("a", declaredMostBytes+1, &a) == nil || declare(strings.Repeat("n", leaseNameMost+1), 0, nil) == nil {
		t.Error("one over the size was declared")
	}
	if readText(t, d.resourcesFile()) != was {
		t.Error("the refusal changed the file")
	}
	for i := len(d.declared); i < declaredMost; i++ {
		d.declared = append(d.declared, wire.DeclaredResource{Name: wire.ResourceName(fmt.Sprint("r", i)), DisplayName: "r", Holders: 1, Description: "d"})
	}
	if declare("one more", 0, nil) == nil {
		t.Error("one over the count was declared")
	}
	must(t, declare("a", 0, &a))
}

func TestBotCopiesOverTheirLimitAreRefused(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.botMu.Lock()
	defer d.botMu.Unlock()
	sized := func(n int) botCopies {
		b, err := json.Marshal(botCopies{Machines: map[string]string{"": botHas}})
		must(t, err)
		return botCopies{Machines: map[string]string{strings.Repeat("m", n-len(b)): botHas}}
	}
	must(t, d.saveBotCopiesLocked(sized(botCopiesMostBytes)))
	was := readText(t, d.botCopiesPath())
	overLimit(t, d.saveBotCopiesLocked(sized(botCopiesMostBytes+1)))
	if readText(t, d.botCopiesPath()) != was {
		t.Error("the refusal changed the file")
	}
}

// Over a limit the windows kept before stay, a new one is left out, and a forgotten one goes.
func TestLocalClientsOverTheirLimitsAreLeftOut(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	file := filepath.Join(d.Store.Loc.Root, localClientsFile)
	kept := func() map[wire.UUID]bool {
		var l wire.MachineClients
		must(t, json.Unmarshal([]byte(readText(t, file)), &l))
		out := map[wire.UUID]bool{}
		for _, c := range l.Clients {
			out[c.ID] = true
		}
		return out
	}
	var list wire.MachineClients
	for i := range localClientsMost {
		list.Clients = append(list.Clients, wire.MachineClient{ID: wire.UUID(fmt.Sprint("W", i)), Kind: "mac"})
	}
	d.SetLocalClients(list)
	if len(kept()) != localClientsMost {
		t.Fatalf("at the limit: %d", len(kept()))
	}
	list.Clients = append([]wire.MachineClient{{ID: "NEW", Kind: "mac"}}, list.Clients...)
	d.SetLocalClients(list)
	if k := kept(); len(k) != localClientsMost || k["NEW"] {
		t.Errorf("over it: %d, the new one %v", len(k), k["NEW"])
	}
	list.Clients = list.Clients[:localClientsMost-1]
	d.SetLocalClients(list)
	if k := kept(); len(k) != localClientsMost-1 || !k["NEW"] || k[wire.UUID(fmt.Sprint("W", localClientsMost-1))] {
		t.Errorf("after some went: %d", len(k))
	}
	big := wire.MachineClient{ID: "BIG", Kind: "mac"}
	one := len(mustJSON(big))
	big.Kind = strings.Repeat("k", localClientMost-one+len("mac"))
	d.SetLocalClients(wire.MachineClients{Clients: []wire.MachineClient{big, {ID: "SMALL"}}})
	if k := kept(); len(k) != 2 {
		t.Errorf("a window at its limit: %v", k)
	}
	big.Kind += "k"
	d.SetLocalClients(wire.MachineClients{Clients: []wire.MachineClient{big, {ID: "SMALL"}}})
	if k := kept(); len(k) != 1 || !k["SMALL"] {
		t.Errorf("a window over its limit: %v", k)
	}
	list = wire.MachineClients{}
	for i := range 300 {
		list.Clients = append(list.Clients, wire.MachineClient{ID: wire.UUID(fmt.Sprint("B", i)), Kind: strings.Repeat("k", localClientMost-200)})
	}
	d.SetLocalClients(list)
	if n := len(readText(t, file)); n > localClientsFileMost || len(kept()) < 250 {
		t.Errorf("%d bytes of %d windows", n, len(kept()))
	}
}

// The lock's own limits bound lock.json: one that breaks them is not taken.
func TestALockEntryOverItsLimitIsNotTaken(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	body := make([]byte, machinelock.MostBody+1)
	if d.TakeLock(wire.Lock{Person: "sam", Entries: []wire.LockEntry{{Body: body, Sig: make([]byte, 64)}}}) {
		t.Error("it was taken")
	}
	if fileExists(d.lockFilePath()) {
		t.Error("it is on the disk")
	}
}

func TestAnMCPAnswerOverItsLimitsIsRefused(t *testing.T) {
	t.Parallel()
	s := func(n int) *string { x := strings.Repeat("x", n); return &x }
	events := func(n int) []mcpPolled { return make([]mcpPolled, n) }
	for name, c := range map[string]struct {
		r  mcpPollResult
		ok bool
	}{
		"a cursor at its limit":   {mcpPollResult{Cursor: s(mcpCursorMost)}, true},
		"a cursor over it":        {mcpPollResult{Cursor: s(mcpCursorMost + 1)}, false},
		"an id at its limit":      {mcpPollResult{Events: []mcpPolled{{EventID: s(mcpEventIDMost)}}}, true},
		"an id over it":           {mcpPollResult{Events: []mcpPolled{{EventID: s(mcpEventIDMost + 1)}}}, false},
		"events at their limit":   {mcpPollResult{Events: events(mcpSeenLimit)}, true},
		"events over their limit": {mcpPollResult{Events: events(mcpSeenLimit + 1)}, false},
	} {
		if e := mcpPollTooLarge(c.r); (e == nil) != c.ok {
			t.Errorf("%s: %v", name, e)
		}
	}
	recs := &mcpRecords{Subscriptions: map[string]*mcpRecord{}}
	for i := range mcpSubscriptionsMost + 1 {
		recs.Subscriptions[fmt.Sprint(i)] = &mcpRecord{LastNamedAt: wire.Date(float64(i))}
	}
	if got := recs.bounded(); len(got.Subscriptions) != mcpSubscriptionsMost || got.Subscriptions["0"] != nil || len(recs.Subscriptions) != mcpSubscriptionsMost+1 {
		t.Errorf("%d subscriptions", len(got.Subscriptions))
	}
	delete(recs.Subscriptions, "1")
	if got := recs.bounded(); len(got.Subscriptions) != mcpSubscriptionsMost || got.Subscriptions["0"] == nil {
		t.Errorf("at the limit: %d subscriptions", len(got.Subscriptions))
	}
}

func TestAnOrgRouteOverItsLimitLeavesTheOneHeld(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	route := wire.DeliveredRoute{ID: "r", Name: "r", Version: 1}
	base := len(mustJSON(route))
	route.Name += strings.Repeat("n", orgRouteMostBytes-base)
	must(t, d.keepOrgRoute(route))
	file := filepath.Join(d.orgRoutesDir(), "r.json")
	was := readText(t, file)
	route.Name += "n"
	overLimit(t, d.keepOrgRoute(route))
	if readText(t, file) != was {
		t.Error("the refusal changed the file")
	}
}

func TestYourPolicyOverItsLimitLeavesTheOneInForce(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	must(t, d.editYoursLocked(func(*wire.PolicyRules) error { return nil }))
	d.yoursMu.Lock()
	was, revision := readText(t, d.yourPolicyPath()), d.yoursNow.policy.Revision
	d.yoursMu.Unlock()
	overLimit(t, d.editYoursLocked(func(r *wire.PolicyRules) error {
		r.Environment = &wire.PolicyEnvironment{PassThrough: []string{strings.Repeat("X", yourPolicyMostBytes)}}
		return nil
	}))
	d.yoursMu.Lock()
	defer d.yoursMu.Unlock()
	if readText(t, d.yourPolicyPath()) != was || d.yoursNow.policy.Revision != revision {
		t.Error("the refusal changed your policy")
	}
	must(t, d.writeYoursLocked(make([]byte, yourPolicyMostBytes)))
	overLimit(t, d.writeYoursLocked(make([]byte, yourPolicyMostBytes+1)))
}

// The old settings files stay when the layer made of them is refused.
func TestAFirstStartMoveOverTheLimitLosesNothing(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	display := filepath.Join(d.SettingsDir, "display.json")
	limit := filepath.Join(d.SettingsDir, "workflow-limit.json")
	mustWrite(t, display, `{"noticeBurst":2}`)
	mustWrite(t, limit, `{"total":7}`)
	mustWrite(t, filepath.Join(d.SettingsDir, passThroughFile), `{"names":["`+strings.Repeat("X", yourPolicyMostBytes)+`"]}`)
	d.yoursMu.Lock()
	d.yoursRead = false
	d.readYoursLocked()
	d.yoursMu.Unlock()
	if readText(t, display) != `{"noticeBurst":2}` || !fileExists(limit) || fileExists(d.yourPolicyPath()) {
		t.Error("the refused move took the old settings away")
	}
}

func TestTheUserSettingsFilesOverTheirLimitsAreRefused(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	mark := userSettingsSyncState{Secrets: []string{}, User: "u"}
	b, err := diskjson.Marshal(mark)
	must(t, err)
	mark.User += strings.Repeat("u", userMarkMostBytes-len(b))
	d.userScopeMu.Lock()
	must(t, d.saveUserMarkLocked(mark))
	was := readText(t, d.userSettingsSyncPath())
	mark.User += "u"
	overLimit(t, d.saveUserMarkLocked(mark))
	if readText(t, d.userSettingsSyncPath()) != was || len(d.userMark.User) == len(mark.User) {
		t.Error("the refusal changed the mark")
	}
	d.userScopeMu.Unlock()

	home := t.TempDir()
	config := func(size int) wire.JSONValue {
		text := strings.Repeat("x", size)
		return mustJSON(wire.UserAgentConfig{Instructions: &text, Personas: map[string]string{"p": fmt.Sprint(size)}})
	}
	must(t, errOnly(writeAgentConfig(home, config(agentConfigFileMost), nil)))
	overLimit(t, errOnly(writeAgentConfig(home, config(agentConfigFileMost+1), nil)))
	if got := readText(t, filepath.Join(home, ".agents", "personas", "p.md")); got != fmt.Sprint(agentConfigFileMost) {
		t.Errorf("the refused settings wrote %s", got)
	}
	many := map[string]string{}
	for i := range agentConfigFilesMost + 1 {
		many[fmt.Sprint("p", i)] = "x"
	}
	overLimit(t, errOnly(writeAgentConfig(home, mustJSON(wire.UserAgentConfig{Personas: many}), nil)))

	servers := func(size int) wire.JSONValue { return []byte(`{"s":{"command":"` + strings.Repeat("x", size) + `"}}`) }
	must(t, d.writeMCPServers(home, servers(10)))
	path := filepath.Join(home, ".agents", "mcp.json")
	was = readText(t, path)
	overLimit(t, d.writeMCPServers(home, servers(mcpcatalog.MostBytes)))
	if readText(t, path) != was {
		t.Error("the refusal changed mcp.json")
	}
	after := &userSettingsSyncState{}
	text := func(n int) wire.JSONValue { return mustJSON(wire.UserEnvironment{Text: strings.Repeat("#", n)}) }
	must(t, errOnly(d.writeYoursEnvLocked(home, text(envfile.FileMost), userSettingsSyncState{}, after)))
	if err := errOnly(d.writeYoursEnvLocked(home, text(envfile.FileMost+1), *after, after)); err == nil {
		t.Error("an environment over its limit was written")
	}
	if got := readText(t, filepath.Join(home, envRel)); len(got) != envfile.FileMost {
		t.Errorf("the environment is %d bytes", len(got))
	}
}

func TestAConnectorThatMakesMCPJSONTooLargeIsRefused(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mcp.json")
	entry := func(size int) skillcatalog.Value {
		e := skillcatalog.Object()
		e.Set("command", skillcatalog.String(strings.Repeat("x", size)))
		return e
	}
	must(t, mcpcatalog.Upsert(path, "a", entry(0)))
	base := len(readText(t, path))
	must(t, mcpcatalog.Upsert(path, "a", entry(mcpcatalog.MostBytes-base)))
	was := readText(t, path)
	if len(was) != mcpcatalog.MostBytes {
		t.Fatalf("%d bytes", len(was))
	}
	err := mcpcatalog.Upsert(path, "b", entry(0))
	overLimit(t, err)
	if readText(t, path) != was {
		t.Error("the refusal changed the file")
	}
	if told := mcpcatalog.WriteError(path, err); told.Why.Failed == nil {
		t.Errorf("the person is told %+v", told.Why)
	}
	// A file that is too large already can shrink.
	writeFile(t, path, strings.Replace(was, "xx", strings.Repeat("x", 10), 1))
	must(t, mcpcatalog.Remove(path, "a"))
}

func TestViewCatalogsStayWithinTheirLimits(t *testing.T) {
	clock := atTime(t, time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC))
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	c := viewCatalog{Version: viewCatalogVersion, Server: "s", MadeAt: clock.now()}
	b, err := json.MarshalIndent(c, "", "  ")
	must(t, err)
	c.Server += strings.Repeat("s", viewCatalogMost-len(b))
	must(t, d.writeViewCatalog("k", c))
	c.Server += "s"
	overLimit(t, d.writeViewCatalog("k", c))
	if fileExists(d.viewCatalogPath("k")) {
		t.Error("the catalog before it stayed, and it is stale")
	}
	for i := range viewCatalogsMost + 1 {
		key := fmt.Sprintf("k%03d", i)
		must(t, d.writeViewCatalog(key, viewCatalog{Version: viewCatalogVersion, Server: key, MadeAt: clock.now()}))
		at := clock.now().Add(time.Duration(i) * time.Second)
		must(t, os.Chtimes(d.viewCatalogPath(key), at, at))
	}
	d.sweepViewCatalogs()
	if fileExists(d.viewCatalogPath("k000")) || !fileExists(d.viewCatalogPath("k001")) {
		t.Error("the oldest is not the one that went")
	}
	clock.add(viewCatalogFresh)
	d.sweepViewCatalogs()
	if names, _ := os.ReadDir(filepath.Dir(d.viewCatalogPath("k"))); len(names) != 0 {
		t.Errorf("%d catalogs past their age", len(names))
	}
}
