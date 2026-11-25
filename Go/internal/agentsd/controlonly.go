package agentsd

import (
	"encoding/json"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A machine that runs the control plane and no agents (#7314, epic #6608) joins the current
// control plane all the same, so the store copy and the handover have a link to it. That link
// is all it keeps: it takes no sessions, folders, providers or workflows.

// NoAgentsRefusal is what a control-only machine answers each request for agent work.
const NoAgentsRefusal = "This machine runs the control plane and no agents: it takes no sessions, folders or other agent work."

// controlOnlyAsks are the control plane's channel-0 asks that a control-only machine answers:
// only what reads the machine itself, for control/eligible (#6611). The store copy (#6612)
// adds its own here.
var controlOnlyAsks = map[string]bool{
	wire.CallMachineUsage.Name: true,
}

// AskedControlOnly is Asked on a control-only machine: the asks of controlOnlyAsks, and no
// other, which it leaves unanswered as a host that does not know the method.
func (d *Daemon) AskedControlOnly(method string, params json.RawMessage) (any, bool) {
	if !controlOnlyAsks[method] {
		return nil, false
	}
	return d.Asked(method, params)
}

// RefuseAgentWork is Accept on a control-only machine: a channel the control plane opens
// gets each request refused, and no notification is read.
func RefuseAgentWork(conn controllink.Conn) {
	for line := range conn.In() {
		var m message
		if json.Unmarshal([]byte(line), &m) != nil || m.Method == "" || len(m.ID) == 0 {
			continue
		}
		b, _ := json.Marshal(message{JSONRPC: "2.0", ID: m.ID, Error: &Error{Code: CodeNotPermitted, Message: NoAgentsRefusal}})
		if conn.Write(string(b)) != nil {
			return
		}
	}
}
