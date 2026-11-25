package whatsappmcp

import (
	"strings"
	"testing"
	"time"
)

// #6908: a message too large for the store leaves the earlier ones.
func TestAMessageOverTheStoreLeavesTheEarlierOnes(t *testing.T) {
	t.Skip("#7191")
	s := &Store{Dir: t.TempDir()}
	now := time.Now()
	if err := s.Add("1", Stored{ID: "a", At: now.Add(-time.Minute), Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	err := s.Add("1", Stored{ID: "b", At: now, Text: strings.Repeat("x", maxStoreBytes)})
	t.Log(err)
	msgs, err := s.Messages("1")
	if err != nil || len(msgs) == 0 || msgs[0].ID != "a" {
		t.Fatalf("%d messages are kept (%v)", len(msgs), err)
	}
}
