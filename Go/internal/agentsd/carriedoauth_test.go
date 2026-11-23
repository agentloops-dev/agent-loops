package agentsd

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A host says it can keep an OAuth connection, starts one with an address to open, keeps no
// verifier in the answer, and revokes a connection at the provider when it forgets its
// provider (#6072).
func TestACarriedOAuthConnectionStartsAndIsRevokedWhenForgotten(t *testing.T) {
	t.Parallel()

	d, _ := carriedDaemon(t)
	d.handlers = map[string]Handler{}
	if !slices.Contains(d.Capabilities(), wire.CapProvidersCarry3) {
		t.Fatalf("%v", d.Capabilities())
	}
	var mu sync.Mutex
	var revoked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		revoked = append(revoked, r.URL.Path+" "+r.Form.Get("token"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	c := d.carried()
	flow := c.oauth.Flows["digitalocean-api"]
	flow.Revoke = server.URL + "/revoke"
	c.oauth.HTTP = server.Client()

	out := ask(t, d, wire.CallProvidersOauth.Name, wire.ProviderOAuthCarry{Op: "start", Preset: "digitalocean-api"})
	if out.Error != nil || !strings.Contains(string(out.Result), "code_challenge=") ||
		strings.Contains(string(out.Result), "verifier") {
		t.Fatalf("%s %v", out.Result, out.Error)
	}
	// A finish whose state this machine never made.
	provider := wire.ProviderID("p1")
	state, code := "not-ours", "c"
	out = ask(t, d, wire.CallProvidersOauth.Name, wire.ProviderOAuthCarry{Op: "finish", Preset: "digitalocean-api",
		Provider: &provider, State: &state, Code: &code})
	if out.Code == nil || *out.Code != "expired" {
		t.Fatalf("%+v", out)
	}

	// As a finished connection keeps it.
	if err := c.store.Set("p1", map[string]string{"DIGITALOCEAN_API_TOKEN": "access-1",
		"DIGITALOCEAN_OAUTH_REFRESH_TOKEN": "refresh-1", "DIGITALOCEAN_OAUTH_EXPIRES_AT": "2999-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	// A pasted token over it keeps no refresh token, so it is never refreshed over.
	if err := c.store.Set("p2", map[string]string{"DIGITALOCEAN_OAUTH_REFRESH_TOKEN": "refresh-2"}); err != nil {
		t.Fatal(err)
	}
	if out := ask(t, d, wire.CallProvidersKeepSecrets.Name, wire.ProviderSecretsKeep{Provider: "p2",
		Secrets: map[string]string{"DIGITALOCEAN_API_TOKEN": "dop_v1_pasted"}}); out.Error != nil { //nolint:gosec // G101: a fake token
		t.Fatal(*out.Error)
	}
	if kept := c.store.Values("p2"); len(kept) != 1 || kept["DIGITALOCEAN_API_TOKEN"] != "dop_v1_pasted" {
		t.Fatalf("%v", kept)
	}

	if out := ask(t, d, wire.CallProvidersForget.Name, wire.ProviderForget{Provider: "p1"}); out.Error != nil {
		t.Fatal(*out.Error)
	}
	if out := ask(t, d, wire.CallProvidersForget.Name, wire.ProviderForget{Provider: "p2"}); out.Error != nil {
		t.Fatal(*out.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(revoked, []string{"/revoke refresh-1", "/revoke access-1"}) {
		t.Fatalf("revoked %v", revoked)
	}
	if len(c.store.Values("p1")) != 0 {
		t.Fatal("the secrets stayed")
	}
}
