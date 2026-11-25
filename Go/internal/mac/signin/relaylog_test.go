package signin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/rolllog"
)

// #7247: a line of host-relay.log is cut to its largest, the two files hold their bytes, and the
// sweep takes them out past their age.
func TestTheLimitsOfTheRelayLog(t *testing.T) {
	s := &Relays{Logs: t.TempDir()}
	file := filepath.Join(s.Logs, "host-relay.log")
	stamp := len("2006-01-02T15:04:05Z ")
	whole := strings.Repeat("é", (logLineMost-stamp-1)/2-1) + "e"
	s.write(whole)
	s.write(strings.Repeat("é", logLineMost))
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) != 3 || !bytes.HasSuffix(lines[0], []byte(whole+"\n")) || len(lines[1]) > logLineMost || !bytes.HasSuffix(lines[1], []byte("é…\n")) {
		t.Fatalf("%d lines of %d and %d bytes", len(lines), len(lines[0]), len(lines[1]))
	}
	for range 3 * logLimit / logLineMost {
		s.write(strings.Repeat("x", logLineMost))
	}
	for _, f := range []string{file, rolllog.Previous(file)} {
		info, err := os.Stat(f)
		if err != nil || info.Size() > logLimit {
			t.Fatalf("%s: %v", f, err)
		}
		old := time.Now().Add(-7 * 24 * time.Hour)
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	s.Sweep()
	if _, err := os.Stat(rolllog.Previous(file)); !os.IsNotExist(err) {
		t.Fatal("the old lines were kept")
	}
	if info, err := os.Stat(file); err == nil && info.Size() > 0 {
		t.Fatal("the old lines were kept")
	}
	// A Mac that lends nothing has no log.
	none := &Relays{Logs: t.TempDir()}
	none.Sweep()
	if entries, _ := os.ReadDir(none.Logs); len(entries) != 0 {
		t.Fatal("the sweep made a log")
	}
}
