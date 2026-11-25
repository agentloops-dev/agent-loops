package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/capacity"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlsetup"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// control/eligible (#6611, epic #6608): whether a machine can take the control role. It
// gathers facts, changes nothing, and fails closed: a fact the machine does not say refuses it.

const (
	// controlMemoryFloor is the machine that the docs call enough, 1 GB: the kernel of one
	// reports a little less than it has.
	controlMemoryFloor = 900_000_000
	// controlDiskFloor is the free disk needed on top of the size of the store.
	controlDiskFloor = 2_000_000_000
	eligiblePatience = 5 * time.Second
	eligibleDial     = 3 * time.Second
)

// eligibleFacts is what control/eligible knows of a machine.
type eligibleFacts struct {
	record     records.MachineRecord
	singleUser bool
	allowSleep bool
	// unreachable is why the control plane cannot reach it, "" when it can.
	unreachable string
	// idleMinutes is after how long its provider stops it, 0 for never.
	idleMinutes int64
	// usage is nil when the machine did not say it.
	usage *wire.MachineUsageState
	// level is the last one it pushed, for when usage has none.
	level        wire.CapacityLevel
	storeBytes   int64
	planeVersion string
}

// eligibleReasons is each reason the machine cannot take the control role, in the order a
// person reads them.
func eligibleReasons(f eligibleFacts) []wire.ControlEligibleReason {
	out := []wire.ControlEligibleReason{}
	add := func(reason wire.ControlIneligibility, format string, args ...any) {
		out = append(out, wire.ControlEligibleReason{Reason: reason, Text: fmt.Sprintf(format, args...)})
	}
	if f.unreachable != "" {
		add(wire.ControlIneligibilityNotReachable, "%s", f.unreachable)
	}
	if strings.HasPrefix(strings.ToLower(f.record.Platform), "macos") && (!f.singleUser || !f.allowSleep) {
		words := "It is a Mac, and a Mac can sleep."
		if f.singleUser {
			words = "It is a Mac, and a Mac can sleep. Nothing reaches your agents while it does."
		}
		add(wire.ControlIneligibilityMacSleeps, "%s", words)
	}
	if f.idleMinutes > 0 {
		add(wire.ControlIneligibilityStopsWhenIdle, "It stops when it is idle (after %d min).", f.idleMinutes)
	}
	for _, words := range belowFloor(f) {
		add(wire.ControlIneligibilityBelowFloor, "%s", words)
	}
	if !f.singleUser && f.record.HasRole(records.RoleAgents) {
		add(wire.ControlIneligibilityBothRoles, "It runs agents. With more than one person, the control plane needs a machine that runs no agents: an agent there could read the control key.")
	}
	if olderThan(f.record.Version, f.planeVersion) {
		add(wire.ControlIneligibilityOlderVersion, "It runs version %s, older than this control plane (%s). Update it first.", f.record.Version, f.planeVersion)
	}
	return out
}

// belowFloor is how the machine is below the floor, or near or at its capacity.
func belowFloor(f eligibleFacts) []string {
	if f.usage == nil {
		// A machine that cannot be reached says nothing: that is the reason, not its floor.
		if f.unreachable != "" {
			return nil
		}
		return []string{"It did not say how much memory and disk it has. Update it, then try again."}
	}
	var out []string
	u := f.usage.Machine
	if u.MemoryTotal < controlMemoryFloor {
		out = append(out, fmt.Sprintf("It has %s of memory. The control plane needs 1 GB.", gigabytes(u.MemoryTotal)))
	}
	if len(f.usage.Disks) == 0 {
		out = append(out, "It did not say how much disk it has. Try again in a minute.")
	}
	need := controlDiskFloor + f.storeBytes
	for _, d := range f.usage.Disks {
		switch {
		case d.Reading.FreeBytes < need:
			out = append(out, fmt.Sprintf("Its disk %s has %s free. The control plane needs %s.", d.Reading.Mount, gigabytes(d.Reading.FreeBytes), gigabytes(need)))
		case d.Level != wire.DiskLevelOk:
			out = append(out, fmt.Sprintf("Its disk %s is low: %s free.", d.Reading.Mount, gigabytes(d.Reading.FreeBytes)))
		}
	}
	level := f.level
	if u.Capacity != nil {
		level = *u.Capacity
	}
	switch capacity.Rank(level) {
	case 1:
		out = append(out, "It is near capacity.")
	case 2:
		out = append(out, "It is at capacity.")
	}
	return out
}

func gigabytes(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

// olderThan says version is older than plane, by their first three numbers. A control plane
// with no numbers (a build from source) is older than nothing here; a machine with none is
// older than one with them.
func olderThan(version, plane string) bool {
	first := func(v string) []int {
		parts := versionParts(v)
		return parts[:min(3, len(parts))]
	}
	p := first(plane)
	return len(p) > 0 && slices.Compare(first(version), p) < 0
}

// controlEligible is control/eligible.
func (m *Methods) controlEligible(ctx context.Context, params json.RawMessage, from Caller) (any, error) {
	request, err := require[wire.ControlEligibleRequest](params, "ControlEligibleRequest")
	if err != nil {
		return nil, err
	}
	if !m.s.isAdmin(m.callerPerson(from)) {
		return nil, fail(notPermitted, "Only an admin can move the control plane.")
	}
	if m.s.cfg.Hosted {
		return nil, fail(notPermitted, "A machine of yours can’t run a hosted control plane.")
	}
	h := m.records.Host(string(request.Machine))
	if h == nil {
		return nil, fail(noSuchHost, "No machine is called %s.", request.Machine)
	}
	if h.ID == m.s.controlMachineID() {
		return nil, fail(invalidParams, "%s already runs this control plane.", h.Name)
	}
	reasons := eligibleReasons(m.s.eligibleFacts(ctx, *h, request.AllowSleep != nil && *request.AllowSleep))
	return wire.ControlEligible{Ok: len(reasons) == 0, Reasons: reasons}, nil
}

// eligibleFacts reads what control/eligible judges h by.
func (s *Service) eligibleFacts(ctx context.Context, h records.MachineRecord, allowSleep bool) eligibleFacts {
	f := eligibleFacts{record: h, singleUser: !s.isTeam(), allowSleep: allowSleep, planeVersion: s.cfg.Version}
	_, f.storeBytes = s.metrics.storeSize()
	if n, ok := s.capacityOf(h.ID); ok {
		f.level = n.level
	}
	if ws := s.router.Workspace(h.ID); ws != nil {
		for _, minutes := range []*int64{ws.StopWhenIdleMinutes, ws.IdleTimeoutMinutes} {
			if minutes != nil && *minutes > 0 && (f.idleMinutes == 0 || *minutes < f.idleMinutes) {
				f.idleMinutes = *minutes
			}
		}
	}
	f.unreachable = s.whyUnreachable(ctx, h.ID)
	if answer := s.router.Ask(h.ID, wire.CallMachineUsage.Name, wire.Empty{}, eligiblePatience); answer != nil && len(answer.Result) > 0 {
		var usage wire.MachineUsageState
		if json.Unmarshal(answer.Result, &usage) == nil {
			f.usage = &usage
		}
	}
	return f
}

// whyUnreachable is why this copy cannot reach the machine at its tunnel or at the public
// address its uplink comes from, "" when it can. Only that address and the control plane's
// own ports are tried, so the call cannot be used to look at other machines.
func (s *Service) whyUnreachable(ctx context.Context, host string) string {
	if state, ok := s.router.State(host); !ok || !state.online() {
		return "It is offline."
	}
	if s.tunnels != nil {
		if t := s.tunnels.State(host); t != nil && t.Up {
			return ""
		}
	}
	from, here := s.router.UplinkAddress(host)
	if !here {
		return "It is connected to another copy of this control plane, which can’t check it from here."
	}
	addr, err := netip.ParseAddr(from)
	if err != nil || !publicAddress(addr) {
		return "Agent Loops can’t reach this machine from outside: it has no public address."
	}
	dial := s.dialMachine
	if dial == nil {
		dial = dialTCP
	}
	ctx, cancel := context.WithTimeout(ctx, eligibleDial)
	defer cancel()
	ports := []string{"443", fmt.Sprint(controlsetup.DefaultPort)}
	answered := make(chan bool, len(ports))
	for _, port := range ports {
		go func() { answered <- dial(ctx, net.JoinHostPort(addr.String(), port)) == nil }()
	}
	for range ports {
		if <-answered {
			return ""
		}
	}
	return fmt.Sprintf("Agent Loops can’t reach this machine from outside: nothing answers at %s on port %s. Run agentsd install --control there, and open the port.",
		addr, strings.Join(ports, " or "))
}

// publicAddress says clients elsewhere can dial addr. 100.64/10 stays in: a machine is seen
// to come from it only inside a tailnet, which is a tunnel.
func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsGlobalUnicast() && !addr.IsPrivate()
}

func dialTCP(ctx context.Context, address string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

// UplinkAddress is the address the host's uplink comes from; false when it does not end at
// this copy.
func (r *Router) UplinkAddress(host string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.hosts[host]
	if h == nil || !h.local {
		return "", false
	}
	if c, ok := h.conn.(*Conn); ok {
		return c.Remote, true
	}
	return "", true
}
