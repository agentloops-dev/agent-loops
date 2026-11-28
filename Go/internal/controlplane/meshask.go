package controlplane

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
)

// A copy asks the copy that holds a host for what only that copy can see of it (#7315): the
// address its uplink comes from, its tunnel, and the calls on its channel 0. The ask and its
// answer go on the copies' link: {"ask":{"id","host","method","params"}} and
// {"answer":{"id","result"}}. The holder answers only for a host it holds, and never asks on,
// so an ask cannot go round the copies. A copy that does not know the method, or an older
// copy, does not answer, and the ask runs out at its context.

// peerAsk is an ask of the copy that holds Host.
type peerAsk struct {
	ID     string          `json:"id"`
	Host   string          `json:"host"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// peerAnswer is its answer: no Result when the copy does not hold the host or knows no such
// method.
type peerAnswer struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
}

// peerMethod answers an ask for a host this copy holds; false when it has no answer.
type peerMethod func(ctx context.Context, s *Service, host string, params json.RawMessage) (any, bool)

// peerMethods are the asks a copy answers.
var peerMethods = map[string]peerMethod{
	peerEligible: eligibleForPeer,
}

// peerAsks are the asks of this copy that wait for an answer, by id.
type peerAsks struct {
	mu      sync.Mutex
	waiting map[string]chan json.RawMessage
}

// holderLink is the link to the copy that holds host, nil when this copy holds it or knows no
// link to its holder.
func (m *mesh) holderLink(ctx context.Context, host string) *peerLink {
	if m.s.router.Holds(host) {
		return nil
	}
	m.mu.Lock()
	peer := ""
	if p := m.proxies[host]; p != nil && p.isOpen() {
		peer = p.peer
	}
	m.mu.Unlock()
	if peer == "" {
		lease := m.leases.holder(ctx, host)
		if lease == nil || lease.Copy == m.s.copyID {
			return nil
		}
		peer = lease.Copy
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.links[peer]
}

// askHolder asks the copy that holds host, and waits for its answer until ctx ends; false when
// no copy answered with a result.
func (m *mesh) askHolder(ctx context.Context, host, method string, params any) (json.RawMessage, bool) {
	link := m.holderLink(ctx, host)
	if link == nil {
		return nil, false
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, false
	}
	id := records.NewUUID()
	answer := make(chan json.RawMessage, 1)
	m.asks.mu.Lock()
	if m.asks.waiting == nil {
		m.asks.waiting = map[string]chan json.RawMessage{}
	}
	m.asks.waiting[id] = answer
	m.asks.mu.Unlock()
	defer func() {
		m.asks.mu.Lock()
		delete(m.asks.waiting, id)
		m.asks.mu.Unlock()
	}()
	if !link.conn.Send(encodePeer("ask", peerAsk{ID: id, Host: host, Method: method, Params: raw})) {
		return nil, false
	}
	select {
	case result := <-answer:
		return result, len(result) > 0
	case <-ctx.Done():
		return nil, false
	}
}

// answerPeer answers a peer's ask, with no result when it is not for a host held here.
func (m *mesh) answerPeer(ask peerAsk, link *peerLink) {
	out := peerAnswer{ID: ask.ID}
	if f := peerMethods[ask.Method]; f != nil && m.s.router.Holds(ask.Host) {
		if result, ok := f(context.Background(), m.s, ask.Host, ask.Params); ok {
			if raw, err := json.Marshal(result); err == nil {
				out.Result = raw
			}
		}
	}
	link.conn.Send(encodePeer("answer", out))
}

// answered is a peer's answer to an ask of this copy's; one nobody waits for is dropped.
func (m *mesh) answered(a peerAnswer) {
	m.asks.mu.Lock()
	answer := m.asks.waiting[a.ID]
	m.asks.mu.Unlock()
	if answer != nil {
		select {
		case answer <- a.Result:
		default:
		}
	}
}
