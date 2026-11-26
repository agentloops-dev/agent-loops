package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Root set-up (#7154; spec of #7151, sections 1 and 5): the person's root.sh, which the first
// boot of a machine that a provider made runs as root, beside this installer. The first boot
// writes it to RootFolder, runs it with a limit of 10 minutes, and writes root.log, then
// root.exit; root's to write, anyone's to read. The installer only reads them: it reports the
// Root set-up step after Compare digits, and steps 5 and 6 wait for it, because the first boot
// restarts the user's manager when root.sh gave the user a group, and that restart would end a
// runtime's install.

const (
	// RootFolder is where the first boot keeps root.sh and its result
	// (workspacecommands.RootSetupFolder).
	RootFolder = "/var/lib/agents-setup"
	// RootReadyFile, in the host's root, says that the Root set-up step has begun
	// (workspacecommands.RootReadyFile): from then a restart of the user's manager breaks no step.
	RootReadyFile = "setup-root-ready"
	// RootWait is how long the step waits for root.exit after root.sh was written: the limit of
	// 10 minutes, its 10 s to be killed, and the restart. A root.sh that restarts the machine
	// has no result, and the step fails after it (not supported).
	RootWait = 12 * time.Minute
	// rootLogMost is the end of root.log that the step reads; rootLines is how many of its
	// last lines a failure shows.
	rootLogMost = 1 << 20
	rootLines   = 5
)

// rootResult is root.sh's exit code, when root.exit has one.
func rootResult(folder string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(folder, "root.exit"))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return code, true
}

// rootLogTail is the last lines of root.log, read from its last rootLogMost bytes.
func rootLogTail(folder string, n int) []string {
	f, err := os.Open(filepath.Join(folder, "root.log"))
	if err != nil {
		return nil
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > rootLogMost {
		_, _ = f.Seek(info.Size()-rootLogMost, io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(f, rootLogMost))
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// rootFailure is why root.sh failed, in words, by its exit code: timeout's 124, or 137 when it
// was killed after the limit.
func rootFailure(code int) string {
	if code == 124 || code == 137 {
		return "root.sh passed its 10 minutes and was stopped"
	}
	return fmt.Sprintf("root.sh ended with exit %d", code)
}

// errRootFailed is a Root set-up that failed: steps 5 and 6 do not start.
var errRootFailed = errors.New("root set-up failed")

// rootSetup is the Root set-up step. With no root.sh in folder it says nothing: no step. It
// makes RootReadyFile, waits for root.exit (its last line of root.log as it runs), then for the
// host to be running again after a restart of the user's manager, and says how it ended.
func rootSetup(ctx context.Context, d Deps, l Layout, rep *Reporter) error {
	folder := d.RootFolder
	script, err := os.Stat(filepath.Join(folder, "root.sh"))
	if err != nil {
		return nil
	}
	step := wire.MachineSetupStepIDRoot
	rep.Say(step, wire.MachineSetupStateRunning, "Running root.sh", "")
	// store-bounds: not a store: one fixed empty file, the mark that the step began
	_ = os.WriteFile(filepath.Join(l.Root, RootReadyFile), nil, 0o600)
	deadline := script.ModTime().Add(d.RootWait)
	code, ok := rootResult(folder)
	for !ok {
		if time.Now().After(deadline) {
			err := fmt.Sprintf("root.sh has no result after %s: a root.sh that restarts the machine is not supported", shortDuration(d.RootWait))
			rep.Say(step, wire.MachineSetupStateFailed, strings.Join(rootLogTail(folder, rootLines), "\n"), err)
			return errRootFailed
		}
		if last := rootLogTail(folder, 1); len(last) == 1 {
			rep.Say(step, wire.MachineSetupStateRunning, last[0], "")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Poll):
		}
		code, ok = rootResult(folder)
	}
	// The first boot restarts the user's manager before it writes root.exit, when root.sh gave
	// the user a group: the host comes back by itself.
	back := time.Now().Add(d.JoinWait)
	for !d.Running(l.Root) && time.Now().Before(back) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Poll):
		}
	}
	if code != 0 {
		rep.Say(step, wire.MachineSetupStateFailed, strings.Join(rootLogTail(folder, rootLines), "\n"), rootFailure(code))
		return errRootFailed
	}
	rep.Say(step, wire.MachineSetupStateDone, "root.sh · exit 0", "")
	return nil
}

// RootLogMost is the most of root.log that Show Log reads (#7159): its end.
const RootLogMost = 64 << 10

// RootLog is the end of root.log in folder, at most most bytes, and whether it is longer (#7159):
// what Show Log shows. Empty when root.sh never ran here.
func RootLog(folder string, most int64) (string, bool) {
	f, err := os.Open(filepath.Join(folder, "root.log"))
	if err != nil {
		return "", false
	}
	defer f.Close()
	cut := false
	if info, err := f.Stat(); err == nil && info.Size() > most {
		_, _ = f.Seek(info.Size()-most, io.SeekStart)
		cut = true
	}
	data, _ := io.ReadAll(io.LimitReader(f, most))
	if cut {
		// From the first whole line.
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), cut
}

// RootAgain is the Root set-up step alone, read again inside the host (#7159): Check, after the
// person ran the copied root command on a machine whose other steps are done. root is the host's
// root. With no root.sh in d.RootFolder it says nothing.
func RootAgain(ctx context.Context, d Deps, root string) error {
	d.defaults()
	if d.Running == nil {
		d.Running = func(string) bool { return true }
	}
	return rootSetup(ctx, d, Layout{Root: root}, moreReports(ctx, root))
}
