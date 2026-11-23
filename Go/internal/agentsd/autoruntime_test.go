package agentsd

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/yamlnode"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Auto and the shared words (#2658): every cell of the shipped table is filled and gives no
// more access than its value says; Auto picks the first runtime that can take the session,
// leaves out the excluded and the out ones, tries a rate-limited one last, and says why
// when none can; the person's own cells win; workflows on Auto take only the shared words.

// SC-004, SC-005.
func TestTheShippedAutoMapIsWholeAndSafe(t *testing.T) {
	t.Parallel()

	settings := wire.AutoSettings{}
	for _, rt := range catalog {
		for _, f := range autoFields {
			checkShippedAutoField(t, rt.ID, f, settings)
		}
	}
	if m := cellMapping("opencode", wire.AutoFieldSandbox, "on", settings, nil); m.Mark != wire.AutoMarkNotPossible {
		t.Fatalf("%+v", m)
	}
	if m := cellMapping("grok", wire.AutoFieldPermission, "plan", settings, nil); m.Mark != wire.AutoMarkNearestSafer || m.Note == nil || *m.Note != "nearest safer: no plan mode" {
		t.Fatalf("%+v", m)
	}
}

// checkShippedAutoField checks that each cell of field f on runtime rtID is filled and gives
// no more access than its value says.
func checkShippedAutoField(t *testing.T, rtID string, f wire.AutoField, settings wire.AutoSettings) {
	t.Helper()
	minRank := 99
	for _, l := range levelsOf(f) {
		if c := shippedCell(rtID, f, l); c.mark != wire.AutoMarkNotPossible && c.rank < minRank {
			minRank = c.rank
		}
	}
	for _, l := range levelsOf(f) {
		m := cellMapping(rtID, f, l, settings, nil)
		if m.Words == "" || m.Mark == "" {
			t.Errorf("%s %s %s: an empty cell", rtID, f, l)
		}
		if m.Mark != wire.AutoMarkNotPossible && m.Native == nil {
			t.Errorf("%s %s %s: no setting", rtID, f, l)
		}
		ranks, ranked := levelRank[f]
		if !ranked || m.Mark == wire.AutoMarkNotPossible {
			continue
		}
		c := shippedCell(rtID, f, l)
		switch {
		case c.rank <= ranks[l]:
		case c.mark == wire.AutoMarkNearestSafer && c.rank == minRank:
			// No setting of the runtime gives less: the nearest one, said in the cell.
		default:
			t.Errorf("%s %s %s: gives more access (%d) than its value (%d)", rtID, f, l, c.rank, ranks[l])
		}
	}
}

// A model the runtime no longer offers is dropped, and Auto passes that runtime by (FR-020).
func TestADroppedCell(t *testing.T) {
	t.Parallel()

	cat := "model"
	offers := []wire.ConfigOption{{ID: "model", Name: "Model", Type: "select", Category: &cat,
		Options: json.RawMessage(`[{"value":"sonnet","name":"Sonnet 5"},{"value":"opus[1m]","name":"Opus 5.5"}]`)}}
	settings := wire.AutoSettings{}
	if m := cellMapping("claude", wire.AutoFieldModel, "fast", settings, offers); m.Mark != wire.AutoMarkDropped {
		t.Fatalf("%+v", m)
	}
	if m := cellMapping("claude", wire.AutoFieldModel, "balanced", settings, offers); m.Mark != wire.AutoMarkAutoMapped || m.Words != "Sonnet 5" {
		t.Fatalf("%+v", m)
	}
	// Fable, its most capable (#3041), with 1M context or not, else Opus: the first the plan has.
	if m := cellMapping("claude", wire.AutoFieldModel, "smart", settings, offers); m.Native == nil || stringValue(m.Native.Options["model"]) != "opus[1m]" {
		t.Fatalf("%+v", m)
	}
	offers[0].Options = json.RawMessage(`[{"value":"opus","name":"Opus 5.5"}]`)
	if m := cellMapping("claude", wire.AutoFieldModel, "smart", settings, offers); m.Native == nil || stringValue(m.Native.Options["model"]) != "opus" {
		t.Fatalf("%+v", m)
	}
	offers[0].Options = json.RawMessage(`[{"value":"opus[1m]","name":"Opus 5.5"},{"value":"claude-fable-5-1[1m]","name":"Fable 5.1"}]`)
	if m := cellMapping("claude", wire.AutoFieldModel, "smart", settings, offers); m.Native == nil || stringValue(m.Native.Options["model"]) != "claude-fable-5-1[1m]" {
		t.Fatalf("%+v", m)
	}
}

// Codex takes the newest Luna, Sol and Astra it offers (#6891), and none it does not offer.
func TestCodexTakesTheNewestOfEachLine(t *testing.T) {
	t.Parallel()

	cat := "model"
	offers := []wire.ConfigOption{{ID: "model", Name: "Model", Type: "select", Category: &cat,
		Options: json.RawMessage(`[{"value":"gpt-6-astra","name":"6 Astra"},{"value":"gpt-6-sol","name":"6 Sol"},{"value":"gpt-6-luna","name":"6 Luna"},` +
			`{"value":"gpt-5.6-sol","name":"5.6 Sol"},{"value":"gpt-5.6-terra","name":"5.6 Terra"},{"value":"gpt-5.6-luna","name":"5.6 Luna"},{"value":"gpt-6.2-sol","name":"6.2 Sol"}]`)},
		{ID: "reasoning_effort", Name: "Effort", Type: "select", Options: json.RawMessage(`[{"value":"low","name":"Low"},{"value":"high","name":"High"}]`)}}
	model := func(level string, offers []wire.ConfigOption) wire.AutoMapping {
		return cellMapping("codex", wire.AutoFieldModel, level, wire.AutoSettings{}, offers)
	}
	for level, want := range map[string]string{"fast": "gpt-6-luna", "balanced": "gpt-6.2-sol", "smart": "gpt-6-astra"} {
		if m := model(level, offers); m.Native == nil || stringValue(m.Native.Options["model"]) != want || m.Mark != wire.AutoMarkAutoMapped {
			t.Fatalf("%s: %+v", level, m)
		}
	}
	if m := model("smart", offers); stringValue(m.Native.Options["reasoning_effort"]) != "high" {
		t.Fatalf("%+v", m)
	}
	// A stale offer of 5.6 only: no Astra, so Smart is dropped, not given another line.
	offers[0].Options = json.RawMessage(`[{"value":"gpt-5.6-sol","name":"5.6 Sol"},{"value":"gpt-5.6-luna","name":"5.6 Luna"}]`)
	if m := model("fast", offers); stringValue(m.Native.Options["model"]) != "gpt-5.6-luna" {
		t.Fatalf("%+v", m)
	}
	if m := model("smart", offers); m.Mark != wire.AutoMarkDropped {
		t.Fatalf("%+v", m)
	}
	// Before Codex has said: the first named model.
	if m := model("balanced", nil); stringValue(m.Native.Options["model"]) != "gpt-6-sol" {
		t.Fatalf("%+v", m)
	}
}

// A start on Auto asks Codex again before it passes it by for a model the cached offer lacks
// (#6891); a failed ask says why, and the prompt stays.
func TestAutoRefreshesAStaleCodexOffer(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := "file://" + t.TempDir()
	cat, modeCat, collabCat, effortCat := "model", "mode", "collaboration_mode", "thought_level"
	offer := func(models string) optionEntry {
		return optionEntry{Options: []wire.ConfigOption{{ID: "model", Name: "Model", Type: "select", Category: &cat,
			CurrentValue: json.RawMessage(`"gpt-5.6-luna"`), Options: json.RawMessage(models)},
			{ID: "reasoning_effort", Name: "Effort", Type: "select", Category: &effortCat, CurrentValue: json.RawMessage(`"medium"`),
				Options: json.RawMessage(`[{"value":"low","name":"Low"},{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]`)},
			{ID: "mode", Name: "Mode", Type: "select", Category: &modeCat, Options: json.RawMessage(`[{"value":"read-only","name":"Read Only"},{"value":"agent","name":"Agent"}]`)},
			{ID: "collaboration_mode", Name: "Collaboration", Type: "select", Category: &collabCat, Options: json.RawMessage(`[{"value":"default","name":"Default"},{"value":"plan","name":"Plan"}]`)}}}
	}
	d.remember(optionKey("codex", wire.URL(dir), nil), offer(`[{"value":"gpt-5.6-luna","name":"5.6 Luna"}]`))
	var asked atomic.Int32
	d.optionMu.Lock()
	d.offersOf = func(*runtimeDef, string) (optionEntry, error) {
		asked.Add(1)
		return offer(`[{"value":"gpt-6-astra","name":"6 Astra"},{"value":"gpt-6-luna","name":"6 Luna"}]`), nil
	}
	d.optionMu.Unlock()
	out(d, "claude", false)
	var p wire.AutoPreview
	mustUnmarshal(t, c.call(t, 1, "auto/preview", map[string]any{"levels": map[string]any{"model": "smart"}}), &p)
	if p.RuntimeID != nil && *p.RuntimeID == "codex" {
		t.Fatalf("a preview asks no runtime: %+v", p)
	}
	var agent wire.UUID
	mustUnmarshal(t, c.call(t, 2, "agents/start", map[string]any{"runtimeID": "auto", "cwd": dir, "prompt": "hello", "levels": map[string]any{"model": "smart"}}), &agent)
	if a := d.Store.Agent(agent); a.RuntimeID != "codex" || stringValue(a.StartOptions.Values["model"]) != "gpt-6-astra" || asked.Load() != 1 {
		t.Fatalf("%s %s asked %d", a.RuntimeID, a.StartOptions.Values, asked.Load())
	}
	d.optionMu.Lock()
	d.offersOf = func(rt *runtimeDef, _ string) (optionEntry, error) {
		return optionEntry{}, &Error{Code: CodeRuntimeWillNotStart, Message: rt.Name + " did not start"}
	}
	d.optionMu.Unlock()
	d.remember(optionKey("codex", wire.URL(dir), nil), offer(`[{"value":"gpt-5.6-luna","name":"5.6 Luna"}]`))
	c.call(t, 3, "auto/set", map[string]any{"order": []string{"codex"}, "excluded": []string{"claude", "cursor", "copilot", "grok", "opencode", "antigravity"}, "overrides": []any{}})
	err := c.callError(t, 4, "agents/start", map[string]any{"runtimeID": "auto", "cwd": dir, "prompt": "hello", "levels": map[string]any{"model": "smart"}})
	if err == nil || err.Code != wire.FailureAutoNoRuntime || !strings.Contains(err.Message, "Codex: could not read its models (Codex did not start)") {
		t.Fatalf("%+v", err)
	}
}

func out(d *Daemon, runtime string, rateLimited bool) {
	s := availableState(ownKey(runtime))
	s.Status = wire.AllowanceStateStatus{Out: &wire.AllowanceStateStatusOut{Why: wire.AllowanceStateOutReasonAllowanceSpent}}
	if rateLimited {
		s.Status = wire.AllowanceStateStatus{RateLimited: &wire.AllowanceStateStatusRateLimited{Until: now()}}
	}
	d.allowMu.Lock()
	d.allowancesLocked()[s.CredentialKey] = s
	d.allowMu.Unlock()
}

func TestAutoPicksTheRuntime(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := "file://" + t.TempDir()
	startAuto := func(id int, levels map[string]any) wire.Agent {
		var agent wire.UUID
		mustUnmarshal(t, c.call(t, id, "agents/start", map[string]any{"runtimeID": "auto", "cwd": dir, "prompt": "hello", "levels": levels}), &agent)
		return *d.Store.Agent(agent)
	}
	a := startAuto(1, map[string]any{"model": "balanced", "permission": "ask"})
	if a.RuntimeID != "claude" || a.Auto == nil || a.Auto.RuntimeID != "claude" {
		t.Fatalf("%s %+v", a.RuntimeID, a.Auto)
	}
	if stringValue(a.StartOptions.Values["model"]) != "sonnet" || stringValue(a.StartOptions.Values["mode"]) != "default" {
		t.Fatalf("%s", a.StartOptions.Values)
	}
	if a.Levels == nil || a.Levels.Model == nil || *a.Levels.Model != wire.ModelLevelBalanced || *a.Levels.Permission != wire.PermissionLevelAsk {
		t.Fatalf("%+v", a.Levels)
	}
	// Claude out: the next in the order. Its first turn ends first: a turn that works makes
	// its credential available again (allowanceWorked), so one that ended after out would
	// put Claude back (#6239).
	waitFor(t, "the first turn's end", func() bool { return d.Store.Agent(a.ID).State == wire.AgentStateFinished })
	out(d, "claude", false)
	if a := startAuto(2, map[string]any{"permission": "plan"}); a.RuntimeID != "codex" || stringValue(a.StartOptions.Values["collaboration_mode"]) != "plan" ||
		len(a.Auto.Skipped) != 1 || a.Auto.Skipped[0].Reason != "out" {
		t.Fatalf("%s %+v", a.RuntimeID, a.Auto)
	}
	// The person's order and Include switches.
	c.call(t, 3, "auto/set", map[string]any{"order": []string{"grok", "codex"}, "excluded": []string{"codex"}, "overrides": []any{}})
	if a := startAuto(4, nil); a.RuntimeID != "grok" {
		t.Fatal(a.RuntimeID)
	}
	// Runtime sandbox: Grok cannot, and nor can the rest once Claude is out and Codex
	// left out. The prompt stays, with why for each.
	err := c.callError(t, 5, "agents/start", map[string]any{"runtimeID": "auto", "cwd": dir, "prompt": "hello", "sandbox": "on"})
	if err == nil || !strings.Contains(err.Message, "Grok: no Runtime sandbox") {
		t.Fatalf("%+v", err)
	}
}

func TestAutoWhenNoRuntimeCan(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	var all []string
	for _, r := range catalog {
		if r.ID != "claude" {
			all = append(all, r.ID)
		}
	}
	c.call(t, 1, "auto/set", map[string]any{"order": []string{}, "excluded": all, "overrides": []any{}})
	out(d, "claude", false)
	err := c.callError(t, 2, "agents/start", map[string]any{"runtimeID": "auto", "cwd": "file://" + t.TempDir(), "prompt": "hello"})
	if err == nil || err.Code != wire.FailureAutoNoRuntime || !strings.Contains(err.Message, "Claude: out") || !strings.Contains(err.Message, "Codex: left out") {
		t.Fatalf("%+v", err)
	}
	var p wire.AutoPreview
	mustUnmarshal(t, c.call(t, 3, "auto/preview", map[string]any{"cwd": "file://" + t.TempDir()}), &p)
	if p.RuntimeID != nil || len(p.Skipped) != len(catalog) {
		t.Fatalf("%+v", p)
	}
	// A rate-limited runtime is still taken when no other can.
	out(d, "claude", true)
	mustUnmarshal(t, c.call(t, 4, "auto/preview", map[string]any{"cwd": "file://" + t.TempDir()}), &p)
	if p.RuntimeID == nil || *p.RuntimeID != "claude" {
		t.Fatalf("%+v", p)
	}
}

// A picture goes to a runtime that takes pictures (#3162): one that said it does not is passed
// by, at the start and in the preview; one not started yet is not passed by on a guess.
func TestAutoPassesByARuntimeThatTakesNoPictures(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := "file://" + t.TempDir()
	yes, no := true, false
	d.noteAccount("claude", false, wire.ACPPromptCapabilities{Image: &no})
	d.noteAccount("codex", false, wire.ACPPromptCapabilities{Image: &yes})
	picture := []map[string]any{{"id": "00000000-0000-0000-0000-000000000001", "displayName": "shot.png",
		"block": map[string]any{"type": "image", "mimeType": "image/png", "data": "iVBORw0KGgo="}}}
	var p wire.AutoPreview
	mustUnmarshal(t, c.call(t, 1, "auto/preview", map[string]any{"cwd": dir, "needs": map[string]any{"image": true}}), &p)
	if p.RuntimeID == nil || *p.RuntimeID != "codex" || len(p.Skipped) != 1 || p.Skipped[0].Reason != "takes no pictures" {
		t.Fatalf("%+v", p)
	}
	mustUnmarshal(t, c.call(t, 2, "auto/preview", map[string]any{"cwd": dir}), &p)
	if p.RuntimeID == nil || *p.RuntimeID != "claude" {
		t.Fatalf("without a picture: %+v", p)
	}
	var agent wire.UUID
	mustUnmarshal(t, c.call(t, 3, "agents/start", map[string]any{"runtimeID": "auto", "cwd": dir, "prompt": "look", "attachments": picture}), &agent)
	if a := d.Store.Agent(agent); a.RuntimeID != "codex" || a.Auto == nil || a.Auto.Skipped[0].Reason != "takes no pictures" {
		t.Fatalf("%s %+v", a.RuntimeID, a.Auto)
	}
	// None that takes pictures: the prompt stays, and the refusal says why.
	d.noteAccount("codex", false, wire.ACPPromptCapabilities{Image: &no})
	var rest []string
	for _, r := range catalog {
		if r.ID != "claude" && r.ID != "codex" {
			rest = append(rest, r.ID)
		}
	}
	c.call(t, 4, "auto/set", map[string]any{"order": []string{}, "excluded": rest, "overrides": []any{}})
	err := c.callError(t, 5, "agents/start", map[string]any{"runtimeID": "auto", "cwd": dir, "prompt": "look", "attachments": picture})
	if err == nil || !strings.Contains(err.Message, "Claude: takes no pictures") || !strings.Contains(err.Message, "Codex: takes no pictures") {
		t.Fatalf("%+v", err)
	}
}

// The person's own cell wins, on Auto and on a named runtime (FR-016, FR-018); a native value
// in the start wins over a level (the ⋯ own menu).
func TestTheMappingsAreThePersons(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	bypass := map[string]any{"options": map[string]any{"mode": "bypassPermissions"}}
	c.call(t, 1, "auto/set", map[string]any{"order": []string{}, "excluded": []string{},
		"overrides": []any{map[string]any{"runtimeID": "claude", "field": "permission", "level": "approveForMe", "native": bypass}}})
	var m wire.AutoMappings
	mustUnmarshal(t, c.call(t, 2, "auto/mappings", map[string]any{}), &m)
	found := false
	for _, cell := range m.Cells {
		if cell.RuntimeID == "claude" && cell.Field == wire.AutoFieldPermission && cell.Level == "approveForMe" {
			found = cell.Mark == wire.AutoMarkYours && stringValue(cell.Native.Options["mode"]) == "bypassPermissions"
		}
	}
	if !found || len(m.Cells) != len(catalog)*12 || len(m.Choices) == 0 {
		t.Fatalf("%v %d", found, len(m.Cells))
	}
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 3, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(), "prompt": "hello",
		"levels": map[string]any{"permission": "approveForMe", "model": "smart"}, "startOptions": map[string]any{"values": map[string]any{"model": "haiku"}, "extraArguments": []string{}}}), &id)
	a := d.Store.Agent(id)
	if stringValue(a.StartOptions.Values["mode"]) != "bypassPermissions" || stringValue(a.StartOptions.Values["model"]) != "haiku" || a.Auto != nil {
		t.Fatalf("%s", a.StartOptions.Values)
	}
	if a.Levels == nil || a.Levels.Permission == nil || *a.Levels.Permission != wire.PermissionLevelApproveForMe || a.Levels.Model != nil {
		t.Fatalf("%+v", a.Levels)
	}
}

// A record keeps the levels it was given when it last changed: a cell the person moved
// since, or a record from before levels were kept, said Custom for a mode a cell now gives
// (#5730). auto/set and a branch match them again.
func TestLevelsFollowTheMappings(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := t.TempDir()
	// Claude's own Auto mode: no shipped cell gives it, so Custom, and no levels kept.
	a := seeded(t, d, dir, func(a *wire.Agent) { a.StartOptions.Values["mode"] = jsonString("auto") })
	permission := func(id wire.UUID) *wire.PermissionLevel {
		if l := d.Store.Agent(id).Levels; l != nil {
			return l.Permission
		}
		return nil
	}
	if d.rematchLevels(); permission(a.ID) != nil {
		t.Fatalf("auto is no shipped cell: %v", *permission(a.ID))
	}
	auto := map[string]any{"options": map[string]any{"mode": "auto"}}
	c.call(t, 1, "auto/set", map[string]any{"order": []string{}, "excluded": []string{},
		"overrides": []any{map[string]any{"runtimeID": "claude", "field": "permission", "level": "approveForMe", "native": auto}}})
	// On a miss it says what the record, the saved settings and their match were (#6432),
	// before the daemon shuts down; waitFor's failure gives every goroutine's stack.
	defer func() {
		if t.Failed() {
			r := d.Store.Agent(a.ID)
			s := d.autoSettings()
			t.Logf("levels %s, settings %s, they match %s; still running: %v",
				mustJSON(r.Levels), mustJSON(s), mustJSON(d.matchLevelsWith(r, s)), d.liveJobs())
		}
	}()
	waitFor(t, "Approve for me", func() bool {
		p := permission(a.ID)
		return p != nil && *p == wire.PermissionLevelApproveForMe
	})
	// A branch says the mode it runs in, not Custom.
	b := seeded(t, d, dir, func(a *wire.Agent) { a.StartOptions.Values["mode"] = jsonString("plan") })
	if _, err := d.Store.Append(b.ID, wire.TranscriptEntry{ID: store.NewUUID(), At: now(),
		Kind: wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	entries, _, _ := d.Store.Transcript(b.ID, 0)
	id, err := d.branchFrom(wire.BranchFromRequest{AgentID: b.ID, EntryID: entries[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if p := permission(id); p == nil || *p != wire.PermissionLevelPlan {
		t.Fatalf("branch: %v", p)
	}
}

// OpenCode's Ask and Approve for me cells both set mode=build: a start and a later change
// must keep the level the person chose, as the web page already does (#2893, #6822).
func TestAnOpenCodePermissionChoiceStaysWhenCellsShareAnOption(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := t.TempDir()
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "opencode",
		"cwd": "file://" + dir, "prompt": "hello", "levels": map[string]any{"permission": "approveForMe"}}), &id)
	level := func() *wire.PermissionLevel {
		if l := d.Store.Agent(id).Levels; l != nil {
			return l.Permission
		}
		return nil
	}
	if a := d.Store.Agent(id); stringValue(a.StartOptions.Values["mode"]) != "build" {
		t.Fatalf("start: %+v", a.StartOptions.Values)
	}
	if l := level(); l == nil || *l != wire.PermissionLevelApproveForMe {
		t.Fatalf("start: %v", l)
	}
	for i, want := range []wire.PermissionLevel{wire.PermissionLevelAsk, wire.PermissionLevelApproveForMe} {
		mustUnmarshal(t, c.call(t, i+2, "agents/setLevels", map[string]any{"agentID": id,
			"levels": map[string]any{"permission": string(want)}}), &wire.Agent{})
		if l := level(); l == nil || *l != want {
			t.Fatalf("setLevels %s: %v", want, l)
		}
	}
}

// A kept level still has to match its cell (#5730): move OpenCode's Approve for me cell off
// build, and the record rematches to Ask (#6822).
func TestAStaleOpenCodeLevelRematchesWhenItsCellMoves(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := t.TempDir()
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "opencode",
		"cwd": "file://" + dir, "prompt": "hello", "levels": map[string]any{"permission": "approveForMe"}}), &id)
	if l := d.Store.Agent(id).Levels; l == nil || l.Permission == nil || *l.Permission != wire.PermissionLevelApproveForMe {
		t.Fatalf("start: %+v", l)
	}
	auto := wire.AutoSettings{Order: []string{}, Excluded: []string{}, Overrides: []wire.AutoOverride{{RuntimeID: "opencode",
		Field: wire.AutoFieldPermission, Level: string(wire.PermissionLevelApproveForMe),
		Native: wire.NativeSetting{Options: map[string]wire.JSONValue{"mode": jsonString("plan")}}}}}
	if err := d.writeSetting("auto.json", auto); err != nil {
		t.Fatal(err)
	}
	d.rematchLevels()
	if l := d.Store.Agent(id).Levels; l == nil || l.Permission == nil || *l.Permission != wire.PermissionLevelAsk {
		t.Fatalf("stale Approve for me: %+v", l)
	}
}

// A pass that matches levels again, and an auto/set while it runs: the new settings are
// saved, and their own pass ends, before this pass gets to a record. That record follows the
// settings saved, not the ones the pass read when it began (#6432).
func TestARematchTakesTheSettingsSavedSinceItBegan(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := t.TempDir()
	mode := func(a *wire.Agent) { a.StartOptions.Values["mode"] = jsonString("auto") }
	// The pass takes the newest first: first, then second.
	second := seeded(t, d, dir, mode)
	first := seeded(t, d, dir, func(a *wire.Agent) {
		mode(a)
		a.LastActivityAt = wire.DateOf(time.Now().Add(time.Minute))
	})
	auto := wire.AutoSettings{Order: []string{}, Excluded: []string{}, Overrides: []wire.AutoOverride{{RuntimeID: "claude",
		Field: wire.AutoFieldPermission, Level: string(wire.PermissionLevelApproveForMe),
		Native: wire.NativeSetting{Options: map[string]wire.JSONValue{"mode": jsonString("auto")}}}}}
	if err := d.writeSetting("auto.json", auto); err != nil {
		t.Fatal(err)
	}
	// The person takes the cell back once the pass has changed the first record. The pass of
	// that auto/set finds the second record Custom already, so it changes nothing.
	once := false
	d.changeTold = func(a wire.Agent) {
		if a.ID == first.ID && !once {
			once = true
			if err := d.writeSetting("auto.json", wire.AutoSettings{Order: []string{}, Excluded: []string{}, Overrides: []wire.AutoOverride{}}); err != nil {
				t.Error(err)
			}
		}
	}
	d.rematchLevels()
	if !once {
		t.Fatal("the pass did not change the first record")
	}
	if l := d.Store.Agent(second.ID).Levels; l != nil && l.Permission != nil {
		t.Fatalf("the second record took the old cell: %v", *l.Permission)
	}
}

// A per-agent answer to permission requests, before the person's in Settings (FR-019).
func TestAnAgentsOwnPermissionRequests(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "cursor", "cwd": "file://" + t.TempDir(), "prompt": "hello",
		"levels": map[string]any{"permissionRequests": "alwaysApprove"}}), &id)
	if a := d.Store.Agent(id); !d.agentAlwaysApproves(a) || d.alwaysApproves("cursor") {
		t.Fatal("not the agent's own")
	}
	var a wire.Agent
	mustUnmarshal(t, c.call(t, 2, "agents/setClientPermission", map[string]any{"agentID": id}), &a)
	if a.ClientPermission != nil || d.agentAlwaysApproves(&a) {
		t.Fatalf("%+v", a.ClientPermission)
	}
}

// Story 5: workflows take the shared words; on Auto, only them.
func TestWorkflowWordsOnAuto(t *testing.T) {
	t.Parallel()

	read := func(src string) error {
		m, err := yamlnode.MappingFrom(strings.Split(strings.TrimSpace(src), "\n"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = workflowSettings(m)
		return err
	}
	if err := read("runtime: auto\nmodel: smart\npermission-mode: plan\nsandbox: runtime\n"); err != nil {
		t.Fatal(err)
	}
	if err := read("runtime: auto\nmodel: opus\n"); err == nil || !strings.Contains(err.Error(), "fast") {
		t.Fatal(err)
	}
	if err := read("runtime: claude\nmodel: opus\npermission-mode: acceptEdits\n"); err != nil {
		t.Fatal(err)
	}
	if err := read("permission-requests: always\n"); err == nil {
		t.Fatal("took a word it does not know")
	}
	// A runtime's own mode that says the same as a level is that level on Auto (#2873).
	for word, level := range map[string]wire.PermissionLevel{"auto": wire.PermissionLevelApproveForMe,
		"bypassPermissions": wire.PermissionLevelBypassPermissions, "default": wire.PermissionLevelAsk} {
		levels, _, mode, _, err := fileLevels("auto", "", new(word), "", "")
		if err != nil || mode != nil || *levels.Permission != level {
			t.Fatal(word, levels, mode, err)
		}
		if _, _, mode, _, _ := fileLevels("claude", "", new(word), "", ""); mode == nil || *mode != word {
			t.Fatal("a named runtime keeps its own word", word)
		}
	}
	if err := read("runtime: auto\npermission-mode: acceptEdits\n"); err == nil {
		t.Fatal("took a mode that is no level")
	}
	// A file with no `runtime:` runs on Auto, so its page says a runtime's own value when
	// the file is read, not when a run starts.
	if w := parseWorkflow("---\nmodel: opus\n---\nGo", "x", "/p"); w.Problem == nil || w.Problem.Unreadable == nil ||
		!strings.Contains(w.Problem.Unreadable.Value, "no `runtime:`") {
		t.Fatalf("%+v", w.Problem)
	}
	if w := parseWorkflow("---\npermission-mode: auto\n---\nGo", "x", "/p"); w.Problem != nil {
		t.Fatalf("%+v", w.Problem)
	}
	if w := parseWorkflow("---\nruntime: claude\nmodel: opus\n---\nGo", "x", "/p"); w.Problem != nil {
		t.Fatalf("%+v", w.Problem)
	}
	levels, model, mode, box, err := fileLevels("auto", "fast", new("approve-for-me"), "", "none")
	if err != nil || model != "" || mode != nil || *levels.Model != wire.ModelLevelFast || *levels.Permission != wire.PermissionLevelApproveForMe || *box != wire.SandboxChoiceOff {
		t.Fatal(levels, model, mode, box, err)
	}
}

// FR-022: an Auto session whose runtime ran out carries on where Auto would start one now,
// with its values, only when asked.
func TestContinueOnAuto(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	var first wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "auto", "cwd": "file://" + t.TempDir(), "prompt": "hello",
		"levels": map[string]any{"model": "smart", "permission": "plan"}}), &first)
	out(d, "claude", false)
	var next wire.UUID
	mustUnmarshal(t, c.call(t, 2, "agents/continueInNew", map[string]any{"agentID": first, "auto": true}), &next)
	a := d.Store.Agent(next)
	if a.RuntimeID != "codex" || a.Auto == nil || *a.Auto.Levels.Model != wire.ModelLevelSmart || stringValue(a.StartOptions.Values["collaboration_mode"]) != "plan" {
		t.Fatalf("%s %+v %s", a.RuntimeID, a.Auto, a.StartOptions.Values)
	}
}

// A helper on Auto asks unless its starter approves every request itself: Grok's Approve
// for me is Always Approve, which runs shell commands Claude's Accept edits asks about.
func TestAnAutoHelperAsksLikeItsStarter(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := folder(t, c, 1)
	var others []string
	for _, id := range autoShippedOrder {
		if id != "grok" {
			others = append(others, id)
		}
	}
	c.call(t, 2, "auto/set", map[string]any{"order": []string{"grok"}, "excluded": others, "overrides": []any{}})
	helper := func(n int, level wire.PermissionLevel) *wire.Agent {
		lead := seeded(t, d, dir, func(a *wire.Agent) { a.Levels = &wire.Levels{Permission: &level} })
		m := appClient(t, d, lead.ID)
		m.post("initialize", map[string]any{}, 1)
		text, isErr := m.tool(n, "start_session", map[string]any{"prompt": "x", "runtime": "auto"})
		if isErr {
			t.Fatal(text)
		}
		return d.Store.Agent(helperID(text))
	}
	if a := helper(3, wire.PermissionLevelApproveForMe); a.RuntimeID != "grok" || a.ClientPermission == nil ||
		*a.ClientPermission != wire.ClientPermissionModeDefault || d.agentAlwaysApproves(a) {
		t.Fatalf("%s %v", a.RuntimeID, a.ClientPermission)
	}
	if a := helper(4, wire.PermissionLevelBypassPermissions); !d.agentAlwaysApproves(a) {
		t.Fatalf("%v", a.ClientPermission)
	}
}

// FR-018: a permission mode cell answers requests on a named runtime too. Approve for me on
// OpenCode starts with mode=build and the mapped Yes to all; an explicit per-agent answer wins.
func TestNamedOpenCodeModeCellAnswersRequests(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	dir := "file://" + t.TempDir()
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "opencode", "cwd": dir, "prompt": "hello",
		"levels": map[string]any{"permission": "approveForMe"}}), &id)
	a := d.Store.Agent(id)
	if stringValue(a.StartOptions.Values["mode"]) != "build" || a.ClientPermission == nil ||
		*a.ClientPermission != wire.ClientPermissionModeAlwaysApprove || !d.agentAlwaysApproves(a) {
		t.Fatalf("%s %v %+v", a.RuntimeID, a.ClientPermission, a.StartOptions.Values)
	}
	var own wire.UUID
	mustUnmarshal(t, c.call(t, 2, "agents/start", map[string]any{"runtimeID": "opencode", "cwd": dir, "prompt": "hello",
		"levels": map[string]any{"permission": "approveForMe", "permissionRequests": "default"}}), &own)
	if a := d.Store.Agent(own); a.ClientPermission == nil || *a.ClientPermission != wire.ClientPermissionModeDefault || d.agentAlwaysApproves(a) {
		t.Fatalf("explicit request cards did not win: %v", a.ClientPermission)
	}
}

// A later Permission mode change refreshes the mapped answer; an explicit choice in the same
// setLevels call still wins.
func TestSetLevelsRefreshesTheMappedAnswerOnANamedRuntime(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "opencode", "cwd": "file://" + t.TempDir(), "prompt": "hello",
		"levels": map[string]any{"permission": "approveForMe"}}), &id)
	if a := d.Store.Agent(id); a.ClientPermission == nil || *a.ClientPermission != wire.ClientPermissionModeAlwaysApprove {
		t.Fatalf("start: %v", a.ClientPermission)
	}
	c.call(t, 2, "agents/setLevels", map[string]any{"agentID": id, "levels": map[string]any{"permission": "ask"}})
	if a := d.Store.Agent(id); a.ClientPermission == nil || *a.ClientPermission != wire.ClientPermissionModeDefault || d.agentAlwaysApproves(a) {
		t.Fatalf("ask: %v", a.ClientPermission)
	}
	c.call(t, 3, "agents/setLevels", map[string]any{"agentID": id, "levels": map[string]any{"permission": "approveForMe"}})
	if a := d.Store.Agent(id); a.ClientPermission == nil || *a.ClientPermission != wire.ClientPermissionModeAlwaysApprove || !d.agentAlwaysApproves(a) {
		t.Fatalf("approveForMe: %v", a.ClientPermission)
	}
	c.call(t, 4, "agents/setLevels", map[string]any{"agentID": id, "levels": map[string]any{"permission": "ask", "permissionRequests": "alwaysApprove"}})
	if a := d.Store.Agent(id); a.ClientPermission == nil || *a.ClientPermission != wire.ClientPermissionModeAlwaysApprove || !d.agentAlwaysApproves(a) {
		t.Fatalf("explicit alwaysApprove: %v", a.ClientPermission)
	}
}
