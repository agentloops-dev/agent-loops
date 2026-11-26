package agentsd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/workspacecommands"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7159: the copied root command is the root.sh a client approved for made machines, and none
// while it is not.
func TestTheRootCommandIsOnlyTheApprovedRootScript(t *testing.T) {
	t.Parallel()
	d, phone, _, state, _ := senderWith(t, "echo setup\n")
	if _, err := d.rootCommand(); err == nil || !strings.Contains(err.Error(), "There is no root.sh") {
		t.Fatalf("with no root.sh: %v", err)
	}
	const text = "#!/bin/bash\napt-get install -y gh\n"
	root := filepath.Join(d.PersonalHome, ".agents", "machine-setup", "root.sh")
	must(t, os.WriteFile(root, []byte(text), 0o600))
	if _, err := d.rootCommand(); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a root.sh no client approved: %v", err)
	}
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "root", Approval: approvalOf(t, phone, state, "root", "made", 1, text)}); err != nil {
		t.Fatal(err)
	}
	got, err := d.rootCommand()
	if err != nil || got.Command != workspacecommands.RootCommand([]byte(text)) || got.Hash != scriptHash(text) {
		t.Fatalf("%+v %v", got, err)
	}
	must(t, os.WriteFile(root, []byte(text+"apt-get install -y jq\n"), 0o600))
	if _, err := d.rootCommand(); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a changed root.sh: %v", err)
	}
}

// Not parallel: it sets where root.sh's result is.
func TestRootSetUpAgainNeedsARootScriptHereAndTheLogIsRead(t *testing.T) {
	d, _, _ := joinedMachine(t)
	t.Cleanup(d.Shutdown)
	was := rootSetupFolder
	rootSetupFolder = t.TempDir()
	t.Cleanup(func() { rootSetupFolder = was })
	if err := d.rootAgain(false); err == nil || !strings.Contains(err.Error(), "run the copied root command there first") {
		t.Fatalf("%v", err)
	}
	var got wire.SetupRootLog
	rootLogOf(t, d, &got)
	if got.Text != "" || got.Cut {
		t.Errorf("%+v", got)
	}
	must(t, os.WriteFile(filepath.Join(rootSetupFolder, "root.log"), []byte("Setting up gh\n"), 0o600))
	rootLogOf(t, d, &got)
	if got.Text != "Setting up gh\n" {
		t.Errorf("%+v", got)
	}
}

func rootLogOf(t *testing.T, d *Daemon, got *wire.SetupRootLog) {
	t.Helper()
	raw, err := d.callOwn(wire.CallSetupRootLog.Name, wire.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	*got = wire.SetupRootLog{}
	must(t, json.Unmarshal(raw, got))
}
