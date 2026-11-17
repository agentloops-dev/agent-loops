package agentsd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func writeEnv(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agents", "env"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The login environment, then the repo's file, then yours: yours wins, and a refused name is
// given by neither.
func TestYoursWinsOverTheRepos(t *testing.T) {
	t.Parallel()

	root, home, folder := t.TempDir(), t.TempDir(), t.TempDir()
	personalHomeFor(t, home)
	d, _ := start(t, root)
	writeEnv(t, folder, "REGION=repo\nREPO_ONLY=1\nNODE_OPTIONS=--require ./x.js\nPATH=/evil\n")
	writeEnv(t, home, "# Mine\nREGION=yours\nNODE_OPTIONS=--max-old-space-size=4096\nANTHROPIC_API_KEY=x\n")
	env := d.withPersonEnv([]string{"REGION=login", "PATH=/usr/bin", "KEEP=1"}, folder)
	for name, want := range map[string]string{"REGION": "yours", "REPO_ONLY": "1", "PATH": "/usr/bin", "KEEP": "1",
		"NODE_OPTIONS": "--max-old-space-size=4096"} {
		if got, _ := envValue(env, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, ok := envValue(env, "ANTHROPIC_API_KEY"); ok {
		t.Error("a runtime's account variable is refused in your file too")
	}

	r := d.envResolve(folder)
	got := map[string]wire.EnvResolvedVariable{}
	for _, v := range r.Variables {
		got[v.Name] = v
	}
	if v := got["REGION"]; v.Layer == nil || *v.Layer != wire.EnvLayerYours || !slices.Contains(v.Over, wire.EnvLayerRepo) {
		t.Errorf("REGION: %+v", v)
	}
	if v := got["PATH"]; v.Layer != nil || v.State != wire.EnvVariableStateRefused || v.Reason == nil {
		t.Errorf("PATH: %+v", v)
	}
}

func TestRepoFileRefusesNamesThatRunCode(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		repo bool
		want wire.EnvVariableState
	}{
		{"NODE_OPTIONS", true, wire.EnvVariableStateRefused},
		{"NODE_OPTIONS", false, wire.EnvVariableStateGiven},
		{"GIT_SSH_COMMAND", true, wire.EnvVariableStateRefused},
		{"DYLD_INSERT_LIBRARIES", true, wire.EnvVariableStateRefused},
		{"HOME", false, wire.EnvVariableStateRefused},
		{"AGENTS_ROOT", false, wire.EnvVariableStateRefused},
		{"CLAUDE_CODE_USE_BEDROCK", false, wire.EnvVariableStateRefused},
		{"AWS_PROFILE", false, wire.EnvVariableStateRefused},
		{"TMPDIR", false, wire.EnvVariableStateRefused},
		{"REGION", true, wire.EnvVariableStateGiven},
	} {
		if got, _ := envState(c.name, "v", c.repo, nil); got != c.want {
			t.Errorf("%s (repo %v): %s, want %s", c.name, c.repo, got, c.want)
		}
	}
	if got, _ := envState("DEPLOY_TOKEN", "abc", true, nil); got != wire.EnvVariableStateSecretLooking {
		t.Errorf("a secret's name: %s", got)
	}
	if got, _ := envState("BIG", strings.Repeat("x", 4<<10+1), false, nil); got != wire.EnvVariableStateRefused {
		t.Errorf("a value over 4 KB: %s", got)
	}
	policy := &wire.OrgPolicy{Environment: &wire.PolicyEnvironment{PassThrough: []string{"REGION"}}}
	if got, _ := envState("OTHER", "v", false, policy); got != wire.EnvVariableStatePolicyBlocked {
		t.Errorf("a name the organisation's policy does not allow: %s", got)
	}
	if got, _ := envState("REGION", "v", false, policy); got != wire.EnvVariableStateGiven {
		t.Errorf("a name the policy allows: %s", got)
	}
}

func TestEnvWriteKeepsTheFileAndRefusesSecrets(t *testing.T) {
	t.Parallel()

	root, home, folder := t.TempDir(), t.TempDir(), t.TempDir()
	personalHomeFor(t, home)
	_, c := start(t, root)
	c.call(t, 1, "folders/add", map[string]any{"folder": "file://" + folder})
	writeEnv(t, folder, "# Top\n\n# Where we run\nREGION=eu\n")

	c.call(t, 2, "env/write", map[string]any{"folder": folder, "name": "REGION", "value": "us"})
	c.call(t, 3, "env/write", map[string]any{"folder": folder, "name": "LOG_LEVEL", "value": "debug", "description": "How much to say"})
	b, _ := os.ReadFile(filepath.Join(folder, ".agents", "env"))
	if string(b) != "# Top\n\n# Where we run\nREGION=us\n\n# How much to say\nLOG_LEVEL=debug\n" {
		t.Fatalf("%q", b)
	}
	if e := c.callErr(t, 4, "env/write", map[string]any{"folder": folder, "name": "API_TOKEN", "value": "abc"}); !strings.Contains(e, "secret") {
		t.Error(e)
	}
	if e := c.callErr(t, 5, "env/write", map[string]any{"folder": folder, "name": "NODE_OPTIONS", "value": "x"}); !strings.Contains(e, "NODE_OPTIONS") {
		t.Error(e)
	}
	if e := c.callErr(t, 6, "env/write", map[string]any{"folder": t.TempDir(), "name": "A", "value": "x"}); !strings.Contains(e, "no such folder") {
		t.Error(e)
	}
	c.call(t, 7, "env/write", map[string]any{"folder": folder, "name": "REGION"})

	var list wire.EnvList
	if err := json.Unmarshal(c.call(t, 8, "env/list", map[string]any{}), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Files) != 2 || list.Files[0].Layer != wire.EnvLayerYours || list.Files[0].Exists || list.Files[1].Layer != wire.EnvLayerRepo {
		t.Fatalf("%+v", list.Files)
	}
	repo := list.Files[1]
	if len(repo.Variables) != 1 || repo.Variables[0].Name != "LOG_LEVEL" || repo.Variables[0].Description == nil ||
		*repo.Variables[0].Description != "How much to say" || repo.Variables[0].Line != 4 {
		t.Fatalf("%+v", repo.Variables)
	}
}

// The environment part writes ~/.agents/env; an edit by hand there is kept and marked, until
// Discard writes the part's text again.
func TestEnvironmentPartAndAnEditByHand(t *testing.T) {
	t.Parallel()

	root, home := t.TempDir(), t.TempDir()
	personalHomeFor(t, home)
	d, c := start(t, root)
	send := func(text string) {
		d.Told(wire.NotifyMachineUserSettings.Name, mustJSON(wire.MachineUserSettings{User: "sam",
			Settings: wire.UserSettings{Environment: mustJSON(wire.UserEnvironment{Text: text})}}))
	}
	path := filepath.Join(home, ".agents", "env")
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	byHand := func() bool {
		var list wire.EnvList
		if err := json.Unmarshal(c.call(t, 1, "env/list", map[string]any{}), &list); err != nil {
			t.Fatal(err)
		}
		return list.Files[0].ChangedByHand != nil && *list.Files[0].ChangedByHand
	}
	send("REGION=eu\n")
	if read() != "REGION=eu\n" || byHand() {
		t.Fatalf("written: %q", read())
	}
	send("REGION=us\n")
	if read() != "REGION=us\n" {
		t.Fatalf("a new part is written: %q", read())
	}
	if err := os.WriteFile(path, []byte("REGION=mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	send("REGION=ca\n")
	if read() != "REGION=mine\n" || !byHand() {
		t.Fatalf("an edit by hand is kept and marked: %q", read())
	}
	c.call(t, 2, "env/discard", map[string]any{})
	if read() != "REGION=ca\n" || byHand() {
		t.Fatalf("Discard writes the part again: %q", read())
	}
}

// A person's own file from before the part came is not written over.
func TestAPersonsOwnFileIsKeptOnTheFirstPart(t *testing.T) {
	t.Parallel()

	root, home := t.TempDir(), t.TempDir()
	personalHomeFor(t, home)
	d, _ := start(t, root)
	writeEnv(t, home, "OWN=1\n")
	d.Told(wire.NotifyMachineUserSettings.Name, mustJSON(wire.MachineUserSettings{User: "sam",
		Settings: wire.UserSettings{Environment: mustJSON(wire.UserEnvironment{Text: "OTHER=2\n"})}}))
	if b, _ := os.ReadFile(filepath.Join(home, ".agents", "env")); string(b) != "OWN=1\n" || !d.yoursEnvChangedByHand() {
		t.Fatalf("%q", b)
	}
}
