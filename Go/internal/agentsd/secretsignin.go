package agentsd

import (
	"context"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Step 7 of a machine's set-up (#5361, epic #5353): a runtime signs in with a key the person
// has in Secrets, written on the machine by the person's own client. The Mac's agentsd is
// where Secrets are, so the client asks it for the key (secrets/signInKey) and gives it to
// the machine's own daemon (runtimes/signInKey). The names are the runtime-sign-in table of
// the spec (specs/5353-machine-set-up/runtime-sign-in.md).
//
// A secret with Ask before use is never given (#4013's rule: a flagged value goes into no
// process on the person's say-so alone), and the answer names it so the step can say so. A
// read is recorded as a use of a command, at the place "a machine's sign-in".

// signInSecretNames are the secrets step 7 looks for, per runtime, in order.
var signInSecretNames = map[string][]string{
	"claude":   {"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
	"codex":    {"OPENAI_API_KEY"},
	"copilot":  {"COPILOT_GITHUB_TOKEN"},
	"cursor":   {"CURSOR_API_KEY"},
	"grok":     {"XAI_API_KEY"},
	"opencode": {"OPENROUTER_API_KEY"},
}

// signInKeyPlace is the place a read for a machine's sign-in is recorded at.
const signInKeyPlace = "a machine's sign-in"

func (d *Daemon) registerSecretSignIn() {
	On(d, wire.CallSecretsSignInKey, func(_ context.Context, _ *Client, r wire.SecretSignInKeyRequest) (wire.SecretSignInKey, error) {
		return d.signInKey(strings.TrimSpace(r.RuntimeID))
	})
}

func (d *Daemon) signInKey(runtimeID string) (wire.SecretSignInKey, error) {
	names, ok := signInSecretNames[runtimeID]
	if !ok {
		return wire.SecretSignInKey{}, Invalid("There is no runtime called %q.", runtimeID)
	}
	out := wire.SecretSignInKey{Looked: append([]string{}, names...), Refused: []string{}}
	home := d.personalHome()
	if home == "" {
		return out, nil
	}
	for _, name := range names {
		if !secretIsSet(home, name) {
			continue
		}
		if isSealedSecret(home, name) {
			out.Refused = append(out.Refused, name)
			continue
		}
		v, ok := d.sealedLocalSecret(home, name)
		if !ok {
			v, ok = skillcatalog.LoadSecrets(skillcatalog.SecretsPath(home)).Value(name)
		}
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		d.secretRead("", name, wire.SecretOperationUseCommand, signInKeyPlace)
		out.Name, out.Value = &name, &v
		return out, nil
	}
	return out, nil
}
