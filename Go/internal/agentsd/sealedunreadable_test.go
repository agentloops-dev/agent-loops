package agentsd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// secretLines is list_secrets' lines, for a test; a sealed.json that does not read fails it.
func secretLines(t *testing.T, d *Daemon) []secretListLine {
	t.Helper()
	lines, err := d.secretList("")
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// localRows is secrets/localList, for a test.
func localRows(t *testing.T, d *Daemon) wire.LocalSecrets {
	t.Helper()
	rows, err := d.localSecretList()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// badSealedSecrets is a sealed.json in a scratch home with a torn write: there, and not JSON.
var badSealedSecrets = []byte(`{"v":1,"secrets":{"GITLAB_TOKEN":{"sealed":"AAAA`)

func writeBadSealedSecrets(t *testing.T, home string) {
	t.Helper()
	p := sealedSecretsPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, badSealedSecrets, 0o600); err != nil {
		t.Fatal(err)
	}
}

// sealedSecretsUnchanged fails when sealed.json is not the bad file it was.
func sealedSecretsUnchanged(t *testing.T, home string) {
	t.Helper()
	b, err := os.ReadFile(sealedSecretsPath(home))
	if err != nil || !bytes.Equal(b, badSealedSecrets) {
		t.Fatalf("sealed.json was written over: %q, %v", b, err)
	}
}

// #6486: a sealed.json that is not there reads as empty; one that is there and does not
// decode is errSealedUnreadable, and says where it is.
func TestReadSealedSecretsTellsUnreadableFromMissing(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	f, err := readSealedSecrets(home)
	if err != nil || len(f.Secrets) != 0 {
		t.Fatalf("not there: %+v, %v", f, err)
	}
	writeBadSealedSecrets(t, home)
	f, err = readSealedSecrets(home)
	if !errors.Is(err, errSealedUnreadable) || len(f.Secrets) != 0 {
		t.Fatalf("unreadable: %+v, %v", f, err)
	}
	if !strings.Contains(err.Error(), sealedSecretsPath(home)) || !strings.Contains(err.Error(), "not changed") {
		t.Fatalf("the error does not say where or what: %v", err)
	}
	if len(sealedSecretsHere(home)) != 0 {
		t.Fatal("a look at an unreadable file found secrets")
	}
}

// #6486: no writer of sealed.json writes over one that does not decode. Each one says why.
func TestSealedSecretsWritersRefuseAnUnreadableFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	writeBadSealedSecrets(t, home)

	refused := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, errSealedUnreadable) && (err == nil || !strings.Contains(err.Error(), errSealedUnreadable.Error())) {
			t.Fatalf("%s: %v", what, err)
		}
		sealedSecretsUnchanged(t, home)
	}
	refused("saveSealedSecret", d.saveSealedSecret(home, "NEW_TOKEN", "scratch-6486", "", true))
	refused("saveAgentSecret", d.saveAgentSecret("", "AGENT_TOKEN", "scratch-6486", false))
	refused("setPlainSecrets", d.setPlainSecrets(home, map[string]string{"PLAIN_TOKEN": "scratch-6486"}, ""))
	refused("forgetPlainSecret", d.forgetPlainSecret(home, "GITLAB_TOKEN"))
	_, err := d.syncUserSecrets(home, []wire.UserSecret{{Name: "PERSONS_TOKEN", Value: "scratch-6486"}}, nil, nil, nil, nil, nil)
	refused("syncUserSecrets", err)
	refused("removeLocalSecret (localsecrets.go)", d.removeLocalSecret("GITLAB_TOKEN"))
	refused("changeGrant (secretuse.go)", d.changeGrant("GITLAB_TOKEN", wire.SecretUseFill, "example.com", true))

	// agentsecrets.go: an offer's id is not kept, and the file stays.
	d.noteOffer("GITLAB_TOKEN", "offer-6486")
	sealedSecretsUnchanged(t, home)

	// The move of a secrets.env at a start leaves both files as they are.
	env := skillcatalog.SecretsPath(home)
	if err := os.WriteFile(env, []byte("ENV_TOKEN=scratch-6486\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = d.moveSecretsFile(home, env)
	refused("moveSecretsFile", err)
	if _, err := os.Stat(env); err != nil {
		t.Fatalf("secrets.env went with sealed.json unreadable: %v", err)
	}
}

// #6486: the readers say that sealed.json is unreadable. They do not show an empty list.
func TestSealedSecretsReadersSayTheFileIsUnreadable(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	writeBadSealedSecrets(t, home)

	// secrets/localList (localsecrets.go).
	if msg := c.callErr(t, 1, wire.CallSecretsLocalList.Name, map[string]any{}); !strings.Contains(msg, errSealedUnreadable.Error()) {
		t.Fatalf("secrets/localList: %s", msg)
	}
	// list_secrets (secretlist.go).
	note, err := d.listSecrets("", map[string]json.RawMessage{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, errSealedUnreadable.Error()) || strings.Contains(note, "There are no secrets") {
		t.Fatalf("list_secrets: %s", note)
	}
	// secretuse.go: a grant in a file that does not read does not count, so the person is asked.
	if d.secretGrants().Grants == nil || len(d.secretGrants().Grants) != 0 {
		t.Fatalf("grants: %+v", d.secretGrants())
	}
	sealedSecretsUnchanged(t, home)
}
