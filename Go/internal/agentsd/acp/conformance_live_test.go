//go:build live

package acp

// The conformance checks that need no model, run against each adapter installed on this
// machine (#6186): `Go/scripts/go.sh test -tags live -run Live -v ./internal/agentsd/acp/`.
// The runtime check (assess-runtime) runs it after an adapter update.
//
// An adapter is found by AGENTS_LIVE_<NAME> (a command line, e.g. "node /x/claude-agent-acp.js"),
// else the app's own copy (a Mac's ~/Library/Caches/com.alexecollins.agents/tools), else its name on PATH: CLAUDE claude-agent-acp, CODEX codex-acp, COPILOT copilot,
// OPENCODE opencode, CURSOR cursor-agent, GROK grok. One not found is skipped, and says so.
// No test sends a prompt that costs a model call except the one that kills the adapter, which
// is killed before it can answer.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

var liveAdapters = []struct {
	name, executable string
	args             []string
	// toolset is the app's own copy: <app cache>/tools/<toolset>/current/bin/<shim>.
	toolset, shim string
}{
	{"CLAUDE", "claude-agent-acp", nil, "claude", "npx"},
	{"CODEX", "codex-acp", nil, "codex", "codex-acp"},
	{"COPILOT", "copilot", []string{"--acp"}, "", ""},
	{"OPENCODE", "opencode", []string{"acp"}, "", ""},
	{"CURSOR", "cursor-agent", []string{"acp"}, "", ""},
	{"GROK", "grok", []string{"agent", "stdio"}, "", ""},
}

func eachAdapter(t *testing.T, f func(t *testing.T, l Launch)) {
	found := false
	for _, a := range liveAdapters {
		a := a
		argv := strings.Fields(os.Getenv("AGENTS_LIVE_" + a.name))
		if len(argv) == 0 && a.toolset != "" {
			home, _ := os.UserHomeDir()
			shim := filepath.Join(home, "Library", "Caches", "com.alexecollins.agents", "tools", a.toolset, "current", "bin", a.shim)
			if _, err := os.Stat(shim); err == nil {
				argv = []string{shim}
			}
		}
		if len(argv) == 0 {
			path, err := exec.LookPath(a.executable)
			if err != nil {
				t.Logf("%s: not installed (%s not on PATH), skipped", a.name, a.executable)
				continue
			}
			argv = append([]string{path}, a.args...)
		}
		found = true
		t.Run(a.name, func(t *testing.T) {
			f(t, Launch{Command: argv[0], Args: argv[1:], Env: os.Environ(), Dir: t.TempDir(), Name: a.name})
		})
	}
	if !found {
		t.Skip("no adapter installed")
	}
}

// The adapter answers initialize and session/new, says what it can do, and Stop ends its process.
func TestLiveHandshakeAndNewSession(t *testing.T) {
	eachAdapter(t, func(t *testing.T, l Launch) {
		s, err := Start(context.Background(), l, &recorder{}, nil, nil, nil, "", "")
		if err != nil {
			t.Fatal(err)
		}
		pid := s.Pid()
		t.Logf("protocol %d, load %v, list %v, delete %v, steering %v, options %d", s.Init.ProtocolVersion,
			s.CanLoad(), s.CanList(), s.CanDelete(), s.SupportsSteering(), len(s.NewResult.ConfigOptions))
		if s.ID == "" {
			t.Fatal("no session id")
		}
		finishesIn(t, 15*time.Second, "Stop", s.Stop)
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			// The group is gone with TERM/KILL; a zombie answers 0 until reaped, and Wait reaped it.
			t.Errorf("pid %d still runs after Stop", pid)
		}
	})
}

// A conversation the adapter never had: the session begins again and is Lost, or the start
// says why; it never resumes another one or hangs.
func TestLiveResumeOfAnUnknownConversation(t *testing.T) {
	eachAdapter(t, func(t *testing.T, l Launch) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s, err := Start(ctx, l, &recorder{}, nil, nil, nil, "00000000-0000-4000-8000-000000000000", "")
		if err != nil {
			t.Logf("refused: %v", err)
			return
		}
		defer s.Stop()
		if s.Continued {
			t.Errorf("resumed a conversation that never existed, as %q", s.ID)
		}
		if !s.Lost || s.ID == "" {
			t.Errorf("lost %v, id %q", s.Lost, s.ID)
		}
	})
}

// The adapter is killed during a turn: the turn ends with the exit named, within 15 s.
func TestLiveKilledDuringATurn(t *testing.T) {
	eachAdapter(t, func(t *testing.T, l Launch) {
		s, err := Start(context.Background(), l, &recorder{}, nil, nil, nil, "", "")
		if err != nil {
			t.Fatal(err)
		}
		defer s.Stop()
		if s.Pid() == 0 {
			t.Skip("no process")
		}
		go func() { time.Sleep(200 * time.Millisecond); _ = syscall.Kill(-s.Pid(), syscall.SIGKILL) }()
		finishesIn(t, 15*time.Second, "a turn of a killed adapter", func() {
			_, err := s.Prompt(context.Background(), []sdk.ContentBlock{sdk.TextBlock("Say hi.")})
			if err == nil || !strings.Contains(err.Error(), "the runtime exited") {
				t.Errorf("error %v", err)
			}
		})
	})
}
