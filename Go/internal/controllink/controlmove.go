package controllink

import "fmt"

// ControlMoveInfo is the HPKE info of the control key that control/move seals to the
// machine it moves to (#6612).
const ControlMoveInfo = "agents.control-move.v1"

// ControlMoveAD is the associated data of that seal: the move, the machine and the control
// key's public half. A seal moved to another move or machine, or one of another control
// plane, does not open.
func ControlMoveAD(move, machine string, controlPublic []byte) []byte {
	return fmt.Appendf(nil, "controlmove.v1|%s|%s|%s", move, machine, B64URL(controlPublic))
}

// AskFailure is what Asked answers to refuse an ask, as a JSON-RPC error.
type AskFailure struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
