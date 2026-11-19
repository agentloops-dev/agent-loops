package main

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// SIGUSR1 writes every goroutine's stack to the log, and the host goes on running (#6260).
func TestSIGUSR1LogsEveryGoroutineAndGoesOn(t *testing.T) {
	logs := &lockedBuffer{}
	was := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(was) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dumpStacksOnSignal(ctx)
	for i := 1; i <= 2; i++ {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); strings.Count(logs.String(), "stacks: end") < i; {
			if time.Now().After(deadline) {
				t.Fatalf("no stacks after SIGUSR1 %d:\n%s", i, logs)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	s := logs.String()
	if !strings.Contains(s, "goroutine ") || !strings.Contains(s, "TestSIGUSR1LogsEveryGoroutineAndGoesOn") {
		t.Fatalf("the dump has not every goroutine's stack:\n%s", s)
	}
}
