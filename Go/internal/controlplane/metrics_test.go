package controlplane

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

func scrape(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// The metrics are served on a listener of their own, in the Prometheus text format, and
// count what clients asked, what went to machines, the store and the sessions (#2176).
func TestMetricsAreServedOnTheirOwnListener(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	box := p.host("MACHINE-2")
	defer box.stop()
	_, window := p.client("Alex's MacBook")
	call(t, window, "", "control/status", nil)
	call(t, window, box.member.Host, "daemon/ping", nil)

	addr, err := p.s.ServeMetrics("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	status, body := scrape(t, "http://"+addr+"/metrics", "")
	if status != http.StatusOK {
		t.Fatal(status, body)
	}
	for _, want := range []string{
		"# TYPE agents_control_requests_total counter",
		`agents_control_requests_total{method="control/status",result="ok"} 1`,
		`agents_control_requests_total{method="daemon/ping",result="routed"} 1`,
		`agents_control_request_seconds_bucket{method="control/status",le="+Inf"} 1`,
		`agents_control_request_seconds_count{method="control/status"} 1`,
		`agents_control_store_operations_total{op="get"}`,
		"agents_control_hosts_connected 1",
		"agents_control_clients_connected 1",
		`agents_control_phase{phase="serving"} 1`,
		`agents_control_build_info{version=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s in\n%s", want, body)
		}
	}
	// Only /metrics.
	if status, _ := scrape(t, "http://"+addr+"/", ""); status != http.StatusNotFound {
		t.Fatal(status)
	}
}

// Off by default: the port clients and hosts use serves no metrics.
func TestNoMetricsOnTheClientsPort(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	_, window := p.client("Alex's MacBook")
	call(t, window, "", "control/status", nil)
	_, body := scrape(t, p.url+"/metrics", "")
	if strings.Contains(body, "agents_control_") {
		t.Fatal(body)
	}
}

// The heap and goroutine profiles are served beside /metrics, behind its token (#6583).
func TestProfilesAreServedBesideTheMetrics(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	addr, err := p.s.ServeMetrics("127.0.0.1:0", "s3cret-scrape")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/debug/pprof/heap?debug=1&gc=1": "heap profile:",
		"/debug/pprof/goroutine?debug=1": "goroutine profile: total",
		"/debug/pprof/goroutine?debug=2": "goroutine ",
	} {
		if status, _ := scrape(t, "http://"+addr+path, ""); status != http.StatusUnauthorized {
			t.Errorf("%s with no token: %d", path, status)
		}
		if status, body := scrape(t, "http://"+addr+path, "s3cret-scrape"); status != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("%s: %d %.200s", path, status, body)
		}
	}
	// With no debug, the gzipped protobuf that go tool pprof reads.
	if status, body := scrape(t, "http://"+addr+"/debug/pprof/heap", "s3cret-scrape"); status != http.StatusOK || !strings.HasPrefix(body, "\x1f\x8b") {
		t.Fatalf("%d %q", status, body[:min(len(body), 16)])
	}
	// No other profile, and no index.
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/profile", "/debug/pprof/allocs", "/debug/pprof/cmdline"} {
		if status, _ := scrape(t, "http://"+addr+path, "s3cret-scrape"); status != http.StatusNotFound {
			t.Errorf("%s: %d", path, status)
		}
	}
}

// The port clients and hosts use serves no profile.
func TestNoProfilesOnTheClientsPort(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	for _, path := range []string{"/debug/pprof/heap", "/debug/pprof/heap?debug=1", "/debug/pprof/goroutine?debug=1", "/debug/pprof/goroutine?debug=2", "/debug/pprof/"} {
		status, body := scrape(t, p.url+path, "")
		if strings.Contains(body, "profile:") || strings.Contains(body, "goroutine ") || strings.HasPrefix(body, "\x1f\x8b") {
			t.Errorf("%s: %d %.200s", path, status, body)
		}
	}
}

// A token is asked for when given, and needed for any address but loopback.
func TestMetricsTakeAToken(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	if _, err := p.s.ServeMetrics("0.0.0.0:0", ""); err == nil || !strings.Contains(err.Error(), "need a token") {
		t.Fatal(err)
	}
	addr, err := p.s.ServeMetrics("127.0.0.1:0", "s3cret-scrape")
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := scrape(t, "http://"+addr+"/metrics", ""); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
	if status, _ := scrape(t, "http://"+addr+"/metrics", "wrong"); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
	if status, body := scrape(t, "http://"+addr+"/metrics", "s3cret-scrape"); status != http.StatusOK || !strings.Contains(body, "agents_control_up_seconds") {
		t.Fatal(status, body)
	}
}

// A method a client names is a label of its own only for the first few hundred names.
func TestMetricsMethodsAreBounded(t *testing.T) {
	t.Parallel()
	m := newMetrics()
	for i := range metricsMethods + 50 {
		m.Request("made/up"+itoa(i), "error", 0)
	}
	var b strings.Builder
	m.write(&b, gauges{phase: phaseServing})
	if got := strings.Count(b.String(), "agents_control_requests_total{"); got != metricsMethods+1 {
		t.Fatal(got)
	}
	if !strings.Contains(b.String(), `agents_control_requests_total{method="other",result="error"} 50`) {
		t.Fatal(b.String())
	}
}

func TestCapacityMetricsAreGauges(t *testing.T) {
	m := newMetrics()
	m.setCapacity(controlCapacity{level: "near", reason: "pressure_io", load1: 2.1, load5: 1.5,
		pressureMemory: 5.1, pressureIO: 10.1, memoryAvailable: 123, swapUsed: 4, heapInuse: 456,
		goroutines: 7, openFiles: 8})
	m.setStoreSize(9, 1000)
	var b strings.Builder
	m.write(&b, gauges{phase: phaseServing})
	for _, want := range []string{
		"agents_control_capacity_level 1", `agents_control_capacity_reason{reason="pressure_io"} 1`,
		"agents_control_load_per_core 2.1", "agents_control_pressure_io_full_avg60 10.1",
		"agents_control_memory_available_bytes 123", "agents_control_heap_inuse_bytes 456",
		"agents_control_goroutines 7", "agents_control_open_files 8", "agents_control_store_files 9", "agents_control_store_bytes 1000",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("metrics do not contain %q", want)
		}
	}
}

// stalledWriter is a scraper that stops reading: its first write never returns until let go.
type stalledWriter struct {
	writing chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *stalledWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.writing) })
	<-w.release
	return len(b), nil
}

// A scraper that stops reading mid-scrape holds no lock: requests, events and store
// operations are counted at once while it waits (#2249).
func TestStalledScraperDoesNotBlockCounting(t *testing.T) {
	t.Parallel()
	m := newMetrics()
	for i := range metricsMethods {
		m.Request("made/up"+itoa(i), "ok", time.Millisecond)
	}
	w := &stalledWriter{writing: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		m.write(w, gauges{phase: phaseServing})
		close(done)
	}()
	defer func() {
		close(w.release)
		<-done
	}()
	<-w.writing
	counted := make(chan struct{})
	go func() {
		m.Request("daemon/ping", "routed", 0)
		m.Event("agent/changed", 3)
		m.Store("get", nil, time.Millisecond)
		close(counted)
	}()
	select {
	case <-counted:
	case <-time.After(2 * time.Second):
		t.Fatal("counting waited for a stalled scraper")
	}
}

// JSON lines with the request's fields at debug, and no token in any line: not a param, not
// a code, not a bearer token (#2176).
func TestJSONLogsHoldNoToken(t *testing.T) {
	t.Parallel()
	var out syncBuffer
	logs := NewLog()
	logs.to = &out
	if err := logs.Configure("json", "debug"); err != nil {
		t.Fatal(err)
	}
	p := startPlaneWith(t, store.NewMemory(), controllink.NewPrivateKey(), func(cfg *Config) {
		cfg.Log, cfg.Logger = logs.Write, logs.Logger()
	})
	_, window := p.client("Alex's MacBook")
	// A param that is a token: never logged.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := window.Call(ctx, "", "clients/setPushToken", map[string]any{"token": "push-token-abc123", "environment": "production"}); err == nil {
		t.Fatal("a push token that is not one was taken")
	}
	call(t, window, "", "control/status", nil)
	logs.Write("agents-control[abcd1234]: paired with agents-control:2:h:SECRETCODE and Bearer tok-xyz, token=tok-123")

	data := out.String()
	for _, secret := range []string{"push-token-abc123", "SECRETCODE", "tok-xyz", "tok-123"} {
		if strings.Contains(data, secret) {
			t.Errorf("%s is in the log:\n%s", secret, data)
		}
	}
	var sawRequest, sawCopy bool
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("not a JSON line: %s", scanner.Text())
		}
		for _, field := range []string{"time", "level", "msg"} {
			if _, ok := line[field]; !ok {
				t.Fatalf("no %s in %s", field, scanner.Text())
			}
		}
		if line["msg"] == "request" && line["method"] == "control/status" {
			sawRequest = line["level"] == "DEBUG" && line["result"] == "ok" && line["client_id"] != "" &&
				line["person_id"] != nil && line["request_id"] != "" && line["copy"] != ""
		}
		if line["copy"] == "abcd1234" {
			sawCopy = true
		}
	}
	if !sawRequest || !sawCopy {
		t.Fatalf("request line %v, copy field %v:\n%s", sawRequest, sawCopy, data)
	}
}

// At info, the request lines are dropped; text lines stay as they were.
func TestLogLevelAndText(t *testing.T) {
	t.Parallel()
	var out syncBuffer
	logs := NewLog()
	logs.to = &out
	if err := logs.Configure("", "warn"); err != nil {
		t.Fatal(err)
	}
	logs.Write("agents-control[abcd1234]: an info line")
	logs.WriteAt(4, "agents-control: a warning")
	logs.Logger().Debug("request", "method", "control/status")
	data := out.String()
	if strings.Contains(data, "info line") || strings.Contains(data, "request") ||
		!strings.Contains(data, " agents-control: a warning\n") {
		t.Fatal(data)
	}
	if err := logs.Configure("yaml", ""); err == nil {
		t.Fatal("yaml taken")
	}
	if err := logs.Configure("", "loud"); err == nil {
		t.Fatal("loud taken")
	}
}

// syncBuffer is a buffer the log writes to from many goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// With Copy, each line goes to the file and still to standard error (#4747), so a
// container's file on a volume keeps the lines across a redeploy.
func TestACopiedLogWritesTheFileAndStandardError(t *testing.T) {
	t.Parallel()
	var out syncBuffer
	logs := NewLog()
	logs.to = &out
	file := filepath.Join(t.TempDir(), "control.log")
	if err := logs.Copy(file, LogLimit); err != nil {
		t.Fatal(err)
	}
	logs.Write("workspace: create machine-1 at Codespaces by alex: failed: token=tok-123 no quota")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"file": string(data), "standard error": out.String()} {
		if !strings.Contains(got, "failed: token=<redacted> no quota") || strings.Contains(got, "tok-123") {
			t.Errorf("%s: %q", name, got)
		}
	}
	if !logs.Taken() {
		t.Error("not taken")
	}
}
