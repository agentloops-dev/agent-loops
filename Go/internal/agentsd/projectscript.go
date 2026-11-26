package agentsd

import (
	"log"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
)

// A project's .agents/setup.sh runs when the project is sent to this machine, and when a send
// changes it (#7158), as the installer runs it for each --project. It runs after the send has
// answered, in the background, with no credentials (#5353), one run at a time on this machine.
// The same script that last ran well in this checkout does not run again; one that failed
// runs again at the next send. A failure is this folder's only: it is in its log, and the
// machine stays ready for other work.

// projectScriptTimeout bounds one run, as the installer's step does. A test sets it shorter.
var projectScriptTimeout = hostsetup.ScriptTimeout

// afterReceive starts folder's set-up script when it is not the one that last ran well there,
// and says whether it did.
func (d *Daemon) afterReceive(folder string) bool {
	if hash, ok := hostsetup.ScriptHash(folder); !ok || hash == hostsetup.LastRan(folder) {
		return false
	}
	d.goJob("project set-up script", func() { d.runProjectScript(folder) })
	return true
}

// runProjectScript runs folder's script, when it changed, and says how it went in the log.
func (d *Daemon) runProjectScript(folder string) {
	output, closeLog := hostsetup.OpenScriptLog(filepath.Join(d.Store.Loc.Root, "logs"), folder)
	defer closeLog()
	ran, err := hostsetup.RunScriptIfChanged(d.hostCtx(), folder, d.ownName(), false, projectScriptTimeout, output, nil)
	switch {
	case err != nil:
		log.Printf("set-up: %s in %s: %v", hostsetup.SetupScript, shortPath(folder), err)
	case ran:
		log.Printf("set-up: %s in %s ran", hostsetup.SetupScript, shortPath(folder))
	}
}
