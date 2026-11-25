// Package provideroauth connects a machine provider with OAuth instead of a pasted token
// (#6072, spike #6071), on the machine that carries the provider: it makes and keeps the
// PKCE verifier, spends the code, keeps the tokens with the provider's other secrets in
// ~/.agents-secrets/provider-secrets.json, refreshes the access token before a call, and
// revokes the grant when the provider is forgotten. The control plane only routes the
// start and the finish (Principle 6): it never sees the verifier or a token.
//
// The first provider is DigitalOcean's API preset, digitalocean-api. Its access token goes
// in DIGITALOCEAN_API_TOKEN, the secret a pasted token goes in, so the preset reads either.
package provideroauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/connections"
	"github.com/agentloops-dev/agent-loops/Go/internal/workspacecommands"
)

// The reasons a client can act on (W2b), as ProviderCarried's code says them.
const (
	Denied          = "denied"
	Disconnected    = "disconnected"
	ScopeMissing    = "scope_missing"
	AccountInactive = "account_inactive"
	Expired         = "expired"
)

// Failure is a connect or a refresh that failed for a reason a client can act on.
type Failure struct {
	Code  string
	Words string
}

func (f *Failure) Error() string { return f.Words }

func failure(code, format string, args ...any) *Failure {
	return &Failure{Code: code, Words: fmt.Sprintf(format, args...)}
}

// Flow is one provider's OAuth client: a public client with PKCE, as spike #6071 tested it.
type Flow struct {
	// Preset is the command provider's preset it is for: digitalocean-api.
	Preset string
	// Name is the provider in words: DigitalOcean.
	Name     string
	ClientID string
	// RedirectURI is the hand-back page (connections.HandBackURL).
	RedirectURI string
	// Scopes are asked for; Needs are the ones a connection is refused without.
	Scopes []string
	Needs  []string
	// The provider's pages: where the person approves, where a code or a refresh token is
	// spent, where a token is revoked, and the API that says the account. A test gives fakes.
	Authorize, Token, Revoke, API string
	// The secrets it keeps: the access token (the one the preset reads), and the rest.
	AccessSecret, RefreshSecret, ExpiresSecret, ScopeSecret string
}

// DigitalOceanClientID is the Agent Loops OAuth client on DigitalOcean (#6072): one public
// client, registered once with POST /v1/oauth/register (client_name "Agent Loops"), with the
// redirects https://oauth.agentloops.dev/oauth/callback and http://127.0.0.1/callback.
// Its registration token is the operator's secret DIGITALOCEAN_OAUTH_REGISTRATION_TOKEN,
// never in the repo. The client ID is public. Registered 2026-11-22.
const DigitalOceanClientID = "58d20f0b4c7cffe50c3654ecf03b429b02d292739442030c8ccaac49f0d892ed"

// DigitalOceanScopes are the endpoints the merged code of #6073 and #6577 calls, plus
// tag:create (a create with a new tag makes it) and account:read (the account's status).
var DigitalOceanScopes = []string{"droplet:create", "droplet:read", "droplet:update", "droplet:delete",
	"ssh_key:create", "ssh_key:read", "regions:read", "sizes:read", "tag:create", "account:read"}

// DigitalOcean is the flow for the digitalocean-api preset.
func DigitalOcean() *Flow {
	return &Flow{Preset: "digitalocean-api", Name: "DigitalOcean", ClientID: DigitalOceanClientID, //nolint:gosec // G101: secret names, not credentials
		RedirectURI: connections.HandBackURL, Scopes: DigitalOceanScopes, Needs: DigitalOceanScopes,
		Authorize: "https://cloud.digitalocean.com/v1/oauth/authorize",
		Token:     "https://cloud.digitalocean.com/v1/oauth/token",
		Revoke:    "https://cloud.digitalocean.com/v1/oauth/revoke",
		API:       "https://api.digitalocean.com",
		AccessSecret: workspacecommands.DigitalOceanSecret, RefreshSecret: "DIGITALOCEAN_OAUTH_REFRESH_TOKEN",
		ExpiresSecret: "DIGITALOCEAN_OAUTH_EXPIRES_AT", ScopeSecret: "DIGITALOCEAN_OAUTH_SCOPE"}
}

// SecretNames are the names of the secrets a connection keeps.
func (f *Flow) SecretNames() []string {
	return []string{f.AccessSecret, f.ExpiresSecret, f.RefreshSecret, f.ScopeSecret}
}

// Store is where a carried provider's secrets are: providers.Secrets.
type Store interface {
	Values(provider string) map[string]string
	Set(provider string, secrets map[string]string) error
	// Renew is Set for a refreshed token, which is never refused for its size.
	Renew(provider string, secrets map[string]string) error
	// Reload reads the file again, for a refresh another process may have won.
	Reload()
}

// pendingLife is how long a started connection waits for its code.
const pendingLife = 10 * time.Minute

// refreshLead is how long before its expiry an access token is refreshed.
const refreshLead = 5 * time.Minute

type pending struct {
	flow     *Flow
	verifier string
	until    time.Time
}

// Carrier is the OAuth connections of the providers one machine carries.
type Carrier struct {
	Store Store
	Flows map[string]*Flow
	HTTP  *http.Client
	Now   func() time.Time

	mu      sync.Mutex
	pending map[string]pending
	// One refresh at a time per provider.
	turns map[string]*sync.Mutex
}

// NewCarrier is the connections kept in store, for every preset that has a flow.
func NewCarrier(store Store) *Carrier {
	do := DigitalOcean()
	return &Carrier{Store: store, Flows: map[string]*Flow{do.Preset: do}, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Carrier) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Carrier) flow(preset string) (*Flow, error) {
	if f := c.Flows[preset]; f != nil {
		return f, nil
	}
	return nil, fmt.Errorf("%s can't be connected with a sign-in: give its token instead", preset)
}

func (c *Carrier) codeFlow(f *Flow) *connections.CodeFlow {
	return &connections.CodeFlow{ClientID: f.ClientID, RedirectURI: f.RedirectURI, Scopes: strings.Join(f.Scopes, " "),
		Authorize: f.Authorize, Token: f.Token, Name: f.Name, HTTP: c.HTTP, Now: c.Now}
}

// Start makes a verifier and a state, keeps the verifier here for ten minutes, and gives
// the address the person opens. origin is the web page's, or "" for the Mac and the Remote.
func (c *Carrier) Start(preset, origin string) (address, state string, err error) {
	f, err := c.flow(preset)
	if err != nil {
		return "", "", err
	}
	p, err := connections.NewPKCE(origin)
	if err != nil {
		return "", "", err
	}
	address = c.codeFlow(f).AuthorizeURL(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		c.pending = map[string]pending{}
	}
	now := c.now()
	for s, held := range c.pending {
		if now.After(held.until) {
			delete(c.pending, s)
		}
	}
	c.pending[p.State] = pending{flow: f, verifier: p.Verifier, until: now.Add(pendingLife)}
	return address, p.State, nil
}

// Connected is who a finished connection is for. It holds no token.
type Connected struct {
	Account, Team, Scope string
	SecretNames          []string
}

// Finish spends the code with the verifier its state keeps, checks the scopes and the
// account, and keeps the tokens for provider. providerError is the callback's error, such as
// access_denied, when there was one. The state is spent either way.
func (c *Carrier) Finish(ctx context.Context, provider, state, code, providerError string) (Connected, error) {
	c.mu.Lock()
	held, ok := c.pending[state]
	delete(c.pending, state)
	c.mu.Unlock()
	if providerError == "access_denied" {
		return Connected{}, failure(Denied, "You chose Deny, so Agent Loops isn't connected.")
	}
	if !ok || c.now().After(held.until) {
		return Connected{}, failure(Expired, "That sign-in was started too long ago, or on another machine: start again.")
	}
	f := held.flow
	if providerError != "" {
		return Connected{}, fmt.Errorf("%s said %s: start again", f.Name, providerError)
	}
	if code == "" {
		return Connected{}, errors.New("the sign-in gave no code: start again")
	}
	answer, err := c.exchange(ctx, f, code, held.verifier)
	if err != nil {
		return Connected{}, err
	}
	granted := strings.Fields(answer.Scope)
	var missing []string
	for _, s := range f.Needs {
		if !slices.Contains(granted, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		_ = c.revoke(ctx, f, answer.AccessToken, answer.RefreshToken) // A refused grant is revoked when it can be; the refusal is what is said.
		return Connected{}, failure(ScopeMissing, "%s gave Agent Loops less than it needs (no %s): connect again and allow all of it.",
			f.Name, strings.Join(missing, ", "))
	}
	if status, words, err := c.accountStatus(ctx, f, answer.AccessToken); err == nil && status != "" && status != "active" {
		_ = c.revoke(ctx, f, answer.AccessToken, answer.RefreshToken) // A refused grant is revoked when it can be; the refusal is what is said.
		if words == "" {
			words = "its status is " + status
		}
		return Connected{}, failure(AccountInactive, "Your %s account can't make machines yet: %s.", f.Name, words)
	}
	if err := c.Store.Set(provider, c.kept(f, answer)); err != nil {
		_ = c.revoke(ctx, f, answer.AccessToken, answer.RefreshToken) // A refused grant is revoked when it can be; the refusal is what is said.
		return Connected{}, errors.New("this machine could not keep the connection")
	}
	return Connected{Account: answer.Info.Email, Team: answer.Info.TeamName, Scope: answer.Scope,
		SecretNames: f.SecretNames()}, nil
}

func (c *Carrier) kept(f *Flow, a tokenAnswer) map[string]string {
	expires := ""
	if a.ExpiresIn > 0 {
		expires = c.now().Add(time.Duration(a.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return map[string]string{f.AccessSecret: a.AccessToken, f.RefreshSecret: a.RefreshToken,
		f.ExpiresSecret: expires, f.ScopeSecret: a.Scope}
}

// tokenAnswer is DigitalOcean's token answer: the pair, and whose it is.
type tokenAnswer struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Info             struct {
		Email    string `json:"email"`
		TeamName string `json:"team_name"`
	} `json:"info"`
}

// post spends a code or a refresh token. A failure says what failed, never the form.
func (c *Carrier) post(ctx context.Context, f *Flow, form url.Values) (tokenAnswer, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenAnswer{}, 0, fmt.Errorf("the request to %s could not be made", f.Name)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return tokenAnswer{}, 0, fmt.Errorf("%s could not be reached", f.Name)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var a tokenAnswer
	_ = json.Unmarshal(body, &a)
	if res.StatusCode != http.StatusOK || a.AccessToken == "" {
		return a, res.StatusCode, fmt.Errorf("%s answered %d%s", f.Name, res.StatusCode, said(a))
	}
	return a, res.StatusCode, nil
}

func said(a tokenAnswer) string {
	if a.Error == "" {
		return ""
	}
	return " (" + a.Error + ")"
}

func (c *Carrier) exchange(ctx context.Context, f *Flow, code, verifier string) (tokenAnswer, error) {
	a, _, err := c.post(ctx, f, url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {f.RedirectURI}, "client_id": {f.ClientID}, "code_verifier": {verifier}})
	if err != nil {
		return tokenAnswer{}, fmt.Errorf("%s did not take the sign-in: %w. Start again", f.Name, err)
	}
	return a, nil
}

// accountStatus is /v2/account's status and status_message.
func (c *Carrier) accountStatus(ctx context.Context, f *Flow, access string) (status, words string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.API+"/v2/account", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	var a struct {
		Account struct {
			Status        string `json:"status"`
			StatusMessage string `json:"status_message"`
		} `json:"account"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&a) != nil {
		return "", "", fmt.Errorf("%s answered %d for the account", f.Name, res.StatusCode)
	}
	return a.Account.Status, a.Account.StatusMessage, nil
}

func (c *Carrier) turn(provider string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turns == nil {
		c.turns = map[string]*sync.Mutex{}
	}
	t := c.turns[provider]
	if t == nil {
		t = &sync.Mutex{}
		c.turns[provider] = t
	}
	return t
}

// Pasted is secrets to keep, with an OAuth connection's own secrets taken away when they
// give its access token by hand: a pasted token replaces the connection, and is never
// refreshed over.
func (c *Carrier) Pasted(secrets map[string]string) map[string]string {
	out := make(map[string]string, len(secrets))
	for k, v := range secrets {
		out[k] = v
	}
	for _, f := range c.Flows {
		if secrets[f.AccessSecret] != "" && secrets[f.RefreshSecret] == "" {
			out[f.RefreshSecret], out[f.ExpiresSecret], out[f.ScopeSecret] = "", "", ""
		}
	}
	return out
}

// Connected says provider's secrets are an OAuth connection's, not a pasted token's.
func (c *Carrier) Connected(values map[string]string) bool { return c.connected(values) != nil }

// connected is the flow of the connection provider's secrets hold, or nil for none (a
// pasted token, or no secrets).
func (c *Carrier) connected(values map[string]string) *Flow {
	for _, f := range c.Flows {
		if values[f.RefreshSecret] != "" {
			return f
		}
	}
	return nil
}

// Fresh refreshes provider's access token when it expires within five minutes, and writes
// the new pair before a call uses it: true when it did. A pasted token is left as it is.
// One refresh at a time per provider. A refresh token that is refused (DigitalOcean: 401
// invalid_request, also when another process spent it first) reads the file again: a pair
// that a race made is used; only when there is none is the connection disconnected.
func (c *Carrier) Fresh(ctx context.Context, provider string) (bool, error) {
	turn := c.turn(provider)
	turn.Lock()
	defer turn.Unlock()
	values := c.Store.Values(provider)
	f := c.connected(values)
	if f == nil || !c.expiring(values[f.ExpiresSecret]) {
		return false, nil
	}
	refresh := values[f.RefreshSecret]
	a, status, err := c.post(ctx, f, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if err == nil {
		if a.Scope == "" {
			a.Scope = values[f.ScopeSecret]
		}
		if a.RefreshToken == "" {
			a.RefreshToken = refresh
		}
		if err := c.Store.Renew(provider, c.kept(f, a)); err != nil {
			return false, errors.New("this machine could not keep the new token")
		}
		return true, nil
	}
	if status != http.StatusUnauthorized && status != http.StatusBadRequest {
		// Not reached, or a fault there: the old token may still work until it expires.
		return false, err
	}
	c.Store.Reload()
	again := c.Store.Values(provider)
	if again[f.RefreshSecret] != "" && again[f.RefreshSecret] != refresh && !c.expiring(again[f.ExpiresSecret]) {
		return true, nil
	}
	return false, failure(Disconnected, "%s disconnected Agent Loops: connect it again.", f.Name)
}

func (c *Carrier) expiring(at string) bool {
	if at == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return true
	}
	return !c.now().Add(refreshLead).Before(t)
}

// Revoke revokes provider's connection at the provider, when its secrets hold one: both
// tokens stop, and the grant goes from the account's list. It revokes the refresh token, then the access token. It is done before the secrets
// are forgotten; a revoke that fails is said, and the secrets go all the same.
func (c *Carrier) Revoke(ctx context.Context, provider string) error {
	values := c.Store.Values(provider)
	f := c.connected(values)
	if f == nil || values[f.AccessSecret] == "" {
		return nil
	}
	return c.revoke(ctx, f, values[f.AccessSecret], values[f.RefreshSecret])
}

// revoke revokes the refresh token, then the access token (RFC 7009). On the real account
// a revoke of the access token alone left the refresh token working and the grant in the
// account's list (#6072), so both go.
func (c *Carrier) revoke(ctx context.Context, f *Flow, access, refresh string) error {
	var first error
	if refresh != "" {
		first = c.revokeToken(ctx, f, access, refresh)
	}
	if err := c.revokeToken(ctx, f, access, access); err != nil && first == nil {
		first = err
	}
	return first
}

func (c *Carrier) revokeToken(ctx context.Context, f *Flow, access, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Revoke,
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s could not be reached to revoke", f.Name)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d to the revoke", f.Name, res.StatusCode)
	}
	return nil
}
