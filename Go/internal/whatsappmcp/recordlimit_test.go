package whatsappmcp

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// #7191: a message too large for the store is cut, and leaves the earlier ones.
func TestAMessageOverTheStoreLeavesTheEarlierOnes(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Now()
	if err := s.Add("1", Stored{ID: "a", At: now.Add(-time.Minute), Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("1", Stored{ID: "b", At: now, Text: strings.Repeat("é", maxStoreBytes)}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages("1")
	if err != nil || len(msgs) != 2 || msgs[0].ID != "a" || msgs[0].Text != "hello" {
		t.Fatalf("%d messages are kept (%v)", len(msgs), err)
	}
	cut := msgs[1].Text
	if len(cut) > maxRecordBytes || !utf8.ValidString(cut) || !strings.HasPrefix(cut, "éé") || !strings.HasSuffix(cut, cutMark) {
		t.Fatalf("the large message is %d bytes, valid %v", len(cut), utf8.ValidString(cut))
	}
}

// #7191: a text at the limit is kept whole, and one byte more cuts it between characters.
func TestATextIsCutOnlyOverTheRecordLimit(t *testing.T) {
	whole := strings.Repeat("x", maxRecordBytes)
	if cutText(whole) != whole {
		t.Fatal("a text at the limit is cut")
	}
	for pad := range 4 {
		long := strings.Repeat("x", pad) + strings.Repeat("😀", maxRecordBytes/4) + "x"
		if cut := cutText(long); len(cut) > maxRecordBytes || len(cut) <= maxRecordBytes-len(cutMark)-4 || !utf8.ValidString(cut) || !strings.HasSuffix(cut, cutMark) {
			t.Fatalf("after %d bytes: %d bytes, valid %v", pad, len(cut), utf8.ValidString(cut))
		}
	}
}
