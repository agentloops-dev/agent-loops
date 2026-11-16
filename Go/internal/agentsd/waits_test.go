package agentsd

import (
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// The longest a test helper waits for the daemon (#5632): a minute.
const longestWait = time.Minute

// waitLimit is how long a test helper waits for the daemon to do a thing (#5632): the
// time the run of the tests has left less 10 s, at most a minute, as controlplane's
// waitTold. A fixed limit of 5 to 20 s failed tests on a Mac or a CI runner under load;
// a longer one alone hides a lost wake-up, so waitFailed shows which of the two it was.
func waitLimit(t testing.TB) time.Duration {
	limit := longestWait
	if d, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if deadline, set := d.Deadline(); set {
			limit = min(limit, time.Until(deadline)-10*time.Second)
		}
	}
	return limit
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
