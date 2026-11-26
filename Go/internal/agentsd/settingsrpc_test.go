package agentsd

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestTheWindowSavesTheSettingsTheHostHadNoWriteFor(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()

	var cp wire.ClientPermissionSettings
	mustUnmarshal(t, c.call(t, 1, "clientPermissions/set", wire.ClientPermissionSettings{Cursor: "alwaysApprove", Grok: "default", Opencode: "alwaysApprove"}), &cp)
	if cp.Cursor != "alwaysApprove" || cp.Grok != "default" || cp.Opencode != "alwaysApprove" {
		t.Fatalf("%+v", cp)
	}
	mustUnmarshal(t, c.call(t, 2, "clientPermissions/state", nil), &cp)
	if cp.Opencode != "alwaysApprove" {
		t.Fatalf("not kept %+v", cp)
	}

	var sb wire.SandboxSettings
	mustUnmarshal(t, c.call(t, 3, "sandbox/set", wire.SandboxSettings{Defaults: map[string]wire.SandboxChoice{
		"claude": "off", "gemini": "on", "cursor": "off", "codex": "runtime",
	}}), &sb)
	if len(sb.Defaults) != 1 || sb.Defaults["claude"] != "off" {
		t.Fatalf("dropped what the catalog does not offer: %+v", sb)
	}

	var limit wire.WorkflowLimitSettings
	mustUnmarshal(t, c.call(t, 4, "workflows/limit", nil), &limit)
	if limit.Total != nil {
		t.Fatalf("a missing file is the default, got %+v", limit)
	}
	total := int64(12)
	mustUnmarshal(t, c.call(t, 5, "workflows/setLimit", wire.WorkflowLimitSettings{Total: &total}), &limit)
	if limit.Total == nil || *limit.Total != 12 || d.workflowTotal() != 12 {
		t.Fatalf("%+v total %d", limit, d.workflowTotal())
	}
	if e := c.callError(t, 6, "workflows/setLimit", map[string]any{"total": 0}); e == nil || e.Message != "Workflows turned on must be between 1 and 100." {
		t.Fatalf("%+v", e)
	}
	max := int64(100)
	c.call(t, 7, "workflows/setLimit", wire.WorkflowLimitSettings{Total: &max})
	if d.workflowTotal() != 100 {
		t.Fatalf("total %d", d.workflowTotal())
	}
	if e := c.callError(t, 8, "workflows/setLimit", map[string]any{"total": 101}); e == nil || e.Message != "Workflows turned on must be between 1 and 100." {
		t.Fatalf("%+v", e)
	}
	if d.workflowTotal() != 100 {
		t.Fatal("a refused total was saved")
	}
}

func TestRetentionDescribesADeleteUntilItIsConfirmed(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	old := wire.DateOf(time.Now().Add(-100 * 24 * time.Hour))
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		by := wire.AgentArchivedReasonByUser
		a.State, a.ArchivedAt, a.ArchivedReason = wire.AgentStateArchived, &old, &by
	})
	ask := func(id int, keep wire.RetentionKeepFor, confirmed bool) wire.RetentionSetResult {
		t.Helper()
		var r wire.RetentionSetResult
		mustUnmarshal(t, c.call(t, id, "retention/set", wire.RetentionSetRequest{Settings: wire.RetentionSettings{KeepFor: keep}, Confirmed: confirmed}), &r)
		return r
	}
	preview := ask(1, wire.RetentionKeepForDays7, false)
	if preview.Applied || preview.WouldDelete == nil || preview.WouldDelete.Count != 1 || preview.State != nil {
		t.Fatalf("%+v", preview)
	}
	if d.keepFor() != wire.RetentionKeepForDays30 || d.Store.Agent(a.ID) == nil {
		t.Fatal("an unconfirmed delete was saved")
	}
	// Forever is 90 days (#6677), so it too would delete one archived 100 days ago.
	if kept := ask(2, wire.RetentionKeepForForever, false); kept.Applied || kept.WouldDelete == nil || kept.WouldDelete.Count != 1 {
		t.Fatalf("%+v", kept)
	}
	done := ask(3, wire.RetentionKeepForDays7, true)
	if !done.Applied || done.State == nil || done.State.ArchivedCount != 0 || d.Store.Agent(a.ID) != nil {
		t.Fatalf("%+v still %v", done, d.Store.Agent(a.ID) != nil)
	}
}

func TestAlwaysApproveAnswersBeforeACard(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	if err := d.writeSetting("client-permissions.json", wire.ClientPermissionSettings{Cursor: "alwaysApprove", Grok: "default", Opencode: "default"}); err != nil {
		t.Fatal(err)
	}
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) { a.RuntimeID = "cursor" })
	l := &live{d: d, id: a.ID, pending: map[wire.UUID]*pendingPermission{}}
	allow := sdk.RequestPermissionRequest{Options: []sdk.PermissionOption{
		{Kind: sdk.PermissionOptionKindAllowAlways, Name: "Always", OptionId: "always"},
		{Kind: sdk.PermissionOptionKindAllowOnce, Name: "Once", OptionId: "once"},
	}}
	if got := l.Permission(context.Background(), allow); got != "once" {
		t.Fatalf("allow once first, got %q", got)
	}
	entries, _, _ := d.Store.Transcript(a.ID, 0)
	if len(entries) != 0 {
		t.Fatalf("a card was asked: %+v", entries)
	}

	onlyRefuse := sdk.RequestPermissionRequest{Options: []sdk.PermissionOption{
		{Kind: sdk.PermissionOptionKindRejectOnce, Name: "No", OptionId: "no"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := l.Permission(ctx, onlyRefuse); got != "" {
		t.Fatal(got)
	}
	entries, _, _ = d.Store.Transcript(a.ID, 0)
	asked := false
	for _, e := range entries {
		asked = asked || e.Kind.PermissionAsked != nil
	}
	if !asked {
		t.Fatal("a request with no allow option skipped the card")
	}
}

func TestAPersonMovesAndDoesNotCancelTheAgentsOwnMove(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	a := seeded(t, d, dir, func(a *wire.Agent) { a.State, a.EndedReason = wire.AgentStateRunning, nil })
	var ans wire.MoveAnswer
	mustUnmarshal(t, c.call(t, 1, "agents/move", map[string]any{"agentID": a.ID, "target": map[string]any{"newWorktree": map[string]any{"name": "fix"}},
		"removeLeft": false, "discardChanges": false}), &ans)
	if ans.When != wire.MoveWhenAfterTurn || !strings.HasPrefix(ans.Message, "Moving to ") {
		t.Fatalf("%+v", ans)
	}
	if got := d.Store.Agent(a.ID); got.PendingMove == nil || got.PendingMove.AskedBy != wire.MoveAskerPerson {
		t.Fatalf("%+v", got.PendingMove)
	}
	mustUnmarshal(t, c.call(t, 2, "agents/move", map[string]any{"agentID": a.ID, "removeLeft": false, "discardChanges": false}), &ans)
	if ans.When != wire.MoveWhenNothing || ans.Message != "Move cancelled." || d.Store.Agent(a.ID).PendingMove != nil {
		t.Fatalf("%+v pending %v", ans, d.Store.Agent(a.ID).PendingMove)
	}

	name := "fix"
	d.change(a.ID, func(a *wire.Agent) {
		a.PendingMove = &wire.PendingMove{AskedBy: wire.MoveAskerAgent, AskedAt: now(), Target: wire.MoveTarget{NewWorktree: &wire.MoveTargetNewWorktree{Name: &name}}}
	})
	mustUnmarshal(t, c.call(t, 3, "agents/move", map[string]any{"agentID": a.ID, "removeLeft": false, "discardChanges": false}), &ans)
	if ans.When != wire.MoveWhenNothing || ans.Message != "No move was waiting." || d.Store.Agent(a.ID).PendingMove.AskedBy != wire.MoveAskerAgent {
		t.Fatalf("%+v %+v", ans, d.Store.Agent(a.ID).PendingMove)
	}
}

// The app's own tools are allowed with no card, under Claude's name and Antigravity's
// (agents_<tool>, #1361); a runtime's own tool with a look-alike name still asks.
func TestAppToolTitlesAllowedWithNoCard(t *testing.T) {
	t.Parallel()

	for title, want := range map[string]bool{"mcp__agents__lease_resource": true, "agents_lease_resource": true,
		"agents_show_file": true, "agents_browser_navigate": true, "agents_delete_everything": false,
		"Run client_create_file?": false, "lease_resource": false} {
		if got := isAppToolTitle(title); got != want {
			t.Errorf("%s: %v", title, got)
		}
	}
}
