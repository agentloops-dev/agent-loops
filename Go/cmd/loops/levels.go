package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// startLevelsKey is the preference that keeps the model and permission levels last chosen on
// any client's start form (#3798), kept on the person's Mac's host (#1033).
const startLevelsKey = "startLevels"

// macHost is the id of the person's Mac's host, which keeps the display choices (#1501).
const macHost = "mac"

// modeWords are --mode's words for each permission level, the words the daemon also reads
// (autopick's permissionLevelWords), with the wire's own names too.
var modeWords = map[string]wire.PermissionLevel{
	"plan": wire.PermissionLevelPlan, "ask": wire.PermissionLevelAsk,
	"approve-for-me": wire.PermissionLevelApproveForMe, "bypass-permissions": wire.PermissionLevelBypassPermissions,
	string(wire.PermissionLevelApproveForMe):      wire.PermissionLevelApproveForMe,
	string(wire.PermissionLevelBypassPermissions): wire.PermissionLevelBypassPermissions,
}

// checkMode refuses a --mode that is not a permission level.
func checkMode(mode string) error {
	if mode == "" {
		return nil
	}
	if _, ok := modeWords[mode]; !ok {
		return &exit{code: exitError, msg: fmt.Sprintf("--mode is plan, ask, approve-for-me or bypass-permissions, not %q", mode)}
	}
	return nil
}

// startLevels is the levels a new session starts at (#6651): the ones kept on the other
// clients' start forms (#3798), read from the Mac's host, else from the session's own
// machine, with --mode over the permission. Nil when there is nothing to send, so the
// daemon's own default holds.
func startLevels(ctx context.Context, c caller, machine, mode string) *wire.Levels {
	var out wire.Levels
	hosts := []string{macHost}
	if machine != macHost {
		hosts = append(hosts, machine)
	}
	for _, h := range hosts {
		if l, ok := keptLevels(ctx, c, h); ok {
			out = l
			break
		}
	}
	if p, ok := modeWords[mode]; ok {
		out.Permission = &p
	}
	if out.Model == nil && out.Permission == nil {
		return nil
	}
	return &out
}

// keptLevels reads startLevels from one host's preferences. ok is false when the host does
// not answer or keeps no levels; a level it does not know is left out.
func keptLevels(ctx context.Context, c caller, host string) (wire.Levels, bool) {
	r, err := c.Call(ctx, host, "preferences/get", wire.Empty{})
	if err != nil {
		return wire.Levels{}, false
	}
	var prefs map[string]json.RawMessage
	if json.Unmarshal(r, &prefs) != nil {
		return wire.Levels{}, false
	}
	var kept struct {
		Model      *string `json:"model"`
		Permission *string `json:"permission"`
	}
	raw, ok := prefs[startLevelsKey]
	if !ok || json.Unmarshal(raw, &kept) != nil {
		return wire.Levels{}, false
	}
	var out wire.Levels
	models := []wire.ModelLevel{wire.ModelLevelFast, wire.ModelLevelBalanced, wire.ModelLevelSmart}
	if kept.Model != nil && slices.Contains(models, wire.ModelLevel(*kept.Model)) {
		m := wire.ModelLevel(*kept.Model)
		out.Model = &m
	}
	permissions := []wire.PermissionLevel{wire.PermissionLevelPlan, wire.PermissionLevelAsk, wire.PermissionLevelApproveForMe, wire.PermissionLevelBypassPermissions}
	if kept.Permission != nil && slices.Contains(permissions, wire.PermissionLevel(*kept.Permission)) {
		p := wire.PermissionLevel(*kept.Permission)
		out.Permission = &p
	}
	return out, out.Model != nil || out.Permission != nil
}
