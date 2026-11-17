package agentsd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// personsApp is a client of the person's, as machine/secretClients gives it, and the offers
// the daemon made to it.
type personsApp struct {
	private []byte
	id      string
	mu      sync.Mutex
	offers  []wire.AgentSecretOffer
	asked   []string
}

func (a *personsApp) offered() []wire.AgentSecretOffer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.offers)
}

// open is what the person's client does with its copy of an offer.
func (a *personsApp) open(t *testing.T, o wire.AgentSecretOffer) string {
	t.Helper()
	ad := controllink.AgentSecretAD("sam", orgTestHost, a.id, string(o.Name), string(o.Offer), o.NeedsApproval != nil && *o.NeedsApproval)
	plain, err := controllink.Open(a.private, controllink.AgentSecretPurpose, ad, o.Sealed[a.id])
	if err != nil {
		t.Fatalf("the app's copy does not open: %v", err)
	}
	return string(plain)
}

// withPersonsApp is a control plane that has machine.agent-secrets/1 and one client of Sam's.
func withPersonsApp(t *testing.T, d *Daemon) *personsApp {
	t.Helper()
	private := controllink.NewPrivateKey()
	public, err := controllink.PublicKey(private)
	if err != nil {
		t.Fatal(err)
	}
	app := &personsApp{private: private, id: controllink.ClientRecipient("3d0c4b4e-0000-4000-8000-00000000000a")}
	d.SetControlCapabilities(func() []string { return []string{wire.CapMachineAgentSecrets1} })
	user := "sam"
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		app.mu.Lock()
		defer app.mu.Unlock()
		app.asked = append(app.asked, method)
		switch method {
		case wire.CallMachineSecretClients.Name:
			return mustJSON(wire.SealRecipients{User: &user, Recipients: []wire.SealRecipient{{ID: app.id, Name: "Sam's Mac", PublicKey: public}}}), nil
		case wire.CallMachineOfferSecret.Name:
			app.offers = append(app.offers, params.(wire.AgentSecretOffer))
		}
		return json.RawMessage(`{}`), nil
	})
	return app
}

// #5308: an agent's save here is sealed to the person's clients only and offered; this
// machine keeps its own copy while the offer waits, also over the person's of that name;
// the person's copy replaces it once the offer is taken, and one dropped with no copy goes.
func TestAnAgentsSecretIsOfferedToThePersonsApps(t *testing.T) {
	t.Parallel()

	root, home := t.TempDir(), t.TempDir()
	personalHomeFor(t, home)
	_, public := machineKeys(t, root)
	d, _ := start(t, root)
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	app := withPersonsApp(t, d)

	if err := d.saveAgentSecret("", "GITLAB_TOKEN", "glpat-agent-saved", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the offer", func() bool { return readSealedSecrets(home).Secrets["GITLAB_TOKEN"].Offer != "" })
	offers := app.offered()
	if len(offers) != 1 || offers[0].Name != "GITLAB_TOKEN" || len(offers[0].Sealed) != 1 || app.open(t, offers[0]) != "glpat-agent-saved" {
		t.Fatalf("%+v", offers)
	}
	for recipient := range offers[0].Sealed {
		if recipient != app.id {
			t.Fatalf("sealed to %s: a machine seals to the person's apps only", recipient)
		}
	}
	offer := offers[0].Offer
	if s := readSealedSecrets(home).Secrets["GITLAB_TOKEN"]; s.From != sealedFromAgent || string(offer) != s.Offer {
		t.Fatalf("%+v", s)
	}
	send := func(waiting []wire.WaitingOffer, secrets ...wire.SealedUserSecret) {
		d.Told(wire.NotifyMachineUserSettings.Name, mustJSON(wire.MachineUserSettings{User: "sam", Secrets: secrets, OffersWaiting: waiting}))
	}

	// The person has a GITLAB_TOKEN of their own, and the offer waits: this machine keeps the
	// agent's, and lists it On This Machine.
	send([]wire.WaitingOffer{{Name: "GITLAB_TOKEN", Offer: offer}}, sealedSecret(t, public, "sam", "GITLAB_TOKEN", 1, "glpat-the-persons"))
	if v, _, _ := d.localSecret("", "GITLAB_TOKEN"); v != "glpat-agent-saved" {
		t.Fatalf("%q", v)
	}
	if rows := d.localSecretList().Secrets; len(rows) != 1 || rows[0].Name != "GITLAB_TOKEN" {
		t.Fatalf("%+v", rows)
	}

	// Taken (Use the Agent's, or Keep Yours): the person's copy replaces this machine's.
	send(nil, sealedSecret(t, public, "sam", "GITLAB_TOKEN", 2, "glpat-agent-saved"))
	if s := readSealedSecrets(home).Secrets["GITLAB_TOKEN"]; s.From != sealedFromUser {
		t.Fatalf("%+v", s)
	}
	if v, _, _ := d.localSecret("", "GITLAB_TOKEN"); v != "glpat-agent-saved" {
		t.Fatalf("%q", v)
	}
	if rows := d.localSecretList().Secrets; len(rows) != 0 {
		t.Fatalf("%+v", rows)
	}

	// An offer that went with no copy of the person's coming: this machine's goes too.
	if err := d.saveAgentSecret("", "NPM_TOKEN", "npm_agent_saved", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second offer", func() bool { return readSealedSecrets(home).Secrets["NPM_TOKEN"].Offer != "" })
	send(nil, sealedSecret(t, public, "sam", "GITLAB_TOKEN", 2, "glpat-agent-saved"))
	if _, ok := readSealedSecrets(home).Secrets["NPM_TOKEN"]; ok {
		t.Fatal("a dropped offer's copy stayed")
	}
	assertNotUnder(t, root, "npm_agent_saved")
}

// Alex, 2026-11-15: an old save is offered at the next delivery, but not a passkey, not a
// connector form's secret (an MCP server names it) and not a name the control plane does not
// take.
func TestOldSavesAreOfferedButNotPasskeysOrConnectorSecrets(t *testing.T) {
	t.Parallel()

	root, home := t.TempDir(), t.TempDir()
	personalHomeFor(t, home)
	machineKeys(t, root)
	if err := os.MkdirAll(filepath.Join(home, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "mcp.json"), []byte(`{"mcpServers":{
		"linear":{"url":"https://mcp.linear.app/mcp","headers":{"Authorization":"Bearer ${LINEAR_TOKEN}"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _ := start(t, root)
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()
	for name, value := range map[string]string{
		"OLD_TOKEN":    "old-agent-save-1234",
		"LINEAR_TOKEN": "lin_api_connector",
		"SITE_PASSKEY": `{"site":"example.com","passkeys":[]}`,
		"lower_case":   "not-taken-by-the-control-plane",
	} {
		if err := d.saveSecret(name, value, false); err != nil {
			t.Fatal(err)
		}
	}
	app := withPersonsApp(t, d)
	d.offerWaitingAgentSecrets()
	offers := app.offered()
	if len(offers) != 1 || offers[0].Name != "OLD_TOKEN" || app.open(t, offers[0]) != "old-agent-save-1234" {
		t.Fatalf("%+v", offers)
	}
	if s := readSealedSecrets(home).Secrets["OLD_TOKEN"]; s.From != sealedFromAgent || s.Offer == "" {
		t.Fatalf("%+v", s)
	}
	// Offered once: the next delivery does not offer it again.
	d.offerWaitingAgentSecrets()
	if n := len(app.offered()); n != 1 {
		t.Fatalf("%d offers", n)
	}
}

// On a control plane older than #5308, nothing is offered and the secret stays here.
func TestNoOfferWithoutTheControlPlanesCapability(t *testing.T) {
	t.Parallel()

	root, home := t.TempDir(), t.TempDir()
	personalHomeFor(t, home)
	machineKeys(t, root)
	d, _ := start(t, root)
	defer d.Shutdown()
	app := withPersonsApp(t, d)
	d.SetControlCapabilities(func() []string { return nil })
	d.offerAgentSecret("", "GITLAB_TOKEN", "glpat-x", false)
	if len(app.asked) != 0 {
		t.Fatalf("%v", app.asked)
	}
}
