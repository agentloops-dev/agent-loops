package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/capacity"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
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
	// address is where it reached its control role, "" when it did not or by its tunnel.
	address string
	// idleMinutes is after how long its provider stops it, 0 for never.
	idleMinutes int64
	// usage is nil when the machine did not say it.
	usage *wire.MachineUsageState
	// level is the last one it pushed, for when usage has none.
	level        wire.CapacityLevel
	storeBytes   int64
	storeRecords int64
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
	facts := m.s.eligibleFacts(ctx, *h, request.AllowSleep != nil && *request.AllowSleep)
	reasons := eligibleReasons(facts)
	return eligibleAnswer(facts, reasons), nil
}

// eligibleAnswer is control/eligible's answer: the reasons, and what the screen shows of the
// machine (#7313): where it was reached, its memory and the free space on its fullest disk.
func eligibleAnswer(f eligibleFacts, reasons []wire.ControlEligibleReason) wire.ControlEligible {
	out := wire.ControlEligible{Ok: len(reasons) == 0, Reasons: reasons,
		FrozenSeconds: new(frozenSeconds(f.storeRecords, f.storeBytes))}
	if f.address != "" {
		out.Address = new(f.address)
	}
	if f.usage != nil {
		out.MemoryBytes = new(f.usage.Machine.MemoryTotal)
		for i, d := range f.usage.Disks {
			if i == 0 || d.Reading.FreeBytes < *out.DiskFreeBytes {
				out.DiskFreeBytes = new(d.Reading.FreeBytes)
			}
		}
	}
	return out
}

// eligibleFacts reads what control/eligible judges h by.
func (s *Service) eligibleFacts(ctx context.Context, h records.MachineRecord, allowSleep bool) eligibleFacts {
	f := eligibleFacts{record: h, singleUser: !s.isTeam(), allowSleep: allowSleep, planeVersion: s.cfg.Version}
	f.storeRecords, f.storeBytes = s.metrics.storeSize()
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
	f.address, f.unreachable = s.reach(ctx, h.ID)
	if answer := s.router.Ask(h.ID, wire.CallMachineUsage.Name, wire.Empty{}, eligiblePatience); answer != nil && len(answer.Result) > 0 {
		var usage wire.MachineUsageState
		if json.Unmarshal(answer.Result, &usage) == nil {
			f.usage = &usage
		}
	}
	return f
}

// reach is where this copy reached the machine's control role, as a person reads it, and
// why it could not, "" when it could (#7313). A machine with the control role said its port,
// and its domain when it has one; one with none is tried on the control plane's own ports.
// Only the machine's own address is tried: its domain, when it said one and the domain has a
// public address, else the public address its uplink comes from. So the call cannot be used
// to look at other machines. Its tunnel, when up, reaches it with no address to show.
func (s *Service) reach(ctx context.Context, host string) (address, why string) {
	if state, ok := s.router.State(host); !ok || !state.online() {
		return "", "It is offline."
	}
	if s.tunnels != nil {
		if t := s.tunnels.State(host); t != nil && t.Up {
			return "", ""
		}
	}
	from, here := s.router.UplinkAddress(host)
	if !here {
		return "", "It is connected to another copy of this control plane, which can’t check it from here."
	}
	control := s.router.ControlAddress(host)
	name, ip, why := s.reachAt(ctx, from, control)
	if why != "" {
		return "", why
	}
	ports := []int64{443, controlsetup.DefaultPort}
	if control != nil {
		ports = []int64{control.Port}
	}
	dial := s.dialMachine
	if dial == nil {
		dial = dialTCP
	}
	ctx, cancel := context.WithTimeout(ctx, eligibleDial)
	defer cancel()
	answered := make(chan int64, len(ports))
	for _, port := range ports {
		go func() {
			if dial(ctx, net.JoinHostPort(ip.String(), fmt.Sprint(port))) != nil {
				port = 0
			}
			answered <- port
		}()
	}
	for range ports {
		if port := <-answered; port != 0 {
			if port == 443 {
				return name, ""
			}
			return net.JoinHostPort(name, fmt.Sprint(port)), ""
		}
	}
	words := make([]string, len(ports))
	for i, port := range ports {
		words[i] = fmt.Sprint(port)
	}
	if control != nil {
		return "", fmt.Sprintf("Agent Loops can’t reach this machine’s control plane: nothing answers at %s on port %d. Open the port there.", name, control.Port)
	}
	return "", fmt.Sprintf("Agent Loops can’t reach this machine from outside: nothing answers at %s on port %s. Run agentsd install --control there, and open the port.",
		name, strings.Join(words, " or "))
}

// reachAt is the name to show and the address to dial for a machine whose uplink comes from
// from: its domain and the domain's first public address, when it said a domain; else its
// uplink's address. why is set when there is no public address to dial.
func (s *Service) reachAt(ctx context.Context, from string, control *wire.MachineControlAddress) (name string, ip netip.Addr, why string) {
	if control != nil && control.Domain != nil {
		domain := *control.Domain
		lookup := s.lookupMachine
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		ctx, cancel := context.WithTimeout(ctx, eligibleDial)
		defer cancel()
		addrs, err := lookup(ctx, domain)
		if err != nil || len(addrs) == 0 {
			return "", netip.Addr{}, fmt.Sprintf("Agent Loops can’t find %s, the domain of this machine’s control plane. Check its DNS record.", domain)
		}
		for _, a := range addrs {
			if publicAddress(a) {
				return domain, a.Unmap(), ""
			}
		}
		return "", netip.Addr{}, fmt.Sprintf("Agent Loops can’t reach this machine from outside: %s has no public address.", domain)
	}
	addr, err := netip.ParseAddr(from)
	if err != nil || !publicAddress(addr) {
		return "", netip.Addr{}, "Agent Loops can’t reach this machine from outside: it has no public address."
	}
	return addr.Unmap().String(), addr.Unmap(), ""
}

// validControlAddress is what a hello says of its control role, when it is a port and,
// if any, a domain name; else nil, so a hello cannot name a URL or another port's host.
func validControlAddress(a *wire.MachineControlAddress) *wire.MachineControlAddress {
	if a == nil || a.Port < 1 || a.Port > 65535 {
		return nil
	}
	out := &wire.MachineControlAddress{Port: a.Port}
	if a.Domain != nil {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(*a.Domain), "."))
		if !domainName(domain) {
			return nil
		}
		out.Domain = &domain
	}
	return out
}

// domainName says d is a DNS name of letters, digits, hyphens and dots, with two labels or
// more, and not an IP address.
func domainName(d string) bool {
	if len(d) == 0 || len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	if _, err := netip.ParseAddr(d); err == nil {
		return false
	}
	for label := range strings.SplitSeq(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// publicAddress says clients elsewhere can dial addr. 100.64/10 stays in: a machine is seen
// to come from it only inside a tailnet, which is a tunnel.
func publicAddress(addr netip.Addr) bool { return controllink.PublicAddress(addr) }

// helloAnswer is the answer to host's machine/hello: this control plane's capabilities, and
// the address the host's uplink comes from when it is public (#7493), so that an install of
// the control role on a machine behind NAT can say where it is reached.
func (m *Methods) helloAnswer(host string) wire.MachineHelloAnswer {
	answer := wire.MachineHelloAnswer{Capabilities: capabilities()}
	if from, ok := m.router.UplinkAddress(host); ok {
		if addr, err := netip.ParseAddr(from); err == nil && publicAddress(addr) {
			answer.Address = new(addr.Unmap().String())
		}
	}
	return answer
}

func dialTCP(ctx context.Context, address string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

// SetControlAddress keeps where the host's control role serves, as its hello on its uplink
// here said (#7313). A new uplink starts with none, until its own hello.
func (r *Router) SetControlAddress(host string, address *wire.MachineControlAddress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h := r.hosts[host]; h != nil && h.local {
		h.control = address
	}
}

// ControlAddress is where the host's control role serves, as its uplink here said; nil when
// it said none, or its uplink is not here.
func (r *Router) ControlAddress(host string) *wire.MachineControlAddress {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h := r.hosts[host]; h != nil && h.local {
		return h.control
	}
	return nil
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

// The measures that a move's frozen time is judged by (#7395), from #7394's: the copy sends
// copyBatchRecords records or copyBatchBytes bytes a call and checks sumsBatchKeys keys a
// call, about 2 calls for 1,000 records, so 5,000 records over a link of 50 ms a call are
// frozen for under a second and 40,000 for about 3 s. On top: each record's read from this
// copy's store, the bytes over the link, and the take-over.
const (
	// frozenFixed is the freeze, the new copy's take-over and the first reconnects.
	frozenFixed = 5 * time.Second
	// frozenCall is one call's round trip over the handover session: #7394's slow link.
	frozenCall = 50 * time.Millisecond
	// frozenRead is one record's read from this copy's store.
	frozenRead = 100 * time.Microsecond
	// frozenBytesPerSecond is the link's speed, 10 MiB/s.
	frozenBytesPerSecond = 10 << 20
)

// frozenSeconds is about how long a move keeps the control plane frozen, for a store of
// that many records and bytes: the calls of the copy and its check, each record's read, and
// the bytes over the link, whole seconds rounded up.
func frozenSeconds(records, bytes int64) int64 {
	records, bytes = max(records, 0), max(bytes, 0)
	calls := ceilDiv(records, copyBatchRecords) + ceilDiv(bytes, copyBatchBytes) + ceilDiv(records, sumsBatchKeys)
	took := (frozenFixed + time.Duration(calls)*frozenCall + time.Duration(records)*frozenRead).Seconds() +
		float64(bytes)/frozenBytesPerSecond
	return int64(math.Ceil(took))
}

func ceilDiv(n, d int64) int64 { return (n + d - 1) / d }
