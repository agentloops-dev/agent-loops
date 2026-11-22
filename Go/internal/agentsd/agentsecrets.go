package agentsd

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// An agent's secret becomes the person's (#5308, specs/5308-agent-secrets-sync). An agent
// saves a secret here (ask_secret, capture_secret, generate_secret, a sign-up password): it
// is kept in sealed.json with from "agent", and this machine seals the value to each of the
// person's clients (machine/secretClients) and gives the envelopes to the control plane
// (machine/offerSecret). It never seals to another machine. A client of the person's opens
// its copy and sets the secret as one of theirs, sealed to every machine; then the offer
// goes, and the person's copy replaces this one at the next machine/userSettings.
//
// While an offer waits (machine/userSettings' offersWaiting), this machine keeps its own copy
// of the name, also when the person has one of that name: the person chooses Keep Yours or
// Use the Agent's on a client. A machine that offered nothing yet keeps its copy too, and
// offers it at the next delivery.
//
// Passkeys and a connector form's secrets stay on this machine (Alex, 2026-11-15). An old
// save (from "", before #5308) is offered when no MCP server names it and it is no passkey.

// sealedFromAgent is a secret an agent saved here, for the person (#5308).
const sealedFromAgent = "agent"

// offerAsk bounds the calls that make one offer.
const offerAsk = 30 * time.Second

// saveAgentSecret is saveSecret for an agent's save that goes to the person: kept here at once,
// then offered to the person's clients. agent is the session that saved it, if known.
func (d *Daemon) saveAgentSecret(agent wire.UUID, name, value string, needsApproval bool) error {
	home := d.personalHome()
	if home == "" {
		return errors.New("this machine has no personal home")
	}
	change := wire.SecretOperationWhatAdded
	if secretIsSet(home, name) {
		change = wire.SecretOperationWhatReplaced
	}
	if err := d.saveSealedSecret(home, name, value, sealedFromAgent, needsApproval); err != nil {
		return err
	}
	d.secretChanged(agent, name, change, true)
	d.mcpChanged()
	d.goJob("offer agent secret", func() { d.offerAgentSecret(agent, name, value, needsApproval) })
	return nil
}

// offerAgentSecret seals value to the person's clients and gives it to the control plane, then
// notes the offer on the record, if it is still the one that was sealed. Quiet when this
// machine is on no control plane, or on one older than #5308: the secret stays here, and is
// offered at the next delivery.
func (d *Daemon) offerAgentSecret(agent wire.UUID, name, value string, needsApproval bool) {
	if !secretNameHere.MatchString(name) || !d.controlHas(wire.CapMachineAgentSecrets1) || d.askControl.Load() == nil {
		return
	}
	d.agentOffersMu.Lock()
	defer d.agentOffersMu.Unlock()
	d.offerIfStillWaitingLocked(agent, name, value, needsApproval)
}

// offerIfStillWaitingLocked offers name when sealed.json, read now, still holds it as an agent's
// secret with no offer (#6626): a run that waited on agentOffersMu decided from an older read,
// and the person may have taken the name since (from "user"), or another run offered it. The
// value is the record's own, opened now; fallback is for one that opens only with a person's
// approval (a Linux value with Ask before use), which the save had in hand. Called with
// agentOffersMu held.
func (d *Daemon) offerIfStillWaitingLocked(agent wire.UUID, name, fallback string, needsApproval bool) {
	home := d.personalHome()
	if home == "" {
		return
	}
	s, ok := d.waitingAgentSecret(home, name)
	if !ok {
		return
	}
	value, err := d.openSealedSecret(sealedLocal, name, s)
	if err != nil {
		if fallback == "" {
			return
		}
		value = fallback
	} else {
		needsApproval = !s.NoAsk
	}
	d.offerAgentSecretLocked(agent, name, value, needsApproval)
}

// waitingAgentSecret is name's record in sealed.json when it waits for an offer: an agent's
// save with no offer yet, or an old save (from "") that qualifies (decision 2) and that the
// person has none of. Never one of the person's own (from "user").
func (d *Daemon) waitingAgentSecret(home, name string) (sealedValue, bool) {
	s, ok := sealedSecretsHere(home)[name]
	switch {
	case !ok || !secretNameHere.MatchString(name) || s.Offer != "" || s.Release:
		return s, false
	case s.From == sealedFromAgent:
		return s, true
	case s.From == "":
		d.userScopeMu.Lock()
		mark := d.userMarkLocked()
		d.userScopeMu.Unlock()
		return s, !slices.Contains(mark.Secrets, name) && d.oldAgentSave(home, name)
	}
	return s, false
}

// offerAgentSecretLocked makes the offer. Called with agentOffersMu held.
func (d *Daemon) offerAgentSecretLocked(agent wire.UUID, name, value string, needsApproval bool) {
	ask := d.askControl.Load()
	if ask == nil {
		return
	}
	d.orgRouteMu.Lock()
	id := d.identityLocked()
	d.orgRouteMu.Unlock()
	if id == nil {
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, offerAsk)
	defer cancel()
	raw, err := (*ask)(ctx, wire.CallMachineSecretClients.Name, wire.Empty{})
	if err != nil {
		log.Printf("agent secrets: %s: the person's apps were not read: %v", name, err)
		return
	}
	var clients wire.SealRecipients
	if json.Unmarshal(raw, &clients) != nil || clients.User == nil || len(clients.Recipients) == 0 {
		log.Printf("agent secrets: %s: the person has no app to seal it to yet; it stays on this machine", name)
		return
	}
	offer := string(store.NewUUID())
	o := wire.AgentSecretOffer{Offer: wire.UUID(offer), Name: wire.UserSecretName(name), SavedAt: nowFunc().UTC().Format(time.RFC3339),
		Sealed: map[string]wire.SealedEnvelope{}}
	if needsApproval {
		o.NeedsApproval = new(true)
	}
	if agent != "" {
		o.Agent = &agent
	}
	if about := readSecretDescriptions(d.personalHome())[name]; about != "" {
		o.Description = new(about)
	}
	for _, c := range clients.Recipients {
		key := []byte(c.PublicKey)
		if len(key) == 0 || !strings.HasPrefix(c.ID, "c:") {
			continue
		}
		ad := controllink.AgentSecretAD(*clients.User, id.host, c.ID, name, offer, needsApproval)
		e, err := controllink.Seal(key, controllink.AgentSecretPurpose, ad, []byte(value))
		if err != nil {
			continue
		}
		o.Sealed[c.ID] = e
	}
	if len(o.Sealed) == 0 {
		log.Printf("agent secrets: %s: no app of the person's has a key it seals to", name)
		return
	}
	if _, err := (*ask)(ctx, wire.CallMachineOfferSecret.Name, o); err != nil {
		log.Printf("agent secrets: %s was not offered: %v", name, err)
		return
	}
	d.noteOffer(name, offer)
	log.Printf("agent secrets: %s offered to the person's apps (%d)", name, len(o.Sealed))
}

// noteOffer keeps the offer's id on the record, so the next delivery says whether it waits.
func (d *Daemon) noteOffer(name, offer string) {
	home := d.personalHome()
	if home == "" {
		return
	}
	d.sealedSecretsMu.Lock()
	defer d.sealedSecretsMu.Unlock()
	f, err := readSealedSecrets(home)
	if err != nil {
		log.Printf("agent secrets: %s's offer was not kept: %v", name, err)
		return
	}
	s, ok := f.Secrets[name]
	if !ok || s.From != sealedFromAgent && s.From != "" {
		return
	}
	s.From, s.Offer = sealedFromAgent, offer
	f.Secrets[name] = s
	if err := writeSealedSecrets(home, f); err != nil {
		log.Printf("agent secrets: %s's offer was not kept: %v", name, err)
	}
}

// offerWaitingAgentSecrets offers each agent's secret here that has no offer yet, and each old
// save that qualifies (decision 2), after a delivery. One that does not open here (a Linux
// value with Ask before use, which opens only with a person's approval) stays here.
//
// Each delivery calls it, and each offer brings a delivery, so one run goes at a time: a call
// while one runs asks for one more run after it, and returns (#6626). Before, each delivery
// started a run that read sealed.json at once and then waited its turn for each offer: about
// a thousand runs piled up, and each offered names the person had taken since, once a second.
func (d *Daemon) offerWaitingAgentSecrets() {
	d.offersRunMu.Lock()
	if d.offersRunning {
		d.offersAgain = true
		d.offersRunMu.Unlock()
		return
	}
	d.offersRunning = true
	d.offersRunMu.Unlock()
	for {
		d.offerWaitingAgentSecretsOnce()
		d.offersRunMu.Lock()
		if !d.offersAgain || d.ctx.Err() != nil {
			d.offersRunning, d.offersAgain = false, false
			d.offersRunMu.Unlock()
			return
		}
		d.offersAgain = false
		d.offersRunMu.Unlock()
	}
}

// offerWaitingAgentSecretsOnce is one run: each name is read again just before its offer.
func (d *Daemon) offerWaitingAgentSecretsOnce() {
	home := d.personalHome()
	if home == "" || !d.controlHas(wire.CapMachineAgentSecrets1) || d.askControl.Load() == nil {
		return
	}
	d.agentOffersMu.Lock()
	defer d.agentOffersMu.Unlock()
	var names []string
	for name := range sealedSecretsHere(home) {
		if _, ok := d.waitingAgentSecret(home, name); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if d.ctx.Err() != nil {
			return
		}
		d.offerIfStillWaitingLocked("", name, "", false)
	}
}

// oldAgentSave is whether a secret kept here before #5308 was an agent's: no MCP server names
// it (a connector's form sets those) and it is no passkey.
func (d *Daemon) oldAgentSave(home, name string) bool {
	if d.secretNamedElsewhere(name, "", "", home) {
		return false
	}
	v, ok := d.sealedLocalSecret(home, name)
	if !ok {
		return false
	}
	_, err := parsePasskeys(v)
	return err != nil
}

// keptForOffers is, for a delivery, each agent's secret here that keeps this machine's copy:
// its offer waits, or it was not offered yet. done is each one whose offer was taken or
// dropped: the person's copy replaces it, or, when the person has none and none is coming,
// it goes.
func keptForOffers(f sealedSecretsFile, waiting []wire.WaitingOffer) (keep, done map[string]bool) {
	keep, done = map[string]bool{}, map[string]bool{}
	open := map[string]string{}
	for _, w := range waiting {
		open[string(w.Name)] = string(w.Offer)
	}
	for name, s := range f.Secrets {
		if s.From != sealedFromAgent {
			continue
		}
		if s.Offer == "" || open[name] == s.Offer {
			keep[name] = true
		} else {
			done[name] = true
		}
	}
	return keep, done
}
