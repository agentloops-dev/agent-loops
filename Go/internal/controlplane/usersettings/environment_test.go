package usersettings

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func envPart(text string) json.RawMessage {
	b, _ := json.Marshal(wire.UserEnvironment{Text: text})
	return b
}

func TestEnvironmentPartRefusesSecrets(t *testing.T) {
	if err := checkEnvironment(envPart("# Where we run\nREGION=eu\n")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []json.RawMessage{envPart("GITHUB_TOKEN=abc\n"), envPart("X=ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"),
		json.RawMessage(`{"text":"A=1","other":1}`)} {
		if err := checkEnvironment(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestEnvironmentChangesNamesOnly(t *testing.T) {
	got := EnvironmentChanges(envPart("A=1\nB=2\nC=3\n"), envPart("# now described\nA=1\nB=9\nD=4\n"))
	if !slices.Equal(got, []string{"A", "B", "C", "D"}) {
		t.Fatal(got)
	}
	if got := EnvironmentChanges(nil, envPart("A=1\n")); !slices.Equal(got, []string{"A"}) {
		t.Fatal(got)
	}
}
