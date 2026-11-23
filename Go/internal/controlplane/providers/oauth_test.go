package providers

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// An OAuth connection is started and finished on the machine that will carry the
// provider; the control plane routes the two steps, keeps neither the verifier nor a token,
// and adds the provider with the account the machine says (#6072).
func TestAnOAuthConnectionIsMadeOnItsMachine(t *testing.T) {
	h := newHarness(t)
	h.carry()
	var finished wire.ProviderOAuthCarry
	h.router.asking = func(host, method string, params json.RawMessage) json.RawMessage {
		if method != wire.CallProvidersOauth.Name {
			return rawOf(wire.ProviderCarried{})
		}
		var ask wire.ProviderOAuthCarry
		must(t, json.Unmarshal(params, &ask))
		if ask.Op == "start" {
			address, state := "https://cloud.digitalocean.com/v1/oauth/authorize?state=s1", "s1"
			return rawOf(wire.ProviderCarried{Result: rawOf(wire.ProviderOAuthCarried{URL: &address, State: &state})})
		}
		finished = ask
		account, team := "agents@example.com", "My Team"
		return rawOf(wire.ProviderCarried{Result: rawOf(wire.ProviderOAuthCarried{Account: &account, Team: &team,
			SecretNames: []string{"DIGITALOCEAN_API_TOKEN", "DIGITALOCEAN_OAUTH_REFRESH_TOKEN"}})})
	}
	out := h.must(MethodProvidersOAuthStart, map[string]any{"preset": "digitalocean-api"})
	started := out.(wire.ProviderOAuthStarted)
	if started.State != "s1" || string(started.Machine) != carrier || !strings.HasPrefix(started.URL, "https://cloud.digitalocean.com/") {
		t.Fatalf("%+v", started)
	}
	out = h.must(MethodProvidersOAuthFinish, map[string]any{"state": "s1", "code": "the-code"})
	summary := out.(ProviderSummary)
	if summary.Name != "DigitalOcean" || deref(summary.Preset) != "digitalocean-api" || summary.Oauth == nil ||
		deref(summary.Oauth.Account) != "agents@example.com" || deref(summary.Oauth.Team) != "My Team" ||
		summary.Oauth.State != "connected" || !summary.Secrets["DIGITALOCEAN_OAUTH_REFRESH_TOKEN"] {
		t.Fatalf("%+v", summary)
	}
	if finished.Op != "finish" || deref(finished.Code) != "the-code" || deref(finished.State) != "s1" ||
		finished.Provider == nil || string(*finished.Provider) != string(summary.ID) {
		t.Fatalf("the finish asked %+v", finished)
	}
	// No secret value went to the machine from here: it had them already.
	for _, a := range h.router.asks() {
		if a.method == wire.CallProvidersKeepSecrets.Name {
			t.Fatalf("secrets were sent: %s", a.params)
		}
		var carry wire.ProviderCarry
		if a.method == wire.CallProvidersCarry.Name && json.Unmarshal(a.params, &carry) == nil && len(carry.Secrets) > 0 {
			t.Fatalf("a call carried secrets: %s", a.params)
		}
	}
	// A state is spent once.
	_, err := h.call(MethodProvidersOAuthFinish, map[string]any{"state": "s1", "code": "the-code"})
	if reason(err) != "expired" {
		t.Fatalf("%v", err)
	}
}

func TestAnOAuthDenyAndAMachineTooOld(t *testing.T) {
	h := newHarness(t)
	h.carry()
	// DigitalOcean's Deny has no state.
	_, err := h.call(MethodProvidersOAuthFinish, map[string]any{"error": "access_denied"})
	if reason(err) != "denied" {
		t.Fatalf("%v", err)
	}
	// The machine's own reason comes through.
	h.router.asking = func(host, method string, params json.RawMessage) json.RawMessage {
		var ask wire.ProviderOAuthCarry
		_ = json.Unmarshal(params, &ask)
		if ask.Op == "start" {
			address, state := "https://example.com/a", "s2"
			return rawOf(wire.ProviderCarried{Result: rawOf(wire.ProviderOAuthCarried{URL: &address, State: &state})})
		}
		words, code := "DigitalOcean gave Agent Loops less than it needs.", "scope_missing"
		return rawOf(wire.ProviderCarried{Error: &words, Code: &code})
	}
	h.must(MethodProvidersOAuthStart, map[string]any{"preset": "digitalocean-api"})
	_, err = h.call(MethodProvidersOAuthFinish, map[string]any{"state": "s2", "code": "c"})
	if reason(err) != "scope_missing" {
		t.Fatalf("%v", err)
	}
	if len(h.providers.Summaries()) != 0 {
		t.Fatal("a provider was added")
	}
	// A preset with no sign-in, and a machine too old to keep one.
	if _, err := h.call(MethodProvidersOAuthStart, map[string]any{"preset": "aws-ec2-api"}); err == nil {
		t.Fatal("a preset with no flow started")
	}
	h.router.mu.Lock()
	if h.router.lacks == nil {
		h.router.lacks = map[string]bool{}
	}
	h.router.lacks[carrier+" "+wire.CapProvidersCarry3] = true
	h.router.mu.Unlock()
	if _, err := h.call(MethodProvidersOAuthStart, map[string]any{"preset": "digitalocean-api"}); err == nil ||
		!strings.Contains(err.Error(), "update Agent Loops") {
		t.Fatalf("%v", err)
	}
}

func TestAnOAuthConnectionSaysWhenItIsDisconnected(t *testing.T) {
	r := ProviderRecord{OAuth: &OAuthConnection{Account: "a@example.com"}}
	if s := summaryOAuth(r, listed{}); s == nil || s.State != "connected" {
		t.Fatalf("%+v", s)
	}
	if s := summaryOAuth(r, listed{code: "disconnected"}); s.State != "disconnected" {
		t.Fatalf("%+v", s)
	}
	if summaryOAuth(ProviderRecord{}, listed{}) != nil {
		t.Fatal("a pasted token is an OAuth connection")
	}
	// A pasted token replaces the connection's own secrets, but not the token's name.
	got := withoutOAuthNames([]string{"DIGITALOCEAN_API_TOKEN", "DIGITALOCEAN_OAUTH_REFRESH_TOKEN", "DIGITALOCEAN_OAUTH_SCOPE"})
	if !slices.Equal(got, []string{"DIGITALOCEAN_API_TOKEN"}) {
		t.Fatalf("%v", got)
	}
}

func reason(err error) string {
	var f *ProviderFailure
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}
