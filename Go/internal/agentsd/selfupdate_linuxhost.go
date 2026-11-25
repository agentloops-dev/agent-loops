package agentsd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/linuxrelease"
	"github.com/agentloops-dev/agent-loops/Go/internal/releasesig"
)

// What a Linux machine's self-update does to ~/.agents-server/bin (#5788), with no build
// tag, so the tests run on a Mac too; registerSelfUpdate (selfupdate_linux.go) uses it.

// linuxArch is a Go architecture as uname -m and the release's files say it.
func linuxArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return goarch
}

// restartsItself is whether something starts this host again when it ends with
// ExitRestart: the systemd user service (install.sh's agents-host.service, Restart=always;
// systemd sets INVOCATION_ID for what it runs), or the supervisor that --detach starts,
// whose pid is in the root's supervise.lock.
func restartsItself(root string) bool {
	if os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	b, err := os.ReadFile(filepath.Join(root, "supervise.lock")) //nolint:gosec // G703: the host's own root
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid == os.Getppid()
}

// asLinux makes u the updater of a Linux host that runs from current, as bin/current.
func (u *selfUpdater) asLinux(current, arch string) {
	u.asset = func(version string) string { return linuxrelease.Tarball(arch, version) }
	u.open = func(version, work, file string) (string, error) {
		return openLinuxRelease(current, arch, version, work, file)
	}
	u.place = func(work string) error { placeLinuxServers(current, arch, work); return nil }
	u.version = func(next string) (string, error) { return linkedVersion(current, next) }
	// SHA256SUMS is checked against its signature, the tarball against SHA256SUMS, and each
	// program in it against its own .sha256, all before version runs the new host.
	u.signed = releasesig.Verify
	u.verify = func(string, string) error { return nil }
}

// openLinuxRelease unpacks a checked tarball in work and puts its agentsd at
// bin/agentsd-<sha256>, beside this one. It is a link to it, in work, that takes
// bin/current's place; the MCP servers stay in work/new until placeLinuxServers.
func openLinuxRelease(current, arch, version, work, file string) (string, error) {
	out := filepath.Join(work, "new")
	if err := os.Mkdir(out, 0o700); err != nil {
		return "", err
	}
	if err := linuxrelease.Unpack(file, out, arch, selfUpdateLimit); err != nil {
		return "", fmt.Errorf("Agent Loops could not unpack release %s: %w", version, err) //nolint:staticcheck // ST1005: the person reads it as the update's message
	}
	_ = os.Remove(file)
	host := filepath.Join(out, "agentsd-linux-"+arch)
	sha := linuxrelease.FileSum(host)
	if sha == "" {
		return "", fmt.Errorf("Release %s holds no host for %s-linux.", version, arch) //nolint:staticcheck // ST1005: the person reads it as the update's message
	}
	name := "agentsd-" + sha
	if err := os.Chmod(host, 0o700); err != nil { //nolint:gosec // G302: a program, which only this user runs
		return "", err
	}
	if err := os.Rename(host, filepath.Join(filepath.Dir(current), name)); err != nil {
		return "", fmt.Errorf("Agent Loops could not put release %s beside itself: %w", version, err) //nolint:staticcheck // ST1005: the person reads it as the update's message
	}
	next := filepath.Join(work, "current")
	if err := os.Symlink(name, next); err != nil {
		return "", err
	}
	return next, nil
}

// placeLinuxServers puts the release's MCP servers beside agentsd, each in one rename. One
// the release does not have stays as it is, as install.sh leaves it out.
func placeLinuxServers(current, arch, work string) {
	for _, program := range linuxrelease.Programs {
		if program == "agentsd" {
			continue
		}
		src := filepath.Join(work, "new", program+"-linux-"+arch)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Chmod(src, 0o700); err == nil { //nolint:gosec // G302: a program, which only this user runs
			err = os.Rename(src, filepath.Join(filepath.Dir(current), program))
			if err != nil {
				log.Printf("self-update: %s did not go in place: %v", program, err)
			}
		}
	}
}

// linkedVersion is what the agentsd a link in work names says it is (agentsd --version).
func linkedVersion(current, next string) (string, error) {
	target, err := os.Readlink(next)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, filepath.Join(filepath.Dir(current), target), "--version").Output() //nolint:gosec // G204: the agentsd this update just checked
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("it says no version")
	}
	return fields[0], nil
}

var hostBinary = regexp.MustCompile(`^agentsd-[0-9a-f]{64}$`)

// pruneOldHosts takes away the agentsd copies beside bin/current that it does not name,
// once the new copy has stayed up: the one before it is no longer needed to go back to.
func pruneOldHosts(current string) {
	target, err := os.Readlink(current)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(filepath.Dir(current))
	if err != nil {
		return
	}
	for _, e := range entries {
		if hostBinary.MatchString(e.Name()) && e.Name() != filepath.Base(target) {
			_ = os.Remove(filepath.Join(filepath.Dir(current), e.Name())) //nolint:gosec // G703: a name read from our own bin folder
		}
	}
}
