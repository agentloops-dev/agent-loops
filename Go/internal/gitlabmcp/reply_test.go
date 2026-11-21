package gitlabmcp

import (
	"encoding/json"
	"io"
	"testing"
)

// reply writes body to a fake server's answer. It runs on the server's goroutine, so a
// failed write fails the test with t.Error, not t.Fatal.
func reply(t testing.TB, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Error(err)
	}
}

// replyJSON writes v as JSON to a fake server's answer, as reply does.
func replyJSON(t testing.TB, w io.Writer, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

// unmarshal reads a request that a fake server got, as reply does.
func unmarshal(t testing.TB, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Error(err)
	}
}
