package controllink

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A client's calls to machines checked end to end (#3492, specs/3399-machine-lock D6; #6563
// for loops). A machine that holds its person's lock takes a call from the control plane
// only on a channel a client of the lock opened with lock/hello. With UseLock, a Session
// opens one to each machine it calls, the first time, seals every request on it, and takes
// an answer from it only when its MAC checks, as the web page's lockChannel.ts and link.ts
// do. A machine with no lock answers lock/hello with a failure, and its calls go as before.

// ClientLock is a client's signing key in its person's machine lock, and the lock as the
// client checked it.
type ClientLock struct {
	Signer *ecdsa.PrivateKey
	State  *machinelock.State
}

// helloWait bounds lock/hello: a machine that does not answer it is called as before.
const helloWait = 30 * time.Second

// route is the channel to one machine.
type route struct {
	// ready is closed when lock/hello has ended, with a channel or none.
	ready chan struct{}
	// ch is the channel; nil when the machine opened none (it has no lock, or refused).
	ch *machinelock.Channel
	// required: the lock names the machine, so an answer with no MAC is not taken from it.
	required bool
	// helloID is lock/hello's id: its answer has no MAC, its signature is its proof.
	helloID atomic.Int64
	// replied: lock/hello was answered, so every line from the machine after it has a MAC.
	replied atomic.Bool
	// send is held from a seal to its write, so the counts go out in order.
	send sync.Mutex
}

// UseLock checks every call to a machine end to end from now on.
func (s *Session) UseLock(l *ClientLock) {
	s.mu.Lock()
	s.lock = l
	s.routes = map[string]*route{}
	s.mu.Unlock()
}

func (s *Session) routeOf(host string) *route {
	if host == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routes[host]
}

func (s *Session) dropRoute(host string, r *route) {
	s.mu.Lock()
	if s.routes[host] == r {
		delete(s.routes, host)
	}
	s.mu.Unlock()
}

// routeFor is the channel a call to host goes on, opened first when there is none yet; nil
// with no lock, for the control plane, and for lock/hello itself.
func (s *Session) routeFor(ctx context.Context, host, method string) (*route, error) {
	if host == "" || method == wire.CallLockHello.Name {
		return nil, nil
	}
	s.mu.Lock()
	l := s.lock
	if l == nil {
		s.mu.Unlock()
		return nil, nil
	}
	r := s.routes[host]
	opened := r == nil
	if opened {
		r = &route{ready: make(chan struct{}), required: l.State.Names(host)}
		s.routes[host] = r
	}
	s.mu.Unlock()
	if opened {
		s.hello(ctx, host, r, l)
	}
	select {
	case <-r.ready:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// hello is lock/hello to host. When it fails the route has no channel, and the calls go as
// before the lock: a machine with a lock refuses them, and says why.
func (s *Session) hello(ctx context.Context, host string, r *route, l *ClientLock) {
	defer close(r.ready)
	h, err := machinelock.NewHello(l.Signer)
	if err != nil {
		return
	}
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), helloWait)
	defer cancel()
	id := s.next.Add(1)
	r.helloID.Store(id)
	raw, err := s.call(hctx, id, host, wire.CallLockHello.Name, h.Params, nil)
	if err != nil {
		return
	}
	var reply wire.LockHelloReply
	if json.Unmarshal(raw, &reply) != nil {
		return
	}
	if ch, err := h.Finish(l.State, host, reply); err == nil {
		r.ch = ch
	}
}

// heard is a line from the route's machine as the session takes it: opened when it has a
// MAC, false when it is not taken.
func (r *route) heard(raw json.RawMessage) (json.RawMessage, bool) {
	var m struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return raw, true
	}
	if m.ID != nil && *m.ID == r.helloID.Load() && m.Method == "" {
		r.replied.Store(true)
		return raw, true
	}
	if !r.replied.Load() {
		// Before lock/hello's answer the machine MACs nothing, and no call waits on it yet.
		return raw, true
	}
	<-r.ready
	answer := m.ID != nil && m.Method == ""
	if r.ch == nil {
		if r.required && answer && m.Result != nil {
			return refusal(*m.ID, "The machine's answer was not signed, so it was not taken."), true
		}
		return raw, true
	}
	if machinelock.Sealed(string(raw)) {
		opened, err := r.ch.Open(string(raw))
		if err != nil {
			if answer {
				return refusal(*m.ID, "The machine's answer does not check, so it was not taken."), true
			}
			return nil, false
		}
		return json.RawMessage(opened), true
	}
	switch {
	case answer && m.Error != nil:
		// A machine's refusals go with no MAC.
		return raw, true
	case answer:
		return refusal(*m.ID, "The machine's answer was not signed, so it was not taken."), true
	}
	// What the machine says unasked comes with its MAC once the channel is up.
	return nil, false
}

// refusedHere marks a refusal this side made of a machine's answer, so Call does not send
// the call again for it: the machine took the call, and the control plane changed its answer.
const refusedHere = "client"

func refusal(id int64, why string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": wire.FailureNotInLock, "message": why, "data": refusedHere}})
	return b
}

// machineRefused says err is a machine's own refusal as not from the lock.
func machineRefused(err error) bool {
	if FailureCode(err) != wire.FailureNotInLock {
		return false
	}
	var e struct {
		Data string `json:"data"`
	}
	_ = json.Unmarshal([]byte(err.Error()), &e)
	return e.Data != refusedHere
}

// FailureCode is the JSON-RPC code of a call's error, as Call gives it; 0 for none, or an
// error that is not a refusal.
func FailureCode(err error) int {
	if err == nil {
		return 0
	}
	var e struct {
		Code int `json:"code"`
	}
	if json.Unmarshal([]byte(err.Error()), &e) != nil {
		return 0
	}
	return e.Code
}
