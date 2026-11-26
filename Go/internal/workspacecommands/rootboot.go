package workspacecommands

import (
	"encoding/base64"
	"errors"
	"os"
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

// RootCommand is the one line a person runs as root, or with sudo, on a machine that cannot run
// root.sh by itself (#7159): their own server, or a made machine whose root.sh failed. It is
// RootBoot in base64, after it takes away the last result, so that Root set-up reads the new one,
// and it waits for root.sh to end. The script is in base64, so the line pastes as it is.
func RootCommand(script []byte) string {
	if len(script) == 0 {
		return ""
	}
	lines := "rm -f " + RootSetupFolder + "/root.exit\n" + RootBoot(script) + "\nwait\n" +
		"echo \"root.sh ended with exit $(cat " + RootSetupFolder + "/root.exit). Now press Check in Agent Loops.\"\n"
	return "printf %s " + base64.StdEncoding.EncodeToString([]byte(lines)) + " | base64 -d | sudo sh"
}

// rootBootFile is where a command's ${rootBoot} is (#7159): a file of the lines, readable only by
// this user, because a command line can be too long for root.sh and shows in ps. None, and an
// empty value, when there is no root.sh. The caller removes it after the create.
func rootBootFile(rootBoot string) (string, error) {
	if rootBoot == "" {
		return "", nil
	}
	// store-bounds: not a store: one file of at most RootBootMost, removed after its create
	f, err := os.CreateTemp("", "agents-root-boot-*.sh")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(rootBoot + "\nwait\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(f.Name())
		return "", errors.Join(werr, cerr)
	}
	return f.Name(), nil
}

// putRootBootFile makes ${rootBoot} in a command's values the path of a file of the lines
// (#7159), not the lines, and gives what removes it after the create. A request takes the lines.
func putRootBootFile(making Making, values map[string]string) (func(), error) {
	if making.CreateRequest != nil || !making.RootBootRuns() {
		return func() {}, nil
	}
	file, err := rootBootFile(values["rootBoot"])
	if err != nil {
		return nil, Failure{Reason: "Root set-up could not be written for the create: " + err.Error()}
	}
	values["rootBoot"] = file
	return func() {
		if file != "" {
			_ = os.Remove(file)
		}
	}, nil
}
