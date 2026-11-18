// Package apicalls counts a forge server's calls to its forge's API, by caller, and keeps what
// the forge says is left of each rate limit (#6164). Each forge server wraps its HTTP client's
// transport in a Meter's; mcpevents names the caller (an event's poll or a tool) in the context.
// Every 10 minutes the server's log gets one line with the calls by caller and the budget left,
// and a line when a budget goes under a fifth. Nothing about the calls leaves the machine.
package apicalls

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type callerKey struct{}

// WithCaller is ctx with the caller named: "event pr.blocked", "tool show_pull_request".
func WithCaller(ctx context.Context, caller string) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// Caller is the caller ctx names, or "other".
func Caller(ctx context.Context) string {
	if c, ok := ctx.Value(callerKey{}).(string); ok && c != "" {
		return c
	}
	return "other"
}

// Every is how often Run writes the summary line.
const Every = 10 * time.Minute

// lowShare is the part of a budget under which the log says so, once for each reset.
const lowShare = 5

// Budget is what the forge last said of one of its rate limits.
type Budget struct {
	Resource  string
	Limit     int
	Remaining int
	// Reset is when the forge fills it again; zero when it did not say.
	Reset time.Time
	At    time.Time
}

// Count is one caller's calls to one resource since the last summary.
type Count struct {
	Caller, Resource string
	Calls            int
	// NotModified are the calls answered 304, which GitHub does not count against a budget.
	NotModified int
	// Refused are the calls answered 403 or 429.
	Refused int
}

// Meter counts the calls of one forge server.
type Meter struct {
	// Forge names the forge in the log: "github", "gitlab".
	Forge string
	// Log writes a line to the server's log.
	Log func(string)
	// Resource names the budget a request counts against, when the forge's headers do not.
	Resource func(*http.Request) string

	mu      sync.Mutex
	counts  map[[2]string]*Count
	budgets map[string]Budget
	warned  map[string]time.Time
	since   time.Time
}

// New is a meter for forge that logs with log.
func New(forge string, log func(string)) *Meter {
	return &Meter{Forge: forge, Log: log, since: time.Now()}
}

// Transport is next with every call counted.
func (m *Meter) Transport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return transport{next: next, m: m}
}

// Client is c with its transport counted; c itself is changed, and nil is a new client.
func (m *Meter) Client(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	c.Transport = m.Transport(c.Transport)
	return c
}

type transport struct {
	next http.RoundTripper
	m    *Meter
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.next.RoundTrip(req)
	t.m.record(req, res, time.Now())
	return res, err
}

func (m *Meter) record(req *http.Request, res *http.Response, now time.Time) {
	var h http.Header
	status := 0
	if res != nil {
		h, status = res.Header, res.StatusCode
	}
	b, ok := budgetOf(h, now)
	resource := b.Resource
	if resource == "" && m.Resource != nil {
		resource = m.Resource(req)
	}
	if resource == "" {
		resource = "api"
	}
	b.Resource = resource
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = map[[2]string]*Count{}
		m.budgets = map[string]Budget{}
		m.warned = map[string]time.Time{}
	}
	key := [2]string{Caller(req.Context()), resource}
	c := m.counts[key]
	if c == nil {
		c = &Count{Caller: key[0], Resource: key[1]}
		m.counts[key] = c
	}
	c.Calls++
	switch status {
	case http.StatusNotModified:
		c.NotModified++
	case http.StatusForbidden, http.StatusTooManyRequests:
		c.Refused++
	}
	if !ok {
		return
	}
	m.budgets[resource] = b
	if b.Limit <= 0 || b.Remaining*lowShare >= b.Limit {
		delete(m.warned, resource) // filled again: the next fall is said again
		return
	}
	if last, said := m.warned[resource]; !said || !last.Equal(b.Reset) {
		m.warned[resource] = b.Reset
		if m.Log != nil {
			m.Log(fmt.Sprintf("api calls: %s %s budget low: %s", m.Forge, resource, b.words(now)))
		}
	}
}

// budgetOf reads a rate limit from the headers forges send: GitHub's and Azure DevOps'
// X-RateLimit-*, GitLab's and Forgejo's RateLimit-*, and Linear's X-RateLimit-Requests-*.
func budgetOf(h http.Header, now time.Time) (Budget, bool) {
	for _, prefix := range []string{"X-RateLimit-", "RateLimit-", "X-RateLimit-Requests-"} {
		limit, errL := strconv.Atoi(h.Get(prefix + "Limit"))
		remaining, errR := strconv.Atoi(h.Get(prefix + "Remaining"))
		if errL != nil || errR != nil {
			continue
		}
		b := Budget{Resource: strings.ToLower(h.Get(prefix + "Resource")), Limit: limit, Remaining: remaining, At: now}
		if reset, err := strconv.ParseInt(h.Get(prefix+"Reset"), 10, 64); err == nil && reset > 0 {
			b.Reset = resetTime(reset, now)
		}
		return b, true
	}
	return Budget{}, false
}

// resetTime reads a reset as the forges send it: Unix seconds (GitHub, GitLab), Unix
// milliseconds (Linear), or seconds from now (the IETF draft's RateLimit-Reset).
func resetTime(v int64, now time.Time) time.Time {
	switch {
	case v > 1e12:
		return time.UnixMilli(v)
	case v > 1e9:
		return time.Unix(v, 0)
	}
	return now.Add(time.Duration(v) * time.Second)
}

func (b Budget) words(now time.Time) string {
	s := fmt.Sprintf("%d of %d left", b.Remaining, b.Limit)
	if !b.Reset.IsZero() {
		s += fmt.Sprintf(", full again in %s", b.Reset.Sub(now).Round(time.Minute))
	}
	return s
}

// Snapshot is the counts since the last summary, most calls first, and every budget.
func (m *Meter) Snapshot() (since time.Time, counts []Count, budgets []Budget) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.counts {
		counts = append(counts, *c)
	}
	for _, b := range m.budgets {
		budgets = append(budgets, b)
	}
	slices.SortFunc(counts, func(a, b Count) int {
		if a.Calls != b.Calls {
			return b.Calls - a.Calls
		}
		return strings.Compare(a.Caller+a.Resource, b.Caller+b.Resource)
	})
	slices.SortFunc(budgets, func(a, b Budget) int { return strings.Compare(a.Resource, b.Resource) })
	return m.since, counts, budgets
}

// Summary is the summary line for the calls since the last one, and starts the count again.
// It is empty when there were no calls.
func (m *Meter) Summary(now time.Time) string {
	since, counts, budgets := m.Snapshot()
	m.mu.Lock()
	m.counts = nil
	m.since = now
	if m.budgets == nil {
		m.budgets = map[string]Budget{}
		m.warned = map[string]time.Time{}
	}
	m.mu.Unlock()
	if len(counts) == 0 {
		return ""
	}
	total := 0
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		total += c.Calls
		p := fmt.Sprintf("%s %s %d", c.Caller, c.Resource, c.Calls)
		if c.NotModified > 0 {
			p += fmt.Sprintf(" (%d unchanged)", c.NotModified)
		}
		if c.Refused > 0 {
			p += fmt.Sprintf(" (%d refused)", c.Refused)
		}
		parts = append(parts, p)
	}
	line := fmt.Sprintf("api calls: %s, %d in %s: %s", m.Forge, total, now.Sub(since).Round(time.Second), strings.Join(parts, "; "))
	if len(budgets) > 0 {
		left := make([]string, 0, len(budgets))
		for _, b := range budgets {
			left = append(left, b.Resource+" "+b.words(now))
		}
		line += "; budget: " + strings.Join(left, "; ")
	}
	return line
}

// Run writes the summary line every Every until ctx ends.
func (m *Meter) Run(ctx context.Context) {
	t := time.NewTicker(Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if line := m.Summary(now); line != "" && m.Log != nil {
				m.Log(line)
			}
		}
	}
}
