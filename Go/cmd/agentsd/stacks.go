package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"

	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
)

// dumpStacksOnSignal writes every goroutine's stack to the log on each SIGUSR1, and goes on
// running (#6260): `kill -USR1 <agentsd pid>` shows what a stalled host waits on, with no
// stop that would end the stall. It stops with ctx.
func dumpStacksOnSignal(ctx context.Context) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1)
	safe.Go("stack dump", func() {
		defer signal.Stop(sigs)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigs:
				log.Print(goroutineStacks())
			}
		}
	})
}

// goroutineStacks is every goroutine's stack, as a panic prints it, with a line before it.
func goroutineStacks() string {
	var b bytes.Buffer
	b.WriteString("stacks: every goroutine, on SIGUSR1 (#6260)\n")
	_ = pprof.Lookup("goroutine").WriteTo(&b, 2)
	b.WriteString("stacks: end\n")
	return b.String()
}
