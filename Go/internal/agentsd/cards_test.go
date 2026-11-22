package agentsd

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Question and permission cards end to end (#6188, epic #6183): the fake runtime asks, each
// client hears the card within cardLimit of the ask, the control plane is offered it once
// (its push to the phone and the web), and the card goes everywhere when it is answered,
// stopped or lost to a restart. The client models' side is the recorded stream
// Packages/Wire/streams/session-cards.json, which StreamHarnessTests and the web's
// streamHarness play.

// cardLimit is the fixed time for a card to reach every client of its machine, and its
// offer the control plane (#6188: 2 s on one machine, well above a loopback's).
const cardLimit = 2 * time.Second

// cardLine is one line a listening client read, and when.
type cardLine struct {
	at     time.Time
	method string
	params json.RawMessage
	id     string
	failed bool
}

// listener is a client that only listens, and answers a card when told: every line it
// reads is kept with its time, so the test reads when each client heard the card.
type listener struct {
	c    *conn
	mu   sync.Mutex
	got  []cardLine
	next int
}

func listen(t *testing.T, d *Daemon) *listener {
	t.Helper()
	l := &listener{c: newConn()}
	go d.Accept(l.c)
	t.Cleanup(func() { close(l.c.in) })
	clientAdded(t, d, l.c)
	go func() {
		for {
			select {
			case line := <-l.c.out:
				var m message
				_ = json.Unmarshal([]byte(line), &m) // a line it cannot read is kept empty
				l.mu.Lock()
				params := m.Params
				if m.Method == "" {
					params = m.Result
				}
				l.got = append(l.got, cardLine{at: time.Now(), method: m.Method, params: params,
					id: strings.TrimSpace(string(m.ID)), failed: m.Error != nil})
				l.mu.Unlock()
			case <-d.ctx.Done():
				return
			}
		}
	}()
	return l
}

// wait is the first line since the last wait that ok accepts.
func (l *listener) wait(t *testing.T, what string, ok func(cardLine) bool) cardLine {
	t.Helper()
	var found *cardLine
	waitFor(t, what, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		for i := l.next; i < len(l.got); i++ {
			if ok(l.got[i]) {
				found, l.next = &l.got[i], i+1
				return true
			}
		}
		return false
	})
	return *found
}

// count is how many lines ok accepts, of all it read.
func (l *listener) count(ok func(cardLine) bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, h := range l.got {
		if ok(h) {
			n++
		}
	}
	return n
}

func (l *listener) send(id int, method string, params any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	l.c.in <- string(b)
}

// offers keeps what the daemon tells the control plane, with when.
type offers struct {
	mu   sync.Mutex
	sent []wire.AttentionNeed
	at   []time.Time
}

func (o *offers) tell(method string, params any) {
	if method != wire.CallAttentionNeed.Name {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, params.(wire.AttentionNeed))
	o.at = append(o.at, time.Now())
}

// of is each offer (need) and each withdraw of agent id's need of kind.
func (o *offers) of(id wire.UUID, kind wire.NeedKind) (offered []time.Time, withdrawn int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, m := range o.sent {
		if m.Need != nil && m.Need.Kind == kind && m.Need.AgentID != nil && *m.Need.AgentID == id {
			offered = append(offered, o.at[i])
		}
		if w := m.Withdraw; w != nil && ((kind == wire.NeedKindPermission && w.Permission != nil) ||
			(kind == wire.NeedKindElicitation && (w.Elicitation != nil || w.Request != nil))) {
			withdrawn++
		}
	}
	return offered, withdrawn
}

// withdrawnLast is whether agent id's need of kind was offered and a withdraw came after
// its last offer.
func (o *offers) withdrawnLast(id wire.UUID, kind wire.NeedKind) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	offered, withdrawn := false, false
	for _, m := range o.sent {
		if m.Need != nil && m.Need.Kind == kind && m.Need.AgentID != nil && *m.Need.AgentID == id {
			offered, withdrawn = true, false
		}
		if m.Withdraw != nil && offered {
			withdrawn = true
		}
	}
	return withdrawn
}

// cardKind is one of the two cards: its notification, its pending read, its answer.
type cardKind struct {
	name, prompt, notification, pending string
	need                                wire.NeedKind
	// card is the request a notification raises (nil: the card goes), with its time.
	card   func(json.RawMessage) (id, agent wire.UUID, asked *wire.Date)
	answer func(card wire.UUID, i int) (string, any)
	// answered is the transcript's line for an answer.
	answered func(wire.TranscriptEntryKind) bool
}

var cardKinds = []cardKind{
	{
		name: "permission", prompt: "Please ask, then write hi.txt", notification: "agent/permission",
		pending: "permissions/pending", need: wire.NeedKindPermission,
		card: func(p json.RawMessage) (wire.UUID, wire.UUID, *wire.Date) {
			var n wire.PermissionNotification
			if json.Unmarshal(p, &n) != nil || n.Request == nil {
				return n.RequestID, n.AgentID, nil
			}
			return n.RequestID, n.AgentID, &n.Request.AskedAt
		},
		answer: func(card wire.UUID, i int) (string, any) {
			return "permissions/answer", map[string]any{"permissionID": card, "optionID": []string{"allow", "reject"}[i%2]}
		},
		answered: func(k wire.TranscriptEntryKind) bool { return k.PermissionAnswered != nil },
	},
	{
		name: "question", prompt: "Fill in a form", notification: "agent/elicitation",
		pending: "elicitations/pending", need: wire.NeedKindElicitation,
		card: func(p json.RawMessage) (wire.UUID, wire.UUID, *wire.Date) {
			var n wire.ElicitationNotification
			if json.Unmarshal(p, &n) != nil || n.Request == nil {
				return n.RequestID, n.AgentID, nil
			}
			return n.RequestID, n.AgentID, &n.Request.AskedAt
		},
		answer: func(card wire.UUID, i int) (string, any) {
			return "elicitations/answer", map[string]any{"requestID": card, "action": "accept",
				"content": map[string]any{"colour": []string{"red", "blue"}[i%2]}}
		},
		answered: func(k wire.TranscriptEntryKind) bool { return k.ElicitationAnswered != nil },
	},
}

// raised is a listener's line that raises a card of k for agent (any agent when "").
func (k cardKind) raised(agent wire.UUID) func(cardLine) bool {
	return func(h cardLine) bool {
		if h.method != k.notification {
			return false
		}
		_, a, asked := k.card(h.params)
		return asked != nil && (agent == "" || a == agent)
	}
}

// gone is a listener's line that takes card down.
func (k cardKind) gone(card wire.UUID) func(cardLine) bool {
	return func(h cardLine) bool {
		if h.method != k.notification {
			return false
		}
		id, _, asked := k.card(h.params)
		return asked == nil && id == card
	}
}

// shown waits for the card on every client, within cardLimit of its ask, and for its one
// offer to the control plane; it is the card's id.
func (k cardKind) shown(t *testing.T, agent wire.UUID, control *offers, clients ...*listener) wire.UUID {
	t.Helper()
	var card wire.UUID
	for i, l := range clients {
		h := l.wait(t, fmt.Sprintf("client %d hears the %s card", i, k.name), k.raised(agent))
		id, _, asked := k.card(h.params)
		if late := h.at.Sub(asked.Time()); late > cardLimit {
			t.Errorf("client %d heard the %s card %v after it was asked (limit %v)", i, k.name, late, cardLimit)
		}
		if card != "" && id != card {
			t.Fatalf("client %d heard card %s, another heard %s", i, id, card)
		}
		card = id
		asked0 := asked.Time()
		if control != nil {
			waitFor(t, "the "+k.name+" card offered", func() bool { o, _ := control.of(agent, k.need); return len(o) > 0 })
			o, _ := control.of(agent, k.need)
			if late := o[len(o)-1].Sub(asked0); late > cardLimit {
				t.Errorf("the %s card was offered %v after it was asked (limit %v)", k.name, late, cardLimit)
			}
		}
	}
	return card
}

func (k cardKind) start(t *testing.T, c *conn, id int, cwd string) wire.UUID {
	t.Helper()
	var agent wire.UUID
	if err := json.Unmarshal(c.call(t, id, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + cwd,
		"prompt": k.prompt}), &agent); err != nil {
		t.Fatal(err)
	}
	return agent
}

// Each card shows on every client within cardLimit, a client that comes after it reads it,
// the control plane is offered it once, and of two answers at once one wins: both clients
// see the card go, and the record holds one answer.
func TestEachCardShowsOnEveryClientAndOneAnswerWins(t *testing.T) {
	for _, k := range cardKinds {
		t.Run(k.name, func(t *testing.T) {
			t.Parallel()
			fakeRuntimes(t)
			d, c := start(t, t.TempDir())
			control := &offers{}
			d.SetControl(control.tell)
			one, two := listen(t, d), listen(t, d)
			agent := k.start(t, c, 1, t.TempDir())
			card := k.shown(t, agent, control, one, two)

			k.readLate(t, d, c, agent, card)
			k.oneAnswerWins(t, d, control, agent, card, one, two)
		})
	}
}

// An offer that is still on its way to the control plane when the card is answered goes
// before the answer's withdraw (#6627): the control plane does not keep a card that was
// answered. The tell holds the card's offer until the answer's round has had time to
// withdraw it; the rounds send in their order, so the withdraw comes last.
func TestAnOfferStillOnItsWayGoesBeforeItsWithdraw(t *testing.T) {
	for _, k := range cardKinds {
		t.Run(k.name, func(t *testing.T) {
			t.Parallel()
			k.offerBeforeWithdraw(t)
		})
	}
}

func (k cardKind) offerBeforeWithdraw(t *testing.T) {
	t.Helper()
	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	control := &offers{}
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	d.SetControl(func(method string, params any) {
		if n, ok := params.(wire.AttentionNeed); ok && n.Need != nil {
			once.Do(func() {
				close(held)
				select {
				case <-release:
				case <-afterLimit(t):
				}
			})
		}
		control.tell(method, params)
	})
	one := listen(t, d)
	agent := k.start(t, c, 1, t.TempDir())
	h := one.wait(t, "the client hears the "+k.name+" card", k.raised(agent))
	card, _, _ := k.card(h.params)
	select {
	case <-held:
	case <-afterLimit(t):
		t.Fatal("the card was never offered to the control plane")
	}
	method, params := k.answer(card, 0)
	one.send(100, method, params)
	// Time for the answer's round to withdraw the card, before its offer goes. The
	// answer's round waits for the offer to go, so the card goes after the release.
	time.AfterFunc(200*time.Millisecond, func() { close(release) })
	one.wait(t, "the client sees the card go", k.gone(card))
	waitFor(t, "the turn's end", func() bool { return d.Store.Agent(agent).State == wire.AgentStateFinished })
	d.reconsider()
	if !control.withdrawnLast(agent, k.need) {
		control.mu.Lock()
		defer control.mu.Unlock()
		t.Fatalf("the control plane was not told last that the card is gone: %+v", control.sent)
	}
}

// A card of an agent that is stopped goes from every client and from the control plane,
// and an answer that comes after is refused, not taken (failure mode 4).
func TestACardOfAStoppedAgentGoesEverywhere(t *testing.T) {
	for _, k := range cardKinds {
		t.Run(k.name, func(t *testing.T) {
			t.Parallel()
			fakeRuntimes(t)
			d, c := start(t, t.TempDir())
			control := &offers{}
			d.SetControl(control.tell)
			one, two := listen(t, d), listen(t, d)
			agent := k.start(t, c, 1, t.TempDir())
			card := k.shown(t, agent, control, one, two)
			c.call(t, 2, "agents/stop", map[string]any{"agentID": agent})
			for i, l := range []*listener{one, two} {
				l.wait(t, fmt.Sprintf("client %d sees the card go", i), k.gone(card))
			}
			waitFor(t, "stopped", func() bool { return d.Store.Agent(agent).State == wire.AgentStateStopped })
			waitFor(t, "withdrawn from the control plane", func() bool { _, w := control.of(agent, k.need); return w > 0 })
			method, params := k.answer(card, 0)
			one.send(3, method, params)
			if h := one.wait(t, "the late answer's reply", func(h cardLine) bool { return h.id == "3" }); !h.failed {
				t.Fatalf("an answer to a stopped agent's card was taken: %s", h.params)
			}
			entries, _, _ := d.Store.Transcript(agent, 0)
			for _, e := range entries {
				if k.answered(e.Kind) && k.name == "permission" {
					t.Fatalf("a late answer was recorded: %+v", e)
				}
			}
		})
	}
}

// A stop lets the runtime end its turn before the permission card's wait has woken: the
// runtime's own session/cancel cancels its request (#6367). The card's late wake-up does
// not take the stopped agent back to running.
func TestAPermissionCardLetGoAfterTheStopLeavesItStopped(t *testing.T) {
	t.Parallel()
	fakeRuntimes(t)
	k := cardKinds[0]
	d, c := start(t, t.TempDir())
	d.SetControl((&offers{}).tell)
	one := listen(t, d)
	var agent wire.UUID
	if err := json.Unmarshal(c.call(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(),
		"prompt": "Please ask, and end at once on a stop"}), &agent); err != nil {
		t.Fatal(err)
	}
	k.shown(t, agent, nil, one)
	held, moved := make(chan func(), 1), make(chan struct{})
	hold := func(l *live, move func()) {
		if l.id != agent {
			move()
			return
		}
		held <- move
		<-moved
	}
	permissionLetGo.Store(&hold)
	t.Cleanup(func() { permissionLetGo.CompareAndSwap(&hold, nil) })
	c.call(t, 2, "agents/stop", map[string]any{"agentID": agent})
	var move func()
	select {
	case move = <-held:
	case <-afterLimit(t):
		waitFailed(t, "the card was never let go")
	}
	waitFor(t, "stopped", func() bool { return d.Store.Agent(agent).State == wire.AgentStateStopped })
	move()
	close(moved)
	if a := d.Store.Agent(agent); a.State != wire.AgentStateStopped {
		t.Fatalf("the card's late wake-up moved the stopped agent to %s", a.State)
	}
}

// A card that waits while the daemon restarts (failure mode 2) is on no client after it
// and on no pending read, its agent does not wait on a card that is not there, and the
// question asked again after the pick-up shows on every client within cardLimit and is
// offered to the control plane once.
func TestACardAfterARestartShowsAgain(t *testing.T) {
	t.Parallel()
	fakeRuntimes(t)
	testHooksOf(t).change(func(h *testHooks) { h.opts.runtimeEnv = append(h.opts.runtimeEnv, "AGENTS_FAKE_ASK_AGAIN=1") })
	root := t.TempDir()
	k := cardKinds[0]
	d, c := start(t, root)
	d.SetControl((&offers{}).tell)
	agent := k.start(t, c, 1, t.TempDir())
	before := k.shown(t, agent, nil, listen(t, d))
	d.Shutdown()

	d2, c2 := start(t, root)
	control := &offers{}
	d2.SetControl(control.tell)
	one, two := listen(t, d2), listen(t, d2)
	if got := d2.pendingPermissions(); len(got) != 0 {
		t.Fatalf("pending after the restart: %+v", got)
	}
	if a := d2.Store.Agent(agent); a.State == wire.AgentStateWaitingOnUser {
		t.Fatalf("waiting on a card no client can see: %+v", a.State)
	}
	d2.PickUpAfterRestart()
	again := k.shown(t, agent, control, one, two)
	if again == before {
		t.Fatalf("the card asked again kept the gone card's id %s", before)
	}
	if offered, _ := control.of(agent, k.need); len(offered) != 1 {
		t.Fatalf("offered %d times", len(offered))
	}
	c2.call(t, 2, "permissions/answer", map[string]any{"permissionID": again, "optionID": "allow"})
	for i, l := range []*listener{one, two} {
		l.wait(t, fmt.Sprintf("client %d sees the card go", i), k.gone(again))
	}
	waitFor(t, "the turn's end", func() bool { return d2.Store.Agent(agent).State == wire.AgentStateFinished })
}

// readLate is failure mode 1: a client that was not there when the card was asked reads it,
// and attention/pending holds one need for it.
func (k cardKind) readLate(t *testing.T, d *Daemon, c *conn, agent, card wire.UUID) {
	t.Helper()
	late := listen(t, d)
	late.send(1, k.pending, map[string]any{})
	read := late.wait(t, "the pending read", func(h cardLine) bool { return h.id == "1" })
	if !strings.Contains(string(read.params), string(card)) {
		var got []json.RawMessage
		late.mu.Lock()
		for _, h := range late.got {
			got = append(got, h.params)
		}
		late.mu.Unlock()
		t.Fatalf("%s does not hold card %s: %s", k.pending, card, got)
	}
	var pending wire.AttentionPending
	if err := json.Unmarshal(c.call(t, 2, "attention/pending", nil), &pending); err != nil {
		t.Fatal(err)
	}
	needs := 0
	for _, n := range pending.Needs {
		if n.Kind == k.need && n.AgentID != nil && *n.AgentID == agent {
			needs++
		}
	}
	if needs != 1 {
		t.Fatalf("attention/pending has %d needs for the card: %+v", needs, pending.Needs)
	}
}

// oneAnswerWins is failure modes 3 and 5: two clients answer at once, one answer wins,
// both see the card go, and the control plane was offered it once and had it withdrawn.
func (k cardKind) oneAnswerWins(t *testing.T, d *Daemon, control *offers, agent, card wire.UUID, one, two *listener) {
	t.Helper()
	for i, l := range []*listener{one, two} {
		method, params := k.answer(card, i)
		l.send(100+i, method, params)
	}
	for i, l := range []*listener{one, two} {
		l.wait(t, fmt.Sprintf("client %d sees the card go", i), k.gone(card))
	}
	waitFor(t, "the turn's end", func() bool { return d.Store.Agent(agent).State == wire.AgentStateFinished })
	entries, _, _ := d.Store.Transcript(agent, 0)
	answers := 0
	for _, e := range entries {
		if k.answered(e.Kind) {
			answers++
		}
	}
	if answers != 1 {
		t.Fatalf("%d answers recorded, want 1", answers)
	}
	// Offered once (failure mode 5), however often the daemon looks again, and withdrawn.
	d.reconsider()
	offered, withdrawn := control.of(agent, k.need)
	if len(offered) != 1 || withdrawn < 1 {
		t.Fatalf("offered %d times, withdrawn %d times: %+v", len(offered), withdrawn, control.sent)
	}
	for i, l := range []*listener{one, two} {
		if n := l.count(k.raised(agent)); n != 1 {
			t.Errorf("client %d heard the card raised %d times", i, n)
		}
	}
}
