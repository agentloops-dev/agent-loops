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
	// Port is the serving port; DefaultPort, or 443 with a domain, when 0.
	Port int
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
	Bin     string // the host's bin folder, where agents-control sits beside agentsd
	Control string // agents-control's own folder (--home): certificate, key and store
	Unit    string
}

// LayoutOf is the layout under home.
func LayoutOf(home string) Layout {
	return Layout{
		Bin:     filepath.Join(home, ".agents-server", "bin"),
		Control: filepath.Join(home, ".agents-control"),
		Unit:    filepath.Join(home, ".config", "systemd", "user", UnitName),
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

// Unit is the systemd user unit's text.
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

[Install]
WantedBy=default.target
`, env, strings.Join(args, " "))
}

// Install puts agents-control in the host's bin, writes the unit, starts it, and waits for
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
		out, err := d.Run(l.Binary(), "code", "--host", "--home", l.Control)
		if err == nil {
			if code := strings.TrimSpace(out); strings.HasPrefix(code, "agents-control:") {
				return code, nil
			}
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

// place puts agents-control in the host's bin: it is already there, or it is agentsd's
// sibling (the release unpacked), else the install says what is missing.
func place(executable string, l Layout) error {
	if err := os.MkdirAll(l.Bin, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(l.Binary()); err == nil {
		return nil
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
	if sibling == "" {
		return fmt.Errorf("agents-control is not beside agentsd (%s) or in %s: unpack the release's tarball and run agentsd from it", filepath.Dir(executable), l.Bin)
	}
	data, err := os.ReadFile(sibling)
	if err != nil {
		return err
	}
	tmp := l.Binary() + ".new"
	// store-bounds: not a store: the release's program, copied once beside agentsd
	if err := os.WriteFile(tmp, data, 0o700); err != nil { //nolint:gosec // G306: a program must run
		return err
	}
	return os.Rename(tmp, l.Binary())
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
