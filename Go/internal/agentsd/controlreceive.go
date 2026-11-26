package agentsd

import (
	"bytes"
	"encoding/json"
	"log"
	"os"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlsetup"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// control/receive (#6612): the control plane moves to this machine. It sends the control
// key sealed to this machine's own key, and this host opens it, keeps it for the machine's
// own copy (agents-control, the control role's unit, #6610) and starts that copy empty, to
// receive the store. Only the control plane asks it, on channel 0: it is no window's method.

// receiveControl is the step the copy is started again with; a test stands in for it.
func (d *Daemon) receiveControl(key []byte, move, url string) (string, string, error) {
	if d.ReceiveControl != nil {
		return d.ReceiveControl(key, url)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return controlsetup.Receive(home, key, move, url, controlsetup.Deps{Systemd: controlsetup.SystemdUser, Run: controlsetup.RunCommand})
}

// controlReceive is control/receive's answer, or its refusal in words.
func (d *Daemon) controlReceive(params json.RawMessage) any {
	var request wire.ControlReceiveRequest
	if err := json.Unmarshal(params, &request); err != nil || request.Move == "" || request.URL == "" {
		return controllink.AskFailure{Code: CodeInvalidParams, Message: "Say the move, the address and the key."}
	}
	d.orgRouteMu.Lock()
	id := d.identityLocked()
	d.orgRouteMu.Unlock()
	if id == nil {
		return controllink.AskFailure{Code: CodeNotPermitted, Message: "This machine has not joined a control plane."}
	}
	// The key sealed is the control plane's own: the one this host joined.
	d.orgMu.Lock()
	control := d.orgKey
	d.orgMu.Unlock()
	if len(control) > 0 && !bytes.Equal(control, request.ControlKey) {
		return controllink.AskFailure{Code: CodeNotPermitted, Message: "That is not the control plane this machine joined."}
	}
	key, err := controllink.Open(id.private, controllink.ControlMoveInfo, controllink.ControlMoveAD(request.Move, id.host, request.ControlKey), request.Key)
	if err != nil {
		return controllink.AskFailure{Code: CodeNotPermitted, Message: "The control key did not open here: it was sealed to another machine."}
	}
	if public, err := controllink.PublicKey(key); err != nil || !bytes.Equal(public, request.ControlKey) {
		return controllink.AskFailure{Code: CodeNotPermitted, Message: "The key sent is not the control plane's."}
	}
	url, pin, err := d.receiveControl(key, request.Move, request.URL)
	clear(key)
	if err != nil {
		log.Printf("control/receive: %v", err)
		return controllink.AskFailure{Code: CodeInternal, Message: err.Error()}
	}
	log.Printf("control/receive: this machine's copy is empty and receives the control plane at %s", url)
	out := wire.ControlReceive{URL: url}
	if pin != "" {
		out.Pin = &pin
	}
	return out
}
