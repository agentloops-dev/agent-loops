package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
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
func TestWhyUnreachableDialsTheUplinksAddress(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	box := p.host("MACHINE-2")
	p.s.router.mu.Lock()
	p.s.router.hosts[box.member.Host].conn.(*Conn).Remote = "203.0.113.7"
	p.s.router.mu.Unlock()
	answers := "203.0.113.7:8791"
	p.s.dialMachine = func(_ context.Context, address string) error {
		if address == answers {
			return nil
		}
		return errors.New("refused")
	}
	if why := p.s.whyUnreachable(context.Background(), box.member.Host); why != "" {
		t.Fatal(why)
	}
	answers = ""
	if why := p.s.whyUnreachable(context.Background(), box.member.Host); !strings.Contains(why, "nothing answers at 203.0.113.7 on port 443 or 8791") {
		t.Fatal(why)
	}
}
