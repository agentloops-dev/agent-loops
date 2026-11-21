package hosted

import (
	"encoding/json"
	"io"
	"testing"
)

// replyJSON writes v as JSON to a fake server's answer. It runs on the server's goroutine,
// so a failed write fails the test with t.Error, not t.Fatal.
func replyJSON(t testing.TB, w io.Writer, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}
