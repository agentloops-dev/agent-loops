// Package controlsetup installs the control role on a Linux machine (#6610, epic #6608): the
// release's agents-control runs as a systemd user unit beside agentsd, as the Mac app runs two
// launchd jobs. No root and no container: hosts never escalate.
//
// TLS is made in the process (golang.org/x/crypto/acme/autocert) when a domain is given, and
// is the control plane's own self-signed certificate, pinned in every code, when it is not.
// Decided: in the process, not a Caddy unit, because it is one program fewer to download,
// supervise and keep in step with the release, and Caddy cannot bind port 443 as a user
// either. deploy/Caddyfile.public stays for the container, whose Caddy also fronts a second
// name and a Cloudflare proxy.
package controlsetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/web"
	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
)

// UnitName is the systemd user unit.
const UnitName = "agents-control.service"

// DefaultPort is the control plane's own port; with a domain it is 443, where the
// certificate's TLS-ALPN challenge is answered.
const DefaultPort = 8791

// Options is one run of the installer.
type Options struct {
	// Home is the machine user's home.
	Home string
	// Executable is the running agentsd; agents-control is its sibling or is in the host's bin.
	Executable string
	// Name is the control plane's name; the domain, or the machine's name, when empty.
	Name string
	// Domain, when set, is the name a publicly trusted certificate is made for (ACME).
	Domain string
	// ACMEEmail is the address Let's Encrypt writes to about the certificate.
	ACMEEmail string
	// Agents is whether this machine also runs agents: the control plane then goes first.
	Agents bool
	// Port is the serving port; DefaultPort, or 443 with a domain, when 0.
	Port int
	// Page says that the web page is in place, so the unit serves it; Install sets it.
	Page bool
}

// Deps are what the installer does outside itself, so a test can stand in for each.
type Deps struct {
	// Systemd says whether this machine has a systemd user session.
	Systemd func() bool
	// Run runs one command and returns its combined output.
	Run func(name string, args ...string) (string, error)
	// Poll and Wait bound the wait for the first start to make the store.
	Poll, Wait time.Duration
	Out        io.Writer
}

// Layout is where the install puts things, under Home.
type Layout struct {
	Bin      string // the host's bin folder, where agents-control sits beside agentsd
	Control  string // agents-control's own folder (--home): certificate, key and store
	Unit     string
	HostUnit string // the host's drop-in on a machine with both roles (#6613)
	Web      string // the built web page (the release's web/), beside Bin
}

// LayoutOf is the layout under home.
func LayoutOf(home string) Layout {
	return Layout{
		Bin:      filepath.Join(home, ".agents-server", "bin"),
		Control:  filepath.Join(home, ".agents-control"),
		Unit:     filepath.Join(home, ".config", "systemd", "user", UnitName),
		HostUnit: filepath.Join(home, ".config", "systemd", "user", "agents-host.service.d", "10-control-first.conf"),
		Web:      filepath.Join(home, ".agents-server", "web"),
	}
}

// Binary is the installed agents-control.
func (l Layout) Binary() string { return filepath.Join(l.Bin, "agents-control") }

func (o *Options) port() int {
	switch {
	case o.Port != 0:
		return o.Port
	case o.Domain != "":
		return 443
	}
	return DefaultPort
}

// URL is the address the control plane answers at, when the install knows it (a domain);
// else empty, and the control plane takes the machine's own name.
func (o *Options) URL() string {
	if o.Domain == "" {
		return ""
	}
	if p := o.port(); p != 443 {
		return "https://" + net.JoinHostPort(o.Domain, strconv.Itoa(p))
	}
	return "https://" + o.Domain
}

// hostDropIn puts agentsd and its agents after the control plane on a machine with both
// roles (#6613): a starved control plane drops every client (#6569). It is a drop-in, as the
// host's own unit is written by its installer and stays as it is. CPUWeight is what decides
// between two units where the user manager has the cpu controller; Nice where it does not.
// AGENTS_CONTROL_HERE makes the host's capacity level keep room for the control plane.
const hostDropIn = `[Service]
Environment=AGENTS_CONTROL_HERE=1
Nice=5
CPUWeight=50
IOSchedulingClass=best-effort
IOSchedulingPriority=7
`

// writeHostDropIn writes the host's drop-in with both roles, and removes it with one. A host
// that runs takes it at its next start: the install does not stop its agents.
func writeHostDropIn(l Layout, agents bool) error {
	if !agents {
		if err := os.Remove(l.HostUnit); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.HostUnit), 0o700); err != nil {
		return err
	}
	// store-bounds: not a store: the host unit's drop-in, one small file rewritten whole by each install
	return os.WriteFile(l.HostUnit, []byte(hostDropIn), 0o600)
}

// Unit is the systemd user unit's text. Its weight, IO priority and kept memory are within
// what a user manager allows with no root; it leaves out what it cannot apply (#6613). A
// Nice below 0 needs a privilege, so the host is put lower instead (hostDropIn).
func Unit(o Options) string {
	args := []string{"serve", "--home", "%h/.agents-control", "--bind", "0.0.0.0", "--port", strconv.Itoa(o.port())}
	if o.Name != "" {
		args = append(args, "--name", strconv.Quote(o.Name))
	}
	if o.Domain != "" {
		args = append(args, "--acme-domain", o.Domain)
		if o.ACMEEmail != "" {
			args = append(args, "--acme-email", o.ACMEEmail)
		}
	}
	if o.Page {
		// At the domain's own origin, where a browser trusts the certificate; with no domain,
		// on this machine's loopback, as on the Mac.
		flag := "--web"
		if o.Domain != "" {
			flag = "--public-web"
		}
		args = append(args, flag, "%h/.agents-server/web")
	}
	env := ""
	if u := o.URL(); u != "" {
		env = "Environment=AGENTS_CONTROL_URL=" + u + "\n"
	}
	return fmt.Sprintf(`[Unit]
Description=Agent Loops control plane
After=network-online.target

[Service]
%sExecStart=%%h/.agents-server/bin/agents-control %s
Restart=always
RestartSec=5
CPUWeight=400
IOSchedulingClass=best-effort
IOSchedulingPriority=0
MemoryLow=128M

[Install]
WantedBy=default.target
`, env, strings.Join(args, " "))
}

// Installed says whether this machine has the control role by this install.
func Installed(home string) bool {
	_, err := os.Stat(LayoutOf(home).Unit)
	return err == nil
}

// Install puts agents-control in the host's bin and the web page beside it, writes the unit, starts it, and waits for
// the store to be made, so that a host code can be issued. It does nothing a user cannot.
func Install(ctx context.Context, o Options, d Deps) error {
	if o.Home == "" {
		return errors.New("no home folder")
	}
	d.defaults()
	l := LayoutOf(o.Home)
	if !d.Systemd() {
		return errors.New("this machine has no systemd user session; the control role runs as a unit there (loginctl enable-linger, or log in with a session)")
	}
	if o.Domain != "" && o.port() != 443 {
		fmt.Fprintf(d.Out, "Port %d is not 443: Let's Encrypt must reach this machine on 443, so forward it here.\n", o.port())
	}
	if o.Domain != "" && o.port() == 443 {
		fmt.Fprintln(d.Out, "The unit listens on 443 as a user: allow it with `sysctl net.ipv4.ip_unprivileged_port_start=443` (root, once), or forward 443 and give --port.")
	}
	if err := place(o.Executable, l); err != nil {
		return err
	}
	var err error
	if o.Page, err = placePage(o.Executable, l); err != nil {
		return err
	}
	if !o.Page {
		fmt.Fprintln(d.Out, "No web page: there is no web folder beside agentsd, so this control plane serves none.")
	}
	if err := os.MkdirAll(filepath.Dir(l.Unit), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(l.Control, 0o700); err != nil {
		return err
	}
	// store-bounds: not a store: the systemd unit file, one small file rewritten whole by each install
	if err := os.WriteFile(l.Unit, []byte(Unit(o)), 0o600); err != nil {
		return err
	}
	if err := writeHostDropIn(l, o.Agents); err != nil {
		return err
	}
	for _, step := range [][]string{{"daemon-reload"}, {"enable", UnitName}, {"restart", UnitName}} {
		if out, err := d.Run("systemctl", append([]string{"--user"}, step...)...); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", step[0], err, strings.TrimSpace(out))
		}
	}
	fmt.Fprintln(d.Out, "The control plane runs as the systemd user unit agents-control.")
	return nil
}

// HostCode issues a code for this machine's own host to join with, once the control plane
// has made its store.
func HostCode(ctx context.Context, home string, d Deps) (string, error) {
	d.defaults()
	l := LayoutOf(home)
	deadline := time.Now().Add(d.Wait)
	for {
		out, err := d.Run(l.Binary(), "code", "--host", "--here", "--home", l.Control)
		if err == nil {
			if code := strings.TrimSpace(out); strings.HasPrefix(code, "agents-control:") {
				return code, nil
			}
		}
		// A team's control plane gives this machine no agents (#7305): no wait changes that.
		if strings.Contains(out, machine.BothRolesRefused) {
			_ = writeHostDropIn(l, false)
			return "", fmt.Errorf("%s", machine.BothRolesRefused)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the control plane did not make a host code in %s: %v: %s", d.Wait, err, strings.TrimSpace(out))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(d.Poll):
		}
	}
}

// place puts agents-control in the host's bin: agentsd's sibling (the release unpacked), also
// in place of an older one, so that the install run again from a newer release updates it.
// With no sibling, the one that is there stays; with neither, it says what is missing.
func place(executable string, l Layout) error {
	if err := os.MkdirAll(l.Bin, 0o700); err != nil {
		return err
	}
	// The release's tarball names it agents-control-linux-<arch>; a built folder, agents-control.
	var sibling string
	for _, name := range []string{"agents-control", "agents-control-linux-" + releaseArch()} {
		p := filepath.Join(filepath.Dir(executable), name)
		if _, err := os.Stat(p); err == nil {
			sibling = p
			break
		}
	}
	old, oldErr := os.ReadFile(l.Binary())
	if sibling == "" {
		if oldErr == nil {
			return nil
		}
		return fmt.Errorf("agents-control is not beside agentsd (%s) or in %s: unpack the release's tarball and run agentsd from it", filepath.Dir(executable), l.Bin)
	}
	data, err := os.ReadFile(sibling)
	if err != nil {
		return err
	}
	if oldErr == nil && bytes.Equal(old, data) {
		return nil
	}
	tmp := l.Binary() + ".new"
	// store-bounds: not a store: the release's program, one copy beside agentsd
	if err := os.WriteFile(tmp, data, 0o700); err != nil { //nolint:gosec // G306: a program must run
		return err
	}
	return os.Rename(tmp, l.Binary())
}

// placePage puts the release's web folder, beside the running agentsd, in place of the one that
// is there, so that the page and agents-control stay one release. It copies only what the
// page's MANIFEST lists, with the hash it lists. With no folder beside agentsd, the page that is
// there stays. It says whether there is a page.
func placePage(executable string, l Layout) (bool, error) {
	from := filepath.Join(filepath.Dir(executable), "web")
	manifest, err := os.ReadFile(filepath.Join(from, "MANIFEST")) //nolint:gosec // G304: the release's own folder
	if errors.Is(err, os.ErrNotExist) {
		_, err := web.LoadFiles(l.Web)
		return err == nil, nil
	} else if err != nil {
		return false, err
	}
	files, err := web.LoadFiles(from)
	if err != nil {
		return false, fmt.Errorf("the release's web page: %w", err)
	}
	next, old := l.Web+".new", l.Web+".old"
	for _, dir := range []string{next, old} {
		if err := os.RemoveAll(dir); err != nil {
			return false, err
		}
	}
	write := func(name string, data []byte) error {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("the release's web page: %s is outside its folder", name)
		}
		file := filepath.Join(next, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return err
		}
		// store-bounds: not a store: the release's web page, one copy, replaced whole by each install
		return os.WriteFile(file, data, 0o600) //nolint:gosec // G703: a name from the checked manifest, local to our own folder
	}
	for name, f := range files {
		if err := write(filepath.FromSlash(strings.TrimPrefix(name, "/")), f.Data); err != nil {
			return false, err
		}
	}
	if err := write("MANIFEST", manifest); err != nil {
		return false, err
	}
	// agents-control reads the page into memory when it starts, so the swap is not seen by one
	// that runs; the restart after it takes the new page.
	if err := os.Rename(l.Web, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.Rename(next, l.Web); err != nil {
		return false, err
	}
	return true, os.RemoveAll(old)
}

// releaseArch is the architecture in the release's file names.
func releaseArch() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return "x86_64"
}

func (d *Deps) defaults() {
	if d.Poll == 0 {
		d.Poll = time.Second
	}
	if d.Wait == 0 {
		d.Wait = time.Minute
	}
	if d.Out == nil {
		d.Out = io.Discard
	}
}

// SystemdUser says whether systemd runs here with a user session (a codespace's systemctl is
// a shim, #3366).
func SystemdUser() bool {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "--user", "show-environment").Run() == nil
}

// RunCommand runs a command and returns its combined output.
func RunCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
