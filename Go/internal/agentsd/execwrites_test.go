package agentsd

import (
	"errors"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
)

// TestTheProgramsAgentsdWritesRunAtOnceWhileOthersFork writes each program that agentsd
// writes and runs at once (the gh shim, a toolset's shim, the git hook) while other goroutines
// fork all the time, as the daemon does. With os.WriteFile, Linux fails some of the runs with
// "text file busy" (ETXTBSY, #7431): a child forked while the file is open for writing holds
// it until the child execs.
func TestTheProgramsAgentsdWritesRunAtOnceWhileOthersFork(t *testing.T) {
	t.Parallel()
	if goruntime.GOOS == "windows" {
		t.Skip("no shell scripts")
	}

	writers := []struct {
		name  string
		write func(dir string) (string, error)
	}{
		{"gh shim", func(dir string) (string, error) {
			return filepath.Join(dir, "gh"), writeGHShim(dir, "/nonexistent/agentsd", "/bin/true")
		}},
		{"toolset shim", func(dir string) (string, error) {
			return filepath.Join(dir, "bin", "tool"), writeShim(dir, "tool", []string{"#!/bin/sh", "exit 0"})
		}},
		{"git hook", func(dir string) (string, error) {
			return filepath.Join(dir, trailerHookScript), writeTrailerHooks(dir, map[string]bool{})
		}},
	}

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

	for _, w := range writers {
		root := t.TempDir()
		for i := range 50 {
			path, err := w.write(filepath.Join(root, strconv.Itoa(i)))
			if err != nil {
				t.Fatalf("%s %d: %v", w.name, i, err)
			}
			// Only the exec matters: the hook exits non-zero outside a git folder.
			if err := exec.Command(path).Run(); errors.Is(err, syscall.ETXTBSY) {
				t.Fatalf("%s %d: %v", w.name, i, err)
			}
		}
	}
}
