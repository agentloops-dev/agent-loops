package controllink

import (
	"encoding/json"
	"testing"
	"time"
)

// What the control plane tells a host on channel 0 reaches Told; a reply to an Ask, and
// a request, do not (#1118).
func TestChannelZeroNotificationsAreTold(t *testing.T) {
	heard := make(chan string, 4)
	u := &Uplink{Told: func(method string, params json.RawMessage) { heard <- method + " " + string(params) }}
	u.message(0, `{"jsonrpc":"2.0","method":"machine/clients","params":{"clients":[]}}`)
	u.message(0, `{"jsonrpc":"2.0","id":3,"method":"runtimes/list","params":{}}`)
	u.message(0, `{"jsonrpc":"2.0","id":4,"result":{}}`)
	select {
	case got := <-heard:
		if got != `machine/clients {"clients":[]}` {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("machine/clients was not told")
	}
	select {
	case got := <-heard:
		t.Fatal("also told", got)
	case <-time.After(100 * time.Millisecond):
	}
}
