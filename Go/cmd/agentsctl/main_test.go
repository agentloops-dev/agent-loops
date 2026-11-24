package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
)

// unpair deletes the pairing, also when the control plane does not answer (#6808).
func TestUnpairDeletesThePairing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ctl.json")
	t.Setenv("AGENTSCTL", file)
	var say bytes.Buffer
	if err := unpair(t.Context(), &say); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("with no pairing: %v", err)
	}
	b, _ := json.Marshal(controllink.ClientMembership{Client: "A", Private: controllink.NewPrivateKey(), URL: "ws://127.0.0.1:1"})
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unpair(t.Context(), &say); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the pairing is still here: %v", err)
	}
	if !strings.Contains(say.String(), "unpaired here") {
		t.Fatalf("said %q", say.String())
	}
}
