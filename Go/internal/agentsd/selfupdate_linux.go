package agentsd

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/buildinfo"
	"github.com/agentloops-dev/agent-loops/Go/internal/linuxrelease"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// registerSelfUpdate serves selfUpdate/status and selfUpdate/set on a Linux machine whose
// host runs from ~/.agents-server/bin/current (#5788, epic #5785): install.sh, an install
// over SSH and `agentsd install` put it so. A test's host serves neither, nor a host run
// from anywhere else: nothing would start the new copy.
func (d *Daemon) registerSelfUpdate() {
	if testing.Testing() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	current := filepath.Join(filepath.Dir(exe), "current")
	if fi, err := os.Lstat(current); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return
	}
	file := filepath.Join(d.Store.Loc.Root, selfUpdateFile)
	keep, back := settleSelfUpdates(current, buildinfo.Version, file, exchangePaths)
	if back {
		// Nothing runs yet: the service or the supervisor starts the old copy at once.
		log.Printf("self-update: the old copy is back in bin/current; the host starts again to run it")
		os.Exit(ExitRestart)
	}
	if len(keep) > 0 {
		d.goJob("self-update-settle", func() {
			select {
			case <-d.ctx.Done():
			case <-time.After(selfUpdateHealthy):
				for _, work := range keep {
					os.RemoveAll(work)
				}
				pruneOldHosts(current)
			}
		})
	}
	u := newSelfUpdater(file, current, buildinfo.Version)
	u.control = func() []releaseSource { return controlSources(d.Store.Loc.Root) }
	arch := linuxArch(goruntime.GOARCH)
	u.asset = func(version string) string { return linuxrelease.Tarball(arch, version) }
	u.open = func(version, work, file string) (string, error) {
		return openLinuxRelease(current, arch, version, work, file)
	}
	u.place = func(work string) error { placeLinuxServers(current, arch, work); return nil }
	u.version = func(next string) (string, error) { return linkedVersion(current, next) }
	// The tarball is checked against SHA256SUMS, and each program in it against its own
	// .sha256: a Linux host has no signature to check besides.
	u.verify = func(string, string) error { return nil }
	u.restart = func() error {
		if d.Quit == nil {
			return fmt.Errorf("this host cannot stop itself")
		}
		d.selfUpdateState.restart.Store(true)
		d.Quit()
		return nil
	}
	if u.status.State != wire.SelfUpdateStateCannot && !restartsItself(d.Store.Loc.Root) {
		why := "This machine's host runs with no service or supervisor to start it again, so it does not update itself. Install it again with the join command to update it."
		u.status.State, u.status.Message = wire.SelfUpdateStateCannot, &why
	}
	d.serveSelfUpdate(u)
}
