package agentsd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The secrets of the person's setup.sh (#7156, gate 1 of #7151, section 4). setup.sh names them
// in a `# secrets: GH_TOKEN NPM_TOKEN` line in the comment lines at its top, 20 at most. Before
// the run, agentsd:
//
//  1. refuses the run, and names the secret, when one is not the person's or has Ask before use:
//     nothing asks in the middle of a run;
//  2. waits, in the state needs-you, until each one is on this machine: a client of the person
//     seals a copy for it (#3397). The wait is not part of the run's time limit;
//  3. starts the script with agents-secret on its PATH and a grant whose subject is this run, not
//     a session: it gives only the declared names, and ends when the run ends (its process group
//     is killed then too);
//  4. records each read with the place setup.sh and the script's hash.
//
// Only a setup.sh that this machine took by the rules of setupdelivery.go runs, so only such a
// text gets a grant. A project's script gets none (#5353).

const (
	// setupSecretsMost is the most names one setup.sh can declare.
	setupSecretsMost = 20
	// setupSecretPlace is the place of a read by setup.sh, before its hash.
	setupSecretPlace = "setup.sh"
)

// setupSecretsPoll is how often the wait looks for the copies again. A test sets it shorter.
var setupSecretsPoll = 5 * time.Second

// scriptGrant is the grant of one run of setup.sh: the names it declared, and its text's hash.
type scriptGrant struct {
	names []string
	hash  string
}

// declaredSecrets is the names in the `# secrets:` lines of the comment lines at the top of text
// (after a #! line, with blank lines between them), each once, in their order.
func declaredSecrets(text string) ([]string, error) {
	var names []string
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i == 0 && strings.HasPrefix(line, "#!") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		rest, ok := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(line, "#")), "secrets:")
		if !ok {
			continue
		}
		for _, n := range strings.Fields(rest) {
			if !secretNamePattern.MatchString(n) {
				return nil, fmt.Errorf("setup.sh's # secrets: line names %q, which is not a secret's name: "+
					"capital letters, digits and _, from a letter", n)
			}
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	if len(names) > setupSecretsMost {
		return nil, fmt.Errorf("setup.sh's # secrets: line names %d secrets; %d at most", len(names), setupSecretsMost)
	}
	return names, nil
}

// setupSecretsState is where names stand on this machine: problem names each one that cannot be
// given (not the person's, or with Ask before use), and waiting each one that is the person's
// but has no copy here yet.
func (d *Daemon) setupSecretsState(names []string) (problem string, waiting []string) {
	home := d.personalHome()
	if home == "" {
		if len(names) == 0 {
			return "", nil
		}
		return "This machine's user has no home for secrets, so setup.sh cannot read " + andList(names) + ".", nil
	}
	d.userScopeMu.Lock()
	mark := d.userMarkLocked()
	d.userScopeMu.Unlock()
	var asks, missing []string
	for _, n := range names {
		if isSealedSecret(home, n) {
			asks = append(asks, n)
			continue
		}
		if secretIsSet(home, n) {
			continue
		}
		i := slices.IndexFunc(mark.Waiting, func(a wire.UserSecretAbout) bool { return string(a.Name) == n })
		switch {
		case i >= 0 && mark.Waiting[i].NeedsApproval != nil && *mark.Waiting[i].NeedsApproval:
			asks = append(asks, n)
		case i >= 0, mark.User == "":
			// The person's and on its way; or this machine has not had the person's settings
			// yet, so it cannot say.
			waiting = append(waiting, n)
		default:
			missing = append(missing, n)
		}
	}
	var says []string
	if len(missing) > 0 {
		says = append(says, "setup.sh declares "+andList(missing)+", which "+isOrAre(missing, "is not one of your secrets", "are not among your secrets")+
			". Add "+itOrThem(missing)+" in Settings ▸ Secrets, or take "+itOrThem(missing)+" out of setup.sh's # secrets: line.")
	}
	if len(asks) > 0 {
		says = append(says, "setup.sh declares "+andList(asks)+", which "+isOrAre(asks, "has", "have")+
			" Ask before use: agentsd never puts such a value in a script. Turn Ask before use off for "+andList(asks)+
			" in Settings ▸ Secrets, or take "+itOrThem(asks)+" out of setup.sh's # secrets: line.")
	}
	return strings.Join(says, " "), waiting
}

// waitForSetupSecrets waits until each of names is on this machine, and says what it waits for
// with wait. It returns why the run cannot start, or ctx's error when the host stops.
func (d *Daemon) waitForSetupSecrets(ctx context.Context, names []string, wait func(words string)) error {
	said := ""
	for {
		problem, waiting := d.setupSecretsState(names)
		if problem != "" {
			return fmt.Errorf("%s", problem)
		}
		if len(waiting) == 0 {
			return nil
		}
		if words := "Open Agent Loops to send " + andList(waiting) + " to this machine."; words != said {
			said = words
			log.Printf("set-up: setup.sh waits for %s", andList(waiting))
			wait(words)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(setupSecretsPoll):
		}
	}
}

// setupSecretEnv is env with agents-secret first on PATH and a grant for one run of setup.sh of
// hash, which gives names only; and the function that ends the grant. With no names, or when
// agentsd cannot listen, it is env as it was.
func (d *Daemon) setupSecretEnv(env, names []string, hash string) ([]string, func(), error) {
	if len(names) == 0 {
		return env, func() {}, nil
	}
	self := d.credentialSelf
	if self == "" {
		var err error
		if self, err = os.Executable(); err != nil {
			return env, func() {}, fmt.Errorf("agents-secret: %w", err)
		}
	}
	port, err := d.startAppTools()
	if err != nil {
		return env, func() {}, fmt.Errorf("agents-secret could not listen: %w", err)
	}
	bin := filepath.Join(d.Store.Loc.Root, "runtimes", "secret")
	if err := writeSecretShim(bin, self); err != nil {
		return env, func() {}, fmt.Errorf("agents-secret could not be written: %w", err)
	}
	grant := string(store.NewUUID())
	d.credMu.Lock()
	if d.scriptGrants == nil {
		d.scriptGrants = map[string]scriptGrant{}
	}
	d.scriptGrants[grant] = scriptGrant{names: names, hash: hash}
	d.credMu.Unlock()
	end := func() {
		d.credMu.Lock()
		delete(d.scriptGrants, grant)
		d.credMu.Unlock()
	}
	env = without(env, secretURLEnv, secretGrantEnv)
	env = append(env, secretURLEnv+"=http://127.0.0.1:"+strconv.Itoa(port)+secretCommandPath, secretGrantEnv+"="+grant)
	return prependPath(env, bin), end, nil
}

// scriptGrantOf is the run of setup.sh whose grant asks r.
func (d *Daemon) scriptGrantOf(r *http.Request) (scriptGrant, bool) {
	grant := bearer(r.Header.Get("Authorization"))
	if grant == "" {
		return scriptGrant{}, false
	}
	d.credMu.Lock()
	defer d.credMu.Unlock()
	g, ok := d.scriptGrants[grant]
	return g, ok
}

// secretForScript is name's value for a run of setup.sh, with the read recorded; or the status
// and the words that say why it is not given.
func (d *Daemon) secretForScript(g scriptGrant, name string) (value string, status int, words string) {
	if name == "" {
		return "", http.StatusBadRequest, "give the secret's name: agents-secret NAME"
	}
	if !slices.Contains(g.names, name) {
		return "", http.StatusForbidden, "setup.sh does not declare " + name + ": add it to the # secrets: line at its top."
	}
	home := d.personalHome()
	if home != "" && isSealedSecret(home, name) {
		return "", http.StatusForbidden, name + " has Ask before use, so agentsd never puts its value in a script. " +
			"A person turns Ask before use off for " + name + " in Settings ▸ Secrets."
	}
	v, ok, _ := d.localSecret("", name)
	if !ok {
		return "", http.StatusNotFound, name + " is not on this machine."
	}
	log.Printf("agents-secret: %s read by setup.sh", name) //nolint:gosec // name is one the script declared, which match secretNamePattern
	d.secretRead("", name, wire.SecretOperationUseCommand, setupSecretPlace+" "+g.hash)
	return v, http.StatusOK, ""
}

func isOrAre(names []string, one, more string) string {
	if len(names) == 1 {
		return one
	}
	return more
}

func itOrThem(names []string) string { return isOrAre(names, "it", "them") }

// andList is "A", "A and B" or "A, B and C".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
