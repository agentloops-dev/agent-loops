package agentsd

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// secrets/localList (#5122): the secrets this machine keeps for itself, for Settings ▸ You ▸
// Secrets ▸ On This Machine. An agent saved them (ask_secret, capture_secret,
// generate_secret) or a connector's form set them, so they are in sealed.json (or a
// secrets.env not moved yet) only: not the person's (Settings ▸ You ▸ Secrets, which the
// control plane lists) and not a session's own (secrets/sessionList). Without this list a
// person could not see them, nor turn Ask before use off with secrets/setNeedsApproval, nor
// remove one with secrets/localRemove (#6335). Names only, never a value.
//
// It names every secret that list_secrets says is on this machine and is not the person's
// (#6335): an agent must never use a secret that the person cannot find.

// localSecretList is every secret in this machine's store that is not the person's, by name.
func (d *Daemon) localSecretList() (wire.LocalSecrets, error) {
	out := wire.LocalSecrets{Secrets: []wire.LocalSecret{}}
	home := d.personalHome()
	if home == "" {
		return out, nil
	}
	user := d.personsSecretNames()
	descriptions := readSecretDescriptions(home)
	row := func(name string, setAt wire.Date, ask bool) wire.LocalSecret {
		r := wire.LocalSecret{Name: name, SetAt: setAt, BackedUp: new(false)}
		if ask {
			r.NeedsApproval = new(true)
		}
		if about := descriptions[name]; about != "" {
			r.Description = new(about)
		}
		if op, ok := d.secretLastOp(name); ok {
			r.LastOp = &wire.SecretLastOp{What: op.What, At: op.At, Agent: op.Agent}
		}
		return r
	}
	f, err := readSealedSecrets(home)
	if err != nil {
		// An empty list here would say this machine keeps no secret of its own (#6486).
		return wire.LocalSecrets{}, Invalid("The secrets on this machine cannot be listed: %s.", err)
	}
	sealed := f.Secrets
	for name, s := range sealed {
		if s.From == sealedFromUser || user[name] {
			continue
		}
		out.Secrets = append(out.Secrets, row(name, s.SetAt, !s.NoAsk))
	}
	// A secrets.env not moved into sealed.json yet (MoveSecretsFile): list_secrets names it too.
	for _, name := range skillcatalog.LoadSecrets(skillcatalog.SecretsPath(home)).Names() {
		if _, ok := sealed[name]; ok || user[name] {
			continue
		}
		out.Secrets = append(out.Secrets, row(name, 0, false))
	}
	sort.Slice(out.Secrets, func(i, j int) bool { return out.Secrets[i].Name < out.Secrets[j].Name })
	// A key that does not read stops every secret here, the person's too (#6632): the page says
	// so once, with what to do, in place of a line in the log at each delivery.
	if len(sealed) > 0 || len(d.personsSecretNames()) > 0 {
		if problem := d.sealingKeyProblem(); problem != "" {
			out.KeyProblem = &problem
		}
	}
	return out, nil
}

// personsSecretNames is the names of the person's secrets that the last sync sealed here.
func (d *Daemon) personsSecretNames() map[string]bool {
	d.userScopeMu.Lock()
	mark := d.userMarkLocked()
	d.userScopeMu.Unlock()
	user := map[string]bool{}
	for _, n := range mark.Secrets {
		user[n] = true
	}
	return user
}

// errPersonsSecret refuses secrets/localRemove for one of the person's: it is on every
// machine of theirs, and Remove under Yours takes it out everywhere.
var errPersonsSecret = errors.New("it is one of your secrets: remove it under Yours, which takes it out on every machine")

// removeLocalSecret is secrets/localRemove (#6335): one secret this machine keeps for itself
// goes from sealed.json, any secrets.env not moved yet, and descriptions.json. Agents here
// can no longer use it.
func (d *Daemon) removeLocalSecret(name string) error {
	home := d.personalHome()
	if home == "" {
		return errNoPersonalHome
	}
	if !secretAskName.MatchString(name) {
		return Invalid("A secret's name is letters, digits and underscores, e.g. GITLAB_PASSWORD.")
	}
	if d.personsSecretNames()[name] {
		return Invalid("%s was not removed: %s.", name, errPersonsSecret)
	}
	d.sealedSecretsMu.Lock()
	f, err := readSealedSecrets(home)
	s, inStore := f.Secrets[name]
	switch {
	case err != nil:
		err = Invalid("%s was not removed: %s.", name, err)
	case inStore && s.From == sealedFromUser:
		err = Invalid("%s was not removed: %s.", name, errPersonsSecret)
	case inStore:
		delete(f.Secrets, name)
		if writeSealedSecrets(home, f) != nil {
			err = errors.New("sealed.json could not be written")
		}
	}
	d.sealedSecretsMu.Unlock()
	if err != nil {
		return err
	}
	inEnv := skillcatalog.LoadSecrets(skillcatalog.SecretsPath(home)).IsSet(name)
	if !inStore && !inEnv {
		return Invalid("%s is not on this machine.", name)
	}
	if err := removePlainSecrets(home, name); err != nil {
		return err
	}
	if err := setSecretDescription(home, name, ""); err != nil {
		return err
	}
	d.secretChanged("", name, wire.SecretOperationWhatRemoved, true)
	d.mcpChanged()
	d.goJob("tellSecretsHeld", func() { d.tellSecretsHeld() })
	return nil
}

// Only the person's own clients reach these (Role.Allows): an agent's connection has none.
func (d *Daemon) registerLocalSecrets() {
	On(d, wire.CallSecretsLocalList, func(_ context.Context, _ *Client, _ wire.Empty) (wire.LocalSecrets, error) {
		return d.localSecretList()
	})
	On(d, wire.CallSecretsLocalRemove, func(_ context.Context, _ *Client, in wire.LocalSecretRemoveRequest) (wire.LocalSecrets, error) {
		if err := d.removeLocalSecret(strings.TrimSpace(in.Name)); err != nil {
			return wire.LocalSecrets{}, err
		}
		return d.localSecretList()
	})
}
