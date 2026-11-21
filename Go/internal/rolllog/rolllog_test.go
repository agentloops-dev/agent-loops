package rolllog

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRollsPastTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	f, err := Open(path, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	line := strings.Repeat("a", 39) + "\n" // 40 bytes
	for range 2 {
		write(t, f, line)
	}
	if _, err := os.Stat(Previous(path)); !os.IsNotExist(err) {
		t.Fatal("rolled under the limit")
	}
	// The third line would take it to 120 bytes: the 80 go aside first.
	write(t, f, line)
	if got := read(t, Previous(path)); got != line+line {
		t.Fatalf("previous %q", got)
	}
	if got := read(t, path); got != line {
		t.Fatalf("current %q", got)
	}
	// Writing on never makes either file much bigger than the limit.
	for range 1000 {
		write(t, f, line)
	}
	for _, p := range []string{path, Previous(path)} {
		if fi, _ := os.Stat(p); fi.Size() > 100 {
			t.Fatalf("%s is %d bytes", p, fi.Size())
		}
	}
	// A line larger than the limit still goes into an empty log, whole.
	big := strings.Repeat("d", 300)
	write(t, f, big)
	if got := read(t, path); got != big {
		t.Fatalf("%d bytes", len(got))
	}
}

func TestOpenRollsALogAlreadyPastTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.log")
	must(t, os.WriteFile(path, []byte(strings.Repeat("x", 200)), 0o600))
	f, err := Open(path, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	write(t, f, "new\n")
	if read(t, path) != "new\n" || len(read(t, Previous(path))) != 200 {
		t.Fatal("not rolled at open")
	}
}

// The supervisor and agentsd both write daemon.log: a roll by one is followed by the
// other, which neither writes into the previous log nor rolls the new one aside.
func TestTwoWritersShareOneLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	host, _ := Open(path, 100, false)
	defer host.Close()
	supervisor, _ := Open(path, 100, false)
	defer supervisor.Close()
	write(t, host, strings.Repeat("h", 60)+"\n")
	write(t, supervisor, strings.Repeat("s", 37)+"\n") // 99 bytes, both counted
	write(t, host, "rolls\n")
	write(t, supervisor, "after\n")
	if got := read(t, path); got != "rolls\nafter\n" {
		t.Fatalf("current %q", got)
	}
	if got := read(t, Previous(path)); !strings.HasPrefix(got, "hhh") || !strings.HasSuffix(got, "sss\n") {
		t.Fatalf("previous %q", got)
	}
	// Removed by hand: started afresh.
	os.Remove(path)
	write(t, host, "again\n")
	if got := read(t, path); got != "again\n" {
		t.Fatalf("after a removal %q", got)
	}
}

// What goes past Write, as a panic on the duplicated standard error does, is rolled by
// Check (#3687), which leaves a log under its limit alone.
func TestCheckRollsWhatWentPastWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	f, err := Open(path, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	write(t, f, "line\n")
	f.Check()
	if _, err := os.Stat(Previous(path)); !os.IsNotExist(err) {
		t.Fatal("rolled under the limit")
	}
	write(t, f.File(), strings.Repeat("p", 200))
	f.Check()
	if len(read(t, Previous(path))) != 205 {
		t.Fatal("not rolled")
	}
	write(t, f, "after\n")
	if got := read(t, path); got != "after\n" {
		t.Fatalf("current %q", got)
	}
}

// must fails the test at once when a set-up step fails.
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// write writes s to w, and fails the test when it cannot.
func write(t testing.TB, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatal(err)
	}
}
