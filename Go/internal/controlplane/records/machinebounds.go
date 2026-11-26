package records

import (
	"bytes"
	"context"
	"fmt"

	"github.com/agentloops-dev/agent-loops/Go/internal/sizewords"
)

// The machines' bounds (#6758, epic #6669). They are a person's data, so a write over a
// limit is refused, and nothing is evicted to make room. A write that keeps a joined
// machine in use (its key renewed, its epoch, its hello, its workspace's state) is never
// refused for room: it would cut the machine off. Room for a hello is kept at its join.
// A record with only the control role does not count (#7304): the control plane makes one
// for its own machine, and nobody can remove it.
const (
	// MachineMostBytes is the largest machine record.
	MachineMostBytes = 32 << 10
	// MachinesMostBytes is the most an organisation's machines come to (#6669). Tombstones,
	// gone in a week, may come to as much again: a removal makes room at once.
	MachinesMostBytes = 1 << 20
	// machineHelloBytes is what a new machine takes with its first hello, which says its
	// capabilities.
	machineHelloBytes = 4 << 10
)

// MachineLimitError is a write refused at a limit, in the words a person reads.
type MachineLimitError struct{ Words string }

func (e *MachineLimitError) Error() string { return e.Words }

// MachineRoom refuses a new machine when the machines are at their limit. One joined
// already with the key has its room.
func (r *Records) MachineRoom(ctx context.Context, publicKey []byte) error {
	for _, h := range r.Hosts() {
		if len(publicKey) > 0 && bytes.Equal(h.PublicKey, publicKey) {
			return nil
		}
	}
	return r.machinesWithin(ctx, machineHelloBytes)
}

// machinesWithin refuses when the machines and grow more bytes are over MachinesMostBytes.
func (r *Records) machinesWithin(ctx context.Context, grow int) error {
	listed, err := r.Store.List(ctx, MachinesPrefix)
	if err != nil {
		return err
	}
	live, all := int64(grow), int64(grow)
	r.mu.Lock()
	control := map[string]bool{}
	for id, h := range r.hosts {
		if h.record.ControlOnly() {
			control[HostKey(id)] = true
		}
	}
	for _, k := range listed {
		if control[k.Key] {
			continue
		}
		size := k.Size
		if size < 0 {
			size = MachineMostBytes
		}
		all += size
		if r.tombstones[k.Key] != k.ETag {
			live += size
		}
	}
	r.mu.Unlock()
	if live > MachinesMostBytes {
		return &MachineLimitError{"This control plane keeps as many machines as it can. Remove one that is not used, then try again."}
	}
	if all > 2*MachinesMostBytes {
		return &MachineLimitError{"Too many machines were removed in the last week. Try again in a few days."}
	}
	return nil
}

// newMachineWithin refuses a new machine's record over either limit.
func (r *Records) newMachineWithin(ctx context.Context, h MachineRecord) error {
	size, err := machineSize(h)
	if err != nil {
		return err
	}
	if size > MachineMostBytes {
		return machineTooLarge(size)
	}
	if h.ControlOnly() {
		return nil
	}
	return r.machinesWithin(ctx, max(size, machineHelloBytes))
}

// machineChangeWithin refuses a change that grows a machine's record past
// MachineMostBytes, and one that grows what a person set on it (its labels and what is lent
// to it) while the machines are at their limit. A renewal, an owner and an epoch always pass.
// A control-only record that gets the agents role counts from now on, as a new machine.
func (r *Records) machineChangeWithin(ctx context.Context, before, after MachineRecord) error {
	if before.ControlOnly() && !after.ControlOnly() {
		return r.newMachineWithin(ctx, after)
	}
	was, err := machineSize(before)
	if err != nil {
		return err
	}
	// What the control plane itself notes does not count as growth.
	noted := after
	noted.Rev, noted.Expires, noted.KnownEpoch, noted.Owner = before.Rev, before.Expires, before.KnownEpoch, before.Owner
	size, err := machineSize(noted)
	if err != nil {
		return err
	}
	if size <= was {
		return nil
	}
	if size > MachineMostBytes {
		return machineTooLarge(size)
	}
	if after.ControlOnly() {
		return nil
	}
	set := func(h MachineRecord) (int, error) {
		return machineSize(MachineRecord{Labels: h.Labels, SignInFrom: h.SignInFrom, GitFrom: h.GitFrom})
	}
	had, err := set(before)
	if err != nil {
		return err
	}
	has, err := set(after)
	if err != nil {
		return err
	}
	if has <= had {
		return nil
	}
	return r.machinesWithin(ctx, size-was)
}

func machineSize(h MachineRecord) (int, error) {
	data, err := Encode(h)
	return len(data), err
}

func machineTooLarge(size int) error {
	return &MachineLimitError{fmt.Sprintf("This machine's details are too large to keep: %s, and the most is %s. Remove some of its labels, or some of what is lent to it.", sizewords.Int(size), sizewords.Int(MachineMostBytes))}
}

// RemoveHostIf removes a machine that still passes still, read again if another copy wrote
// it first: one renewed or linked to a workspace there meanwhile stays. It says whether it went.
func (r *Records) RemoveHostIf(ctx context.Context, id string, still func(MachineRecord) bool) (bool, error) {
	return r.removeHost(ctx, id, still)
}
