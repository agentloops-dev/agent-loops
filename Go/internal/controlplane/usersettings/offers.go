package usersettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Offers (#5308, specs/5308-agent-secrets-sync): a secret an agent saved on one of the
// person's machines, sealed by that machine to the person's clients only. The control plane
// keeps the envelopes and opens none. A client of the person's opens its own and makes the
// secret one of theirs, sealed to every machine and client (user/setSecret with offer); then
// the offer goes. A machine never seals to another machine, so a machine taken over reaches
// the person's other machines only through a person's client, and never under a name the
// person has: that waits for Keep Yours or Use the Agent's.
//
//	v1/users/<user>/offers/<machine>.<NAME>.json   one per machine and name

// MaxOffers bounds the offers one person has waiting.
const MaxOffers = 100

// offerID is the wire's UUID, as user/setSecret's offer must be.
var offerID = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`)

// ErrNoOffer is an offer that is not there: taken, dropped, or replaced by a newer one.
var ErrNoOffer = errors.New("no such offer")

// offerRecord is an offer as stored.
type offerRecord struct {
	Offer         string                         `json:"offer"`
	Name          string                         `json:"name"`
	Machine       string                         `json:"machine"`
	MachineName   string                         `json:"machineName"`
	Agent         string                         `json:"agent,omitempty"`
	SavedAt       string                         `json:"savedAt"`
	NeedsApproval bool                           `json:"needsApproval,omitempty"`
	Description   string                         `json:"description,omitempty"`
	Sealed        map[string]wire.SealedEnvelope `json:"sealed"`
}

func offersPrefix(user string) string { return Prefix + user + "/offers/" }
func offerKey(user, machine, name string) string {
	return offersPrefix(user) + machine + "." + name + ".json"
}

// Offered is what a machine's offer was, for the audit and the person's clients.
type Offered struct {
	Name, Machine, MachineName, Agent string
	// Replaced is an offer of the same name from the same machine that this one replaced.
	Replaced bool
}

// Offer keeps an agent's secret as machine sealed it to the person's clients, allowed. A
// copy for anything else (a machine, a client that is not the person's) is dropped, and one
// with none left is refused.
func (b *Book) Offer(ctx context.Context, user, machine, machineName string, o wire.AgentSecretOffer, allowed map[string]bool) (Offered, error) {
	if err := checkUser(user); err != nil {
		return Offered{}, err
	}
	if !userID.MatchString(machine) {
		return Offered{}, fmt.Errorf("%w: no such machine", ErrInvalid)
	}
	name := string(o.Name)
	if err := CheckName(name); err != nil {
		return Offered{}, err
	}
	if !offerID.MatchString(string(o.Offer)) {
		// user/setSecret takes only a UUID: an offer with any other id could never be taken.
		return Offered{}, fmt.Errorf("%w: an offer's id is an upper-case UUID", ErrInvalid)
	}
	if err := checkEnvelopes(o.Sealed); err != nil {
		return Offered{}, err
	}
	description, err := CheckSecretDescription(o.Description)
	if err != nil {
		return Offered{}, err
	}
	sealed := map[string]wire.SealedEnvelope{}
	for recipient, e := range o.Sealed {
		if strings.HasPrefix(recipient, "c:") && allowed[recipient] {
			sealed[recipient] = e
		}
	}
	if len(sealed) == 0 {
		return Offered{}, fmt.Errorf("%w: none of the apps it was sealed to is the person's now; try again", ErrInvalid)
	}
	b.adding.Lock()
	defer b.adding.Unlock()
	had, err := b.offerRecords(ctx, user)
	if err != nil {
		return Offered{}, err
	}
	key := offerKey(user, machine, name)
	replaced := false
	for _, r := range had {
		if offerKey(user, r.Machine, r.Name) == key {
			replaced = true
		}
	}
	if !replaced && len(had) >= MaxOffers {
		return Offered{}, fmt.Errorf("%w: a person has at most %d agents' secrets waiting; open Agents to take them", ErrInvalid, MaxOffers)
	}
	r := offerRecord{Offer: string(o.Offer), Name: name, Machine: machine, MachineName: machineName, SavedAt: o.SavedAt,
		NeedsApproval: o.NeedsApproval != nil && *o.NeedsApproval, Description: description, Sealed: sealed}
	if o.Agent != nil {
		r.Agent = string(*o.Agent)
	}
	if r.SavedAt == "" {
		r.SavedAt = b.stamp()
	}
	data, err := records.Encode(r)
	if err != nil {
		return Offered{}, err
	}
	if len(data) > MaxSecretRecord {
		return Offered{}, fmt.Errorf("%w: a secret, sealed to every app, is over %d MB", ErrInvalid, MaxSecretRecord>>20)
	}
	err = b.change(ctx, key, func([]byte) ([]byte, error) { return data, nil })
	return Offered{Name: name, Machine: machine, MachineName: machineName, Agent: r.Agent, Replaced: replaced}, err
}

// offerRecords is every offer a person has waiting.
func (b *Book) offerRecords(ctx context.Context, user string) ([]offerRecord, error) {
	listed, err := b.store.List(ctx, offersPrefix(user))
	if err != nil {
		return nil, err
	}
	var out []offerRecord
	for _, k := range listed {
		if !strings.HasSuffix(k.Key, ".json") {
			continue
		}
		o, err := b.store.Get(ctx, k.Key)
		if err != nil {
			return nil, err
		}
		if o == nil {
			continue
		}
		var r offerRecord
		if json.Unmarshal(o.Data, &r) != nil || offerKey(user, r.Machine, r.Name) != k.Key {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt < out[j].SavedAt })
	return out, nil
}

// offerByID is one waiting offer, by its id.
func (b *Book) offerByID(ctx context.Context, user, id string) (offerRecord, error) {
	if err := checkUser(user); err != nil {
		return offerRecord{}, err
	}
	all, err := b.offerRecords(ctx, user)
	if err != nil {
		return offerRecord{}, err
	}
	for _, r := range all {
		if r.Offer == id {
			return r, nil
		}
	}
	return offerRecord{}, ErrNoOffer
}

// Offers is user/secretOffers' rows for client (c:<CLIENT UUID>): each waiting offer, with
// the client's own copy when the machine sealed one to it, and conflict when the person has
// a secret of that name already.
func (b *Book) Offers(ctx context.Context, user, client string) ([]wire.SecretOffer, error) {
	if err := checkUser(user); err != nil {
		return nil, err
	}
	all, err := b.offerRecords(ctx, user)
	if err != nil {
		return nil, err
	}
	names, err := b.liveNames(ctx, user)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	rows := []wire.SecretOffer{}
	for _, r := range all {
		row := wire.SecretOffer{Offer: wire.UUID(r.Offer), Name: wire.UserSecretName(r.Name), Machine: wire.MachineRef(r.Machine),
			MachineName: r.MachineName, SavedAt: r.SavedAt}
		if r.Agent != "" {
			row.Agent = new(wire.UUID(r.Agent))
		}
		if r.NeedsApproval {
			row.NeedsApproval = new(true)
		}
		if r.Description != "" {
			row.Description = new(r.Description)
		}
		if have[r.Name] {
			row.Conflict = new(true)
		}
		if e, ok := r.Sealed[client]; ok {
			row.Mine = &e
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// TakeOffer is the offer that a user/setSecret takes: it must be waiting and of the same
// name. It gives where the secret came from, for the secret's row.
func (b *Book) TakeOffer(ctx context.Context, user, id, name string) (wire.SecretSavedBy, error) {
	r, err := b.offerByID(ctx, user, id)
	if err != nil {
		return wire.SecretSavedBy{}, err
	}
	if r.Name != name {
		return wire.SecretSavedBy{}, fmt.Errorf("%w: that offer is for %s", ErrInvalid, r.Name)
	}
	by := wire.SecretSavedBy{Machine: wire.MachineRef(r.Machine), Name: r.MachineName, At: r.SavedAt}
	if r.Agent != "" {
		by.Agent = new(wire.UUID(r.Agent))
	}
	return by, nil
}

// DropOffer removes a waiting offer, after it was taken or for Keep Yours. It gives what the
// offer was.
func (b *Book) DropOffer(ctx context.Context, user, id string) (Offered, error) {
	r, err := b.offerByID(ctx, user, id)
	if err != nil {
		return Offered{}, err
	}
	if err := b.store.Delete(ctx, offerKey(user, r.Machine, r.Name)); err != nil {
		return Offered{}, err
	}
	return Offered{Name: r.Name, Machine: r.Machine, MachineName: r.MachineName, Agent: r.Agent}, nil
}

// DropMachineOffers removes a machine's offers: the machine was removed (FR-026).
func (b *Book) DropMachineOffers(ctx context.Context, user, machine string) (int, error) {
	if checkUser(user) != nil {
		return 0, nil
	}
	all, err := b.offerRecords(ctx, user)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range all {
		if r.Machine != machine {
			continue
		}
		if err := b.store.Delete(ctx, offerKey(user, r.Machine, r.Name)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// WaitingOffers is a machine's own offers that wait, for its machine/userSettings: it keeps
// its copy of each name until the offer is gone.
func (b *Book) WaitingOffers(ctx context.Context, user, machine string) ([]wire.WaitingOffer, error) {
	if checkUser(user) != nil {
		return nil, nil
	}
	all, err := b.offerRecords(ctx, user)
	if err != nil {
		return nil, err
	}
	var out []wire.WaitingOffer
	for _, r := range all {
		if r.Machine == machine {
			out = append(out, wire.WaitingOffer{Name: wire.UserSecretName(r.Name), Offer: wire.UUID(r.Offer)})
		}
	}
	return out, nil
}
