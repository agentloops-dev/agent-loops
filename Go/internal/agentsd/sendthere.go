package agentsd

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"slices"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// start_session with send (#2929): before the session starts on another machine, the
// branch the agent is on, with its uncommitted changes, goes to the folder there, as the
// window's Send to ‹machine›… does (send.go, receive.go). This host packs it and sends each
// part through the control plane (machines/sendFolder), which hands it to that machine's
// folders/receiveUpload; folders/receive there then makes the copy, or brings it up to
// date. It never overwrites work done there: receive refuses first.

const sendFolderMethod = "machines/sendFolder"

// sendThereParts is the most of a file one machines/sendFolder carries: well under the
// control plane's message limit once it is base64.
const sendThereParts = 4 << 20

// sendingBranch is what start_session sends: the repository the agent works in, the
// branch it is on and its origin, and the heads the folder there has already.
type sendingBranch struct {
	path, branch string
	origin       *string
	heads        []string
}

// branchToSend is the branch the agent works on, or why it can't be sent.
func (d *Daemon) branchToSend(me *wire.Agent) (sendingBranch, error) {
	cwd := store.PathURL(store.FolderPath(me.Cwd))
	path, err := sendRepository(cwd)
	if err != nil {
		return sendingBranch{}, err
	}
	facts, err := d.sendFacts(wire.SendFactsRequest{Folder: cwd})
	if err != nil {
		return sendingBranch{}, err
	}
	if facts.Current == nil || *facts.Current == "" {
		return sendingBranch{}, Invalid("%s is on no branch. Check out a branch to send.", shortPath(path))
	}
	return sendingBranch{path: path, branch: *facts.Current, origin: facts.Origin}, nil
}

// readyThere is the heads folder on machine has, which the send can leave out, or why
// it can't take b: it is offline, or the folder has work of its own. Asked before the
// person is, so they are never asked about what can't be done (#855).
func (d *Daemon) readyThere(me *wire.Agent, machine, folder string) ([]string, error) {
	var state wire.MachineFolderSent
	if err := d.askSend(me, wire.MachineFolderSend{Host: machine, State: &wire.ReceiveStateRequest{Folder: folder}}, &state); err != nil {
		return nil, err
	}
	if state.State == nil {
		return nil, nil
	}
	if state.State.Exists && !state.State.Repository {
		return nil, &Error{Code: wire.FailureFolderInTheWay, Message: folder + " is already there and is not a Git repository."}
	}
	if state.State.OwnChanges {
		return nil, &Error{Code: wire.FailureFolderInTheWay, Message: folder + " has changes of its own there."}
	}
	return state.State.Heads, nil
}

// sendThere sends b to folder on machine, leaving out the commits it has; me asks.
func (d *Daemon) sendThere(me *wire.Agent, machine, folder string, b sendingBranch) error {
	_, err := d.packThere(me, machine,
		wire.SendPackRequest{Folder: store.PathURL(b.path), Branches: []string{b.branch}, LeavingOut: b.heads, IncludeChanges: true},
		wire.ReceiveRequest{Folder: folder, Checkout: b.branch, Origin: b.origin}, nil)
	return err
}

// packThere makes what pack asks for, sends it to machine, and has receive make or bring up
// to date the folder there with it, which it answers with; me asks. sent, when not nil,
// hears the bytes sent and the total after each part (#4613), and before that the bytes the
// machine got from the folder's origin (#7119).
func (d *Daemon) packThere(me *wire.Agent, machine string, pack wire.SendPackRequest, receive wire.ReceiveRequest,
	sent func(done, total int64)) (wire.FolderSummary, error) {
	if receive.Origin != nil {
		origin := withoutCredential(*receive.Origin)
		receive.Origin = &origin
	}
	// The folder there gets what its origin has from there (#7119), and the bundle the rest.
	heads, seed := d.seedThere(me, machine, pack, receive, sent)
	if seed == nil {
		return d.packRest(me, machine, pack, receive, sent)
	}
	pack.LeavingOut = append(slices.Clone(pack.LeavingOut), heads...)
	receive.Seed = seed
	made, err := d.packRest(me, machine, pack, receive, sent)
	if err != nil {
		stop := wire.ReceiveSeedRequest{Seed: *seed, Folder: receive.Folder, Origin: *receive.Origin, Stop: new(true)}
		if err := d.askSend(me, wire.MachineFolderSend{Host: machine, Seed: &stop}, nil); err != nil {
			log.Printf("send: could not stop a seed: %v", err)
		}
	}
	return made, err
}

// packRest is packThere once the machine has what the folder's origin gave it.
func (d *Daemon) packRest(me *wire.Agent, machine string, pack wire.SendPackRequest, receive wire.ReceiveRequest,
	sent func(done, total int64)) (wire.FolderSummary, error) {
	packed, err := d.sendPack(pack)
	for _, f := range []*wire.SendFile{packed.Bundle, packed.Patch} {
		if f != nil {
			defer func(id wire.UUID) {
				if file, err := d.outgoingFile(id); err == nil {
					os.Remove(file)
				}
			}(f.ID)
		}
	}
	if err != nil {
		return wire.FolderSummary{}, err
	}
	var total, done int64
	for _, f := range []*wire.SendFile{packed.Bundle, packed.Patch} {
		if f != nil {
			total += f.Size
		}
	}
	part := func(n int64) {
		done += n
		if sent != nil {
			sent(done, total)
		}
	}
	part(0)
	receive.Branches = packed.Commits
	if packed.Bundle != nil {
		id, err := d.uploadThere(me, machine, packed.Bundle.ID, part)
		if err != nil {
			return wire.FolderSummary{}, err
		}
		receive.Bundle = &id
	}
	if packed.Patch != nil {
		id, err := d.uploadThere(me, machine, packed.Patch.ID, part)
		if err != nil {
			return wire.FolderSummary{}, err
		}
		receive.Patch = &id
	}
	var made wire.MachineFolderSent
	if err := d.hostCtx().Err(); err != nil {
		return wire.FolderSummary{}, &Error{Code: wire.FailureNotSupported, Message: "this machine stopped before the send was done."}
	}
	if err := d.askSend(me, wire.MachineFolderSend{Host: machine, Receive: &receive}, &made); err != nil {
		return wire.FolderSummary{}, err
	}
	if made.Folder == nil {
		return wire.FolderSummary{}, nil
	}
	return *made.Folder, nil
}

// uploadThere sends the outgoing file id to machine in parts, and is the upload's id there.
// sent, when not nil, hears the size of each part once it is there.
func (d *Daemon) uploadThere(me *wire.Agent, machine string, id wire.UUID, sent func(int64)) (wire.UUID, error) {
	file, err := d.outgoingFile(id)
	if err != nil {
		return "", err
	}
	f, err := os.Open(file)
	if err != nil {
		return "", fileError(CodeFileNotReadable, err.Error())
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fileError(CodeFileNotReadable, err.Error())
	}
	upload, size := store.NewUUID(), fi.Size()
	atOnce := 1
	part := func(ctx context.Context, n int64) error {
		offset := n * sendThereParts
		buf := make([]byte, min(sendThereParts, size-offset))
		if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
			return fileError(CodeFileNotReadable, err.Error())
		}
		answer, err := d.sendPartStep(ctx, me, wire.MachineFolderSend{Host: machine,
			Upload: &wire.ReceiveUploadRequest{Upload: upload, Offset: offset, Data: buf}})
		if err == nil && n == 0 {
			atOnce = partsAtOnce(answer)
		}
		return err
	}
	done := func(n int64) {
		if sent != nil {
			sent(min(sendThereParts, size-n*sendThereParts))
		}
	}
	// The first part alone: it makes the file there, and its answer says how many at once.
	parts := (size + sendThereParts - 1) / sendThereParts
	if err := d.inParts(0, min(parts, 1), 1, part, done); err != nil {
		return "", err
	}
	if err := d.inParts(1, parts, atOnce, part, done); err != nil {
		return "", err
	}
	return upload, nil
}

// askSend is one machines/sendFolder; out, when not nil, takes its answer.
func (d *Daemon) askSend(me *wire.Agent, step wire.MachineFolderSend, out *wire.MachineFolderSent) error {
	return d.askSendCtx(d.ctx, me, step, out)
}

func (d *Daemon) askSendCtx(ctx context.Context, me *wire.Agent, step wire.MachineFolderSend, out *wire.MachineFolderSent) error {
	raw, err := d.askWorkspacesCtx(ctx, me.ID, sendFolderMethod, step)
	if err != nil || out == nil {
		return err
	}
	if json.Unmarshal(raw, out) != nil {
		return &Error{Code: wire.FailureNotSupported, Message: "the control plane's answer could not be read."}
	}
	return nil
}
