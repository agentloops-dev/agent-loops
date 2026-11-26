package agentsd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// control/receive (#6612) opens the control key only when it was sealed to this machine for
// the control plane it joined, and starts its copy with it.
func TestControlReceiveOpensTheKeySealedHere(t *testing.T) {
	t.Parallel()
	machine, control := controllink.NewPrivateKey(), controllink.NewPrivateKey()
	machinePublic, _ := controllink.PublicKey(machine)
	controlPublic, _ := controllink.PublicKey(control)
	var started []byte
	d := &Daemon{ReceiveControl: func(key []byte, url string) (string, string, error) {
		started = append([]byte(nil), key...)
		return url, "PIN", nil
	}}
	d.orgIdentity = &machineIdentity{host: "h1", private: machine}
	d.orgKey = controlPublic
	ask := func(move, sealedFor string, key []byte) any {
		t.Helper()
		public, _ := controllink.PublicKey(key)
		sealed, err := controllink.Seal(machinePublic, controllink.ControlMoveInfo, controllink.ControlMoveAD(move, sealedFor, public), key)
		if err != nil {
			t.Fatal(err)
		}
		params, _ := json.Marshal(wire.ControlReceiveRequest{Move: "m1", URL: "https://203.0.113.7:8791", ControlKey: public, Key: sealed})
		answer, ok := d.Asked(wire.CallControlReceive.Name, params)
		if !ok {
			t.Fatal("not answered")
		}
		return answer
	}
	if got, ok := ask("m1", "h1", control).(wire.ControlReceive); !ok || got.URL != "https://203.0.113.7:8791" || *got.Pin != "PIN" || !bytes.Equal(started, control) {
		t.Fatalf("%+v", got)
	}
	for name, answer := range map[string]any{
		"another machine's": ask("m1", "h2", control),
		"another move's":    ask("m2", "h1", control),
		"another plane's":   ask("m1", "h1", controllink.NewPrivateKey()),
	} {
		if f, ok := answer.(controllink.AskFailure); !ok || f.Code != CodeNotPermitted {
			t.Errorf("%s: %+v", name, answer)
		}
	}
	if !controlOnlyAsks[wire.CallControlReceive.Name] {
		t.Error("a control-only machine does not answer control/receive")
	}
}
