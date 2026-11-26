package agentsd

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/internal/workspacecommands"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Root set-up by hand and again (#7159; spec of #7151, section 1): the command a person runs as
// root where nothing can run root.sh for them, the log of the last run, and Root set-up read
// again, by Try Again or Check.

// rootSetupFolder is where root.sh, its log and its result are on this machine; a test's own.
var rootSetupFolder = hostsetup.RootFolder

func (d *Daemon) registerRootSetup() {
	On(d, wire.CallSetupRootCommand, func(context.Context, *Client, wire.Empty) (wire.SetupRootCommand, error) {
		return d.rootCommand()
	})
	On(d, wire.CallSetupRootLog, func(context.Context, *Client, wire.Empty) (wire.SetupRootLog, error) {
		text, cut := hostsetup.RootLog(rootSetupFolder, hostsetup.RootLogMost)
		return wire.SetupRootLog{Text: text, Cut: cut}, nil
	})
	On(d, wire.CallSetupRootAgain, func(_ context.Context, _ *Client, p wire.SetupRootAgain) (wire.Empty, error) {
		return wire.Empty{}, d.rootAgain(p.Rest)
	})
}

// rootCommand is setup/rootCommand: this machine's root.sh, as a client approved it for made
// machines, in the one line a person runs as root on another machine.
func (d *Daemon) rootCommand() (wire.SetupRootCommand, error) {
	file, most, _ := setupScriptFile("root")
	if f := d.readSetupScript(file, most); !f.present {
		return wire.SetupRootCommand{}, Invalid("There is no root.sh in ~/.agents/%s on %s.", setupScriptsFolder, d.ownName())
	}
	text, _, err := d.approvedSetupScript("root", machinelock.ScopeMade)
	if err != nil {
		return wire.SetupRootCommand{}, err
	}
	return wire.SetupRootCommand{Command: workspacecommands.RootCommand([]byte(text)), Hash: scriptHash(text)}, nil
}

// rootAgain is setup/rootAgain: Root set-up read again in the background, and with rest the steps
// after it, as the installer runs them. One run of set-up steps at a time here.
func (d *Daemon) rootAgain(rest bool) error {
	if _, err := os.Stat(filepath.Join(rootSetupFolder, "root.sh")); err != nil {
		return Invalid("root.sh has not run on %s: run the copied root command there first.", d.ownName())
	}
	if d.setupRuns.Swap(true) {
		return Invalid("Set-up runs on %s already.", d.ownName())
	}
	d.goJob("root set-up again", func() {
		defer d.setupRuns.Store(false)
		deps := hostsetup.Deps{Call: d.callOwn, RootFolder: rootSetupFolder}
		var err error
		if rest {
			home, herr := os.UserHomeDir()
			if herr != nil {
				log.Printf("set-up: no home folder: %v", herr)
				return
			}
			o := hostsetup.Options{Name: d.ownName(), Home: home, Smoke: true}
			err = hostsetup.Continue(d.hostCtx(), o, deps, d.Store.Loc.Root, wire.MachineSetupStepIDRoot)
		} else {
			err = hostsetup.RootAgain(d.hostCtx(), deps, d.Store.Loc.Root)
		}
		if err != nil {
			log.Printf("set-up: root set-up again: %v", err)
		}
	})
	return nil
}
