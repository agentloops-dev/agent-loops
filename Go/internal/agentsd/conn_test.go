package agentsd

import (
	"encoding/json"
	"testing"
)

// A call's effect that comes before its answer is heard by the until after the call, once,
// and one from before the call is not (#7227).
func TestUntilHearsWhatACallReadPast(t *testing.T) {
	t.Parallel()
	c := newConn()
	c.out <- `{"jsonrpc":"2.0","method":"x/changed","params":0}`
	go func() {
		<-c.in
		c.out <- `{"jsonrpc":"2.0","method":"x/changed","params":1}`
		c.out <- `{"jsonrpc":"2.0","id":1,"result":true}`
		c.out <- `{"jsonrpc":"2.0","method":"x/changed","params":2}`
	}()
	c.call(t, 1, "x/change", nil)
	for _, want := range []string{"1", "2"} {
		c.until(t, "change "+want, func(_ string, p json.RawMessage) bool {
			if string(p) != want {
				t.Errorf("heard %s, want %s", p, want)
			}
			return true
		})
	}
}
