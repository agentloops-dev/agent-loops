package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/rolllog"
)

// supervise runs (a box with no systemd: under systemd, Restart=always does this) `agentsd <args>` and starts it again when it crashes (#1125: a host on a box
// with no systemd stayed down all night after one panic). A clean exit (status 0, which SIGTERM
// gives) is a stop on purpose: the supervisor ends too, so stop.sh and devbox.sh stop, which kill
// the host by its daemon.lock pid, still stop it. Anything else (a panic's status 2, a signal such
// as SIGKILL or SIGSEGV) is a crash: it waits 1 s, doubling to 60 s, and starts the host again.
// After 5 minutes up, the wait goes back to 1 s. Its own pid is in the root's supervise.lock;
// SIGTERM to it stops the host and then the supervisor. A host that ends with
// agentsd.ExitRestart has a self-update in bin/current (#5788): the supervisor starts
// itself again from there, so the supervisor is the new copy too.
func supervise(args []string) {
	root := parse(args).root
	// The host's output at each start: the log as it is then (the host opens its own,
	// rolled, as soon as it starts).
	out := func() io.Writer { return os.Stderr }
	// store-bounds: kind=log; size=8MiB; age=7d; applied=on write, at open and every 30 s (#6735)
	if f, err := rolllog.Open(store.At(root).Log(), rolllog.Limit, false); err == nil {
		out = func() io.Writer { return f.File() }
		setLog(f)
	}
	lock := filepath.Join(root, "supervise.lock")
	// store-bounds: not a store: a lock file that holds the pid of the supervisor.
	if err := os.WriteFile(lock, []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		log.Printf("supervise: %v", err)
	}
	defer os.Remove(lock)

	self, _ := os.Executable()
	// The host is bin/current where there is one (#5788): a self-update points it at the
	// new copy, and a copy that did not stay up points it back.
	host := self
	current := filepath.Join(filepath.Dir(self), "current")
	if fi, err := os.Lstat(current); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		host = current
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	wait, starts := time.Second, 0
	// SIGUSR1 asks the host for its stacks (#6260): the supervisor, which would end on it,
	// passes it on to the host it runs.
	var running atomic.Pointer[os.Process]
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			if p := running.Load(); p != nil {
				_ = p.Signal(syscall.SIGUSR1)
			}
		}
	}()
	for {
		cmd := exec.Command(host, args...)
		// The host stamps its own lines; a panic's stack goes in as it is.
		w := out()
		cmd.Stdout, cmd.Stderr = w, w
		if err := cmd.Start(); err != nil {
			log.Printf("supervise: could not start agentsd: %v", err)
			os.Exit(1)
		}
		starts++
		log.Printf("supervise: agentsd pid %d (start %d)", cmd.Process.Pid, starts)
		up := time.Now()
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		running.Store(cmd.Process)
		var err error
		select {
		case s := <-sigs:
			log.Printf("supervise: %v, stopping agentsd", s)
			stopHost(cmd.Process, exited)
			return
		case err = <-exited:
		}
		if err == nil {
			log.Printf("supervise: agentsd stopped cleanly; the supervisor ends too")
			return
		}
		var ee *exec.ExitError
		what := err.Error()
		if errors.As(err, &ee) && ee.ExitCode() == agentsd.ExitRestart && host == current {
			// A self-update is in place: the supervisor starts again as the new copy too,
			// which starts the host.
			log.Printf("supervise: agentsd stopped to run its update; the supervisor starts again from %s", current)
			_ = os.Remove(lock)
			signal.Stop(sigs)
			err := syscall.Exec(current, append([]string{current, "supervise"}, args...), os.Environ()) //nolint:gosec // G204: this host's own bin/current
			log.Printf("supervise: could not start again from %s: %v", current, err)
			// store-bounds: not a store: a lock file that holds the pid of the supervisor.
			_ = os.WriteFile(lock, []byte(fmt.Sprint(os.Getpid())), 0o600)
			signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
			what = "a restart for its update"
		} else if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGTERM {
				log.Printf("supervise: agentsd was sent SIGTERM; the supervisor ends too")
				return
			}
		}
		if time.Since(up) > 5*time.Minute {
			wait = time.Second
		}
		log.Printf("supervise: agentsd crashed after %s (%s); starting it again in %s", time.Since(up).Round(time.Second), what, wait)
		select {
		case <-time.After(wait):
		case s := <-sigs:
			log.Printf("supervise: %v while waiting; the supervisor ends", s)
			return
		}
		wait = min(wait*2, time.Minute)
	}
}

// stopHost sends the host SIGTERM and waits for it to end. The supervisor ends after it,
// so how the host ended changes nothing.
func stopHost(p *os.Process, exited <-chan error) {
	if err := p.Signal(syscall.SIGTERM); err != nil {
		log.Printf("supervise: %v", err)
	}
	<-exited //nolint:errcheck // the supervisor ends; how agentsd ended does not change that
}
