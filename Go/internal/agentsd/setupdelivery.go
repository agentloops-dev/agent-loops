package agentsd

import (
	"context"
	"errors"
	"fmt"

	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The target's check of a set-up script that another machine sends it (#7167, gate 1 of #7151,
// section 3). The machine takes the text only when all of these are true:
//
//  1. a client of its person's lock signed the approval, and the hash and size are the text's;
//  2. the approval is for this machine by name, or for machines as they are made and this
//     machine is one (made: the caller keeps whether it has taken an automatic send yet);
//  3. its number is not lower than the last one taken here for that script;
//  4. a machine of the lock signed the request that brings it, for this machine.
//
// takeSetupDelivery notes the number before the text is used.

// errNotFromAMachine is a delivery that no machine of the lock signed for this one.
var errNotFromAMachine = errors.New("a set-up script comes signed by one of your machines, for this one")

// checkSetupDelivery checks a delivery of script with text and its approval, from the request a
// handler answers (ctx), and gives back the client that approved it. It notes nothing.
func (d *Daemon) checkSetupDelivery(ctx context.Context, script, text string, approval wire.LockRecord, made bool) (machinelock.Member, error) {
	return d.setupDelivery(ctx, script, text, approval, made, false)
}

// takeSetupDelivery is checkSetupDelivery, and notes the approval's number before it answers.
func (d *Daemon) takeSetupDelivery(ctx context.Context, script, text string, approval wire.LockRecord, made bool) (machinelock.Member, error) {
	return d.setupDelivery(ctx, script, text, approval, made, true)
}

func (d *Daemon) setupDelivery(ctx context.Context, script, text string, approval wire.LockRecord, made, take bool) (machinelock.Member, error) {
	signer, ok := requestSignerOf(ctx)
	if !ok || signer.member.Kind != wire.LockEntryKindAddMachine || signer.to == "" || signer.to != d.ownHostID() {
		return machinelock.Member{}, notInLock(errNotFromAMachine.Error())
	}
	file, most, known := setupScriptFile(script)
	switch {
	case !known:
		return machinelock.Member{}, Invalid("There is no set-up script %q.", script)
	case int64(len(text)) > most || text == "":
		return machinelock.Member{}, Invalid("%s is empty or over %d KiB.", file, most>>10)
	case approval.Kind != machinelock.KindSetupScript || approval.Subject != script || machinelock.Removed(approval):
		return machinelock.Member{}, notInLock("the approval is not of " + file)
	}
	a, err := machinelock.SetupScriptOf(approval.Content)
	if err != nil {
		return machinelock.Member{}, notInLock(err.Error())
	}
	if a.Hash != scriptHash(text) || a.Size != int64(len(text)) {
		return machinelock.Member{}, notInLock("the approval is of another text of " + file)
	}
	var client machinelock.Member
	if take {
		client, err = d.takeRecord(approval, made)
	} else {
		client, err = d.checkRecord(approval, made)
	}
	if err != nil {
		return machinelock.Member{}, notInLock(fmt.Sprintf("the approval of %s: %v", file, err))
	}
	return client, nil
}
