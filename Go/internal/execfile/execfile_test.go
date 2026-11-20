package execfile

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

// TestAScriptRunsAtOnceWhileOthersFork writes scripts and runs each at once while other
// goroutines fork all the time: with os.WriteFile, Linux fails some of the runs with
// "text file busy" (#6369).
func TestAScriptRunsAtOnceWhileOthersFork(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("no shell scripts")
	}

	dir := t.TempDir()
	stop := make(chan struct{})
	var forks sync.WaitGroup
	for range 4 {
		forks.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = exec.Command("/bin/true").Run()
			}
		})
	}
	defer forks.Wait()
	defer close(stop)

	for i := range 100 {
		path := filepath.Join(dir, "s"+strconv.Itoa(i))
		if err := Write(path, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(path).Output()
		if err != nil || string(out) != "ok\n" {
			t.Fatalf("run %d: %q, %v", i, out, err)
		}
	}
}
