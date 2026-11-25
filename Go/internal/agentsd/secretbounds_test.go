package agentsd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func refusedByLimit(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("not refused: %v", err)
	}
	for _, not := range []string{"ONE_MORE", "SCRATCH_", "scratch-value"} {
		if strings.Contains(err.Error(), not) {
			t.Fatal("a refusal holds a name or a value")
		}
	}
}

// #6738: the machine's own secrets and the person's are 200 each, of 16 KiB; over that a write is
// refused, a delivery leaves out what does not fit, and nothing goes.
func TestSealedSecretsAreBounded(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	many := map[string]string{}
	for i := range secretsMost {
		many[fmt.Sprintf("SCRATCH_%d", i)] = "scratch-value"
	}
	must(t, d.setPlainSecrets(home, many, ""))
	refusedByLimit(t, d.saveSealedSecret(home, "ONE_MORE", "scratch-value", "", false))
	refusedByLimit(t, d.setPlainSecrets(home, map[string]string{"ONE_MORE": "scratch-value"}, ""))
	refusedByLimit(t, d.saveSealedSecret(home, "SCRATCH_1", strings.Repeat("scratch-value", 2000), "", false))
	must(t, d.saveSealedSecret(home, "SCRATCH_1", strings.Repeat("v", secretValueMost), "", false))

	// The person's own 200 still come, the one over and the one too large do not.
	sent := []wire.UserSecret{{Name: "HUGE", Value: strings.Repeat("v", secretValueMost+1)}}
	for i := range secretsMost + 1 {
		sent = append(sent, wire.UserSecret{Name: wire.UserSecretName(fmt.Sprintf("SENT_%d", i)), Value: "v"})
	}
	names, err := d.syncUserSecrets(home, sent, nil, nil, nil, nil, nil)
	must(t, err)
	if held := sealedSecretsHere(home); len(names) != secretsMost || len(held) != 2*secretsMost {
		t.Fatalf("%d names, %d held", len(names), len(held))
	}
	// A value that grew too large keeps the one it replaces.
	_, err = d.syncUserSecrets(home, []wire.UserSecret{{Name: "SENT_1", Value: strings.Repeat("v", secretValueMost+1)}}, nil, names, nil, nil, nil)
	must(t, err)
	if v, ok := d.sealedLocalSecret(home, "SENT_1"); !ok || v != "v" {
		t.Fatal("a refused value took out the one it replaces")
	}

	must(t, d.forgetPlainSecret(home, "SCRATCH_2"))
	must(t, d.saveSealedSecret(home, "ONE_MORE", "scratch-value", "", false))
}

// #6739: a session's secrets.
func TestSessionSecretsAreBounded(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	a := seeded(t, d, t.TempDir(), nil)
	f := sessionSecretFile{Secrets: map[string]sessionSecretValue{}}
	for i := range secretsMost {
		f.Secrets[fmt.Sprintf("SCRATCH_%d", i)] = sessionSecretValue{Value: "v"}
	}
	must(t, d.writeSessionSecretsLocked(a.ID, f))
	refusedByLimit(t, d.setSessionSecret(a.ID, "ONE_MORE", "scratch-value", "", false))
	refusedByLimit(t, d.setSessionSecret(a.ID, "SCRATCH_1", strings.Repeat("scratch-value", 2000), "", false))
	must(t, d.setSessionSecret(a.ID, "SCRATCH_1", "v2", "", false))
	for i := range sessionExpiredMost {
		f.Expired = append(f.Expired, fmt.Sprintf("OLD_%d", i))
	}
	must(t, d.writeSessionSecretsLocked(a.ID, f))
	d.expireSessionSecrets(a.ID)
	if f := d.readSessionSecrets(a.ID); len(f.Secrets) != 0 || len(f.Expired) != sessionExpiredMost {
		t.Fatalf("%d and %d after the archive", len(f.Secrets), len(f.Expired))
	}

	for i := range sessionSecretFilesMost {
		must(t, os.WriteFile(filepath.Join(d.sessionSecretsDir(), fmt.Sprintf("x%d.json", i)), []byte("{}"), 0o600))
	}
	b := seeded(t, d, t.TempDir(), nil)
	refusedByLimit(t, d.setSessionSecret(b.ID, "ONE_MORE", "scratch-value", "", false))
}

// #6741, #6742: the hints and the descriptions.
func TestSecretHintsAndDescriptionsAreBounded(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	hints, names := map[string]secretHintRecord{}, []string{}
	for i := range secretsMost {
		n := fmt.Sprintf("SCRATCH_%d", i)
		names = append(names, n)
		hints[n] = secretHintRecord{Hint: "aaaa"}
	}
	writeSecretHints(home, hints, names)
	hints["ONE_MORE"] = secretHintRecord{Hint: "aaaa"}
	refusedByLimit(t, hintsFile.Write(secretHintsPath(home), hints))
	must(t, os.WriteFile(secretHintsPath(home), []byte("{"), 0o600))
	writeSecretHints(home, hints, names)
	if len(readSecretHints(home)) != secretsMost {
		t.Fatal("unreadable hints did not start afresh")
	}

	all := map[string]string{}
	for i := range secretDescriptionsMost {
		all[fmt.Sprintf("SCRATCH_%d", i)] = strings.Repeat("<", 300)
	}
	must(t, descriptionsFile.Write(secretDescriptionsPath(home), all))
	refusedByLimit(t, setSecretDescription(home, "ONE_MORE", "what it is"))
	if err := setSecretDescription(home, "SCRATCH_1", strings.Repeat("x", 4096)); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("%v", err)
	}
	must(t, setSecretDescription(home, "SCRATCH_1", ""))
	must(t, setSecretDescription(home, "ONE_MORE", "what it is"))
	dropSecretDescriptions(home, []string{"ONE_MORE"})
	if readSecretDescriptions(home)["ONE_MORE"] != "" {
		t.Fatal("a description stayed after its secret")
	}
}
