package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/flaky"
)

// faultProxy is a TCP proxy in front of the control plane that cuts the connections through
// it and delays what passes (#6185). It reads who each connection is from its first frame, so
// one proxy can cut the host's link and leave a client's, or the other way round: a member
// proves its address in what it says, so every member must dial the one address.
type faultProxy struct {
	ln    net.Listener
	delay atomic.Int64 // nanoseconds added before each chunk is passed on
	mu    sync.Mutex
	conns map[net.Conn]*proxied
}

// proxied is one connection's two ends and who it is ("" until its first frame is read).
type proxied struct {
	other net.Conn
	who   string
}

var whoIs = regexp.MustCompile(`"id":"([chpw]:[^"]+)"`)

func newFaultProxy(t *testing.T) *faultProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &faultProxy{ln: ln, conns: map[net.Conn]*proxied{}}
	t.Cleanup(func() { _ = ln.Close(); p.cut("") })
	return p
}

func (p *faultProxy) url() string { return "http://" + p.ln.Addr().String() }

func (p *faultProxy) serve(target string) {
	go func() {
		for {
			c, err := p.ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			pc := &proxied{other: up}
			p.mu.Lock()
			p.conns[c], p.conns[up] = pc, pc
			p.mu.Unlock()
			go p.pipe(c, up, pc, true)
			go p.pipe(up, c, pc, false)
		}
	}()
}

func (p *faultProxy) pipe(from, to net.Conn, pc *proxied, fromClient bool) {
	buf := make([]byte, 4096)
	var seen []byte
	for {
		n, err := from.Read(buf)
		if n > 0 {
			if fromClient {
				// The member's own id is in its first frames, after the upgrade request.
				p.mu.Lock()
				if pc.who == "" {
					seen = append(seen, buf[:n]...)
					if m := whoIs.FindSubmatch(unmaskFrames(seen)); m != nil {
						pc.who = string(m[1])
					}
				}
				p.mu.Unlock()
			}
			if d := time.Duration(p.delay.Load()); d > 0 {
				time.Sleep(d)
			}
			if _, werr := to.Write(buf[:n]); werr != nil {
				err = werr
			}
		}
		if err != nil {
			_ = from.Close()
			_ = to.Close()
			p.mu.Lock()
			delete(p.conns, from)
			delete(p.conns, to)
			p.mu.Unlock()
			return
		}
	}
}

// unmaskFrames is what the websocket frames after the HTTP upgrade request say, for the ones
// that are whole; a test's frames are short.
func unmaskFrames(b []byte) []byte {
	_, rest, ok := bytes.Cut(b, []byte("\r\n\r\n"))
	if !ok {
		return nil
	}
	var out []byte
	for len(rest) >= 2 {
		n, at := int(rest[1]&0x7f), 2
		switch n {
		case 126:
			if len(rest) < 4 {
				return out
			}
			n, at = int(rest[2])<<8|int(rest[3]), 4
		case 127:
			return out
		}
		var mask []byte
		if rest[1]&0x80 != 0 {
			if len(rest) < at+4 {
				return out
			}
			mask, at = rest[at:at+4], at+4
		}
		if len(rest) < at+n {
			return out
		}
		for i, c := range rest[at : at+n] {
			if mask != nil {
				c ^= mask[i%4]
			}
			out = append(out, c)
		}
		rest = rest[at+n:]
	}
	return out
}

// cut drops the connections of the member whose id (h:…, c:…) is who, or all of them when who
// is ""; later ones are let through. Ids are compared without case.
func (p *faultProxy) cut(who string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c, pc := range p.conns {
		if who == "" || strings.EqualFold(pc.who, who) {
			_ = c.Close()
		}
	}
}

// observer is a client that dials again whenever its socket ends, as the apps do, and
// keeps what it heard.
type observer struct {
	t       *testing.T
	m       *controllink.ClientMembership
	url     string
	mu      sync.Mutex
	session *controllink.Session
	notes   []controllink.Note
	dials   int
}

func (p *plane) observe(proxyURL, name string) *observer {
	// p.client's own socket is let go: the plane holds one socket for each client.
	m, first := p.client(name)
	first.Close()
	o := &observer{t: p.t, m: m, url: proxyURL}
	ctx, cancel := context.WithCancel(context.Background())
	p.t.Cleanup(cancel)
	go o.run(ctx)
	p.waitFor(func() bool { return o.current() != nil }, name+" connected")
	return o
}

func (o *observer) current() *controllink.Session {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.session
}

func (o *observer) run(ctx context.Context) {
	for ctx.Err() == nil {
		// DialClient takes the endpoints the control plane gives; this one keeps to its proxy.
		o.m.URL, o.m.Pin = o.url, ""
		// The socket's reads live as long as ctx, so it is not given a timeout of its own.
		s, err := controllink.DialClient(ctx, o.m)
		if err != nil {
			o.t.Logf("dial: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		o.mu.Lock()
		o.session = s
		o.dials++
		o.mu.Unlock()
		for n := range s.Notes {
			o.mu.Lock()
			o.notes = append(o.notes, n)
			o.mu.Unlock()
		}
		o.mu.Lock()
		o.session = nil
		o.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
}

func (o *observer) heard(method string) (got []controllink.Note) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, n := range o.notes {
		if n.Method == method {
			got = append(got, n)
		}
	}
	return got
}

// requireInOrder fails when a notification's seq is heard twice or after a later one, or when
// none was heard.
func (o *observer) requireInOrder(t *testing.T, method string) {
	t.Helper()
	last := int64(0)
	for _, n := range o.heard(method) {
		var pr struct{ Seq int64 }
		if err := json.Unmarshal(n.Params, &pr); err != nil {
			t.Fatal(err)
		}
		if pr.Seq <= last {
			t.Fatalf("notification %d came after %d: twice or out of order", pr.Seq, last)
		}
		last = pr.Seq
	}
	if last == 0 {
		t.Fatal("the client heard no notification at all")
	}
}

// hostStates is the states of host that control/hostChanged notes said, in order.
func (o *observer) hostStates(host string) (states []string) {
	for _, n := range o.heard("control/hostChanged") {
		var p struct{ Host, State string }
		_ = json.Unmarshal(n.Params, &p) // a note that is not one has no state
		if p.Host == host || n.Host == host {
			states = append(states, p.State)
		}
	}
	return states
}

// ask is one call on the observer's current socket; false when it has none or it fails.
func (o *observer) ask(host, method string) (json.RawMessage, bool) {
	s := o.current()
	if s == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := s.Call(ctx, host, method, nil)
	return got, err == nil
}

// hostWorld is what a fake host holds: its agents and the question cards waiting on an answer.
type hostWorld struct {
	mu       sync.Mutex
	agents   []map[string]any
	pending  []map[string]any
	nextCard int
}

func (w *hostWorld) snapshot() (agents, pending []map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]any{}, w.agents...), append([]map[string]any{}, w.pending...)
}

// A proxy between the host, the control plane and a client drops and delays their connections
// at random points, and each time the client reads the host's agents and question cards again
// and finds what the host holds, hears of the outage once (offline, then online), and hears
// no notification twice or out of order (#6185).
func TestAProxyCutsTheHostsLinkAndTheClientCatchesUp(t *testing.T) {
	flaky.UnderLoad(t, "#6346")
	t.Parallel()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, 6185)) //nolint:gosec // a fault pattern, not a secret

	proxy := newFaultProxy(t)
	// The plane's own address is the host proxy's: the host and each code go through it.
	p := startPlaneWith(t, store.NewMemory(), controllink.NewPrivateKey(), func(c *Config) { c.URL = proxy.url() })
	direct := p.url[len("http://"):]
	proxy.serve(direct)
	p.redial = []time.Duration{20 * time.Millisecond, 100 * time.Millisecond, 50 * time.Millisecond}

	world := &hostWorld{}
	box := p.host("MACHINE-2")
	box.mu.Lock()
	box.agents = func(json.RawMessage) any { a, _ := world.snapshot(); return a }
	box.answers = map[string]func(json.RawMessage) any{"elicitations/pending": func(json.RawMessage) any { _, q := world.snapshot(); return q }}
	box.mu.Unlock()
	host := box.member.Host
	window := p.observe(proxy.url(), "Window")

	var seq atomic.Int64
	say := func() {
		n := seq.Add(1)
		box.uplink.Broadcast(fmt.Sprintf(`{"jsonrpc":"2.0","method":"agent/changed","params":{"seq":%d}}`, n))
	}
	change := func(i int) {
		world.mu.Lock()
		world.agents = append(world.agents, map[string]any{"id": fmt.Sprintf("A%02d", i), "state": "running"})
		world.nextCard++
		world.pending = append(world.pending, map[string]any{"id": fmt.Sprintf("Q%02d", world.nextCard), "message": "which?"})
		world.mu.Unlock()
		say()
	}

	matches := func() bool {
		wantAgents, wantPending := world.snapshot()
		var gotAgents, gotPending []map[string]any
		a, ok := window.ask(host, "agents/list")
		if !ok || json.Unmarshal(a, &gotAgents) != nil {
			return false
		}
		q, ok := window.ask(host, "elicitations/pending")
		if !ok || json.Unmarshal(q, &gotPending) != nil {
			return false
		}
		norm := func(v []map[string]any) []map[string]any {
			if v == nil {
				return []map[string]any{}
			}
			return v
		}
		return reflect.DeepEqual(norm(wantAgents), norm(gotAgents)) && reflect.DeepEqual(norm(wantPending), norm(gotPending))
	}
	const catchUp = 8 * time.Second
	waitMatch := func(what string) {
		t.Helper()
		deadline := time.Now().Add(catchUp)
		for !matches() {
			if time.Now().After(deadline) {
				a, q := world.snapshot()
				t.Fatalf("%s: the client did not catch up within %s; the host holds %d agents and %d cards", what, catchUp, len(a), len(q))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitMatch("at the start")
	statesBefore := len(window.hostStates(host))

	const faults = 8
	for i := 1; i <= faults; i++ {
		// A burst of changes, and the link cut part way through it.
		burst := 3 + rng.IntN(6)
		cutAt := rng.IntN(burst)
		if rng.IntN(2) == 0 {
			proxy.delay.Store(int64(time.Duration(5+rng.IntN(30)) * time.Millisecond))
		}
		for j := 0; j < burst; j++ {
			if j == cutAt {
				proxy.cut("h:" + host)
			}
			change(i*100 + j)
			time.Sleep(time.Duration(rng.IntN(20)) * time.Millisecond)
		}
		proxy.delay.Store(0)
		// The outage is over when the host is online again and the client holds what it holds.
		p.waitFor(func() bool { s, _ := p.s.Router().State(host); return s.online() }, fmt.Sprintf("fault %d: the host online again", i))
		waitMatch(fmt.Sprintf("fault %d", i))
		// Let the plane's last notice of this fault arrive before the next one is made.
		time.Sleep(200 * time.Millisecond)
		got := window.hostStates(host)[statesBefore:]
		want := []string{}
		for range i {
			want = append(want, "offline", "online")
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("after %d cuts the client heard %v, want one offline and one online for each: %v", i, got, want)
		}
	}

	window.requireInOrder(t, "agent/changed")
}

// A proxy cuts the client's own connection at random points while the host goes on changing:
// the client dials again, reads what the host holds, and loses no question card; a second
// client that stays connected hears one notice for each of the first one's outages (#6185).
func TestAProxyCutsTheClientsLinkAndNoQuestionIsLost(t *testing.T) {
	t.Parallel()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, 6186)) //nolint:gosec // a fault pattern, not a secret

	proxy := newFaultProxy(t)
	p := startPlaneWith(t, store.NewMemory(), controllink.NewPrivateKey(), func(c *Config) { c.URL = proxy.url() })
	proxy.serve(p.url[len("http://"):])
	p.redial = []time.Duration{20 * time.Millisecond, 100 * time.Millisecond, 50 * time.Millisecond}
	world := &hostWorld{}
	box := p.host("MACHINE-2")
	box.mu.Lock()
	box.agents = func(json.RawMessage) any { a, _ := world.snapshot(); return a }
	box.answers = map[string]func(json.RawMessage) any{"elicitations/pending": func(json.RawMessage) any { _, q := world.snapshot(); return q }}
	box.mu.Unlock()
	host := box.member.Host

	stable := p.observe(proxy.url(), "Stable")
	cut := p.observe(proxy.url(), "Cut")
	changesBefore := len(stable.heard("control/clientChanged"))

	const faults = 6
	for i := 1; i <= faults; i++ {
		proxy.cut("c:" + cut.m.Client)
		// A card raised while the client is away, and more as it comes back.
		for j := 0; j < 3; j++ {
			world.mu.Lock()
			world.nextCard++
			world.pending = append(world.pending, map[string]any{"id": fmt.Sprintf("Q%02d", world.nextCard), "message": "which?"})
			world.mu.Unlock()
			time.Sleep(time.Duration(rng.IntN(40)) * time.Millisecond)
		}
		deadline := time.Now().Add(8 * time.Second)
		for {
			_, wantPending := world.snapshot()
			var got []map[string]any
			if raw, ok := cut.ask(host, "elicitations/pending"); ok && json.Unmarshal(raw, &got) == nil && len(got) == len(wantPending) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("fault %d: the cut client did not read the %d cards the host holds", i, len(wantPending))
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		// One outage is at most a notice that it went and a notice that it came back.
		if got := len(stable.heard("control/clientChanged")) - changesBefore; got > 2*i {
			t.Fatalf("after %d cuts the stable client heard %d notices of the other, at most %d", i, got, 2*i)
		}
	}
}
