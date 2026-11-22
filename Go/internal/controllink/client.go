package controllink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// ClientMembership is what a paired client keeps to dial again.
type ClientMembership struct {
	Client     string `json:"client"`
	Private    []byte `json:"privateKey"`
	ControlKey []byte `json:"controlKey"`
	URL        string `json:"url"`
	Pin        string `json:"pin,omitempty"`
	Name       string `json:"name"`
	// What it says it has when it connects (#1748); nil says what a client declares.
	Capabilities []string `json:"-"`
}

// PairClient uses a client code once (clients/announce), as a window does.
func PairClient(ctx context.Context, code *Code, name string) (*ClientMembership, error) {
	return PairClientAs(ctx, code, name, "mac")
}

// PairClientAs is PairClient as another kind of client: "iPhone" or "iPad" for a walk's
// stand-in for a device (#1507).
func PairClientAs(ctx context.Context, code *Code, name, kind string) (*ClientMembership, error) {
	if code.Host {
		return nil, errors.New("that is a host code")
	}
	private := NewPrivateKey()
	pub, _ := PublicKey(private)
	id := strings.ToUpper(newRef())
	l, _, err := Join(ctx, code.URL, code.Pin, Credentials{Identity: "p:" + code.ID(), Key: CodeKey(code.Secret), Kind: "mac", ControlKey: code.ControlKey})
	if err != nil {
		return nil, err
	}
	defer l.Close()
	params, _ := json.Marshal(map[string]any{"id": id, "publicKey": pub, "name": name, "kind": kind})
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "clients/announce", "params": json.RawMessage(params)})
	if err := l.Write(ctx, string(req)); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	line, err := l.Read(rctx)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Result *struct {
			Client string `json:"client"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(line), &reply) != nil || reply.Result == nil {
		return nil, fmt.Errorf("clients/announce: %s", line)
	}
	if reply.Result.Client != "" {
		id = reply.Result.Client
	}
	return &ClientMembership{Client: id, Private: private, ControlKey: code.ControlKey, URL: code.URL, Pin: code.Pin, Name: code.Name}, nil
}

// Session is a client's socket to the control plane: requests to it or to a host, and
// the notifications every host sends.
type Session struct {
	l       LineConn
	next    atomic.Int64
	mu      sync.Mutex
	waiting map[int64]chan json.RawMessage
	Notes   chan Note
	// done is closed when the socket ends: a call waiting then fails at once.
	done chan struct{}
	// lock is the client's key in its machine lock, once UseLock gave it; routes are the
	// checked channel to each machine called since (#6563).
	lock   *ClientLock
	routes map[string]*route
}

// LineConn is what a Session reads and writes lines on: a control plane's socket, or a
// test's stand-in for one.
type LineConn interface {
	Write(ctx context.Context, line string) error
	Read(ctx context.Context) (string, error)
	Close()
}

// NewSession reads lines from l until ctx ends or l does.
func NewSession(ctx context.Context, l LineConn) *Session {
	s := &Session{l: l, waiting: map[int64]chan json.RawMessage{}, Notes: make(chan Note, 4096), done: make(chan struct{})}
	safe.Go("client read", func() { s.read(ctx) })
	return s
}

type Note struct {
	Host   string
	Method string
	Params json.RawMessage
}

func DialClient(ctx context.Context, m *ClientMembership) (*Session, error) {
	key, err := ClientKey(m.Private, m.ControlKey, m.Client)
	if err != nil {
		return nil, err
	}
	epoch := 0
	capabilities := m.Capabilities
	if capabilities == nil {
		capabilities = wire.DeclaredCapabilities["client"]
	}
	l, ok, err := Join(ctx, m.URL, m.Pin, Credentials{Identity: "c:" + m.Client, Key: key, Kind: "mac", ControlKey: m.ControlKey, Epoch: &epoch,
		Capabilities: capabilities})
	if err != nil {
		return nil, err
	}
	// The control plane rotated its key (#997): trusted from now on.
	if fresh, _ := NewKey(ok, key); fresh != nil {
		m.ControlKey = fresh
	}
	return NewSession(ctx, l), nil
}

func (s *Session) read(ctx context.Context) {
	defer close(s.done)
	defer close(s.Notes)
	for {
		line, err := s.l.Read(ctx)
		if err != nil {
			return
		}
		var f struct {
			H string          `json:"h"`
			M json.RawMessage `json:"m"`
		}
		if json.Unmarshal([]byte(line), &f) != nil {
			continue
		}
		if r := s.routeOf(f.H); r != nil {
			body, ok := r.heard(f.M)
			if !ok {
				continue
			}
			f.M = body
		}
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(f.M, &m) != nil {
			continue
		}
		if m.Method != "" && m.ID == nil {
			select {
			case s.Notes <- Note{Host: f.H, Method: m.Method, Params: m.Params}:
			default:
			}
			continue
		}
		if m.ID != nil {
			s.mu.Lock()
			w := s.waiting[*m.ID]
			delete(s.waiting, *m.ID)
			s.mu.Unlock()
			if w != nil {
				w <- f.M
			}
		}
	}
}

// Call sends method to host ("" for the control plane itself) and returns the result. With
// a lock (UseLock), a call to a machine goes on its checked channel; a machine that refuses
// it as not from the lock forgot the channel (its uplink went and came back, #6227), so it
// goes again, once, on a new one.
func (s *Session) Call(ctx context.Context, host, method string, params any) (json.RawMessage, error) {
	r, err := s.routeFor(ctx, host, method)
	if err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, s.next.Add(1), host, method, params, r)
	if r == nil || r.ch == nil || !machineRefused(err) {
		return raw, err
	}
	s.dropRoute(host, r)
	if r, err = s.routeFor(ctx, host, method); err != nil {
		return nil, err
	}
	return s.call(ctx, s.next.Add(1), host, method, params, r)
}

func (s *Session) call(ctx context.Context, id int64, host, method string, params any, r *route) (json.RawMessage, error) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, _ := json.Marshal(msg)
	frame := func(m string) string {
		if host == "" {
			return `{"m":` + m + `}`
		}
		return `{"h":"` + host + `","m":` + m + `}`
	}
	w := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.waiting[id] = w
	s.mu.Unlock()
	var err error
	if r != nil && r.ch != nil {
		// The count goes out in the order it was taken.
		r.send.Lock()
		err = s.l.Write(ctx, frame(r.ch.Seal(string(b))))
		r.send.Unlock()
	} else {
		err = s.l.Write(ctx, frame(string(b)))
	}
	if err != nil {
		s.forget(id)
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w (%v)", ErrLinkLost, err)
	}
	select {
	case raw := <-w:
		var r struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("a reply that is not JSON-RPC: %w", err)
		}
		if r.Error != nil {
			return nil, fmt.Errorf("%s", r.Error)
		}
		return r.Result, nil
	case <-s.done:
		return nil, ErrLinkLost
	case <-ctx.Done():
		s.forget(id)
		return nil, ctx.Err()
	case <-time.After(120 * time.Second):
		s.forget(id)
		return nil, errors.New("no answer in 120 s")
	}
}

// ErrLinkLost is a call that failed because the socket to the control plane ended, not
// because its host refused it: a client may dial again and read again (#6478).
var ErrLinkLost = errors.New("the control plane closed the connection")

func (s *Session) forget(id int64) {
	s.mu.Lock()
	delete(s.waiting, id)
	s.mu.Unlock()
}

func (s *Session) Close() { s.l.Close() }
