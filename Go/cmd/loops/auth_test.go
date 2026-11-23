package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAuthLoginNeedsACode(t *testing.T) {
	err := run(t.Context(), []string{"auth", "login"}, &bytes.Buffer{})
	if codeOf(err) != exitError || !strings.Contains(err.Error(), "loops auth login '<code>'") {
		t.Fatalf("no code: %v", err)
	}
	var say bytes.Buffer
	if err := authLogin(t.Context(), &say, "not a code"); codeOf(err) != exitError || !strings.Contains(err.Error(), "That is not a pairing code") {
		t.Fatalf("a bad code: %v", err)
	}
}

func TestAuthCommandsNeedPairing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"auth", "status"}, {"auth", "logout"}} {
		err := run(t.Context(), args, &bytes.Buffer{})
		if codeOf(err) != exitNotPair {
			t.Errorf("%v: %v, want exit 5", args, err)
		}
	}
}

// The table is the wireframe's, with the profile first (stage 5).
func TestAuthStatusTable(t *testing.T) {
	var out bytes.Buffer
	printStatus(&out, "control.agentloops.dev", "alex-terminal", "alex-mac", "~/Agents")
	want := "PROFILE   CONTROL PLANE               CLIENT          MACHINE    FOLDER\n" +
		"default   control.agentloops.dev      alex-terminal   alex-mac   ~/Agents\n"
	if out.String() != want {
		t.Fatalf("status:\n%s\nwant:\n%s", out.String(), want)
	}
}
