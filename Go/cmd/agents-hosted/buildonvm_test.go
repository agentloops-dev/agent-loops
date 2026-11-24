package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildOnVM is deploy/gcp/build-on-vm.sh (#7030) with a fake sudo and docker, and the folder
// that its lock and what docker writes are in.
func buildOnVM(t *testing.T, docker string, env ...string) (*exec.Cmd, string) {
	t.Helper()
	for _, tool := range []string{"bash", "flock", "timeout", "ps"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool + ": the script runs on the VM, which is Linux")
		}
	}
	dir := t.TempDir()
	for name, text := range map[string]string{"sudo": "#!/bin/sh\nexec \"$@\"\n", "docker": "#!/usr/bin/env bash\n" + docker} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o755); err != nil { //nolint:gosec // G306: a fake tool must be executable
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "../../../deploy/gcp/build-on-vm.sh", "image:tag", "1.2.3")
	cmd.Env = append([]string{
		"PATH=" + dir + ":" + os.Getenv("PATH"), "HOME=" + dir, "DIR=" + dir,
		"AGENTS_BUILD_RUN=" + dir, "AGENTS_BUILD_POLL=0.1",
	}, env...)
	return cmd, dir
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	for range 300 {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("not there: " + path)
}

// slow says it runs, then runs until it is stopped, and says so.
const slow = `trap 'touch "$DIR/stopped"; exit 143' TERM
touch "$DIR/running"
for _ in $(seq 600); do sleep 0.1; done`

func TestBuildOnVMGivesDockerTheContextAndItsExitCode(t *testing.T) {
	cmd, _ := buildOnVM(t, "echo \"$*\"; cat; exit 9")
	cmd.Stdin = strings.NewReader("the context")
	out, _ := cmd.CombinedOutput()
	if cmd.ProcessState.ExitCode() != 9 || !strings.Contains(string(out), "the context") ||
		!strings.Contains(string(out), "--progress=plain") || !strings.Contains(string(out), "VERSION=1.2.3") {
		t.Fatalf("exit %d: %s", cmd.ProcessState.ExitCode(), out)
	}
}

func TestBuildOnVMHasATimeLimit(t *testing.T) {
	cmd, dir := buildOnVM(t, slow, "AGENTS_BUILD_LIMIT=1")
	out, _ := cmd.CombinedOutput()
	if cmd.ProcessState.ExitCode() != 124 {
		t.Fatalf("exit %d: %s", cmd.ProcessState.ExitCode(), out)
	}
	waitFor(t, filepath.Join(dir, "stopped"))
}

// The parent stands in for sshd: when it goes, the build stops. Until then, no other starts.
func TestBuildOnVMStopsWithItsConnectionAndIsOneAtATime(t *testing.T) {
	cmd, dir := buildOnVM(t, slow)
	parent := exec.Command("bash", append([]string{"-c", `"$@" & wait`, "sshd"}, cmd.Args...)...) //nolint:gosec // G204: the test's own script
	parent.Env = cmd.Env
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, filepath.Join(dir, "running"))

	second := exec.Command(cmd.Args[0], cmd.Args[1:]...) //nolint:gosec // G204: the test's own script
	second.Env = cmd.Env
	if out, _ := second.CombinedOutput(); second.ProcessState.ExitCode() != 75 {
		t.Fatalf("second: exit %d: %s", second.ProcessState.ExitCode(), out)
	}

	_ = parent.Process.Kill()
	_ = parent.Wait()
	waitFor(t, filepath.Join(dir, "stopped"))
}
