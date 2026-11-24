package agentsd

import (
	"os"
	"testing"
)

// This test is no ssh login, so a Linux host makes it a stranger (#726): it may ping and
// nothing more. Open, an agent's own shell could answer its prompts and delete other agents.
func TestALinuxHostMakesAShellAStranger(t *testing.T) {
	t.Parallel()

	_, path := serveWith(t, nil)
	if m := socketCall(t, path, "daemon/ping"); m == nil || m.Error != nil {
		t.Fatalf("ping: %+v", m)
	}
	for _, method := range []string{"agents/list", "agents/start", "permissions/answer"} {
		if m := socketCall(t, path, method); m == nil || m.Error == nil || m.Error.Code != CodeNotPermitted {
			t.Fatalf("%s: %+v", method, m)
		}
	}
}

// fakeStatus is a /proc where the caller, whatever its pid, is peer, and peer's parent
// is parent.
func fakeStatus(peer, parent procStatus) func(int) (procStatus, bool) {
	return func(pid int) (procStatus, bool) {
		if pid == peer.parent {
			return parent, true
		}
		return peer, true
	}
}

// rootSSHD is the privileged sshd every login's session is a child of.
var rootSSHD = procStatus{name: "sshd", uid: 0, parent: 1}

// The Mac's forward, an ssh login's session, may call everything; the same name under
// the account's own shell may not.
func TestALinuxHostLetsAnSSHSessionIn(t *testing.T) {
	t.Parallel()

	login := procStatus{name: "sshd-session", uid: os.Geteuid(), parent: 1 << 30}
	_, path := serveWith(t, sshSessions{uid: os.Geteuid(), status: fakeStatus(login, rootSSHD)})
	if m := socketCall(t, path, "agents/list"); m == nil || m.Error != nil {
		t.Fatalf("an ssh login's agents/list: %+v", m)
	}
	shell := procStatus{name: "bash", uid: os.Geteuid(), parent: 1}
	_, path = serveWith(t, sshSessions{uid: os.Geteuid(), status: fakeStatus(login, shell)})
	if m := socketCall(t, path, "agents/list"); m == nil || m.Error == nil || m.Error.Code != CodeNotPermitted {
		t.Fatalf("sshd under a shell: %+v", m)
	}
}

// Another account is closed before a line is read, ssh login or not.
func TestALinuxHostClosesAnotherAccount(t *testing.T) {
	t.Parallel()

	login := procStatus{name: "sshd-session", uid: os.Geteuid(), parent: 1 << 30}
	_, path := serveWith(t, sshSessions{uid: os.Geteuid() + 1, status: fakeStatus(login, rootSSHD)})
	if m := socketCall(t, path, "daemon/ping"); m != nil {
		t.Fatalf("answered another account: %+v", m)
	}
}

// fakeTree is a /proc of pid → status, for a walk up more than one level.
func fakeTree(caller procStatus, tree map[int]procStatus) func(int) (procStatus, bool) {
	return func(pid int) (procStatus, bool) {
		if s, ok := tree[pid]; ok {
			return s, true
		}
		return caller, true
	}
}

// The first-boot installer (#6685) is no ssh login: cloud-init runs it as root, through
// su, then sh. It installs the runtimes (step 5) on the socket, so it is control. The
// same installer started by an agent, under the daemon and systemd --user, is a stranger,
// and so is any other program under su.
func TestALinuxHostLetsItsFirstBootInstallerIn(t *testing.T) {
	t.Parallel()

	me := os.Geteuid()
	caller := procStatus{name: "current", uid: me, parent: 100}
	firstBoot := map[int]procStatus{
		100: {name: "sh", uid: me, parent: 90},
		90:  {name: "su", uid: 0, parent: 80},
		80:  {name: "sh", uid: 0, parent: 1},
	}
	underAgent := map[int]procStatus{
		100: {name: "bash", uid: me, parent: 70},
		70:  {name: "agentsd", uid: me, parent: 60},
		60:  {name: "systemd", uid: me, parent: 1},
	}
	installer := func(int) bool { return true }
	notInstaller := func(int) bool { return false }
	for _, c := range []struct {
		name      string
		tree      map[int]procStatus
		installer func(int) bool
		control   bool
	}{
		{"the installer under cloud-init's su", firstBoot, installer, true},
		{"the installer under an agent", underAgent, installer, false},
		{"another program under cloud-init's su", firstBoot, notInstaller, false},
		{"no installer check", firstBoot, nil, false},
	} {
		_, path := serveWith(t, sshSessions{uid: me, status: fakeTree(caller, c.tree), installer: c.installer})
		m := socketCall(t, path, "runtimes/install")
		refused := m != nil && m.Error != nil && m.Error.Code == CodeNotPermitted
		if m == nil || refused == c.control {
			t.Errorf("%s: runtimes/install: %+v", c.name, m)
		}
	}
}

// startedByRoot stops at pid 1, which every orphan is handed to, and at a bounded depth.
func TestStartedByRootIgnoresPidOne(t *testing.T) {
	t.Parallel()

	me := os.Geteuid()
	orphan := procStatus{name: "current", uid: me, parent: 1}
	if startedByRoot(orphan, fakeTree(orphan, map[int]procStatus{1: {name: "systemd", uid: 0}})) {
		t.Fatal("pid 1 counted as root")
	}
	loop := procStatus{name: "sh", uid: me, parent: 5}
	if startedByRoot(loop, fakeTree(loop, map[int]procStatus{5: loop})) {
		t.Fatal("a loop counted as root")
	}
}

func TestIsInstallCommand(t *testing.T) {
	t.Parallel()

	for cmdline, want := range map[string]bool{
		"/home/a/.agents-server/bin/current\x00install\x00--join\x00abc\x00": true,
		"/home/a/.agents-server/bin/current\x00--root\x00/x\x00--serve\x00":  false,
		"current\x00":         false,
		"current\x00inst\x00": false,
	} {
		if got := isInstallCommand(cmdline); got != want {
			t.Errorf("%q: %v", cmdline, got)
		}
	}
}

// This test binary is not `install`, so it is not the host's installer.
func TestThisTestIsNotTheInstaller(t *testing.T) {
	t.Parallel()

	if ownInstaller(os.Getpid()) {
		t.Fatal("the test binary counted as the installer")
	}
}
