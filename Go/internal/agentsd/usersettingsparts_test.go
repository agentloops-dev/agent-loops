package agentsd

import (
	"encoding/json"
	"os"
	"os/user"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The parts person, auto and retention (#5573): the Mac sets each once at the control plane,
// and each machine keeps it as its own call would have.
func TestPersonAutoAndRetentionPartsAreKept(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	personalHomeFor(t, t.TempDir())
	d, _ := start(t, root)
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()

	name, pronouns := "Sam", "they/them"
	sent := wire.MachineUserSettings{User: "sam", Secrets: []wire.SealedUserSecret{}, Settings: wire.UserSettings{
		Person:    mustJSON(wire.PersonSettings{Name: &name, Pronouns: &pronouns}),
		Auto:      json.RawMessage(`{"order":["codex","claude","codex"],"excluded":["grok"],"overrides":[]}`),
		Retention: mustJSON(wire.RetentionSettings{KeepFor: wire.RetentionKeepForDays7}),
	}}
	if err := d.ApplyUserSettings(sent); err != nil {
		t.Fatal(err)
	}
	if p := readPerson(d.personFile()); p.Name == nil || *p.Name != "Sam" || p.Pronouns == nil || *p.Pronouns != "they/them" {
		t.Fatalf("person %+v", p)
	}
	if a := d.autoSettings(); strings.Join(a.Order, ",") != "codex,claude" || strings.Join(a.Excluded, ",") != "grok" {
		t.Fatalf("auto %+v", a)
	}
	if r := d.retentionState().Settings.KeepFor; r != wire.RetentionKeepForDays7 {
		t.Fatalf("retention %v", r)
	}
	// The same parts again change nothing: no file is written again.
	before, _ := os.Stat(d.personFile())
	if err := d.ApplyUserSettings(sent); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(d.personFile()); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("person.json written again")
	}
}

// The Mac sends the name it resolved. A machine whose own settings give that name already, as
// the Mac's own host does with no name set, keeps its settings: the account's name stays its rule.
func TestPersonPartLeavesTheSameNameAsItIs(t *testing.T) {
	t.Parallel()

	u, err := user.Current()
	first := ""
	if err == nil && len(strings.Fields(u.Name)) > 0 {
		first = strings.Fields(u.Name)[0]
	}
	if first == "" {
		t.Skip("this account has no full name")
	}
	root := t.TempDir()
	personalHomeFor(t, t.TempDir())
	d, _ := start(t, root)
	defer d.Shutdown()
	d.SettingsDir = t.TempDir()

	if err := d.applyPersonPart(mustJSON(wire.PersonSettings{Name: &first})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.personFile()); !os.IsNotExist(err) {
		t.Fatalf("person.json written for the same name: %v", err)
	}
}
