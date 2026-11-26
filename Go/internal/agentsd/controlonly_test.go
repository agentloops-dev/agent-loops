package agentsd

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// lineConn is a controllink.Conn that is fed lines and keeps what is written.
type lineConn struct {
	in      chan string
	mu      sync.Mutex
	written []string
}

func (c *lineConn) Write(line string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, line)
	return nil
}
func (c *lineConn) In() <-chan string { return c.in }
func (c *lineConn) Close()            {}
func (c *lineConn) Device() string    { return "" }

// A control-only machine (#7314) refuses each request on a channel the control plane opens,
// and says nothing to a notification or a reply.
func TestRefuseAgentWork(t *testing.T) {
	t.Parallel()
	c := &lineConn{in: make(chan string, 8)}
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"sessions/start","params":{}}`,
		`{"jsonrpc":"2.0","method":"daemon/presence"}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`not json`,
		`{"jsonrpc":"2.0","id":"f","method":"folders/add"}`,
	} {
		c.in <- line
	}
	close(c.in)
	RefuseAgentWork(c)
	if len(c.written) != 2 {
		t.Fatalf("%q", c.written)
	}
	for i, id := range []string{"1", `"f"`} {
		var m message
		if err := json.Unmarshal([]byte(c.written[i]), &m); err != nil {
			t.Fatal(err)
		}
		if string(m.ID) != id || m.Error == nil || m.Error.Code != CodeNotPermitted || m.Error.Message != NoAgentsRefusal {
			t.Fatalf("%s", c.written[i])
		}
	}
}

// On channel 0 it answers only what reads the machine itself.
func TestAskedControlOnlyLeavesAgentWorkUnanswered(t *testing.T) {
	t.Parallel()
	d := &Daemon{}
	for _, method := range []string{wire.CallWorkflowsFire.Name, wire.CallGitFill.Name, wire.CallRuntimesList.Name,
		wire.CallMachineOrgRouteCheck.Name, "providers/start"} {
		if _, ok := d.AskedControlOnly(method, json.RawMessage(`{}`)); ok {
			t.Errorf("%s was answered", method)
		}
	}
	if !controlOnlyAsks[wire.CallMachineUsage.Name] {
		t.Error("machine/usage is not answered")
	}
}
