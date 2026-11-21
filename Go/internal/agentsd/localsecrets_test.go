package agentsd

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #5122: a secret an agent saved on this machine (here capture_secret's save) is in
// secrets/localList with Ask before use on, a person turns it off there, and the person's
// own secrets and every value stay out of the list.
func TestLocalSecretsListsWhatAnAgentSaved(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	personalHomeFor(t, home)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	const value = "ghp_0123456789abcdefghijklmnop"
	if err := d.saveSecret("GITHUB_SAM_TOKEN", value, true); err != nil {
		t.Fatal(err)
	}
	if err := setSecretDescription(home, "GITHUB_SAM_TOKEN", "The demo account's classic token."); err != nil {
		t.Fatal(err)
	}
	// A secret of the person's (Settings ▸ You ▸ Secrets), as the sync keeps it.
	if err := d.saveSealedSecret(home, "PERSONS_TOKEN", "the-persons-value", sealedFromUser, true); err != nil {
		t.Fatal(err)
	}

	list := func(id int) wire.LocalSecrets {
		t.Helper()
		raw := c.call(t, id, wire.CallSecretsLocalList.Name, map[string]any{})
		if strings.Contains(string(raw), value) || strings.Contains(string(raw), "the-persons-value") {
			t.Fatalf("a value is in the list: %s", raw)
		}
		var out wire.LocalSecrets
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := list(1)
	if len(got.Secrets) != 1 {
		t.Fatalf("%+v", got)
	}
	row := got.Secrets[0]
	if row.Name != "GITHUB_SAM_TOKEN" || row.NeedsApproval == nil || !*row.NeedsApproval ||
		row.Description == nil || *row.Description != "The demo account's classic token." ||
		row.BackedUp == nil || *row.BackedUp || row.SetAt == 0 {
		t.Fatalf("%+v", row)
	}

	c.call(t, 2, wire.CallSecretsSetNeedsApproval.Name, map[string]any{"name": "GITHUB_SAM_TOKEN", "needsApproval": false})
	if after := list(3); len(after.Secrets) != 1 || after.Secrets[0].NeedsApproval != nil {
		t.Fatalf("Ask before use is still on: %+v", after)
	}
	if v, ok := d.plainSecrets(home).Value("GITHUB_SAM_TOKEN"); !ok || v != value {
		t.Fatal("the secret does not read without Ask before use")
	}
	if RoleAgent.Allows(wire.CallSecretsLocalList.Name) || RoleStranger.Allows(wire.CallSecretsLocalList.Name) {
		t.Fatal("an agent's or a stranger's connection may list this machine's secrets")
	}
}

// #6335: a secret capture_secret saved with Ask before use is in secrets/localList, and the
// person removes it there; agents here can no longer use it. One of the person's is refused:
// it goes with Remove under Yours.
func TestLocalSecretsRemoveTakesAnAgentsSaveOut(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	personalHomeFor(t, home)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	const value = "scratch-6335-not-a-real-token"
	if err := d.saveAgentSecret("", "AZURE_DEVOPS_TOKEN", value, true); err != nil {
		t.Fatal(err)
	}
	if err := setSecretDescription(home, "AZURE_DEVOPS_TOKEN", "A scratch PAT."); err != nil {
		t.Fatal(err)
	}
	if err := d.saveSealedSecret(home, "PERSONS_TOKEN", "the-persons-value", sealedFromUser, true); err != nil {
		t.Fatal(err)
	}
	names := func(l wire.LocalSecrets) []string {
		var out []string
		for _, s := range l.Secrets {
			out = append(out, s.Name)
		}
		return out
	}
	// Every name list_secrets says is here and not the person's is in the list.
	for _, l := range secretLines(t, d) {
		if l.HereToo && l.Name != "PERSONS_TOKEN" && !slices.Contains(names(localRows(t, d)), l.Name) {
			t.Fatalf("list_secrets gives %s, but On This Machine does not", l.Name)
		}
	}

	raw := c.call(t, 1, wire.CallSecretsLocalRemove.Name, map[string]any{"name": "AZURE_DEVOPS_TOKEN"})
	var after wire.LocalSecrets
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Secrets) != 0 {
		t.Fatalf("still listed: %s", raw)
	}
	if _, ok := sealedSecretsHere(home)["AZURE_DEVOPS_TOKEN"]; ok {
		t.Fatal("still in sealed.json")
	}
	if _, ok := readSecretDescriptions(home)["AZURE_DEVOPS_TOKEN"]; ok {
		t.Fatal("its description stayed")
	}
	for _, l := range secretLines(t, d) {
		if l.Name == "AZURE_DEVOPS_TOKEN" {
			t.Fatal("list_secrets still gives it")
		}
	}

	if msg := c.callErr(t, 2, wire.CallSecretsLocalRemove.Name, map[string]any{"name": "PERSONS_TOKEN"}); !strings.Contains(msg, "remove it under Yours") {
		t.Fatalf("the person's secret: %s", msg)
	}
	if _, ok := sealedSecretsHere(home)["PERSONS_TOKEN"]; !ok {
		t.Fatal("the person's secret went")
	}
	if msg := c.callErr(t, 3, wire.CallSecretsLocalRemove.Name, map[string]any{"name": "NOT_HERE"}); !strings.Contains(msg, "not on this machine") {
		t.Fatalf("a name that is not here: %s", msg)
	}
	if RoleAgent.Allows(wire.CallSecretsLocalRemove.Name) || RoleStranger.Allows(wire.CallSecretsLocalRemove.Name) {
		t.Fatal("an agent's or a stranger's connection may remove this machine's secrets")
	}
}
