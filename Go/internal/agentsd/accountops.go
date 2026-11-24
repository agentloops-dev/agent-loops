package agentsd

import (
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The record of each read and change of an outside account (#4157). manage_accounts raises
// account.read (a get, or a list filtered to the account's service) or account.changed (an add
// that is new, or that took another's place), and tells the control plane, which writes an audit
// entry for each. A list with no service reads no account. This machine keeps each account's last
// operation (none for a removed one, #5253), and the last read of each secret, so an account's lastOp is the newest of its own and
// a secret.read of the secret or card it names.

// accountOpsSaveEvery is how often a secret read again is written down: an MCP header is read on
// each call.
const accountOpsSaveEvery = time.Minute

// accountOpMostKey is the longest key that is recorded, so that one account or secret with a very
// long name does not refuse the others of its call.
const accountOpMostKey = 512

// accountSecretOpKey is where a secret's last read is kept; an account's operation is at its
// accountOpRecordKey.
func accountSecretOpKey(name string) string { return "secret:" + strings.TrimSpace(name) }

func accountOpRecordKey(service string, address *string) string {
	return "account:" + accountOpKey(service, address)
}

// accountMachine is the machine's name, as an event about an account names it.
var accountMachine = sync.OnceValue(macName)

// accountOpKey is an account as the control plane tells one from another: its service and address.
func accountOpKey(service string, address *string) string {
	return strings.ToLower(service) + "#" + strings.ToLower(deref(address))
}

func (d *Daemon) accountOpsPath() string {
	return filepath.Join(d.Store.Loc.Root, "account-ops.json")
}

// openAccountOps opens the record, which evicts: an account with no operation for 90 days shows
// none. It is the small-file type, not a log: each account and secret has one entry, written over.
func (d *Daemon) openAccountOps() *store.JSONFile[wire.AccountLastOp] {
	return smallFile(d, store.Spec{Name: "account-ops.json", Kind: "key-value", Scope: "person",
		Bounds: store.Bounds{MaxRecordBytes: 1 << 10, MaxCount: 1_000, MaxAge: store.MaxAge, Action: store.EvictOldest}},
		d.accountOpsPath, func(op *wire.AccountLastOp) *wire.Date { return &op.At })
}

// readAccountOps is the record. A file that cannot be read has none.
func (d *Daemon) readAccountOps() map[string]wire.AccountLastOp {
	ops, err := d.accountOps.Read()
	if err != nil {
		log.Printf("account-ops.json: could not read: %v", err)
	}
	return ops
}

func (d *Daemon) updateAccountOps(change func(map[string]wire.AccountLastOp)) {
	if err := d.accountOps.Update(func(m map[string]wire.AccountLastOp) error {
		change(m)
		return nil
	}); err != nil {
		log.Printf("account-ops.json: could not save: %v", err)
	}
}

// noteAccountOps keeps each operation as its account's last and raises its event.
func (d *Daemon) noteAccountOps(agent wire.UUID, ops []wire.AccountOperation) {
	if len(ops) == 0 {
		return
	}
	at := wire.DateOf(nowFunc())
	d.updateAccountOps(func(m map[string]wire.AccountLastOp) {
		for _, op := range ops {
			key := accountOpRecordKey(op.Service, op.Address)
			switch {
			case op.What == wire.AccountOperationWhatRemoved:
				// A removed account has no last operation (#5253): one added again starts afresh.
				delete(m, key)
			case len(key) <= accountOpMostKey:
				m[key] = wire.AccountLastOp{What: op.What, At: at, Agent: op.Agent}
			}
		}
	})
	who := d.agentWho(agent)
	for _, op := range ops {
		details := map[string]string{"service": op.Service, "machine": accountMachine(), "agent": string(agent)}
		if op.Address != nil && *op.Address != "" {
			details["address"] = *op.Address
		}
		name, sentence := "account.read", who+" read the "+op.Service+" account."
		if op.What != wire.AccountOperationWhatRead {
			name, details["change"] = "account.changed", string(op.What)
			sentence = who + " added the " + op.Service + " account."
			switch op.What {
			case wire.AccountOperationWhatEdited:
				sentence = who + " changed the " + op.Service + " account."
			case wire.AccountOperationWhatRemoved:
				sentence = who + " removed the " + op.Service + " account."
			}
		}
		d.raise(wire.EventDraft{Name: name, At: at, Scope: macScope(), Sentence: sentence, Details: details})
	}
}

// noteSecretRead keeps a secret.read's time as the last read of that secret, for the accounts
// that name it.
func (d *Daemon) noteSecretRead(e wire.Event) {
	name := strings.TrimSpace(e.Details["name"])
	if e.Name != "secret.read" || name == "" {
		return
	}
	at := e.At
	if e.LastAt != nil {
		at = *e.LastAt
	}
	key := accountSecretOpKey(name)
	if last, had := d.readAccountOps()[key]; len(key) > accountOpMostKey ||
		had && at.Time().Sub(last.At.Time()) < accountOpsSaveEvery {
		return
	}
	d.updateAccountOps(func(m map[string]wire.AccountLastOp) {
		if last, had := m[key]; !had || at > last.At {
			m[key] = wire.AccountLastOp{What: wire.AccountOperationWhatRead, At: at}
		}
	})
}

// accountLastOp is an account's newest operation in the record ops: its own, or a read of its
// secret or card.
func accountLastOp(ops map[string]wire.AccountLastOp, a wire.OutsideAccount) *wire.AccountLastOp {
	var newest *wire.AccountLastOp
	if op, ok := ops[accountOpRecordKey(a.Service, a.Address)]; ok {
		newest = &op
	}
	for _, name := range []*string{a.Secret, a.Card} {
		if name == nil {
			continue
		}
		if op, ok := ops[accountSecretOpKey(*name)]; ok && (newest == nil || op.At > newest.At) {
			newest = &op
		}
	}
	return newest
}
