package agentsd

import (
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// The longest a test helper waits for the daemon (#5632): a minute.
const longestWait = time.Minute

// The time a wait leaves before the run's deadline, for waitFailed's dump (#5632).
const deadlineMargin = 10 * time.Second

// waitLimit is how long a test helper waits for the daemon to do a thing (#5632): the
// time the run of the tests has left less 10 s, at most a minute, as controlplane's
// waitTold. A fixed limit of 5 to 20 s failed tests on a Mac or a CI runner under load;
// a longer one alone hides a lost wake-up, so waitFailed shows which of the two it was.
//
// When the run has less than 20 s left, the limit is half of what is left, not that time
// less 10 s (#6085). Before, a test that started in the last 10 s of a run (a long run of
// -count=50 with the default -timeout of 10m) had a limit of zero or less, and its first
// wait failed at once: clientAdded said "the daemon never added the test's window client"
// before Accept's goroutine had run once.
func waitLimit(t testing.TB) time.Duration {
	limit := longestWait
	if d, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if deadline, set := d.Deadline(); set {
			limit = min(limit, leftBefore(time.Until(deadline)))
		}
	}
	return limit
}

// leftBefore is the wait for a run with left to go: left less deadlineMargin, or half of
// left when that is more.
func leftBefore(left time.Duration) time.Duration {
	return max(left-deadlineMargin, left/2)
}

// waitFailed fails a test whose wait reached waitLimit, with every goroutine's stack
// first (#5632): a busy daemon (slow under load) and an idle one (a lost wake-up, a
// product bug) then look different in the failure's log.
func waitFailed(t testing.TB, format string, args ...any) {
	t.Helper()
	var stacks strings.Builder
	if p := pprof.Lookup("goroutine"); p != nil {
		if err := p.WriteTo(&stacks, 2); err != nil {
			t.Logf("could not write the goroutines' stacks: %v", err)
		}
	}
	t.Logf("every goroutine when the wait reached its limit:\n%s", stacks.String())
	t.Fatalf(format, args...)
}

// acceptStacks logs the stack of each goroutine in Daemon.Accept, before waitFailed's
// dump of all of them (#6085): a long dump was cut in the log before it got there.
func acceptStacks(t testing.TB) {
	t.Helper()
	var all strings.Builder
	if p := pprof.Lookup("goroutine"); p != nil {
		_ = p.WriteTo(&all, 2)
	}
	var accepts []string
	for _, g := range strings.Split(all.String(), "\n\n") {
		if strings.Contains(g, "agentsd.(*Daemon).Accept(") {
			accepts = append(accepts, g)
		}
	}
	t.Logf("the goroutines in Daemon.Accept (%d):\n%s", len(accepts), strings.Join(accepts, "\n\n"))
}

func TestAWaitNearTheRunsDeadlineStillWaits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ left, want time.Duration }{
		{2 * time.Minute, 110 * time.Second},
		{20 * time.Second, 10 * time.Second},
		{12 * time.Second, 6 * time.Second},
		{4 * time.Second, 2 * time.Second}, // was -6 s: the wait failed at once (#6085)
	} {
		if got := leftBefore(c.left); got != c.want {
			t.Errorf("leftBefore(%v) = %v, want %v", c.left, got, c.want)
		}
	}
}
