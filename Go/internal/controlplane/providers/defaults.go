package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// New Machine with no questions (#3972): a make with no provider, name or options takes
// the person's default provider, the name machine-N and the provider's default options.

// MachineNamesPrefix is where each person's machine-N counter is kept. It is the person's
// current state: it goes with the person (#6759), and not at an age before, so that a
// number is never used twice (#3971).
// store-bounds: kind=key-value; size=64KiB a person; age=until-deleted; applied=on write, on person removal and hourly sweep
const MachineNamesPrefix = "v1/machine-names/"

// machineNamesKey is a person's counter; the admin who signs in with no person has one too.
func machineNamesKey(person string) string {
	if person == "" {
		person = noPerson
	}
	return MachineNamesPrefix + url.PathEscape(person) + ".json"
}

// noPerson stands in a key for the admin who signs in with no person.
const noPerson = "-"

// machineNamesMost is the largest record of a person's. Choices that would take it past
// that are not kept; the counter always is (#6759).
const machineNamesMost = 64 << 10

// machineNamesRoom is the part of machineNamesMost that choices leave free, so that the
// counter and the default provider's id, which are written with no check, always fit.
const machineNamesRoom = 1 << 10

// MachineNamesPerson is the person whose record the key holds. ok is false for the admin
// with no person, and for a key of something else.
func MachineNamesPerson(key string) (person string, ok bool) {
	escaped, isPrefix := strings.CutPrefix(key, MachineNamesPrefix)
	escaped, isJSON := strings.CutSuffix(escaped, ".json")
	person, err := url.PathUnescape(escaped)
	return person, isPrefix && isJSON && err == nil && person != "" && person != noPerson
}

// ForgetMachineNames deletes a removed person's machine-N counter and last choices (#6759).
func ForgetMachineNames(ctx context.Context, s store.Store, person string) error {
	if person == "" || person == noPerson {
		return nil
	}
	return s.Delete(ctx, machineNamesKey(person))
}

// machineNames is v1/machine-names/<person>.json: the number the person's next machine-N
// takes, and what New Machine makes it with (#3979). Next only goes up, so a number is
// never used again, also after its machine is deleted or renamed.
type machineNames struct {
	Next int `json:"next"`
	// The provider New Machine uses: the one made on last, or chosen with Make Default.
	Default string `json:"default,omitempty"`
	// What the person chose on each provider's form the last time they made a machine on it,
	// by provider id: only the values that were not the form's defaults then (#6686), so a
	// default that changes later (#6615's size) is the next make's. The "options" of before
	// held the defaults too, as if chosen, and is no longer read.
	Chosen map[string]json.RawMessage `json:"chosen,omitempty"`
}

// keptFailedMakes is how many makes that failed a provider keeps as rows, newest last.
const keptFailedMakes = 5

// failedMake is a make that failed (#3972): a row in the failed state with the provider's
// reason, shown only to the person who made it. The rows are kept in the store too
// (#4747), at FailedMakesPrefix, so a restart or a redeploy does not lose the reason.
type failedMake struct {
	info   MachineInfo
	person string
	at     time.Time
}

// old says the row is past failedMakesKept.
func (f failedMake) old(now time.Time) bool { return now.Sub(f.at) >= failedMakesKept }

// failedMakesKept is how long a failed make is kept (#6971).
const failedMakesKept = 90 * 24 * time.Hour

// failedMakesMost is the largest record: the oldest rows go first. failedReasonMost is the
// longest reason; a longer one is cut.
const (
	failedMakesMost  = 64 << 10
	failedReasonMost = 4 << 10
)

// FailedMakesPrefix is where each provider's failed makes are kept: one record a provider,
// at most keptFailedMakes rows, the newest last.
// store-bounds: kind=log; count=5 a provider; size=64KiB a record; age=90d; applied=on write and hourly sweep, also until the provider is removed
const FailedMakesPrefix = "v1/failed-makes/"

// failedMakesKey is a provider's record of failed makes.
func failedMakesKey(provider string) string {
	return FailedMakesPrefix + url.PathEscape(provider) + ".json"
}

// keptFailedMake is a failed make as the store keeps it.
type keptFailedMake struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Person string `json:"person,omitempty"`
	// At is when it failed. A row kept before #6971 has none: the first write or sweep gives
	// it that time.
	At *time.Time `json:"at,omitempty"`
}

// row is the failed make as a row of its provider's, with now for one with no time.
func (k keptFailedMake) row(now time.Time) failedMake {
	at := now
	if k.At != nil {
		at = *k.At
	}
	return failedMake{info: MachineInfo{ID: "failed-" + k.Name, Name: k.Name, State: "failed", Reason: &k.Reason}, person: k.Person, at: at}
}

// boundFailed dates the rows with no time, and drops the old ones and the oldest over the
// count and the size. It says the record to keep, and whether it differs from rows.
func boundFailed(rows []keptFailedMake, now time.Time) (kept []keptFailedMake, data []byte, changed bool) {
	for _, k := range rows {
		if k.At == nil {
			k.At, changed = &now, true
		}
		if now.Sub(*k.At) < failedMakesKept {
			kept = append(kept, k)
		}
	}
	if len(kept) > keptFailedMakes {
		kept = kept[len(kept)-keptFailedMakes:]
	}
	for data, _ = json.Marshal(kept); len(data) > failedMakesMost; data, _ = json.Marshal(kept) {
		kept = kept[1:]
	}
	return kept, data, changed || len(kept) != len(rows)
}

// SweepFailedMakes ages out the failed makes in the store (#6971), and deletes those of
// providers that are gone; at most most deletions and writes. full says it stopped at most.
func SweepFailedMakes(ctx context.Context, rs *records.Records, now time.Time, most int) (swept int, full bool, err error) {
	s := rs.Store
	keys, err := s.List(ctx, FailedMakesPrefix)
	if err != nil {
		return 0, false, err
	}
	for _, k := range keys {
		if swept >= most {
			return swept, true, nil
		}
		did, err := sweepFailedMakesOf(ctx, s, k.Key, now)
		if err != nil {
			return swept, false, err
		}
		if did {
			swept++
		}
	}
	return swept, false, nil
}

// sweepFailedMakesOf sweeps one provider's record, and says whether it changed it.
func sweepFailedMakesOf(ctx context.Context, s store.Store, key string, now time.Time) (bool, error) {
	object, err := s.Get(ctx, key)
	if err != nil || object == nil {
		return false, err
	}
	var rows []keptFailedMake
	_ = json.Unmarshal(object.Data, &rows)
	kept, data, changed := boundFailed(rows, now)
	if len(kept) > 0 {
		id, _ := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(key, FailedMakesPrefix), ".json"))
		provider, err := s.Get(ctx, ProviderKey(id))
		if err != nil {
			return false, err
		}
		var record ProviderRecord
		if provider != nil && (json.Unmarshal(provider.Data, &record) != nil || record.Forgotten == nil || !*record.Forgotten) {
			if !changed {
				return false, nil
			}
			_, err = s.Put(ctx, key, data, store.Matching(object.ETag))
			var conflict *store.ConflictError
			if errors.As(err, &conflict) {
				// Another write came between: the next hour's sweep looks again.
				return false, nil
			}
			return err == nil, err
		}
	}
	return true, s.Delete(ctx, key)
}

// defaultProvider is the person's default provider: the one they made a machine on last or
// chose with Make Default (#3979), or else the first one added that they may make machines
// from. One added before addedAt was kept comes first, by name.
func (p *Providers) defaultProvider(person string) string {
	p.mu.Lock()
	held := make([]ProviderRecord, 0, len(p.providers))
	for _, h := range p.providers {
		held = append(held, h.record)
	}
	p.mu.Unlock()
	var mayMake []ProviderRecord
	for _, r := range held {
		if p.mayMakeOn(person, r) {
			mayMake = append(mayMake, r)
		}
	}
	if chosen := p.choices(person).Default; chosen != "" {
		for _, r := range mayMake {
			if r.ID == chosen {
				return chosen
			}
		}
	}
	sort.Slice(mayMake, func(i, j int) bool {
		a, b := mayMake[i], mayMake[j]
		if (a.AddedAt == nil) != (b.AddedAt == nil) {
			return a.AddedAt == nil
		}
		if a.AddedAt != nil && !a.AddedAt.Equal(b.AddedAt.Time) {
			return a.AddedAt.Before(b.AddedAt.Time)
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	if len(mayMake) == 0 {
		return ""
	}
	return mayMake[0].ID
}

// mayMakeOn says the person may make machines on the provider.
func (p *Providers) mayMakeOn(person string, r ProviderRecord) bool {
	return (r.Forgotten == nil || !*r.Forgotten) && contains(r.Capabilities, "create") && p.sees(person, r) && p.MayUse(person, r)
}

// makeDefault is providers/makeDefault (#3979): New Machine uses the provider for the
// person until they make a machine on another one.
func (p *Providers) makeDefault(person, id string) error {
	r, ok := p.record(id)
	if !ok || !p.sees(person, r) {
		return invalid("There is no such provider.")
	}
	if !p.mayMakeOn(person, r) {
		return invalid("%s can’t make machines for you.", r.Name)
	}
	if err := p.changeChoices(person, func(c *machineNames) { c.Default = id }); err != nil {
		return err
	}
	p.changed()
	return nil
}

// lastOptions is what the person chose on the provider the last time they made a machine on
// it, or nil.
func (p *Providers) lastOptions(person, id string) any {
	raw, ok := p.choices(person).Chosen[id]
	if !ok {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}

// madeOn keeps the provider a make used (#3979), and what the person chose on its form: the
// options given that are not the form's defaults (#6686), for the next New Machine. A make
// that took every default keeps no choice, so the next one takes the defaults of then.
// Choices for a provider that is gone go, and choices too large for the record are not
// kept: the earlier ones stay (#7194).
func (p *Providers) madeOn(person, id string, given any, form newMachineForm) {
	options, _ := given.(map[string]any)
	chosen := map[string]any{}
	for key, value := range options {
		if d, ok := form.defaults[key]; !ok || !sameValue(d, value) {
			chosen[key] = value
		}
	}
	tooLarge := false
	err := p.changeChoices(person, func(c *machineNames) {
		c.Default = id
		for other := range c.Chosen {
			if _, ok := p.record(other); !ok {
				delete(c.Chosen, other)
			}
		}
		if len(chosen) == 0 {
			delete(c.Chosen, id)
			return
		}
		if c.Chosen == nil {
			c.Chosen = map[string]json.RawMessage{}
		}
		earlier, had := c.Chosen[id]
		c.Chosen[id] = rawOf(chosen)
		if tooLarge = len(rawOf(c)) > machineNamesMost-machineNamesRoom; tooLarge {
			if had {
				c.Chosen[id] = earlier
			} else {
				delete(c.Chosen, id)
			}
		}
	})
	if tooLarge {
		p.log(fmt.Sprintf("workspace: the options %s made a machine with are not kept: a person's record is %d bytes at most", id, machineNamesMost))
	}
	if err != nil {
		p.log("workspace: could not keep the options " + id + " made a machine with: " + errorWords(err))
	}
}

// sameValue says two of a form's values are the same, as JSON.
func sameValue(a, b any) bool {
	return string(rawOf(a)) == string(rawOf(b))
}

// choices is the person's v1/machine-names record; empty when there is none or it can't be read.
func (p *Providers) choices(person string) machineNames {
	names := machineNames{Next: 1}
	object, err := p.store.Get(context.Background(), machineNamesKey(person))
	if err != nil || object == nil {
		return names
	}
	_ = json.Unmarshal(object.Data, &names)
	return names
}

// changeChoices changes the person's v1/machine-names record, again when another write got there first.
func (p *Providers) changeChoices(person string, change func(*machineNames)) error {
	key := machineNamesKey(person)
	ctx := context.Background()
	for range 5 {
		names := machineNames{Next: 1}
		when := store.Absent
		object, err := p.store.Get(ctx, key)
		if err != nil {
			return err
		}
		if object != nil {
			if err := json.Unmarshal(object.Data, &names); err != nil {
				return err
			}
			when = store.Matching(object.ETag)
		}
		change(&names)
		data, _ := json.Marshal(names)
		if _, err := p.store.Put(ctx, key, data, when); err != nil {
			var conflict *store.ConflictError
			if errors.As(err, &conflict) {
				continue
			}
			return err
		}
		return nil
	}
	return fail(CodeInternalError, "Could not keep what New Machine makes. Try again.")
}

// newMachineForm is a provider's New Machine form, read once: its fields, and each field's
// `default`, or else the first of its `enum`, as the Mac's form shows it.
type newMachineForm struct {
	properties map[string]any
	defaults   map[string]any
}

// formOf reads the provider's New Machine form; an empty one when it can't be read.
func (p *Providers) formOf(id string) newMachineForm {
	form := newMachineForm{properties: map[string]any{}, defaults: map[string]any{}}
	schema, err := p.providerCall(id, func(c Provider) (any, error) { return c.Options() })
	if err != nil {
		return form
	}
	object, _ := valueOf(rawOf(schema)).(map[string]any)
	if properties, ok := object["properties"].(map[string]any); ok {
		form.properties = properties
	}
	for key, property := range form.properties {
		field, _ := property.(map[string]any)
		if d, ok := field["default"]; ok {
			form.defaults[key] = d
		} else if choices, _ := field["enum"].([]any); len(choices) > 0 {
			form.defaults[key] = choices[0]
		}
	}
	return form
}

// values is what New Machine makes with on the form: what the person chose last, over the
// form's defaults, keeping only the fields the form still has and, where a field has an
// enum, only a choice it still offers (#6686).
func (form newMachineForm) values(last any) map[string]any {
	values := map[string]any{}
	for key, d := range form.defaults {
		values[key] = d
	}
	chosen, _ := last.(map[string]any)
	for key, v := range chosen {
		property, ok := form.properties[key]
		if !ok {
			continue
		}
		if choices, _ := property.(map[string]any)["enum"].([]any); len(choices) > 0 && !offers(choices, v) {
			continue
		}
		values[key] = v
	}
	return values
}

// offers says choices has v.
func offers(choices []any, v any) bool {
	for _, c := range choices {
		if sameValue(c, v) {
			return true
		}
	}
	return false
}

// nextName is the person's next machine-N that neither the provider nor a host has yet,
// with the counter moved past it.
func (p *Providers) nextName(person, provider string) (string, error) {
	taken := p.takenNames(provider)
	name := ""
	err := p.changeChoices(person, func(c *machineNames) {
		n := freeNumber(taken, c.Next)
		c.Next = n + 1
		name = fmt.Sprintf("machine-%d", n)
	})
	if err != nil {
		return "", err
	}
	return name, nil
}

// peekName is the name nextName would give, without keeping it: what the New Machine
// sheet fills in (#4634).
func (p *Providers) peekName(person, provider string) string {
	return fmt.Sprintf("machine-%d", freeNumber(p.takenNames(provider), p.choices(person).Next))
}

// freeNumber is the first n, from next, whose machine-n is not taken.
func freeNumber(taken map[string]bool, next int) int {
	n := max(next, 1)
	for taken[fmt.Sprintf("machine-%d", n)] {
		n++
	}
	return n
}

// usedName keeps a machine-N that a make was given by name, as the New Machine sheet
// gives the one peekName filled in, so the counter never gives that number again.
func (p *Providers) usedName(person, name string) {
	var n int
	if _, err := fmt.Sscanf(name, "machine-%d", &n); err != nil || fmt.Sprintf("machine-%d", n) != name {
		return
	}
	if p.choices(person).Next > n {
		return
	}
	err := p.changeChoices(person, func(c *machineNames) { c.Next = max(c.Next, n+1) })
	if err != nil {
		p.log("workspace: could not keep the name " + name + ": " + errorWords(err))
	}
}

// newMachineForm is providers/options (#4634): the provider's New Machine form, with the
// values and the name New Machine would make it with. With no id, the default provider.
func (p *Providers) newMachineForm(person, id string) (any, error) {
	if id == "" {
		if id = p.defaultProvider(person); id == "" {
			return nil, invalid("There is no machine provider you may make machines with. Add one in Settings ▸ Machine Providers.")
		}
	}
	schema, err := p.providerCall(id, func(c Provider) (any, error) { return c.Options() })
	if err != nil {
		return nil, err
	}
	name := p.peekName(person, id)
	provider := wire.ProviderID(id)
	return ProviderOptions{Schema: rawOf(schema), ID: &provider, Values: jsonValue(p.formValues(person, id)), Name: &name}, nil
}

// formValues is what New Machine makes with on the provider, and what a make with no
// options makes (#6686): the person's last choices over the form's defaults.
func (p *Providers) formValues(person, id string) map[string]any {
	return p.formOf(id).values(p.lastOptions(person, id))
}

// takenNames is every host's name and every workspace's name on the provider, in lower case.
func (p *Providers) takenNames(provider string) map[string]bool {
	taken := map[string]bool{}
	for _, h := range p.records.Hosts() {
		taken[strings.ToLower(h.Name)] = true
	}
	p.mu.Lock()
	if l := p.listed[provider]; l != nil {
		for _, w := range l.workspaces {
			taken[strings.ToLower(w.Name)] = true
		}
	}
	p.mu.Unlock()
	return taken
}

// loadFailed reads the provider's failed makes back from the store, the newest last.
func (p *Providers) loadFailed(provider string) {
	object, err := p.store.Get(context.Background(), failedMakesKey(provider))
	if err != nil || object == nil {
		return
	}
	var kept []keptFailedMake
	if json.Unmarshal(object.Data, &kept) != nil {
		return
	}
	p.showFailed(provider, kept)
}

// showFailed makes kept the provider's failed rows here.
func (p *Providers) showFailed(provider string, kept []keptFailedMake) {
	now := time.Now()
	kept, _, _ = boundFailed(kept, now)
	rows := make([]failedMake, 0, len(kept))
	for _, k := range kept {
		rows = append(rows, k.row(now))
	}
	p.mu.Lock()
	p.failedMakes[provider] = rows
	p.mu.Unlock()
}

// keepFailed adds a failed make to the provider's record in the store, the oldest gone
// past keptFailedMakes, again when another copy wrote first, and returns the rows kept.
// Another copy's rows come in too, as the record holds them.
func (p *Providers) keepFailed(provider string, row keptFailedMake) ([]keptFailedMake, error) {
	key := failedMakesKey(provider)
	ctx := context.Background()
	for range 5 {
		var kept []keptFailedMake
		when := store.Absent
		object, err := p.store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if object != nil {
			_ = json.Unmarshal(object.Data, &kept)
			when = store.Matching(object.ETag)
		}
		kept, data, _ := boundFailed(append(kept, row), time.Now())
		if _, err := p.store.Put(ctx, key, data, when); err != nil {
			var conflict *store.ConflictError
			if errors.As(err, &conflict) {
				continue
			}
			return nil, err
		}
		return kept, nil
	}
	return nil, errors.New("another copy kept changing the record")
}

// failed keeps a make that failed as a row of the provider's, the oldest gone past
// keptFailedMakes: in the store, so a restart keeps it (#4747), and here. When the store
// can't take it, the row is kept here only.
func (p *Providers) failed(provider, name, person, reason string) {
	if len(reason) > failedReasonMost {
		reason = strings.ToValidUTF8(reason[:failedReasonMost-len("…")], "") + "…"
	}
	now := time.Now()
	row := keptFailedMake{Name: name, Reason: reason, Person: person, At: &now}
	kept, err := p.keepFailed(provider, row)
	if err != nil {
		p.log("workspace: could not keep the failed make " + name + " of " + provider + ": " + errorWords(err))
		p.mu.Lock()
		for _, f := range p.failedMakes[provider] {
			kept = append(kept, keptFailedMake{Name: f.info.Name, Reason: deref(f.info.Reason), Person: f.person, At: &f.at})
		}
		p.mu.Unlock()
		kept = append(kept, row)
	}
	p.showFailed(provider, kept)
	p.changed()
}

// forgetFailed deletes a provider's failed makes, when the provider goes.
func (p *Providers) forgetFailed(provider string) {
	p.mu.Lock()
	delete(p.failedMakes, provider)
	p.mu.Unlock()
	if err := p.store.Delete(context.Background(), failedMakesKey(provider)); err != nil {
		p.log("workspace: could not forget the failed makes of " + provider + ": " + errorWords(err))
	}
}

// deleteFailed is Delete on a failed make's row (#6078): the row goes from here and from
// the store, so a restart does not bring it back. The provider never made the machine, so
// it is not asked to delete anything. Only the person who made it, or an admin, may.
func (p *Providers) deleteFailed(provider, workspace, person, who string) error {
	maker, ok := p.failedOwner(provider, workspace)
	if !ok {
		return invalid("There is no such failed make.")
	}
	if maker != person && !p.isAdmin(person) {
		return fail(CodeNotPermitted, "Only the person who made it, or an admin, can delete it.")
	}
	name := strings.TrimPrefix(workspace, "failed-")
	if err := p.forgetOneFailed(provider, name); err != nil {
		p.log(fmt.Sprintf("workspace: delete the failed make %s/%s by %s: failed: %s", provider, name, who, errorWords(err)))
		return err
	}
	p.mu.Lock()
	kept := p.failedMakes[provider][:0:0]
	for _, f := range p.failedMakes[provider] {
		if f.info.ID != workspace {
			kept = append(kept, f)
		}
	}
	p.failedMakes[provider] = kept
	p.mu.Unlock()
	p.log(fmt.Sprintf("workspace: delete the failed make %s/%s by %s: ok", provider, name, who))
	p.changed()
	return nil
}

// forgetOneFailed takes the failed make called name out of the provider's record in the
// store, again when another copy wrote first.
func (p *Providers) forgetOneFailed(provider, name string) error {
	key := failedMakesKey(provider)
	ctx := context.Background()
	for range 5 {
		object, err := p.store.Get(ctx, key)
		if err != nil {
			return err
		}
		if object == nil {
			return nil
		}
		var kept []keptFailedMake
		_ = json.Unmarshal(object.Data, &kept)
		left := kept[:0:0]
		for _, k := range kept {
			if k.Name != name {
				left = append(left, k)
			}
		}
		if len(left) == len(kept) {
			return nil
		}
		data, _ := json.Marshal(left)
		if _, err := p.store.Put(ctx, key, data, store.Matching(object.ETag)); err != nil {
			var conflict *store.ConflictError
			if errors.As(err, &conflict) {
				continue
			}
			return err
		}
		return nil
	}
	return errors.New("another copy kept changing the record")
}

// failedOwner is who made the failed row, and whether it is one.
func (p *Providers) failedOwner(provider, workspace string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.failedMakes[provider] {
		if f.info.ID == workspace {
			return f.person, true
		}
	}
	return "", false
}
