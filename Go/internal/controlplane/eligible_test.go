package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// goodUsage is a machine above the floor.
func goodUsage() *wire.MachineUsageState {
	return &wire.MachineUsageState{Machine: wire.MachineUsage{MemoryTotal: 2_000_000_000, Capacity: new(wire.CapacityLevelOk)},
		Disks: []wire.DiskAlarm{{Reading: wire.DiskReading{Mount: "/", FreeBytes: 40_000_000_000, TotalBytes: 60_000_000_000}, Level: wire.DiskLevelOk}}}
}

func TestEligibleReasons(t *testing.T) {
	t.Parallel()
	control := []string{records.RoleControl}
	for _, c := range []struct {
		name   string
		change func(*eligibleFacts)
		want   []wire.ControlIneligibility
	}{
		{"eligible", func(*eligibleFacts) {}, nil},
		{"not reachable", func(f *eligibleFacts) { f.unreachable, f.usage = "It is offline.", nil },
			[]wire.ControlIneligibility{wire.ControlIneligibilityNotReachable}},
		{"a Mac", func(f *eligibleFacts) { f.record.Platform = "macOS 27.0" },
			[]wire.ControlIneligibility{wire.ControlIneligibilityMacSleeps}},
		{"a Mac the person says yes to", func(f *eligibleFacts) { f.record.Platform, f.allowSleep = "macOS 27.0", true }, nil},
		{"a Mac outside single-user, with a yes", func(f *eligibleFacts) {
			f.record.Platform, f.allowSleep, f.singleUser, f.record.Roles = "macOS 27.0", true, false, control
		}, []wire.ControlIneligibility{wire.ControlIneligibilityMacSleeps}},
		{"stops when idle", func(f *eligibleFacts) { f.idleMinutes = 30 },
			[]wire.ControlIneligibility{wire.ControlIneligibilityStopsWhenIdle}},
		{"says no usage", func(f *eligibleFacts) { f.usage = nil },
			[]wire.ControlIneligibility{wire.ControlIneligibilityBelowFloor}},
		{"too little memory", func(f *eligibleFacts) { f.usage.Machine.MemoryTotal = 512_000_000 },
			[]wire.ControlIneligibility{wire.ControlIneligibilityBelowFloor}},
		{"a 1 GB machine", func(f *eligibleFacts) { f.usage.Machine.MemoryTotal = 968_000_000 }, nil},
		{"no disk said", func(f *eligibleFacts) { f.usage.Disks = nil },
			[]wire.ControlIneligibility{wire.ControlIneligibilityBelowFloor}},
		{"a low disk", func(f *eligibleFacts) { f.usage.Disks[0].Level = wire.DiskLevelLow },
			[]wire.ControlIneligibility{wire.ControlIneligibilityBelowFloor}},
		{"no room for the store", func(f *eligibleFacts) { f.storeBytes = 39_000_000_000 },
			[]wire.ControlIneligibility{wire.ControlIneligibilityBelowFloor}},
		{"near capacity", func(f *eligibleFacts) { f.usage.Machine.Capacity = new(wire.CapacityLevelNear) },
			[]wire.ControlIneligibility{wire.ControlIneligibilityBelowFloor}},
		{"full, as it last pushed", func(f *eligibleFacts) { f.usage.Machine.Capacity, f.level = nil, wire.CapacityLevelFull },
			[]wire.ControlIneligibility{wire.ControlIneligibilityBelowFloor}},
		{"both roles in a team", func(f *eligibleFacts) { f.singleUser = false },
			[]wire.ControlIneligibility{wire.ControlIneligibilityBothRoles}},
		{"control only in a team", func(f *eligibleFacts) { f.singleUser, f.record.Roles = false, control }, nil},
		{"an older version", func(f *eligibleFacts) { f.record.Version = "0.39.0" },
			[]wire.ControlIneligibility{wire.ControlIneligibilityOlderVersion}},
		{"several, in order", func(f *eligibleFacts) { f.idleMinutes, f.singleUser, f.record.Version = 30, false, "0.39.0" },
			[]wire.ControlIneligibility{wire.ControlIneligibilityStopsWhenIdle, wire.ControlIneligibilityBothRoles, wire.ControlIneligibilityOlderVersion}},
	} {
		f := eligibleFacts{record: records.MachineRecord{ID: "h", Platform: "Linux", Version: "0.41.0"}, singleUser: true,
			usage: goodUsage(), planeVersion: "0.41.0"}
		c.change(&f)
		var got []wire.ControlIneligibility
		for _, r := range eligibleReasons(f) {
			got = append(got, r.Reason)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOlderThan(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		version, plane string
		want           bool
	}{{"0.9.0", "0.10.0", true}, {"0.10.0", "0.9.0", false}, {"0.41.0", "0.41.0", false}, {"0.41.0-2-gabc", "0.41.0", false},
		{"0.1", "dev", false}, {"dev", "0.41.0", true}, {"", "0.41.0", true}} {
		if got := olderThan(c.version, c.plane); got != c.want {
			t.Errorf("olderThan(%q, %q) = %v", c.version, c.plane, got)
		}
	}
}

func TestPublicAddress(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{"203.0.113.7": true, "2001:db8::1": true, "100.64.1.2": true, "127.0.0.1": false,
		"10.0.0.4": false, "192.168.1.2": false, "169.254.169.254": false, "::1": false, "fd00::1": false, "::ffff:10.0.0.4": false, "0.0.0.0": false} {
		if got := publicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

// The whole call: the machine's own figures and address, an admin's only.
func TestControlEligible(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	p.asked = func(_, method string, _ json.RawMessage) (any, bool) {
		if method == wire.CallMachineUsage.Name {
			return goodUsage(), true
		}
		return nil, false
	}
	box := p.host("MACHINE-2")
	_, window := p.client("Window")
	ask := func(machine string) (out wire.ControlEligible) {
		t.Helper()
		must(t, json.Unmarshal(call(t, window, "", "control/eligible", map[string]any{"machine": machine}), &out))
		return out
	}
	// Its uplink comes from this machine's loopback, which is no public address.
	got := ask(box.member.Host)
	if got.Ok || len(got.Reasons) != 1 || got.Reasons[0].Reason != wire.ControlIneligibilityNotReachable || !strings.Contains(got.Reasons[0].Text, "no public address") {
		t.Fatalf("%+v", got)
	}
	if e := callError(t, window, "", "control/eligible", map[string]any{"machine": "nobody"}); !strings.Contains(e, "No machine") {
		t.Fatal(e)
	}
	if e := callError(t, window, "", "control/eligible", map[string]any{"machine": p.s.controlMachineID()}); !strings.Contains(e, "already runs") {
		t.Fatal(e)
	}
	sam := p.invite(window, "Sam", wire.UserRoleMember)
	if e := callError(t, sam.window, "", "control/eligible", map[string]any{"machine": box.member.Host}); !strings.Contains(e, "Only an admin") {
		t.Fatal(e)
	}
	// With Sam it is a team, and this machine runs agents.
	if got = ask(box.member.Host); len(got.Reasons) != 2 || got.Reasons[1].Reason != wire.ControlIneligibilityBothRoles {
		t.Fatalf("%+v", got)
	}
	box.stop()
	p.waitFor(func() bool { s, ok := p.s.Router().State(box.member.Host); return ok && !s.online() }, "the host offline")
	if got = ask(box.member.Host); got.Ok || got.Reasons[0].Text != "It is offline." {
		t.Fatalf("%+v", got)
	}
}

// A machine at a public address is reachable when one of the control plane's ports answers.
func TestReachDialsTheUplinksAddress(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	box := p.host("MACHINE-2")
	p.s.router.mu.Lock()
	p.s.router.hosts[box.member.Host].conn.(*Conn).Remote = "203.0.113.7"
	p.s.router.mu.Unlock()
	// Each answers is never changed: the dial that did not win can outlive the call.
	only := func(answers string) func(context.Context, string) error {
		return func(_ context.Context, address string) error {
			if address == answers {
				return nil
			}
			return errors.New("refused")
		}
	}
	reach := func() (string, string) { return p.s.reach(context.Background(), box.member.Host) }
	p.s.dialMachine = only("203.0.113.7:8791")
	if address, why := reach(); why != "" || address != "203.0.113.7:8791" {
		t.Fatal(address, why)
	}
	p.s.dialMachine = only("203.0.113.7:443")
	if address, why := reach(); why != "" || address != "203.0.113.7" {
		t.Fatal(address, why)
	}
	p.s.dialMachine = only("")
	if _, why := reach(); !strings.Contains(why, "nothing answers at 203.0.113.7 on port 443 or 8791") {
		t.Fatal(why)
	}

	// A machine with the control role said its port (#7313): only that one is tried.
	p.s.router.SetControlAddress(box.member.Host, &wire.MachineControlAddress{Port: 9000})
	p.s.dialMachine = only("203.0.113.7:8791")
	if _, why := reach(); !strings.Contains(why, "nothing answers at 203.0.113.7 on port 9000. Open the port there.") {
		t.Fatal(why)
	}
	p.s.dialMachine = only("203.0.113.7:9000")
	if address, why := reach(); why != "" || address != "203.0.113.7:9000" {
		t.Fatal(address, why)
	}

	// With a domain, the domain's public address is dialled, and the domain is shown.
	p.s.router.SetControlAddress(box.member.Host, &wire.MachineControlAddress{Domain: new("ams3-1.example.dev"), Port: 443})
	dns := map[string][]netip.Addr{"ams3-1.example.dev": {netip.MustParseAddr("10.0.0.4"), netip.MustParseAddr("198.51.100.9")}}
	p.s.lookupMachine = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := dns[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	p.s.dialMachine = only("198.51.100.9:443")
	if address, why := reach(); why != "" || address != "ams3-1.example.dev" {
		t.Fatal(address, why)
	}
	dns["ams3-1.example.dev"] = []netip.Addr{netip.MustParseAddr("10.0.0.4")}
	if _, why := reach(); !strings.Contains(why, "ams3-1.example.dev has no public address") {
		t.Fatal(why)
	}
	delete(dns, "ams3-1.example.dev")
	if _, why := reach(); !strings.Contains(why, "can’t find ams3-1.example.dev") {
		t.Fatal(why)
	}
}

// The reach and machine/usage run at the same time (#7397): here the domain's lookup answers
// only once the machine was asked for its usage, so run one after the other it would fail.
func TestEligibleFactsAskAtTheSameTime(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	usageAsked := make(chan struct{})
	var once sync.Once
	p.asked = func(_, method string, _ json.RawMessage) (any, bool) {
		if method == wire.CallMachineUsage.Name {
			once.Do(func() { close(usageAsked) })
			return goodUsage(), true
		}
		return nil, false
	}
	box := p.host("MACHINE-2")
	p.s.router.mu.Lock()
	p.s.router.hosts[box.member.Host].conn.(*Conn).Remote = "203.0.113.7"
	p.s.router.mu.Unlock()
	p.s.router.SetControlAddress(box.member.Host, &wire.MachineControlAddress{Domain: new("ams3-1.example.dev"), Port: 443})
	p.s.lookupMachine = func(ctx context.Context, _ string) ([]netip.Addr, error) {
		select {
		case <-usageAsked:
			return []netip.Addr{netip.MustParseAddr("198.51.100.9")}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.s.dialMachine = func(context.Context, string) error { return nil }
	f := p.s.eligibleFacts(context.Background(), records.MachineRecord{ID: box.member.Host}, false)
	if f.unreachable != "" || f.address != "ams3-1.example.dev" || f.usage == nil {
		t.Fatalf("%q %q %v", f.address, f.unreachable, f.usage)
	}
}

// A hello names a port and a domain name, nothing else (#7313).
func TestValidControlAddress(t *testing.T) {
	for _, c := range []struct {
		in   *wire.MachineControlAddress
		want string
	}{
		{nil, "none"},
		{&wire.MachineControlAddress{Port: 0}, "none"},
		{&wire.MachineControlAddress{Port: 70000}, "none"},
		{&wire.MachineControlAddress{Port: 8791}, ":8791"},
		{&wire.MachineControlAddress{Port: 443, Domain: new(" AMS3-1.Example.dev. ")}, "ams3-1.example.dev:443"},
		{&wire.MachineControlAddress{Port: 443, Domain: new("203.0.113.7")}, "none"},
		{&wire.MachineControlAddress{Port: 443, Domain: new("localhost")}, "none"},
		{&wire.MachineControlAddress{Port: 443, Domain: new("evil.example/path")}, "none"},
		{&wire.MachineControlAddress{Port: 443, Domain: new("-a.example.dev")}, "none"},
	} {
		got := "none"
		if a := validControlAddress(c.in); a != nil {
			got = ":" + fmt.Sprint(a.Port)
			if a.Domain != nil {
				got = *a.Domain + got
			}
		}
		if got != c.want {
			t.Errorf("%+v: %s, want %s", c.in, got, c.want)
		}
	}
}

// The answer says the machine's address, memory and fullest disk (#7313).
func TestEligibleAnswerSaysTheMachine(t *testing.T) {
	usage := goodUsage()
	usage.Disks = append(usage.Disks, wire.DiskAlarm{Reading: wire.DiskReading{Mount: "/data", FreeBytes: 30_000_000_000}, Level: wire.DiskLevelOk})
	got := eligibleAnswer(eligibleFacts{address: "ams3-1.example.dev", usage: usage}, nil)
	if !got.Ok || got.Address == nil || *got.Address != "ams3-1.example.dev" ||
		got.MemoryBytes == nil || *got.MemoryBytes != 2_000_000_000 || got.DiskFreeBytes == nil || *got.DiskFreeBytes != 30_000_000_000 {
		t.Fatalf("%+v", got)
	}
	if got := eligibleAnswer(eligibleFacts{unreachable: "It is offline."}, []wire.ControlEligibleReason{{}}); got.Ok || got.Address != nil || got.MemoryBytes != nil || got.DiskFreeBytes != nil {
		t.Fatalf("%+v", got)
	}
}

// A host's hello says where its control role serves, and the next hello with none forgets it.
func TestHelloKeepsTheControlAddress(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	box := p.host("MACHINE-5")
	hello := func(control *wire.MachineControlAddress) {
		params, _ := json.Marshal(wire.MachineHello{Version: "0.6.0", Platform: "linux x86_64", MachineID: "MACHINE-5", Control: control})
		if _, err := p.s.methods.HostSaid(box.member.Host, "machine/hello", params); err != nil {
			t.Fatal(err)
		}
	}
	hello(&wire.MachineControlAddress{Domain: new("Ams3-1.example.dev"), Port: 8443})
	if a := p.s.router.ControlAddress(box.member.Host); a == nil || a.Port != 8443 || a.Domain == nil || *a.Domain != "ams3-1.example.dev" {
		t.Fatalf("%+v", a)
	}
	hello(&wire.MachineControlAddress{Domain: new("not a name"), Port: 443})
	if a := p.s.router.ControlAddress(box.member.Host); a != nil {
		t.Fatalf("a bad domain is kept: %+v", a)
	}
	hello(&wire.MachineControlAddress{Port: 9000})
	hello(nil)
	if a := p.s.router.ControlAddress(box.member.Host); a != nil {
		t.Fatalf("a hello with none keeps %+v", a)
	}
}
