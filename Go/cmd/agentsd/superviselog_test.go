package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/rolllog"
)

// superviseLogRoot is the root that the helper process opens the supervisor's log in.
const superviseLogRoot = "AGENTSD_TEST_SUPERVISE_LOG_ROOT"

// What the detached supervisor's runtime writes to its standard error goes round daemon.log's
// roll, at the log's limit (#7348): detach opens daemon.log raw for it, as here.
func TestTheSupervisorsOwnOutputRollsAtTheLimit(t *testing.T) {
	if root := os.Getenv(superviseLogRoot); root != "" {
		writePastTheLimit(root)
		return
	}
	root := t.TempDir()
	loc := store.At(root)
	if err := os.MkdirAll(loc.Logs, 0o700); err != nil {
		t.Fatal(err)
	}
	logf, err := os.OpenFile(loc.Log(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTheSupervisorsOwnOutputRollsAtTheLimit$")
	cmd.Env = append(os.Environ(), superviseLogRoot+"="+root)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(loc.Log())
	if err != nil {
		t.Fatal(err)
	}
	if now.Size() > rolllog.Limit {
		t.Fatalf("daemon.log is %d bytes, over its limit of %d", now.Size(), rolllog.Limit)
	}
	before, err := os.Stat(rolllog.Previous(loc.Log()))
	if err != nil {
		t.Fatalf("daemon.log did not roll: %v", err)
	}
	if most := int64(rolllog.Limit + superviseChunk); before.Size() > most {
		t.Fatalf("the previous log is %d bytes, over %d", before.Size(), most)
	}
}

// superviseChunk is one write of the helper: a check comes after each, as the watch's does.
const superviseChunk = 256 << 10

// writePastTheLimit is the helper process: the supervisor's log opened, and more than its limit
// written to standard error, past Write: twice the limit, so a raw handle would hold it all.
func writePastTheLimit(root string) {
	f, err := openSuperviseLog(root)
	if err != nil {
		os.Exit(2)
	}
	chunk := append(bytes.Repeat([]byte("x"), superviseChunk-1), '\n')
	for written := 0; written < 2*rolllog.Limit; written += len(chunk) {
		if _, err := os.Stderr.Write(chunk); err != nil {
			os.Exit(3)
		}
		f.Check()
	}
	os.Exit(0)
}
