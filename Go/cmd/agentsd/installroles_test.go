package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlsetup"
	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
)

func TestInstallRefusesTheOtherRole(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name                                string
		joined, unit, control, none, refuse bool
	}{
		{name: "fresh host"},
		{name: "fresh control only", control: true, none: true},
		{name: "fresh both", control: true},
		{name: "control only beside a host", joined: true, control: true, none: true, refuse: true},
		{name: "a host beside the control plane", unit: true, refuse: true},
		{name: "a set-up run again", joined: true, unit: true},
		{name: "both run again", joined: true, unit: true, control: true},
		{name: "control only run again", unit: true, control: true, none: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			touch := func(p string, yes bool) {
				if !yes {
					return
				}
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			touch(filepath.Join(hostsetup.LayoutOf(home).Root, "control-host.json"), c.joined)
			touch(controlsetup.LayoutOf(home).Unit, c.unit)
			if got := refuseInstall(home, c.control, c.none); (got != "") != c.refuse {
				t.Fatalf("refused: %q", got)
			}
		})
	}
}
