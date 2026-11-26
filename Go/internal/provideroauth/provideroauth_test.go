package provideroauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDO is DigitalOcean's OAuth, as spike #6071 found it: PKCE S256 with no secret, a
// code spent once, a refresh that rotates the pair and stops the old access token, a
// second use of a refresh token refused with 401 invalid_request, and a revoke that stops
// both tokens.
type fakeDO struct {
	mu        sync.Mutex
	codes     map[string]string // code: its challenge
	access    map[string]bool
	refresh   map[string]string // refresh token: its access token
	n         int
	scope     string
	status    string
	revoked   []string
	refreshes int
	// gate holds each refresh until it is closed, so two can race.
	gate chan struct{}
}

func newFake(t *testing.T) (*fakeDO, *httptest.Server) {
	f := &fakeDO{codes: map[string]string{}, access: map[string]bool{}, refresh: map[string]string{},
		scope: strings.Join(DigitalOceanScopes, " ") + " snapshot:read tag:read vpc:read", status: "active"}
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	return f, s
}

func (f *fakeDO) pair() map[string]any {
	f.n++
	a, r := fmt.Sprintf("access-%d", f.n), fmt.Sprintf("refresh-%d", f.n)
	f.access[a], f.refresh[r] = true, a
	return map[string]any{"access_token": a, "refresh_token": r, "expires_in": 3600, "scope": f.scope,
		"info": map[string]any{"email": "agents@example.com", "team_name": "My Team"}}
}

func (f *fakeDO) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	deny := func() {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
	}
	switch r.URL.Path {
	case "/token":
		if r.Form.Get("grant_type") == "refresh_token" {
			if f.gate != nil {
				<-f.gate
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			challenge, ok := f.codes[r.Form.Get("code")]
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.Form.Get("client_secret") != "" {
				deny()
				return
			}
			delete(f.codes, r.Form.Get("code"))
			_ = json.NewEncoder(w).Encode(f.pair())
		case "refresh_token":
			f.refreshes++
			old, ok := f.refresh[r.Form.Get("refresh_token")]
			if !ok {
				deny()
				return
			}
			delete(f.refresh, r.Form.Get("refresh_token"))
			delete(f.access, old)
			_ = json.NewEncoder(w).Encode(f.pair())
		}
	case "/revoke":
		f.mu.Lock()
		defer f.mu.Unlock()
		// As the real account does: a revoke stops the token it names. The bearer must
		// still be good.
		token := r.Form.Get("token")
		if !f.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
			deny()
			return
		}
		if _, ok := f.refresh[token]; ok {
			delete(f.refresh, token)
		} else if f.access[token] {
			delete(f.access, token)
		} else {
			deny()
			return
		}
		f.revoked = append(f.revoked, token)
		_, _ = w.Write([]byte(`{}`))
	case "/v2/account":
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
			deny()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"status": f.status,
			"status_message": "Add a payment method to make droplets."}})
	default:
		http.NotFound(w, r)
	}
}

// approve is the person choosing Authorize on the consent page for address: a code.
func (f *fakeDO) approve(t *testing.T, address string) string {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") == "" || q.Get("state") == "" {
		t.Fatalf("the address is not a PKCE request: %s", address)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	code := fmt.Sprintf("code-%d", len(f.codes)+f.n+1)
	f.codes[code] = q.Get("code_challenge")
	return code
}

type memStore struct {
	mu      sync.Mutex
	values  map[string]map[string]string
	reloads int
	// onReload is what another process wrote, read at the next Reload.
	onReload map[string]map[string]string
}

func (m *memStore) Values(provider string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.values[provider] {
		out[k] = v
	}
	return out
}

func (m *memStore) Set(provider string, secrets map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string]map[string]string{}
	}
	mine := m.values[provider]
	if mine == nil {
		mine = map[string]string{}
	}
	for k, v := range secrets {
		if v == "" {
			delete(mine, k)
		} else {
			mine[k] = v
		}
	}
	m.values[provider] = mine
	return nil
}

func (m *memStore) Renew(provider string, secrets map[string]string) error {
	return m.Set(provider, secrets)
}

func (m *memStore) Reload() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloads++
	for p, v := range m.onReload {
		m.values[p] = v
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func carrier(t *testing.T) (*Carrier, *fakeDO, *memStore, *clock) {
	t.Helper()
	fake, server := newFake(t)
	flow := DigitalOcean()
	flow.Authorize, flow.Token, flow.Revoke, flow.API = server.URL+"/authorize", server.URL+"/token",
		server.URL+"/revoke", server.URL
	store := &memStore{}
	now := &clock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	c := &Carrier{Store: store, Flows: map[string]*Flow{flow.Preset: flow}, HTTP: server.Client(), Now: now.Now}
	return c, fake, store, now
}

func connect(t *testing.T, c *Carrier, fake *fakeDO) Connected {
	t.Helper()
	address, state, err := c.Start("digitalocean-api", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Finish(context.Background(), "do", state, fake.approve(t, address), "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func code(err error) string {
	var f *Failure
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}

func TestStartAndFinishKeepTheTokensHere(t *testing.T) {
	c, fake, store, _ := carrier(t)
	got := connect(t, c, fake)
	if got.Account != "agents@example.com" || got.Team != "My Team" {
		t.Fatalf("connected as %+v", got)
	}
	kept := store.Values("do")
	if kept["DIGITALOCEAN_API_TOKEN"] != "access-1" || kept["DIGITALOCEAN_OAUTH_REFRESH_TOKEN"] != "refresh-1" ||
		kept["DIGITALOCEAN_OAUTH_EXPIRES_AT"] != "2026-11-22T13:00:00Z" {
		t.Fatalf("kept %v", kept)
	}
	if !c.Connected(kept) {
		t.Fatal("not seen as an OAuth connection")
	}
	for _, name := range got.SecretNames {
		if strings.Contains(name, "access-") {
			t.Fatal("a token in the names")
		}
	}
}

func TestStartWithAnOriginPutsItInTheState(t *testing.T) {
	c, _, _, _ := carrier(t)
	_, state, err := c.Start("digitalocean-api", "https://control.agentloops.dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state, ".") {
		t.Fatalf("state %q carries no origin", state)
	}
	if _, _, err := c.Start("aws-ec2-api", ""); err == nil {
		t.Fatal("a preset with no flow started")
	}
}

func TestFinishFailures(t *testing.T) {
	c, fake, store, now := carrier(t)
	ctx := context.Background()

	address, state, _ := c.Start("digitalocean-api", "")
	if _, err := c.Finish(ctx, "do", state, "", "access_denied"); code(err) != Denied {
		t.Fatalf("deny: %v", err)
	}
	if _, err := c.Finish(ctx, "do", state, fake.approve(t, address), ""); code(err) != Expired {
		t.Fatalf("a state used twice: %v", err)
	}

	address, state, _ = c.Start("digitalocean-api", "")
	now.add(11 * time.Minute)
	if _, err := c.Finish(ctx, "do", state, fake.approve(t, address), ""); code(err) != Expired {
		t.Fatalf("too late: %v", err)
	}

	fake.scope = "droplet:read"
	address, state, _ = c.Start("digitalocean-api", "")
	if _, err := c.Finish(ctx, "do", state, fake.approve(t, address), ""); code(err) != ScopeMissing {
		t.Fatalf("scopes: %v", err)
	}
	fake.scope = strings.Join(DigitalOceanScopes, " ")

	fake.status = "locked"
	address, state, _ = c.Start("digitalocean-api", "")
	_, err := c.Finish(ctx, "do", state, fake.approve(t, address), "")
	if code(err) != AccountInactive || !strings.Contains(err.Error(), "payment method") {
		t.Fatalf("account: %v", err)
	}
	if len(store.Values("do")) != 0 {
		t.Fatal("a refused connection was kept")
	}
	if len(fake.revoked) != 4 {
		t.Fatalf("refused grants not revoked: %v", fake.revoked)
	}

	fake.status = "active"
	_, state, _ = c.Start("digitalocean-api", "")
	if _, err := c.Finish(ctx, "do", state, "not-a-code", ""); err == nil || code(err) != "" {
		t.Fatalf("a bad code: %v", err)
	}
}

func TestFreshRefreshesNearTheEnd(t *testing.T) {
	c, fake, store, now := carrier(t)
	connect(t, c, fake)
	ctx := context.Background()
	if done, err := c.Fresh(ctx, "do"); done || err != nil {
		t.Fatalf("refreshed a new token: %v %v", done, err)
	}
	now.add(56 * time.Minute)
	if done, err := c.Fresh(ctx, "do"); !done || err != nil {
		t.Fatalf("not refreshed: %v %v", done, err)
	}
	kept := store.Values("do")
	if kept["DIGITALOCEAN_API_TOKEN"] != "access-2" || kept["DIGITALOCEAN_OAUTH_REFRESH_TOKEN"] != "refresh-2" {
		t.Fatalf("kept %v", kept)
	}
	if kept["DIGITALOCEAN_OAUTH_SCOPE"] == "" {
		t.Fatal("scope lost")
	}
}

func TestFreshLeavesAPastedTokenAlone(t *testing.T) {
	c, fake, store, now := carrier(t)
	connect(t, c, fake)
	_ = store.Set("do", c.Pasted(map[string]string{"DIGITALOCEAN_API_TOKEN": "dop_v1_pasted"})) //nolint:gosec // G101: a fake token
	kept := store.Values("do")
	if len(kept) != 1 || c.Connected(kept) {
		t.Fatalf("a pasted token kept the connection: %v", kept)
	}
	now.add(2 * time.Hour)
	if done, err := c.Fresh(context.Background(), "do"); done || err != nil || fake.refreshes != 0 {
		t.Fatalf("refreshed a pasted token: %v %v", done, err)
	}
}

func TestARefusedRefreshIsDisconnected(t *testing.T) {
	c, fake, store, now := carrier(t)
	connect(t, c, fake)
	// The person removed Agent Loops at DigitalOcean.
	fake.mu.Lock()
	fake.refresh = map[string]string{}
	fake.mu.Unlock()
	now.add(time.Hour)
	if _, err := c.Fresh(context.Background(), "do"); code(err) != Disconnected {
		t.Fatalf("got %v", err)
	}
	if store.reloads != 1 {
		t.Fatal("the file was not read again first")
	}
}

func TestARefreshRaceUsesThePairTheWinnerWrote(t *testing.T) {
	c, fake, store, now := carrier(t)
	connect(t, c, fake)
	now.add(time.Hour)
	// Another process spent refresh-1 first, and wrote its new pair to the file.
	fake.mu.Lock()
	won := fake.pair()
	delete(fake.refresh, "refresh-1")
	fake.mu.Unlock()
	store.onReload = map[string]map[string]string{"do": {
		"DIGITALOCEAN_API_TOKEN": won["access_token"].(string), "DIGITALOCEAN_OAUTH_REFRESH_TOKEN": won["refresh_token"].(string),
		"DIGITALOCEAN_OAUTH_EXPIRES_AT": now.Now().Add(time.Hour).Format(time.RFC3339)}}
	done, err := c.Fresh(context.Background(), "do")
	if err != nil || !done {
		t.Fatalf("got %v %v", done, err)
	}
	if store.Values("do")["DIGITALOCEAN_API_TOKEN"] != won["access_token"] {
		t.Fatal("the winner's pair is not used")
	}
}

func TestRefreshesInOneCarrierGoOneAtATime(t *testing.T) {
	c, fake, store, now := carrier(t)
	connect(t, c, fake)
	now.add(time.Hour)
	fake.gate = make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := c.Fresh(context.Background(), "do")
			errs <- err
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(fake.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a refresh failed: %v", err)
		}
	}
	if fake.refreshes != 1 {
		t.Fatalf("%d refreshes, want 1", fake.refreshes)
	}
	if store.Values("do")["DIGITALOCEAN_API_TOKEN"] != "access-2" {
		t.Fatal("the new pair is not kept")
	}
}

func TestRevokeStopsBothTokens(t *testing.T) {
	c, fake, store, _ := carrier(t)
	connect(t, c, fake)
	if err := c.Revoke(context.Background(), "do"); err != nil {
		t.Fatal(err)
	}
	if len(fake.revoked) != 2 || fake.revoked[0] != "refresh-1" || fake.revoked[1] != "access-1" || len(fake.refresh) != 0 {
		t.Fatalf("revoked %v, refresh left %v", fake.revoked, fake.refresh)
	}
	_ = store.Set("other", map[string]string{"DIGITALOCEAN_API_TOKEN": "dop_v1_pasted"}) //nolint:gosec // G101: a fake token
	if err := c.Revoke(context.Background(), "other"); err != nil || len(fake.revoked) != 2 {
		t.Fatal("a pasted token was revoked")
	}
}
