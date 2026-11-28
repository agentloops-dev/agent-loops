package controlsetup

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
)

func TestUnitSelfSigned(t *testing.T) {
	u := Unit(Options{})
	for _, want := range []string{
		"ExecStart=%h/.agents-server/bin/agents-control serve --home %h/.agents-control --bind 0.0.0.0 --port 8791\n",
		"Restart=always", "WantedBy=default.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "acme") || strings.Contains(u, "AGENTS_CONTROL_URL") {
		t.Errorf("a unit with no domain asks for a certificate:\n%s", u)
	}
}

func TestUnitDomainMakesItsOwnCertificate(t *testing.T) {
	o := Options{Domain: "agents.example.com", ACMEEmail: "me@example.com", Name: "My plane"}
	u := Unit(o)
	for _, want := range []string{
		"Environment=AGENTS_CONTROL_URL=https://agents.example.com\n",
		"--port 443", "--acme-domain agents.example.com", "--acme-email me@example.com", `--name "My plane"`} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	o.Port = 8443
	if got := o.URL(); got != "https://agents.example.com:8443" {
		t.Errorf("URL = %q", got)
	}
}

type fake struct {
	calls []string
	codes []string // what `code --host` answers in turn
}

func (f *fake) run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	if filepath.Base(name) == "agents-control" {
		if len(f.codes) == 0 {
			return "", errors.New("no control plane yet")
		}
		c := f.codes[0]
		f.codes = f.codes[1:]
		if c == "" {
			return "no control plane yet", errors.New("exit 1")
		}
		return c + "\n", nil
	}
	return "", nil
}

func setup(t *testing.T) (home, exe string) {
	home = t.TempDir()
	dir := t.TempDir()
	exe = filepath.Join(dir, "agentsd")
	for _, f := range []string{"agentsd", "agents-control"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("#!/bin/sh\n"+f), 0o700); err != nil { //nolint:gosec // G306: the stand-in program must run
			t.Fatal(err)
		}
	}
	return home, exe
}

func TestInstallWritesTheUnitAndStartsIt(t *testing.T) {
	home, exe := setup(t)
	f := &fake{}
	var out bytes.Buffer
	d := Deps{Systemd: func() bool { return true }, Run: f.run, Out: &out}
	if err := Install(context.Background(), Options{Home: home, Executable: exe}, d); err != nil {
		t.Fatal(err)
	}
	l := LayoutOf(home)
	if b, err := os.ReadFile(l.Binary()); err != nil || !strings.Contains(string(b), "agents-control") {
		t.Fatalf("agents-control is not in the host's bin: %v", err)
	}
	if fi, _ := os.Stat(l.Binary()); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v, want 0700", fi.Mode().Perm())
	}
	if b, err := os.ReadFile(l.Unit); err != nil || !strings.Contains(string(b), "agents-control serve") {
		t.Fatalf("no unit: %v", err)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable agents-control.service", "systemctl --user restart agents-control.service"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "sudo") {
			t.Errorf("escalated: %s", c)
		}
	}
}

func TestInstallFindsTheReleasesFileName(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agents-control-linux-"+releaseArch()), []byte("x"), 0o700); err != nil { //nolint:gosec // G306: the stand-in program must run
		t.Fatal(err)
	}
	d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run}
	if err := Install(context.Background(), Options{Home: home, Executable: filepath.Join(dir, "agentsd-linux-"+releaseArch())}, d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LayoutOf(home).Binary()); err != nil {
		t.Error(err)
	}
}

func TestInstallNeedsSystemdAndTheBinary(t *testing.T) {
	home, exe := setup(t)
	d := Deps{Systemd: func() bool { return false }, Run: (&fake{}).run}
	if err := Install(context.Background(), Options{Home: home, Executable: exe}, d); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Errorf("no systemd: %v", err)
	}
	d.Systemd = func() bool { return true }
	lonely := filepath.Join(t.TempDir(), "agentsd")
	if err := Install(context.Background(), Options{Home: home, Executable: lonely}, d); err == nil || !strings.Contains(err.Error(), "not beside agentsd") {
		t.Errorf("no agents-control: %v", err)
	}
}

func TestHostCodeWaitsForTheStore(t *testing.T) {
	home := t.TempDir()
	f := &fake{codes: []string{"", "", "agents-control:4:h:x"}}
	d := Deps{Run: f.run, Poll: time.Millisecond, Wait: time.Minute}
	code, err := HostCode(context.Background(), home, d)
	if err != nil || code != "agents-control:4:h:x" {
		t.Fatalf("code %q, err %v", code, err)
	}
	if len(f.calls) != 3 || !strings.Contains(f.calls[0], "code --host --here --home "+LayoutOf(home).Control) {
		t.Errorf("calls %q", f.calls)
	}
	f = &fake{}
	d = Deps{Run: f.run, Poll: time.Millisecond, Wait: 5 * time.Millisecond}
	if _, err := HostCode(context.Background(), home, d); err == nil {
		t.Error("no code, no error")
	}
}

// With both roles the control plane goes first (#6613): the host gets a drop-in that puts it
// lower, and a machine with the control role only has none.
func TestBothRolesPutTheControlPlaneFirst(t *testing.T) {
	home, exe := setup(t)
	d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run}
	o := Options{Home: home, Executable: exe, Agents: true}
	if err := Install(context.Background(), o, d); err != nil {
		t.Fatal(err)
	}
	l := LayoutOf(home)
	b, err := os.ReadFile(l.HostUnit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Service]", "Environment=AGENTS_CONTROL_HERE=1", "Nice=5", "CPUWeight=50", "IOSchedulingPriority=7"} {
		if !strings.Contains(string(b), want+"\n") {
			t.Errorf("drop-in lacks %q:\n%s", want, b)
		}
	}
	u := Unit(o)
	for _, want := range []string{"CPUWeight=400", "IOSchedulingClass=best-effort", "IOSchedulingPriority=0", "MemoryLow=128M"} {
		if !strings.Contains(u, want+"\n") {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "Nice=-") {
		t.Error("a user unit cannot have a Nice below 0")
	}
	o.Agents = false
	for range 2 {
		if err := Install(context.Background(), o, d); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(l.HostUnit); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the drop-in stays with one role: %v", err)
		}
	}
}

// A team's control plane gives its own machine no agents (#7305): said at once, with no wait.
func TestHostCodeStopsAtATeam(t *testing.T) {
	home := t.TempDir()
	l := LayoutOf(home)
	if err := writeHostDropIn(l, true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	d := Deps{Run: func(string, ...string) (string, error) {
		calls++
		return machine.BothRolesRefused + "\n", errors.New("exit 1")
	}, Poll: time.Millisecond, Wait: time.Minute}
	if _, err := HostCode(context.Background(), home, d); err == nil || err.Error() != machine.BothRolesRefused {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(l.HostUnit); calls != 1 || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%d calls, drop-in: %v", calls, err)
	}
}

// The host says where its control role serves (#7313), from the unit the install wrote.
func TestAddressIsWhatTheUnitServes(t *testing.T) {
	home := t.TempDir()
	if a := Address(home); a != nil {
		t.Fatalf("a machine with no control role says %+v", a)
	}
	for _, c := range []struct {
		o      Options
		domain string
		port   int64
	}{
		{Options{}, "", DefaultPort},
		{Options{Port: 9000}, "", 9000},
		{Options{Domain: "ams3-1.example.dev", Name: "--port 1"}, "ams3-1.example.dev", 443},
		{Options{Domain: "ams3-1.example.dev", Port: 8443}, "ams3-1.example.dev", 8443},
	} {
		a := addressOf(Unit(c.o))
		if a == nil {
			t.Fatalf("%+v: no address", c.o)
		}
		domain := ""
		if a.Domain != nil {
			domain = *a.Domain
		}
		if domain != c.domain || a.Port != c.port {
			t.Errorf("%+v: address %q:%d, want %q:%d", c.o, domain, a.Port, c.domain, c.port)
		}
	}
	unit := LayoutOf(home).Unit
	if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte(Unit(Options{Port: 9001})), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := Address(home); a == nil || a.Port != 9001 || a.Domain != nil {
		t.Errorf("Address = %+v, want port 9001", a)
	}
}

// With no domain, the address in each code is one another machine can reach (#7467): the
// machine's own IP address, or --address, never its hostname.
func TestNoDomainGivesAnAddressAnotherMachineReaches(t *testing.T) {
	public := netip.MustParseAddr("203.0.113.7")
	for _, c := range []struct {
		name    string
		o       Options
		ip      netip.Addr
		public  bool
		wantURL string
		wantOut string
		// seen is the public address a control plane saw the uplink come from (#7493).
		seen netip.Addr
	}{
		{"public", Options{}, public, true, "https://203.0.113.7:8791", "reach this control plane at https://203.0.113.7:8791.", netip.Addr{}},
		{"private", Options{}, netip.MustParseAddr("10.0.0.4"), false, "https://10.0.0.4:8791", "private address", netip.Addr{}},
		{"given", Options{Address: "plane.tail1234.ts.net", Port: 9001}, public, true, "https://plane.tail1234.ts.net:9001", "plane.tail1234.ts.net:9001", netip.Addr{}},
		{"ipv6", Options{}, netip.MustParseAddr("2001:db8::7"), true, "https://[2001:db8::7]:8791", "[2001:db8::7]", netip.Addr{}},
		{"none", Options{}, netip.Addr{}, false, "", "Install again with --address", netip.Addr{}},
		// A cloud VM behind NAT: its interfaces have only a private address (#7493).
		{"behind NAT", Options{}, netip.MustParseAddr("10.128.0.5"), false, "https://198.51.100.9:8791", "takes 198.51.100.9, the public address its uplink came from", netip.MustParseAddr("198.51.100.9")},
		{"none, seen", Options{}, netip.Addr{}, false, "https://198.51.100.9:8791", "reach this control plane at https://198.51.100.9:8791.", netip.MustParseAddr("198.51.100.9")},
		{"own public first", Options{}, public, true, "https://203.0.113.7:8791", "reach this control plane at https://203.0.113.7:8791.", netip.MustParseAddr("198.51.100.9")},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, exe := setup(t)
			var out bytes.Buffer
			d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run, Out: &out,
				Address: func() (netip.Addr, bool) { return c.ip, c.public }, Seen: func() netip.Addr { return c.seen }}
			c.o.Home, c.o.Executable = home, exe
			if err := Install(context.Background(), c.o, d); err != nil {
				t.Fatal(err)
			}
			unit, err := os.ReadFile(LayoutOf(home).Unit)
			if err != nil {
				t.Fatal(err)
			}
			if got := urlHost(string(unit)); c.wantURL == "" && got != "" || c.wantURL != "" && !strings.Contains(string(unit), "Environment=AGENTS_CONTROL_URL="+c.wantURL+"\n") {
				t.Errorf("unit's address, want %q:\n%s", c.wantURL, unit)
			}
			if !strings.Contains(out.String(), c.wantOut) {
				t.Errorf("output lacks %q:\n%s", c.wantOut, out.String())
			}
		})
	}
}

// An install again keeps the address it gave before, so codes already given stay good.
func TestInstallAgainKeepsTheAddress(t *testing.T) {
	home, exe := setup(t)
	d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run,
		Address: func() (netip.Addr, bool) { return netip.MustParseAddr("203.0.113.7"), true }}
	if err := Install(context.Background(), Options{Home: home, Executable: exe}, d); err != nil {
		t.Fatal(err)
	}
	d.Address = func() (netip.Addr, bool) { return netip.MustParseAddr("198.51.100.9"), true }
	if err := Install(context.Background(), Options{Home: home, Executable: exe}, d); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(LayoutOf(home).Unit)
	if !strings.Contains(string(unit), "AGENTS_CONTROL_URL=https://203.0.113.7:8791\n") {
		t.Errorf("the address moved:\n%s", unit)
	}
}

func TestInstallRefusesAnAddressThatIsAURL(t *testing.T) {
	home, exe := setup(t)
	d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run}
	for _, a := range []string{"https://plane.example.com", "plane.example.com:8791", "plane example"} {
		if err := Install(context.Background(), Options{Home: home, Executable: exe, Address: a}, d); err == nil {
			t.Errorf("--address %q: no error", a)
		}
	}
}

func TestPickAddress(t *testing.T) {
	ip := netip.MustParseAddr
	for _, c := range []struct {
		addrs    []netip.Addr
		outbound netip.Addr
		want     netip.Addr
		public   bool
	}{
		// A droplet: its public IPv4 on eth0, beside a private one and a container bridge.
		{[]netip.Addr{ip("127.0.0.1"), ip("172.17.0.1"), ip("10.0.0.4"), ip("203.0.113.7"), ip("2001:db8::7")}, ip("203.0.113.7"), ip("203.0.113.7"), true},
		// Only IPv6 is public.
		{[]netip.Addr{ip("10.0.0.4"), ip("fe80::1"), ip("2001:db8::7")}, ip("10.0.0.4"), ip("2001:db8::7"), true},
		// Behind NAT: the default route's source, not the bridge's.
		{[]netip.Addr{ip("127.0.0.1"), ip("172.17.0.1"), ip("192.168.1.20")}, ip("192.168.1.20"), ip("192.168.1.20"), false},
		// No route out: a private IPv4 on an interface.
		{[]netip.Addr{ip("127.0.0.1"), ip("192.168.1.20")}, netip.Addr{}, ip("192.168.1.20"), false},
		{[]netip.Addr{ip("127.0.0.1"), ip("fe80::1")}, netip.Addr{}, netip.Addr{}, false},
	} {
		got, public := pickAddress(c.addrs, c.outbound)
		if got != c.want || public != c.public {
			t.Errorf("pickAddress(%v, %v) = %v %v, want %v %v", c.addrs, c.outbound, got, public, c.want, c.public)
		}
	}
}
