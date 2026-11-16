package controlplane

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// offerFrom is what a machine does when an agent saves a secret (#5308): it asks for its
// person's clients, seals the value to each, and offers it. extra is a recipient that is not
// a client, which the control plane must drop.
func offerFrom(t *testing.T, box *fakeHost, offer, name, value string, extra ...wire.SealRecipient) {
	t.Helper()
	offerDescribed(t, box, offer, name, value, "", extra...)
}

// offerDescribed is offerFrom with the description the agent gave.
func offerDescribed(t *testing.T, box *fakeHost, offer, name, value, description string, extra ...wire.SealRecipient) {
	t.Helper()
	raw, err := box.uplink.Ask(t.Context(), wire.CallMachineSecretClients.Name, wire.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	var clients wire.SealRecipients
	if err := json.Unmarshal(raw, &clients); err != nil {
		t.Fatal(err)
	}
	if clients.User == nil || len(clients.Recipients) == 0 {
		t.Fatalf("%s", raw)
	}
	o := wire.AgentSecretOffer{Offer: wire.UUID(offer), Name: wire.UserSecretName(name), SavedAt: time.Now().UTC().Format(time.RFC3339),
		Agent: new(wire.UUID("6F1C2D7E-0000-4000-8000-000000000001")), Sealed: map[string]wire.SealedEnvelope{}}
	if description != "" {
		o.Description = &description
	}
	for _, r := range append(clients.Recipients, extra...) {
		if r.ID[:2] != "c:" && len(extra) == 0 {
			t.Fatalf("machine/secretClients gave a machine: %+v", r)
		}
		e, err := controllink.Seal(r.PublicKey, controllink.AgentSecretPurpose,
			controllink.AgentSecretAD(*clients.User, box.member.Host, r.ID, name, offer, false), []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		o.Sealed[r.ID] = e
	}
	if _, err := box.uplink.Ask(t.Context(), wire.CallMachineOfferSecret.Name, o); err != nil {
		t.Fatal(err)
	}
}

// takeOffer is what the person's Mac window or web page does with an offer: it opens its own
// copy and sets the secret as Add a Secret does, sealed to every machine and client.
func takeOffer(t *testing.T, who *person, row wire.SecretOffer, user string, version int64) string {
	t.Helper()
	set, plain := offerSet(t, who, row, user, version)
	call(t, who.window, "", "user/setSecret", set)
	return plain
}

// offerSet is the user/setSecret that takes row, sealed as the person's client seals it.
func offerSet(t *testing.T, who *person, row wire.SecretOffer, user string, version int64) (wire.UserSecretSet, string) {
	t.Helper()
	if row.Mine == nil {
		t.Fatalf("no copy for this client: %+v", row)
	}
	ad := controllink.AgentSecretAD(user, string(row.Machine), controllink.ClientRecipient(who.member.Client), string(row.Name),
		string(row.Offer), deref(row.NeedsApproval))
	plain, err := controllink.Open(who.member.Private, controllink.AgentSecretPurpose, ad, *row.Mine)
	if err != nil {
		t.Fatalf("this client's copy does not open: %v", err)
	}
	var recipients wire.SealRecipients
	if err := json.Unmarshal(call(t, who.window, "", "user/secretRecipients", nil), &recipients); err != nil {
		t.Fatal(err)
	}
	offer := row.Offer
	set := wire.UserSecretSet{Name: row.Name, Version: version, Offer: &offer, Description: row.Description, Values: map[string]wire.SealedEnvelope{}}
	for _, r := range recipients.Recipients {
		e, err := controllink.Seal(r.PublicKey, controllink.UserSecretPurpose, controllink.UserSecretAD(user, r.ID, string(row.Name), version), plain)
		if err != nil {
			t.Fatal(err)
		}
		set.Values[r.ID] = e
	}
	return set, string(plain)
}

func offersOf(t *testing.T, window *controllink.Session) wire.SecretOffers {
	t.Helper()
	var offers wire.SecretOffers
	if err := json.Unmarshal(call(t, window, "", wire.CallUserSecretOffers.Name, wire.Empty{}), &offers); err != nil {
		t.Fatal(err)
	}
	return offers
}

func auditActions(t *testing.T, window *controllink.Session) (map[string]wire.AuditEntry, string) {
	t.Helper()
	raw := call(t, window, "", "audit/list", wire.AuditQuery{})
	var list wire.AuditList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]wire.AuditEntry{}
	for _, e := range list.Entries {
		out[e.Action] = e
	}
	return out, string(raw)
}

// #5308: an agent's secret, offered by its machine to the person's clients only, becomes the
// person's when a client takes it, and goes to every machine; the machine keeps its copy while
// the offer waits; a name the person has waits for Keep Yours; the audit names the secret, the
// machine and the session; nothing the control plane keeps or logs holds the value.
func TestAnAgentsSecretBecomesThePersonsThroughTheirClient(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	told := &deliveries{got: map[string][]wire.MachineUserSettings{}, more: make(chan struct{}, 1)}
	p.told = told.heard
	member, window := p.client("Alex's Mac")
	alex := &person{id: p.s.localOwner(), member: member, window: window}
	devbox := p.machineSaying(alex, takingHello("MACHINE-30"))
	other := p.machineSaying(alex, takingHello("MACHINE-31"))
	p.waitFor(func() bool { _, ok := told.last(other.member.Host); return ok }, "the other machine told at its join")

	// devbox offers; a copy sealed to the other machine is dropped: a machine never seals to
	// another machine.
	const value = "glpat-agents-saved-this-4Kq9Zt"
	var otherMachine wire.SealRecipients
	if err := json.Unmarshal(call(t, window, "", "user/secretRecipients", nil), &otherMachine); err != nil {
		t.Fatal(err)
	}
	var machineRecipient wire.SealRecipient
	for _, r := range otherMachine.Recipients {
		if r.ID == controllink.MachineRecipient(other.member.Host) {
			machineRecipient = r
		}
	}
	offerDescribed(t, devbox, "0FFE0000-0000-4000-8000-000000000001", "GITLAB_TOKEN", value, "gitlab.com, api", machineRecipient)
	waitNote(t, window, wire.NotifyControlSealingWanted.Name)

	offers := offersOf(t, window)
	if len(offers.Offers) != 1 || offers.User == nil || *offers.User != alex.id {
		t.Fatalf("%+v", offers)
	}
	row := offers.Offers[0]
	if row.Name != "GITLAB_TOKEN" || string(row.Machine) != devbox.member.Host || row.MachineName == "" || deref(row.Conflict) ||
		row.Agent == nil {
		t.Fatalf("%+v", row)
	}
	// devbox is told its offer waits, so it keeps its own copy.
	told.waitTold(t, devbox.member.Host, func(u wire.MachineUserSettings) bool {
		return len(u.OffersWaiting) == 1 && u.OffersWaiting[0].Offer == "0FFE0000-0000-4000-8000-000000000001"
	}, "devbox told its offer waits")
	if u, _ := told.last(other.member.Host); len(u.OffersWaiting) != 0 || len(u.Secrets) != 0 {
		t.Fatalf("the other machine was told %+v", u)
	}

	// The window takes it: the secret is the person's, on both machines, and says where it
	// came from.
	if got := takeOffer(t, alex, row, alex.id, time.Now().UnixMilli()); got != value {
		t.Fatalf("opened %q", got)
	}
	if left := offersOf(t, window); len(left.Offers) != 0 {
		t.Fatalf("%+v", left)
	}
	told.waitTold(t, other.member.Host, func(u wire.MachineUserSettings) bool { return secretsOf(other, u)["GITLAB_TOKEN"] == value },
		"the other machine has the token")
	told.waitTold(t, devbox.member.Host, func(u wire.MachineUserSettings) bool {
		return secretsOf(devbox, u)["GITLAB_TOKEN"] == value && len(u.OffersWaiting) == 0
	}, "devbox has the person's copy, and no offer waits")
	var names wire.UserSecretNames
	if err := json.Unmarshal(call(t, window, "", "user/secretNames", nil), &names); err != nil {
		t.Fatal(err)
	}
	if len(names.Secrets) != 1 || names.Secrets[0].SavedBy == nil || names.Secrets[0].SavedBy.Name != row.MachineName ||
		string(names.Secrets[0].SavedBy.Machine) != devbox.member.Host {
		t.Fatalf("%+v", names)
	}
	// The same offer is not taken twice.
	if e := callError(t, window, "", "user/setSecret", wire.UserSecretSet{Name: "GITLAB_TOKEN", Version: time.Now().UnixMilli() + 1,
		Offer: new(wire.UUID("0FFE0000-0000-4000-8000-000000000001")), Values: map[string]wire.SealedEnvelope{}}); !strings.Contains(e, "-32107") {
		t.Fatal(e)
	}

	// The agent saves the name again: it waits for the person, and is never set on its own.
	offerFrom(t, devbox, "0FFE0000-0000-4000-8000-000000000002", "GITLAB_TOKEN", "glpat-a-machine-taken-over")
	offers = offersOf(t, window)
	if len(offers.Offers) != 1 || !deref(offers.Offers[0].Conflict) {
		t.Fatalf("%+v", offers)
	}
	// An app that takes it as a new name, not as Use the Agent's at the kept version plus one, is
	// refused: the person's value is never replaced on its own.
	if err := json.Unmarshal(call(t, window, "", "user/secretNames", nil), &names); err != nil {
		t.Fatal(err)
	}
	had := deref(names.Secrets[0].Version)
	for _, version := range []int64{time.Now().UnixMilli() + 60_000, had + 2} {
		set, _ := offerSet(t, alex, offers.Offers[0], alex.id, version)
		if e := callError(t, window, "", "user/setSecret", set); !strings.Contains(e, "Keep Yours") {
			t.Fatalf("version %d: %s", version, e)
		}
	}
	// Use the Agent's, at the kept version plus one: the agent's value, and the person's own
	// description, which the agent's offer did not give.
	takeOffer(t, alex, offers.Offers[0], alex.id, had+1)
	if err := json.Unmarshal(call(t, window, "", "user/secretNames", nil), &names); err != nil {
		t.Fatal(err)
	}
	if deref(names.Secrets[0].Description) != "gitlab.com, api" {
		t.Fatalf("%+v", names.Secrets[0])
	}
	told.waitTold(t, other.member.Host, func(u wire.MachineUserSettings) bool {
		return secretsOf(other, u)["GITLAB_TOKEN"] == "glpat-a-machine-taken-over"
	}, "the other machine has the agent's value")
	offerFrom(t, devbox, "0FFE0000-0000-4000-8000-000000000004", "GITLAB_TOKEN", "glpat-again")
	offers = offersOf(t, window)
	// Keep Yours: the offer goes, and the person's value stays on every machine.
	call(t, window, "", wire.CallUserDropSecretOffer.Name, wire.SecretOfferRequest{Offer: "0FFE0000-0000-4000-8000-000000000004"})
	if left := offersOf(t, window); len(left.Offers) != 0 {
		t.Fatalf("%+v", left)
	}
	if e := callError(t, window, "", wire.CallUserDropSecretOffer.Name, wire.SecretOfferRequest{Offer: "0FFE0000-0000-4000-8000-000000000004"}); !strings.Contains(e, "-32107") {
		t.Fatal(e)
	}
	if u, _ := told.last(other.member.Host); secretsOf(other, u)["GITLAB_TOKEN"] != "glpat-a-machine-taken-over" {
		t.Fatalf("%+v", u)
	}

	// Another person sees none of Alex's offers.
	sam := p.invite(window, "Sam", wire.UserRoleMember)
	offerFrom(t, devbox, "0FFE0000-0000-4000-8000-000000000003", "NPM_TOKEN", "npm_xyz")
	if got := offersOf(t, sam.window); len(got.Offers) != 0 {
		t.Fatalf("%+v", got)
	}

	// An offer that user/setSecret could never take (its id is not a UUID) is refused.
	if _, err := devbox.uplink.Ask(t.Context(), wire.CallMachineOfferSecret.Name, wire.AgentSecretOffer{Offer: "abc123", Name: "NPM_TOKEN",
		Sealed: map[string]wire.SealedEnvelope{controllink.ClientRecipient(member.Client): {}}}); err == nil || !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("an offer with an id that is not a UUID: %v", err)
	}

	// The audit: each step, with the name, the machine and the session; never the value.
	actions, raw := auditActions(t, window)
	saved, took, kept := actions["An agent saved a secret"], actions["Took an agent's secret"], actions["Kept your secret over an agent's"]
	if !strings.Contains(deref(saved.Target), "GITLAB_TOKEN · "+row.MachineName+" · session 6F1C2D7E") ||
		!strings.Contains(deref(took.Target), "GITLAB_TOKEN") || took.Actor.Client == nil || !strings.Contains(deref(kept.Target), "GITLAB_TOKEN") {
		t.Fatalf("%+v", actions)
	}
	if strings.Contains(raw, "glpat-") {
		t.Fatal("the audit holds a value")
	}

	// A removed machine's offers go with it.
	call(t, window, "", "machines/remove", map[string]any{"host": devbox.member.Host})
	p.waitFor(func() bool { return len(offersOf(t, window).Offers) == 0 }, "devbox's offers gone with it")

	// The store and the log hold no value.
	listed, _ := st.List(t.Context(), "")
	for _, k := range listed {
		if o, _ := st.Get(t.Context(), k.Key); o != nil && (strings.Contains(string(o.Data), "glpat-") || strings.Contains(string(o.Data), "npm_xyz")) {
			t.Fatalf("%s holds a value", k.Key)
		}
	}
	for drained := false; !drained; {
		select {
		case line := <-p.lines:
			if strings.Contains(line, "glpat-") {
				t.Fatalf("logged: %s", line)
			}
		default:
			drained = true
		}
	}
}
