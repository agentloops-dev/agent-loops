package agentsd

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/usersettings"
	"github.com/agentloops-dev/agent-loops/Go/internal/sizewords"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The limits of the person's secret files (#6738, #6739, #6741, #6742). A write over one is
// refused, nothing is evicted, and no name or value is in the words of a refusal.

const (
	// The control plane's limits for a person, for what it sends and again for this machine's own.
	secretsMost     = usersettings.MaxSecrets
	secretValueMost = usersettings.MaxSecret
	// A sealed value with its grants. On Linux the release layer is base64 twice.
	secretRecordBytes = 64 << 10
	// A name and 300 characters, escaped.
	secretDescriptionBytes = 2 << 10
	secretDescriptionsMost = 2 * secretsMost
	secretHintBytes        = 256
	sessionSecretFilesMost = 1000
	sessionExpiredMost     = 2 * secretsMost
	removeSecretFirst      = "remove a secret that is not in use, then try again"
)

// checkSecretValue refuses a value that is too large to keep.
func checkSecretValue(value string) error {
	if len(value) > secretValueMost {
		return store.LimitError{Words: fmt.Sprintf("a secret's value can be at most %s; use a shorter value", sizewords.Int(secretValueMost))}
	}
	return nil
}

func sealedFrom(user bool) func(sealedSecretsFile) map[string]any {
	return func(f sealedSecretsFile) map[string]any {
		out := map[string]any{}
		for n, s := range f.Secrets {
			if (s.From == sealedFromUser) == user {
				out[n] = s
			}
		}
		return out
	}
}

// sealedFile is sealed.json: what the control plane sends never takes the room of this machine's
// own secrets, or the reverse.
//
// store-bounds: kind=secret; count=400 (200 of the person's, 200 of this machine's own); size=64KiB a record; age=until-deleted; applied=on write, refused
var sealedFile = store.SecretFile[sealedSecretsFile]{Name: "sealed.json", Parts: []store.SecretPart[sealedSecretsFile]{
	{What: "secrets of its own", MaxRecordBytes: secretRecordBytes, MaxCount: secretsMost, Remedy: removeSecretFirst, Entries: sealedFrom(false)},
	{What: "secrets from Settings ▸ You ▸ Secrets", MaxRecordBytes: secretRecordBytes, MaxCount: secretsMost, Remedy: removeSecretFirst, Entries: sealedFrom(true)},
}}

// sessionFile is one session's secrets.
//
// store-bounds: kind=secret; count=1000 sessions, 200 secrets and 400 expired names each; size=64KiB a record; age=until-deleted; applied=on write, refused; the values go on archive and the file on delete
var sessionFile = store.SecretFile[sessionSecretFile]{Name: "a session's secrets", Parts: []store.SecretPart[sessionSecretFile]{
	{What: "secrets for one session", MaxRecordBytes: secretRecordBytes, MaxCount: secretsMost, Remedy: removeSecretFirst,
		Entries: func(f sessionSecretFile) map[string]any { return store.Entries(f.Secrets) }},
	// The oldest expired names go before a write (writeSessionSecretsLocked), so the count
	// refuses nothing; a name too long to keep is the refusal the person can see.
	{What: "expired names for one session", MaxRecordBytes: secretHintBytes, MaxCount: sessionExpiredMost, Remedy: "start a new session for more",
		Entries: func(f sessionSecretFile) map[string]any {
			out := map[string]any{}
			for _, n := range f.Expired {
				out[n] = nil
			}
			return out
		}},
}}

// checkSessionRoom refuses the first secret of one more session than the machine keeps.
func checkSessionRoom(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) >= sessionSecretFilesMost {
		return store.LimitError{Words: fmt.Sprintf("this machine keeps secrets for at most %d sessions; delete an archived session that had one first", sessionSecretFilesMost)}
	}
	return nil
}

// hintsFile is hints.json.
//
// store-bounds: kind=secret; count=200; size=256B a record; age=until-deleted; applied=on write, refused; a hint goes with its secret
var hintsFile = store.SecretFile[map[string]secretHintRecord]{Name: "hints.json", Parts: []store.SecretPart[map[string]secretHintRecord]{
	{What: "hints", MaxRecordBytes: secretHintBytes, MaxCount: secretsMost, Remedy: removeSecretFirst, Entries: store.Entries[secretHintRecord]},
}}

// descriptionsFile is descriptions.json. It stays beside the secrets and not in ~/.agents, which
// people sync and commit (Alex, 2026-11-24, #6742).
//
// store-bounds: kind=key-value; count=400; size=2KiB a record; age=until-deleted; applied=on write, refused; a description goes with its secret
var descriptionsFile = store.SecretFile[map[string]string]{Name: "descriptions.json", Parts: []store.SecretPart[map[string]string]{
	{What: "descriptions of secrets", MaxRecordBytes: secretDescriptionBytes, MaxCount: secretDescriptionsMost, Remedy: removeSecretFirst, Entries: store.Entries[string]},
}}

// dropSecretDescriptions takes out the descriptions of secrets that went from this machine.
func dropSecretDescriptions(home string, names []string) {
	all, err := descriptionsFile.Read(secretDescriptionsPath(home))
	if err != nil || len(all) == 0 {
		return
	}
	n := len(all)
	for _, name := range names {
		if !secretIsSet(home, name) {
			delete(all, name)
		}
	}
	if len(all) == n {
		return
	}
	if err := descriptionsFile.Write(secretDescriptionsPath(home), all); err != nil {
		log.Printf("descriptions.json: could not save: %v", err)
	}
}

// syncedSecrets is what one delivery of the person's secrets may write: a value too large, and a
// new name over the count, are left out and said once, so one does not stop the others.
type syncedSecrets struct {
	had   map[string]sealedValue
	count int
	left  int
}

// takes is whether s is kept. A name left out keeps the record it had.
func (t *syncedSecrets) takes(s wire.UserSecret) bool {
	_, held := t.had[string(s.Name)]
	if checkSecretValue(s.Value) != nil || !held && t.count >= secretsMost {
		t.left++
		return false
	}
	t.count++
	return true
}

func (t *syncedSecrets) said() {
	if t.left > 0 {
		log.Printf("sealed secrets: %d of the person's were not kept here: at most %d, each of at most %d KiB", t.left, secretsMost, secretValueMost>>10)
	}
}
