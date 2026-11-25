package agentsd

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Each request checked end to end (#3492, specs/3399-machine-lock D6, D7). Once this machine
// holds its person's lock, a request that comes through the control plane is taken only
// when a key of that lock vouches for it:
//
//   - with an HMAC from a channel a client of the lock opened with lock/hello;
//   - signed by a client of the lock, for a prompt or a start kept while this machine was
//     away (LockSignature, to this machine, at most a day old, once);
//   - signed by another machine of the lock, for the steps of start_session, its calls to
//     the session it started, and send and bring_back (D7);
//   - or one of the few the control plane asks for itself, which start nothing, and the
//     stop of a provider draft's writer, which a client of the lock signed the start of
//     for the control plane's own draft (#3542).
//
// Everything else is refused with notInLock, the control plane's own word for who sent it
// notwithstanding. A machine with no lock yet takes what it took before the lock, so a
// person's set-up keeps working until they have one. daemon.sock is this account's own and
// is not checked; the window's local socket is, so its key comes from the lock too.

// lockSeenFile keeps lockSeen across a restart.
const lockSeenFile = "lock-seen.json"

// mostSeen bounds lockSeen: those signed first go first, and the cutoff moves to them.
const mostSeen = 4096

// lockCutoff is the key in lockSeen of the cutoff: the latest time signed of the IDs that the
// count took out. A request signed then or before is refused, as its ID may be one of those.
const lockCutoff = "cutoff"

// controlAsks is what the control plane asks of a machine for itself, unsigned: whether it
// is there, the Console's and a project's list of sessions, a report's facts, a backup
// before a delete, and the folders a new workspace is cloned with. None starts an agent,
// runs a command or reads a file.
var controlAsks = methodSet([]string{
	"daemon/ping", "daemon/status", wire.CallLockHello.Name,
	wire.CallAgentsList.Name, wire.CallWorktreesAll.Name,
	wire.CallBackupsMake.Name, "folders/clone",
})

// clientSigned is what a client may sign for a machine that is not there: what the control
// plane keeps for a stopped workspace (#691 US3).
var clientSigned = methodSet([]string{wire.CallAgentsStart.Name, wire.CallAgentsPrompt.Name})

// machineSigned is what another of the person's machines may ask (D7): the steps of
// start_session (#2887, #2927), of send and bring_back (#2929, #2947), and of move_session
// (#3717), and the bot's key (#3950). Not terminals, nor any other file.
var machineSigned = methodSet([]string{
	wire.CallAgentsStart.Name, "agents/fromStarter", "agents/endedElsewhere", wire.CallAgentsMoveIn.Name,
	"folders/receiveState", "folders/receiveUpload", "folders/receive", wire.CallFoldersReceiveSeed.Name,
	"folders/sendFacts", "folders/sendPack", "folders/sendRead",
	wire.CallConnectionsBotTake.Name, wire.CallConnectionsBotDrop.Name,
})

// registerLockHello serves lock/hello, so the host says host.lock/3. A checked connection
// answers it before dispatch (readChecked); daemon.sock, which is not checked, has no channel.
func (d *Daemon) registerLockHello() {
	On(d, wire.CallLockHello, func(_ context.Context, _ *Client, _ wire.LockHello) (wire.LockHelloReply, error) {
		return wire.LockHelloReply{}, &Error{Code: wire.FailureNotSupported, Message: "This connection is the machine's own, and needs no channel."}
	})
}

// lockChecked says whether a connection's requests are checked against the lock: every one
// the control plane carries, and the window's local socket; not daemon.sock.
func lockChecked(conn any) bool {
	if sc, ok := conn.(*sockConn); ok {
		return sc.local
	}
	return true
}

// clientLock is a connection's checked channel, once a client opened one. write is held while
// a line goes to the connection, so the answer to lock/hello goes before the first MAC'd line,
// and the counts go out in order (#3541).
type clientLock struct {
	mu      sync.Mutex
	channel *machinelock.Channel
	write   writeLock
}

// writeLock is a client's write lock that a write the host says unasked stops waiting for
// once Shutdown begins (#6603). A client that reads nothing holds the lock up to its
// connection's limit, and with a sync.Mutex each write in line waited for the one before it.
type writeLock struct {
	once    sync.Once
	held    chan struct{}
	waiting atomic.Int32
}

func (l *writeLock) sem() chan struct{} {
	l.once.Do(func() { l.held = make(chan struct{}, 1) })
	return l.held
}

func (l *writeLock) Lock() {
	l.waiting.Add(1)
	defer l.waiting.Add(-1)
	l.sem() <- struct{}{}
}

func (l *writeLock) Unlock() { <-l.sem() }

// lockUnless is Lock, or false at once when stop is closed, also while it waits.
func (l *writeLock) lockUnless(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return false
	default:
	}
	l.waiting.Add(1)
	defer l.waiting.Add(-1)
	select {
	case l.sem() <- struct{}{}:
		return true
	case <-stop:
		return false
	}
}

// inLine is how many writes hold the lock or wait for it.
func (l *writeLock) inLine() int { return int(l.waiting.Load()) + len(l.sem()) }

func (l *clientLock) get() *machinelock.Channel {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.channel
}

func (l *clientLock) set(c *machinelock.Channel) {
	l.mu.Lock()
	l.channel = c
	l.mu.Unlock()
}

// write sends a line from this machine: what it answers and what it says unasked (agent/changed,
// a question card), with the channel's MAC once a client opened one, so the control plane can
// forge neither (#3492, #3541). Refusals go with send: an error with no MAC is taken.
func (c *Client) write(line string) { c.writeOnce(line, false) }

// writeUnasked is write for what the host says unasked (Broadcast, sendEach). Once Shutdown
// has begun it sends nothing, as clientsToTell does (#3828), also when it was already waiting
// for the client's write lock (#6268): a client that reads nothing holds each write up to its
// connection's own limit, and the writes in line behind one went on one at a time, each to
// that limit, past Shutdown's wait for the goroutines that made them. A write in line stops
// waiting as Shutdown begins, not when the one before it is done (#6603).
func (c *Client) writeUnasked(line string) { c.writeOnce(line, true) }

func (c *Client) writeOnce(line string, unasked bool) {
	if unasked && c.d != nil {
		if !c.lock.write.lockUnless(c.d.shutdownBegun()) {
			return
		}
	} else {
		c.lock.write.Lock()
	}
	defer c.lock.write.Unlock()
	if unasked && c.d != nil && c.d.shuttingDown() {
		return
	}
	if ch := c.lock.get(); ch != nil {
		line = ch.Seal(line)
	}
	_ = c.conn.Write(line)
}

func notInLock(why string) *Error {
	return &Error{Code: wire.FailureNotInLock, Message: "This machine takes requests only from your devices: " + why + "."}
}

// readChecked reads one line on a checked connection as dispatch takes it. It answers
// lock/hello and every refusal itself, and then says false.
func (d *Daemon) readChecked(c *Client, line string) (message, bool) {
	var m message
	sealed := machinelock.Sealed(line)
	body := line
	var openErr error
	if sealed {
		if ch := c.lock.get(); ch == nil {
			openErr = errors.New("no channel was opened with lock/hello")
		} else if opened, err := ch.Open(line); err != nil {
			openErr = err
		} else {
			body = opened
		}
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		if openErr == nil {
			c.send(message{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: CodeParse, Message: "parse error"}})
		}
		return m, false
	}
	if m.Method == "" {
		return m, true // a reply: nothing here asks a client anything
	}
	if openErr != nil {
		return m, d.refuse(c, m, openErr.Error())
	}
	if m.Method == wire.CallLockHello.Name {
		d.answerHello(c, m)
		return m, false
	}
	state, pinned := d.heldLock()
	switch {
	case sealed, !pinned:
		return m, true
	case state == nil && !controlAsks[m.Method]:
		return m, d.refuse(c, m, "it has not read your lock yet; try again in a moment")
	}
	if s, ok := machinelock.SignatureOf(m.Lock); ok {
		return m, d.takeSigned(c, state, m, s)
	}
	if controlAsks[m.Method] {
		return m, true
	}
	return m, d.refuse(c, m, "the request is not signed by one of them. Open Agent Loops on this device again; if it asks to join, approve it on another of your devices")
}

// refuse answers a request with notInLock, and says false.
func (d *Daemon) refuse(c *Client, m message, why string) bool {
	log.Printf("lock: refused %s from the control plane: %s", m.Method, why)
	if m.ID != nil {
		c.send(message{JSONRPC: "2.0", ID: m.ID, Error: notInLock(why)})
	}
	return false
}

// answerHello is lock/hello: a client of the lock opens a checked channel.
func (d *Daemon) answerHello(c *Client, m message) {
	answer := func(result any, err error) {
		if m.ID == nil {
			return
		}
		if err != nil {
			var re *Error
			if !errors.As(err, &re) {
				re = notInLock(err.Error())
			}
			c.send(message{JSONRPC: "2.0", ID: m.ID, Error: re})
			return
		}
		b, _ := json.Marshal(result)
		c.send(message{JSONRPC: "2.0", ID: m.ID, Result: b})
	}
	var hello wire.LockHello
	if err := json.Unmarshal(m.Params, &hello); err != nil {
		answer(nil, Invalid("lock/hello: %v", err))
		return
	}
	state, pinned := d.heldLock()
	if !pinned {
		answer(nil, &Error{Code: wire.FailureNotSupported, Message: "This machine holds no lock yet."})
		return
	}
	if state == nil {
		answer(nil, notInLock("it has not read your lock yet; try again in a moment"))
		return
	}
	signer, err := d.machineSigner()
	if err != nil {
		answer(nil, &Error{Code: CodeServer, Message: "This machine's key could not be read: " + err.Error()})
		return
	}
	channel, reply, err := machinelock.Answer(state, signer, hello)
	if err != nil {
		log.Printf("lock: refused a channel: %v", err)
		answer(nil, err)
		return
	}
	if !sameClient(channel.Member.ID, c.said) {
		// The control plane opened the channel for one client, and the lock's key is another's.
		log.Printf("lock: refused a channel: the control plane names client %s, the lock %s", *c.said.Client, channel.Member.ID)
		answer(nil, notInLock("the control plane opened this channel for another of your devices. Open Agent Loops on this device again, so that it adds its key for this sign-in"))
		return
	}
	// The answer goes before the channel is set, and nothing goes between them: it is not
	// MAC'd, its signature is its proof, and every line after it is.
	c.lock.write.Lock()
	answer(reply, nil)
	c.lock.set(channel)
	c.lock.write.Unlock()
	who := channel.Member.Name
	if who == "" {
		who = "a device"
	}
	log.Printf("lock: %s opened a checked channel", who)
}

func (d *Daemon) machineSigner() (*ecdsa.PrivateKey, error) {
	d.lockMu.Lock()
	defer d.lockMu.Unlock()
	if d.lockSigner != nil {
		return d.lockSigner, nil
	}
	if d.HostKey == nil {
		return nil, errors.New("this machine has no key")
	}
	private, found, err := d.HostKey.Load()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("this machine has not joined a control plane")
	}
	key, err := machinelock.MachineSigner(private)
	if err != nil {
		return nil, err
	}
	d.lockSigner = key
	return key, nil
}

// takeSigned checks a signed request: by a client for what may be kept, or by another
// machine of the lock for start_session's steps; each once.
func (d *Daemon) takeSigned(c *Client, state *machinelock.State, m message, s wire.LockSignature) bool {
	host, _, _, ok := d.lockIdentity()
	if !ok {
		return d.refuse(c, m, "this machine has not joined a control plane")
	}
	now := nowFunc()
	member, err := machinelock.CheckRequest(state, host, m.Method, m.Params, s, now)
	if err != nil {
		return d.refuse(c, m, err.Error())
	}
	if member.Client() && !clientSigned[m.Method] || !member.Client() && !machineSigned[m.Method] {
		return d.refuse(c, m, "that key may not sign "+m.Method)
	}
	if err := d.seeSigned(machinelock.SignedID(s), s.At, now); err != nil {
		return d.refuse(c, m, err.Error())
	}
	return true
}

// signedSeen is a signed request taken (by machinelock.SignedID), or, at lockCutoff, the cutoff.
type signedSeen struct {
	At wire.Date `json:"at"` // when it was taken
	// Signed is when the request says it was signed, in Unix seconds.
	Signed int64 `json:"signed"`
}

// UnmarshalJSON also reads the bare time signed that a build from before #6723 wrote. The store
// dates such an entry when it next writes.
func (s *signedSeen) UnmarshalJSON(b []byte) error {
	if json.Unmarshal(b, &s.Signed) == nil {
		return nil
	}
	type plain signedSeen
	return json.Unmarshal(b, (*plain)(s))
}

// openLockSeen opens the signed requests taken. seeSigned, not the store, keeps the count: an ID
// that goes must move the cutoff in the same write. An ID ages out when CheckRequest has refused
// its request as stale for a day, so a clock that is set back has that much room.
func (d *Daemon) openLockSeen() *store.JSONFile[signedSeen] {
	return smallFile(d, store.Spec{Name: lockSeenFile, Kind: "key-value", Scope: "machine",
		Bounds: store.Bounds{MaxRecordBytes: 128, MaxCount: mostSeen + 1, MaxAge: 2 * machinelock.RequestLifetime, Action: store.EvictOldest}},
		func() string { return filepath.Join(d.Store.Loc.Root, lockSeenFile) }, func(s *signedSeen) *wire.Date { return &s.At })
}

// seeSigned notes a signed request as taken. It refuses one that was taken before, one signed at
// the cutoff or before it, and one it cannot note.
func (d *Daemon) seeSigned(id string, at int64, now time.Time) error {
	d.lockMu.Lock()
	defer d.lockMu.Unlock()
	if !d.lockSeenRead {
		if _, err := d.lockSeen.Read(); err != nil {
			// What was taken is not known: nothing signed until now is taken again.
			log.Printf("lock: %v; what was signed until now is refused", err)
			if err := d.lockSeen.Update(func(seen map[string]signedSeen) error {
				seen[lockCutoff] = signedSeen{At: wire.DateOf(now), Signed: now.Add(machinelock.RequestSkew).Unix()}
				return nil
			}); err != nil {
				return errNotNoted(err)
			}
		}
		d.lockSeenRead = true
	}
	var refused error
	if err := d.lockSeen.Update(func(seen map[string]signedSeen) error {
		_, taken := seen[id]
		cutoff, cut := seen[lockCutoff]
		switch {
		case taken:
			refused = machinelock.ErrReplayed
			return nil
		case cut && at <= cutoff.Signed:
			refused = machinelock.ErrStale
			return nil
		}
		delete(seen, lockCutoff)
		seen[id] = signedSeen{At: wire.DateOf(now), Signed: at}
		for len(seen) > mostSeen {
			oldest := ""
			for k, s := range seen {
				if oldest == "" || s.Signed < seen[oldest].Signed {
					oldest = k
				}
			}
			cutoff, cut = signedSeen{At: wire.DateOf(now), Signed: max(cutoff.Signed, seen[oldest].Signed)}, true
			delete(seen, oldest)
		}
		if cut {
			seen[lockCutoff] = cutoff
		}
		return nil
	}); err != nil {
		return errNotNoted(err)
	}
	return refused
}

func errNotNoted(err error) error {
	log.Printf("lock: the signed requests taken could not be kept: %v", err)
	return errors.New("the machine could not note the request as taken")
}
