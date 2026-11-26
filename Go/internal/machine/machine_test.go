package machine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAGivenIDWins(t *testing.T) {
	t.Setenv("AGENTS_MACHINE_ID", " given ")
	if got := ID(); got != "given" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("AGENTS_MACHINE_ID", "")
	if ID() == "" {
		t.Fatal("no id")
	}
}

// A kept id is made once and read back the same (#7399): each new container over the same
// data volume is the same machine.
func TestAKeptIDStaysTheSame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "machine-id")
	first, err := KeptID(path)
	if err != nil || len(first) != 32 {
		t.Fatalf("%q %v", first, err)
	}
	again, err := KeptID(path)
	if err != nil || again != first {
		t.Fatalf("%q, then %q %v", first, again, err)
	}
	if other, _ := KeptID(filepath.Join(t.TempDir(), "machine-id")); other == first {
		t.Fatal("two volumes, one id")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(path, []byte(" given\n"), 0o600))
	if got, err := KeptID(path); err != nil || got != "given" {
		t.Fatalf("%q %v", got, err)
	}
}
