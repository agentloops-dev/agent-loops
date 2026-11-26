package workspacecommands

import (
	"encoding/base64"
	"strconv"
)

// The limits of root set-up (#7154; spec of #7151, sections 1 and 5).
const (
	// RootScriptMost is the most root.sh can be, in bytes.
	RootScriptMost = 10 * 1024
	// RootBootMost is the most a workspace_create's rootBoot can be, in characters: root.sh
	// in base64 and the lines around it.
	RootBootMost = 16 * 1024
	// RootSetupSeconds is root.sh's time limit; it is killed 10 s after it.
	RootSetupSeconds = 600
	// RootSetupFolder is where the first boot keeps root.sh, its log and its exit code:
	// root's to write, anyone's to read.
	RootSetupFolder = "/var/lib/agents-setup"
)

// RootReadyFile is the file, in the host's root, that the installer makes when its Root set-up
// step begins: from then, a restart of the user's manager breaks no step.
const RootReadyFile = "setup-root-ready"

// RootBoot is the first-boot lines that run root.sh (#7154): they write it from base64 into
// RootSetupFolder, and start it as root in the background, with no stdin and the time limit,
// writing root.log. Then they run agents_root_done when the first boot has one (a preset's
// restart of the user's manager when root.sh gave the user a group), and write root.exit last,
// so the installer sees the result once that is done. The first boot waits for it after the
// install. An empty script is no lines.
func RootBoot(script []byte) string {
	if len(script) == 0 {
		return ""
	}
	folder := RootSetupFolder
	return "install -d -m 755 " + folder + "\n" +
		"printf %s '" + base64.StdEncoding.EncodeToString(script) + "' | base64 -d > " + folder + "/root.sh\n" +
		"chmod 644 " + folder + "/root.sh\n" +
		"( c=0; timeout -k 10 " + strconv.Itoa(RootSetupSeconds) + " bash -e " + folder + "/root.sh >" + folder + "/root.log 2>&1 </dev/null || c=$?\n" +
		"  chmod 644 " + folder + "/root.log; ! command -v agents_root_done >/dev/null 2>&1 || agents_root_done\n" +
		"  echo $c >" + folder + "/root.exit.new && chmod 644 " + folder + "/root.exit.new && mv " + folder + "/root.exit.new " + folder + "/root.exit ) &"
}
