package citimings

import (
	"math"
	"slices"
	"sort"
	"time"
)

// sinceFn is how many seconds, to a tenth, a time the forge said is after its run was made;
// false when the forge said no time.
type sinceFn func(string) (float64, bool)

// sinceMade is a sinceFn for a run made at made, reading the forge's times with parse.
func sinceMade(made time.Time, parse func(string) (time.Time, bool)) sinceFn {
	return func(s string) (float64, bool) {
		t, ok := parse(s)
		if !ok {
			return 0, false
		}
		return math.Round(t.Sub(made).Seconds()*10) / 10, true
	}
}

// parseRFC3339 is an RFC 3339 time.
func parseRFC3339(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// attemptSet is a run's attempts by number, each a copy of one base run.
type attemptSet struct {
	base Run
	byN  map[int]*Run
}

func newAttempts(base Run) *attemptSet {
	return &attemptSet{base: base, byN: map[int]*Run{}}
}

// of is attempt n, made from the base run the first time it is asked for.
func (a *attemptSet) of(n int) *Run {
	if a.byN[n] == nil {
		r := a.base
		r.Attempt, r.Jobs = n, []Job{}
		a.byN[n] = &r
	}
	return a.byN[n]
}

// addJob adds job to attempt n.
func (a *attemptSet) addJob(n int, job Job) {
	r := a.of(n)
	r.Jobs = append(r.Jobs, job)
}

// sorted is every attempt, the first first.
func (a *attemptSet) sorted() []Run {
	var out []Run
	for _, r := range a.byN {
		out = append(out, *r)
	}
	sort.Slice(out, func(x, y int) bool { return out[x].Attempt < out[y].Attempt })
	return out
}

// jobsSummary is what an attempt's jobs say of it as a whole.
type jobsSummary struct {
	// ended is the latest end (0 for none); made the earliest made time (+Inf for none).
	ended, made float64
	// failed says a job's conclusion is one of the failing ones; cancelled that one was cancelled.
	failed, cancelled bool
}

// summarize is jobs' summary; a job whose conclusion is in failing failed.
func summarize(jobs []Job, failing ...string) jobsSummary {
	s := jobsSummary{made: math.Inf(1)}
	for _, j := range jobs {
		s.ended = math.Max(s.ended, j.Ended)
		s.made = math.Min(s.made, j.Made)
		s.failed = s.failed || slices.Contains(failing, j.Conclusion)
		s.cancelled = s.cancelled || j.Conclusion == "cancelled"
	}
	return s
}

// earlierConclusion is an earlier attempt's conclusion, from its jobs': it was run again, so
// it did not pass as a whole, but its jobs say how it ended.
func earlierConclusion(failed, cancelled bool) string {
	switch {
	case failed:
		return "failure"
	case cancelled:
		return "cancelled"
	}
	return "success"
}
