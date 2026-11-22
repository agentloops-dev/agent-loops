package main

import (
	"context"
	"errors"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// prefsFake answers preferences/get on each host with its map; a host not in it does not answer.
func prefsFake(byHost map[string]map[string]any) *fake {
	f := &fake{answer: map[string]func(string, any) (any, error){}}
	f.answer["preferences/get"] = func(host string, _ any) (any, error) {
		m, ok := byHost[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		return m, nil
	}
	return f
}

func TestANewSessionStartsAtTheLevelsKeptOnTheOtherClientsNotAtAsk(t *testing.T) {
	f := prefsFake(map[string]map[string]any{
		"mac": {"startLevels": map[string]any{"model": "smart", "permission": "approveForMe"}},
	})
	l := startLevels(context.Background(), f, "linux-1", "")
	if l == nil || l.Model == nil || *l.Model != wire.ModelLevelSmart || l.Permission == nil || *l.Permission != wire.PermissionLevelApproveForMe {
		t.Fatalf("the Mac's kept levels go with the start: %+v", l)
	}
	if len(f.calls) != 1 || f.calls[0] != "mac preferences/get" {
		t.Fatalf("the Mac's host keeps them, so it is read first: %v", f.calls)
	}
}

func TestModeOverridesTheKeptPermissionAndKeepsTheKeptModel(t *testing.T) {
	f := prefsFake(map[string]map[string]any{
		"mac": {"startLevels": map[string]any{"model": "fast", "permission": "approveForMe"}},
	})
	l := startLevels(context.Background(), f, "mac", "plan")
	if l == nil || *l.Model != wire.ModelLevelFast || *l.Permission != wire.PermissionLevelPlan {
		t.Fatalf("--mode plan: %+v", l)
	}
	l = startLevels(context.Background(), f, "mac", "bypass-permissions")
	if *l.Permission != wire.PermissionLevelBypassPermissions {
		t.Fatalf("--mode bypass-permissions: %+v", l)
	}
}

func TestWithNoMacHostTheSessionsOwnMachineIsReadAndWithNothingKeptNoLevelsGo(t *testing.T) {
	f := prefsFake(map[string]map[string]any{
		"linux-1": {"startLevels": map[string]any{"permission": "plan"}},
	})
	l := startLevels(context.Background(), f, "linux-1", "")
	if l == nil || l.Model != nil || *l.Permission != wire.PermissionLevelPlan {
		t.Fatalf("the machine's own kept level: %+v", l)
	}
	f = prefsFake(map[string]map[string]any{"mac": {"startLevels": map[string]any{"model": "huge", "permission": "yolo"}}})
	if l := startLevels(context.Background(), f, "mac", ""); l != nil {
		t.Fatalf("levels it does not know are left out, so the daemon's default holds: %+v", l)
	}
	if l := startLevels(context.Background(), prefsFake(nil), "mac", ""); l != nil {
		t.Fatalf("no host answers: %+v", l)
	}
	if l := startLevels(context.Background(), prefsFake(nil), "mac", "ask"); l == nil || *l.Permission != wire.PermissionLevelAsk || l.Model != nil {
		t.Fatalf("--mode alone: %+v", l)
	}
}

func TestModeIsAPermissionLevel(t *testing.T) {
	for _, ok := range []string{"", "plan", "ask", "approve-for-me", "bypass-permissions", "approveForMe", "bypassPermissions"} {
		if err := checkMode(ok); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
	if err := checkMode("auto"); err == nil {
		t.Fatal("--mode auto is refused with the levels it takes")
	}
	if _, _, err := parse([]string{"--mode", "plan", "fix it"}); err != nil {
		t.Fatalf("--mode parses: %v", err)
	}
}

func TestPrintModeStartsAtTheKeptLevels(t *testing.T) {
	f, started := hostFake(t, []step{{state: wire.AgentStateFinished}})
	f.answer["preferences/get"] = func(string, any) (any, error) {
		return map[string]any{"startLevels": map[string]any{"permission": "approveForMe"}}, nil
	}
	p := place{machine: wire.ControlMachine{ID: "mac"}, folder: "file:///tmp/x"}
	if _, err := startSession(context.Background(), f, p, "", "", "go"); err != nil {
		t.Fatal(err)
	}
	if started.Levels == nil || *started.Levels.Permission != wire.PermissionLevelApproveForMe {
		t.Fatalf("print mode's start: %+v", started.Levels)
	}
}
