package agentsd

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// platformSocketPolicy is RolePolicy.sshSessions (#726): a server's daemon.sock is
// reached through an ssh forward, so only an ssh login's session is control.
func platformSocketPolicy(*Daemon) SocketPolicy {
	return sshSessions{uid: os.Geteuid(), status: readProcStatus, installer: ownInstaller}
}

// sshSessions is a Linux server's door: control for an ssh login's own session process,
// which is what carries the Mac's forward to the socket, and stranger for anything else.
// That is every process an agent starts, orphaned or not: none is sshd's child. The
// control plane's channels never reach the socket, and helpers reach the daemon over its
// MCP endpoint (#185), so neither is lost. An agent that can ssh into its own account with
// a key it holds is that account's login, and is let in as one. Another account is turned
// away before it can send a line.
//
// The one exception is this host's own installer (#6685): `agentsd install --join`, run
// from this host's bin folder by a process that root started on this machine, as
// cloud-init's su does at first boot and an ssh login does. It installs the runtimes,
// signs in and runs the smoke session (steps 5, 7 and 8) on the socket. An agent's
// processes descend from the daemon, or, orphaned, from pid 1 or a systemd --user, so an
// installer an agent starts has no root ancestor and stays a stranger.
type sshSessions struct {
	uid    int
	status func(pid int) (procStatus, bool)
	// installer says whether pid runs `install` from this host's bin folder; nil is never.
	installer func(pid int) bool
}

func (sshSessions) Summary() string {
	return "control for this account's ssh sessions alone, anything else a stranger"
}

func (p sshSessions) Role(c net.Conn) (Role, bool) {
	cred, err := peerCred(c)
	if err != nil {
		log.Printf("socket: %v", err)
		return "", false
	}
	if int(cred.Uid) != p.uid {
		return "", false
	}
	peer, ok := p.status(int(cred.Pid))
	if !ok {
		return RoleStranger, true
	}
	var parent *procStatus
	if s, ok := p.status(peer.parent); ok {
		parent = &s
	}
	if isSSHSession(peer, parent) {
		return RoleControl, true
	}
	if p.installer != nil && p.installer(int(cred.Pid)) && startedByRoot(peer, p.status) {
		return RoleControl, true
	}
	return RoleStranger, true
}

// maxAncestors bounds the walk up the process tree; a real one is a few levels deep.
const maxAncestors = 64

// startedByRoot says whether a root process other than pid 1 is an ancestor of peer:
// cloud-init's su, sudo, or a login's sshd. Every orphan is handed to pid 1, so pid 1
// does not count.
func startedByRoot(peer procStatus, status func(int) (procStatus, bool)) bool {
	pid := peer.parent
	for range maxAncestors {
		if pid <= 1 {
			return false
		}
		s, ok := status(pid)
		if !ok {
			return false
		}
		if s.uid == 0 {
			return true
		}
		pid = s.parent
	}
	return false
}

// ownInstaller says whether pid runs `install` from the folder this host's own
// executable is in (bin/, where install.sh and the installer put every build).
func ownInstaller(pid int) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	proc := "/proc/" + strconv.Itoa(pid)
	exe, err := os.Readlink(proc + "/exe")
	if err != nil {
		return false
	}
	cmdline, err := os.ReadFile(proc + "/cmdline")
	if err != nil {
		return false
	}
	return filepath.Dir(exe) == filepath.Dir(self) && isInstallCommand(string(cmdline))
}

// isInstallCommand says whether a /proc/<pid>/cmdline is `<program> install …`.
func isInstallCommand(cmdline string) bool {
	args := strings.Split(cmdline, "\x00")
	return len(args) > 1 && args[1] == "install"
}

// peerCred is the caller's SO_PEERCRED: its pid and uid when it connected.
func peerCred(c net.Conn) (*unix.Ucred, error) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return nil, unix.EINVAL
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return cred, credErr
}
