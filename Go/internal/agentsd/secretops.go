package agentsd

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The record of each read and change of a secret (#4156). Each one raises an event on this
// machine's stream (secret.read, secret.denied, secret.changed), so wait_for_event and a
// workflow can use it, and goes to the control plane's audit (machine/secretOps). This machine
// keeps each secret's last operation in secret-ops.json, for the names it holds only, and a
// removed secret's goes with it. None of them has the value or its hint: only the name, the
// use and the place.
//
// The reads are agentsd's own: fill_secret (fill, the field's domain), set_ci_secret (ci,
// owner/repo), an HTTP MCP server's ${NAME} that agentsd fills on each request (mcp, the
// server's name, #4120), and what a session is given at its start (env: its own secrets as
// variables, place session, and each server's ${NAME}, place the server's name). secrets.env
// is a plain file that a runtime can read at any time: agentsd sees only what it gives.
//
// An MCP server's header is read on each request, so the audit has at most one entry per
// secret, session, use, place and minute, with a count: the reads wait up to secretOpsBatch
// and go together. A denial and a change go at once. A person's change in Settings ▸ Secrets
// is the control plane's own entry (user/setSecret, user/removeSecret): the machine raises its
// event only.
//
// An operation for the audit stays in secret-ops-pending.json until the control plane answers
// (#6464), each with an id of its own: a stop, a send that failed and a link that dropped lose
// none. The machine sends what waits each time the link comes up, and again secretOpsRetry
// after a send that failed; the control plane keeps an id it has kept already once. The file
// is a bounded small file (openSecretOpsPending, #6721): an operation past its limits goes, with
// a log line, and the audit never has it.

// secretOpsBatch is how long reads wait to go to the audit together; a test makes it shorter.
var secretOpsBatch = time.Minute

// secretOpsRetry is how long a send that failed waits to go again; a test makes it shorter.
var secretOpsRetry = time.Minute

// secretOpsPendingMost bounds the operations kept for the audit until it answers.
const secretOpsPendingMost = 1000

// secretOpsSendMost is how many operations one send has, so that a full file fits the link.
const secretOpsSendMost = 500

// secretOpMostName and secretOpMostPlace are the longest name and place, in bytes, that the audit
// is told: a longer one is cut short, so that no operation is over the file's largest entry.
const (
	secretOpMostName  = 512
	secretOpMostPlace = 1024
)

// secretOpsSaveEvery is how often a last read that only moved on is written down.
const secretOpsSaveEvery = time.Minute

// secretOpsAsk bounds the call that tells the control plane.
const secretOpsAsk = 20 * time.Second

// secretSessionPlace is env's place for a session's own secrets, given as its variables.
const secretSessionPlace = "session"

type secretOpBook struct {
	mu     sync.Mutex
	loaded bool
	// last is each secret's last operation, by its name: secret-ops.json.
	last  map[string]wire.SecretOperation
	saved time.Time
	// pending is secret-ops-pending.json: the operations the audit has not answered yet, by id.
	pending *store.JSONFile[wire.SecretOperation]
	// unsaved is the operations that pending does not have as they are now, by id: the reads
	// that still count, and what a write that failed left.
	unsaved map[string]wire.SecretOperation
	// open is the id of each read in unsaved that still counts, by secretOpKey: the batch
	// that timer closes. A send leaves those out until then.
	open  map[string]string
	timer *time.Timer
	// retry sends again after a send that failed; sending is a send under way, and again
	// says another was asked for while it ran.
	retry          *time.Timer
	sending, again bool
}

// secretOpKey is what one audit entry counts: a secret, a session, a use and a place.
func secretOpKey(op wire.SecretOperation) string {
	var agent wire.UUID
	var use wire.SecretOperationUse
	if op.Agent != nil {
		agent = *op.Agent
	}
	if op.Use != nil {
		use = *op.Use
	}
	return op.Name + "|" + string(agent) + "|" + string(use) + "|" + deref(op.Place) + "|" + string(op.What)
}

func (d *Daemon) secretOpsPath() string {
	if d.Store == nil {
		return ""
	}
	return filepath.Join(d.Store.Loc.Root, "secret-ops.json")
}

func (d *Daemon) secretOpsPendingPath() string {
	return filepath.Join(d.Store.Loc.Root, "secret-ops-pending.json")
}

// openSecretOpsPending opens the operations not yet told, which evict: a machine that its control
// plane does not answer must still use its secrets. A file from before #6721 was a list.
func (d *Daemon) openSecretOpsPending() *store.JSONFile[wire.SecretOperation] {
	f := smallFile(d, store.Spec{Name: "secret-ops-pending.json", Kind: "log", Scope: "machine",
		Bounds: store.Bounds{MaxRecordBytes: 16 << 10, MaxCount: secretOpsPendingMost, MaxAge: store.MaxAge, Action: store.EvictOldest}},
		d.secretOpsPendingPath, func(op *wire.SecretOperation) *wire.Date { return &op.At })
	var old []wire.SecretOperation
	data, err := os.ReadFile(d.secretOpsPendingPath())
	if err != nil || diskjson.Unmarshal(data, &old) != nil {
		return f
	}
	byID := map[string]wire.SecretOperation{}
	for _, op := range old {
		op = boundedSecretOp(op)
		byID[*op.ID] = op
	}
	// Not by Update, which would set the list aside as corrupt.
	raw, err := diskjson.Marshal(byID)
	if err == nil {
		// store-bounds: not a store: the file above, in its new form
		err = store.WriteAtomically(d.secretOpsPendingPath(), raw, 0o600)
	}
	if err != nil {
		log.Printf("secret-ops-pending.json: could not bring over the operations: %v", err)
		d.secretOps.unsaved = byID
	}
	return f
}

// boundedSecretOp is op with an id, and its name and place cut to their longest.
func boundedSecretOp(op wire.SecretOperation) wire.SecretOperation {
	if op.ID == nil || *op.ID == "" {
		id := rand.Text()
		op.ID = &id
	}
	short := func(s string, most int) string {
		if len(s) <= most {
			return s
		}
		log.Printf("secret ops: a name or place of %d bytes is cut to %d for the audit", len(s), most)
		return strings.ToValidUTF8(s[:most], "")
	}
	op.Name = short(op.Name, secretOpMostName)
	if op.Place != nil {
		place := short(*op.Place, secretOpMostPlace)
		op.Place = &place
	}
	return op
}

// savePendingSecretOpsLocked writes what is not saved, and lets go of the operations past their
// age. What a write that failed leaves is bounded too.
func (d *Daemon) savePendingSecretOpsLocked() {
	b := &d.secretOps
	if err := b.pending.Update(func(m map[string]wire.SecretOperation) error {
		maps.Copy(m, b.unsaved)
		return nil
	}); err != nil {
		log.Printf("secret-ops-pending.json: could not save: %v", err)
		if over := len(b.unsaved) - secretOpsPendingMost; over > 0 {
			log.Printf("secret ops: dropped the oldest %d not yet told to the audit: at most %d wait", over, secretOpsPendingMost)
			for _, op := range oldestSecretOps(b.unsaved)[:over] {
				delete(b.unsaved, *op.ID)
			}
		}
		return
	}
	counting := b.countingLocked()
	maps.DeleteFunc(b.unsaved, func(id string, _ wire.SecretOperation) bool { return !counting[id] })
}

// oldestSecretOps is ops, the oldest first, each with its key as its id.
func oldestSecretOps(ops map[string]wire.SecretOperation) []wire.SecretOperation {
	out := make([]wire.SecretOperation, 0, len(ops))
	for id, op := range ops {
		op.ID = &id
		out = append(out, op)
	}
	slices.SortFunc(out, func(x, y wire.SecretOperation) int {
		return cmp.Or(cmp.Compare(x.At, y.At), cmp.Compare(*x.ID, *y.ID))
	})
	return out
}

// countingLocked is the ids of the reads that still count.
func (b *secretOpBook) countingLocked() map[string]bool {
	counting := map[string]bool{}
	for _, id := range b.open {
		counting[id] = true
	}
	return counting
}

// keepSecretOpLocked gives op an id and keeps it until the audit answers.
func (d *Daemon) keepSecretOpLocked(op wire.SecretOperation) {
	b := &d.secretOps
	op = boundedSecretOp(op)
	if b.unsaved == nil {
		b.unsaved = map[string]wire.SecretOperation{}
	}
	b.unsaved[*op.ID] = op
	d.savePendingSecretOpsLocked()
}

// secretOpsLocked is the record, read from disk the first time.
func (d *Daemon) secretOpsLocked() map[string]wire.SecretOperation {
	b := &d.secretOps
	if !b.loaded {
		b.loaded = true
		if path := d.secretOpsPath(); path != "" {
			if raw, err := os.ReadFile(path); err == nil && diskjson.Unmarshal(raw, &b.last) != nil {
				log.Printf("secret-ops.json: unreadable, starting afresh")
				b.last = nil
			}
		}
	}
	if b.last == nil {
		b.last = map[string]wire.SecretOperation{}
	}
	return b.last
}

// heldSecretNames is every name this machine holds a value for: secrets.env, the sealed
// store, and each session's own.
func (d *Daemon) heldSecretNames() map[string]bool {
	held := map[string]bool{}
	home := d.personalHome()
	if home == "" {
		return held
	}
	for _, n := range skillcatalog.LoadSecrets(skillcatalog.SecretsPath(home)).Names() {
		held[n] = true
	}
	for n := range sealedSecretsHere(home) {
		held[n] = true
	}
	if dir := d.sessionSecretsDir(); dir != "" {
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			id := strings.TrimSuffix(f.Name(), ".json")
			if !isAgentFileID(id) {
				continue
			}
			for n := range d.readSessionSecrets(wire.UUID(id)).Secrets {
				held[n] = true
			}
		}
	}
	return held
}

// saveSecretOpsLocked writes the record, bounded to the names this machine holds.
func (d *Daemon) saveSecretOpsLocked() {
	path := d.secretOpsPath()
	if path == "" {
		return
	}
	last := d.secretOps.last
	held := d.heldSecretNames()
	for n := range last {
		if !held[n] {
			delete(last, n)
		}
	}
	trimOldest(last, func(op wire.SecretOperation) wire.Date { return op.At })
	raw, err := json.Marshal(last)
	if err == nil {
		err = store.WriteAtomically(path, raw, 0o600)
	}
	if err != nil {
		log.Printf("secret-ops.json: could not save: %v", err)
		return
	}
	d.secretOps.saved = nowFunc()
}

// secretLastOp is a secret's last operation on this machine, if it has one.
func (d *Daemon) secretLastOp(name string) (wire.SecretOperation, bool) {
	d.secretOps.mu.Lock()
	defer d.secretOps.mu.Unlock()
	op, ok := d.secretOpsLocked()[name]
	return op, ok
}

// secretRead records an agent's read of a secret: use at place.
func (d *Daemon) secretRead(agent wire.UUID, name string, use wire.SecretOperationUse, place string) {
	d.noteSecretOp(wire.SecretOperation{Name: name, What: wire.SecretOperationWhatRead, Use: &use, Place: &place, Agent: agentRef(agent)}, true)
}

// secretDenied records a person's denial of a use with Ask before use.
func (d *Daemon) secretDenied(agent wire.UUID, name string, use wire.SecretUse, place string) {
	u := wire.SecretOperationUse(use)
	d.noteSecretOp(wire.SecretOperation{Name: name, What: wire.SecretOperationWhatDenied, Use: &u, Place: &place, Agent: agentRef(agent)}, true)
}

// secretChanged records a secret added, replaced or removed. audit is false for a change the
// control plane kept already: a person's, in Settings ▸ Secrets.
func (d *Daemon) secretChanged(agent wire.UUID, name string, what wire.SecretOperationWhat, audit bool) {
	d.noteSecretOp(wire.SecretOperation{Name: name, What: what, Agent: agentRef(agent)}, audit)
}

func agentRef(agent wire.UUID) *wire.UUID {
	if agent == "" {
		return nil
	}
	return &agent
}

// noteSecretOp raises the operation's event, keeps it as its secret's last, and sends it to
// the audit: a read in the next batch, anything else at once.
func (d *Daemon) noteSecretOp(op wire.SecretOperation, audit bool) {
	op.Name = strings.TrimSpace(op.Name)
	if op.Name == "" {
		return
	}
	op.At = wire.DateOf(nowFunc())
	d.raise(secretOpEvent(op, d.secretWho(op.Agent)))

	d.secretOps.mu.Lock()
	last := d.secretOpsLocked()
	_, had := last[op.Name]
	if op.What == wire.SecretOperationWhatRemoved {
		delete(last, op.Name)
	} else {
		last[op.Name] = op
	}
	if !had || op.What != wire.SecretOperationWhatRead || nowFunc().Sub(d.secretOps.saved) >= secretOpsSaveEvery {
		d.saveSecretOpsLocked()
	}
	// A machine with no control plane keeps only its events and its record.
	now := false
	if audit && d.askControl.Load() != nil {
		if op.What == wire.SecretOperationWhatRead {
			d.batchSecretOpLocked(op)
		} else {
			d.keepSecretOpLocked(op)
			now = true
		}
	}
	d.secretOps.mu.Unlock()
	if now {
		d.goJob("tellSecretOps", d.sendSecretOps)
	}
}

// batchSecretOpLocked counts a read into its entry of the next batch, and starts the batch.
func (d *Daemon) batchSecretOpLocked(op wire.SecretOperation) {
	b := &d.secretOps
	if b.open == nil {
		b.open = map[string]string{}
	}
	key := secretOpKey(op)
	// A count goes to disk with the next write of the file: a new entry's, the batch's end,
	// or the stop.
	if had, ok := b.unsaved[b.open[key]]; ok {
		n := int64(1)
		if had.Count != nil {
			n = *had.Count
		}
		n++
		had.Count = &n
		b.unsaved[*had.ID] = had
		return
	}
	op = boundedSecretOp(op)
	b.open[key] = *op.ID
	d.keepSecretOpLocked(op)
	if b.timer == nil {
		b.timer = time.AfterFunc(secretOpsBatch, d.jobFunc("secretops timer", d.flushSecretOps))
	}
}

// flushSecretOps ends the batch of reads, with their counts, and sends what waits.
func (d *Daemon) flushSecretOps() {
	b := &d.secretOps
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.open, b.timer = nil, nil
	d.savePendingSecretOpsLocked()
	b.mu.Unlock()
	d.sendSecretOps()
}

// sendSecretOps sends the operations that wait, but the reads that still count, to the control
// plane for its audit, one send at a time. What it answers goes from the file; after a send
// that failed, all of it goes again secretOpsRetry later, and when the link comes up again.
func (d *Daemon) sendSecretOps() {
	ask := d.askControl.Load()
	if ask == nil {
		return
	}
	b := &d.secretOps
	for {
		b.mu.Lock()
		if b.sending {
			b.again = true
			b.mu.Unlock()
			return
		}
		d.savePendingSecretOpsLocked()
		waiting, err := b.pending.Read()
		if err != nil {
			log.Printf("secret-ops-pending.json: could not read: %v", err)
		}
		maps.Copy(waiting, b.unsaved)
		for id := range b.countingLocked() {
			delete(waiting, id)
		}
		ops := oldestSecretOps(waiting)
		ops = ops[:min(len(ops), secretOpsSendMost)]
		more := len(ops) < len(waiting)
		if len(ops) == 0 {
			b.mu.Unlock()
			return
		}
		b.sending, b.again = true, false
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(d.ctx, secretOpsAsk)
		_, err = (*ask)(ctx, wire.CallMachineSecretOps.Name, wire.MachineSecretOpsCall{Ops: ops})
		cancel()

		b.mu.Lock()
		b.sending = false
		if err != nil {
			log.Printf("secret ops: the control plane was not told of %d, kept to send again: %v", len(ops), err)
			d.retrySecretOpsLocked()
			b.mu.Unlock()
			return
		}
		for _, op := range ops {
			delete(b.unsaved, *op.ID)
		}
		if err := b.pending.Update(func(m map[string]wire.SecretOperation) error {
			for _, op := range ops {
				delete(m, *op.ID)
			}
			return nil
		}); err != nil {
			// They go again with the same ids, which the audit keeps once.
			log.Printf("secret-ops-pending.json: could not let go of the %d told: %v", len(ops), err)
			d.retrySecretOpsLocked()
			b.mu.Unlock()
			return
		}
		again := b.again || more
		b.mu.Unlock()
		if !again {
			return
		}
	}
}

// retrySecretOpsLocked sends again in secretOpsRetry.
func (d *Daemon) retrySecretOpsLocked() {
	b := &d.secretOps
	if b.retry != nil {
		return
	}
	b.retry = time.AfterFunc(secretOpsRetry, d.jobFunc("secretops retry", func() {
		b.mu.Lock()
		b.retry = nil
		b.mu.Unlock()
		d.sendSecretOps()
	}))
}

// keepSecretOpsForNextStart writes the reads that still count down with their counts, for the
// next start to send (#6464): the link is closing as the host stops, so no send is tried.
func (d *Daemon) keepSecretOpsForNextStart() {
	b := &d.secretOps
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range []*time.Timer{b.timer, b.retry} {
		if t != nil {
			t.Stop()
		}
	}
	b.open, b.timer, b.retry = nil, nil, nil
	if len(b.unsaved) > 0 {
		d.savePendingSecretOpsLocked()
	}
}

// secretWho is who did it, as an event's sentence starts.
func (d *Daemon) secretWho(agent *wire.UUID) string {
	if agent == nil {
		return "A person"
	}
	return d.agentWho(*agent)
}

// secretOpEvent is an operation's event: name, machine, agent, use and place, and change for
// secret.changed. Never the value, never its hint.
func secretOpEvent(op wire.SecretOperation, who string) wire.EventDraft {
	details := map[string]string{"name": op.Name, "machine": accountMachine()}
	if op.Agent != nil {
		details["agent"] = string(*op.Agent)
	}
	where := ""
	if op.Use != nil {
		details["use"] = string(*op.Use)
		where = " (" + string(*op.Use)
		if op.Place != nil && *op.Place != "" {
			details["place"] = *op.Place
			where += ", " + *op.Place
		}
		where += ")"
	}
	draft := wire.EventDraft{At: op.At, Scope: macScope(), Details: details}
	switch op.What {
	case wire.SecretOperationWhatRead:
		draft.Name, draft.Sentence = "secret.read", who+" read "+op.Name+where+"."
	case wire.SecretOperationWhatDenied:
		draft.Name, draft.Sentence = "secret.denied", "A person denied "+op.Name+where+"."
	default:
		draft.Name, details["change"] = "secret.changed", string(op.What)
		// A person types the value, also for a session's ask_secret: the sentence names no one.
		draft.Sentence = op.Name + " was " + map[wire.SecretOperationWhat]string{wire.SecretOperationWhatAdded: "added",
			wire.SecretOperationWhatReplaced: "replaced", wire.SecretOperationWhatRemoved: "removed"}[op.What] + "."
	}
	return draft
}

// noteStartSecrets records what a session is given at its start: its own secrets as its
// runtime's variables, and each server's ${NAME} but a flagged one, which agentsd fills on
// each request instead (secretproxy.go). A server missing a secret is not given, so every
// other ${NAME} it names was filled.
func (d *Daemon) noteStartSecrets(agent wire.UUID, entries []sessionEntry) {
	for _, n := range slices.Sorted(maps.Keys(d.sessionSecretValues(agent))) {
		d.secretRead(agent, n, wire.SecretOperationUseEnv, secretSessionPlace)
	}
	for _, e := range entries {
		var names []string
		for _, m := range secretRef.FindAllStringSubmatch(string(e.raw), -1) {
			if !slices.Contains(e.flagged, m[1]) && !slices.Contains(names, m[1]) {
				names = append(names, m[1])
			}
		}
		for _, n := range names {
			d.secretRead(agent, n, wire.SecretOperationUseEnv, e.name)
		}
	}
}

type secretChange struct {
	name string
	what wire.SecretOperationWhat
}

// userSecretChanges is what a sync of the person's secrets changed here: a name new, one with
// a newer version, and one gone.
func userSecretChanges(before, after userSettingsSyncState) []secretChange {
	var out []secretChange
	for _, n := range after.Secrets {
		switch {
		case !slices.Contains(before.Secrets, n):
			out = append(out, secretChange{n, wire.SecretOperationWhatAdded})
		case after.Versions[n] > before.Versions[n] && before.Versions[n] > 0:
			out = append(out, secretChange{n, wire.SecretOperationWhatReplaced})
		}
	}
	for _, n := range before.Secrets {
		if !slices.Contains(after.Secrets, n) {
			out = append(out, secretChange{n, wire.SecretOperationWhatRemoved})
		}
	}
	return out
}
