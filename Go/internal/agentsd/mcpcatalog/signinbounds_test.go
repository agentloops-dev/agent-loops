package mcpcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// #6740: one sign-in more than the limit is refused, none goes, and a sign-out still works.
func TestSignInsAreBounded(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	s := &SignIns{Home: t.TempDir(), Now: func() time.Time { return at }}
	plan := Plan{Server: AuthorizationServer{Issuer: "https://as.example", TokenEndpoint: "https://as.example/token"}} //nolint:gosec // G101: scratch addresses, no credentials
	keep := func(server string) error { return s.Keep(server, "x", plan, Client{ID: "c"}, Tokens{Access: "a"}) }
	for i := range signInsMost {
		if err := keep(fmt.Sprintf("https://mcp%d.example", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := keep("https://more.example"); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("not refused: %v", err)
	}
	if err := keep("https://mcp1.example"); err != nil {
		t.Fatalf("a sign-in again: %v", err)
	}
	for i := range needsSignInMost + 10 {
		s.MarkNeedsSignIn(fmt.Sprintf("https://needs%d.example", i), "x", "")
	}
	if n := len(s.read().NeedsSignIn); n != needsSignInMost {
		t.Fatalf("%d need a sign-in", n)
	}
	if err := s.SignOut(context.Background(), "https://mcp1.example", "x"); err != nil || s.State("https://mcp1.example") == "signedIn" {
		t.Fatalf("no sign-out when full: %v", err)
	}
	at = at.Add(needsSignInKept)
	s.MarkNeedsSignIn("https://late.example", "x", "")
	if f := s.read(); len(f.NeedsSignIn) != 1 || len(f.Grants) != signInsMost-1 {
		t.Fatalf("%d and %d after 90 days", len(f.NeedsSignIn), len(f.Grants))
	}
}

// #7203: a refresh whose new tokens cannot be kept is not told as one that was: the spent token
// is not left on the disk, and is not sent again. One that still works stays.
func TestARefreshThatCannotBeKeptLeavesNoSpentToken(t *testing.T) {
	t.Parallel()
	for _, rotates := range []bool{true, false} {
		sent, revoked := 0, 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/revoke" {
				revoked++
				return
			}
			sent++
			out := map[string]any{"access_token": strings.Repeat("a", signInBytes), "token_type": "Bearer", "expires_in": 3600}
			if rotates {
				out["refresh_token"] = "second"
			}
			_ = json.NewEncoder(w).Encode(out)
		}))
		t.Cleanup(srv.Close)
		var logged []string
		s := &SignIns{Home: t.TempDir(), Log: func(l string) { logged = append(logged, l) }}
		server, revoke, soon := "https://mcp.example", srv.URL+"/revoke", 1
		plan := Plan{Server: AuthorizationServer{Issuer: srv.URL, TokenEndpoint: srv.URL + "/token", RevocationEndpoint: &revoke}}
		if err := s.Keep(server, "x", plan, Client{ID: "c"}, Tokens{Access: "a", Refresh: "first", ExpiresIn: &soon}); err != nil {
			t.Fatal(err)
		}
		got := s.BearerFor(t.Context(), server, "x")
		if slices.Contains(logged, "mcp sign-in: x refreshed") {
			t.Errorf("rotates %v: told as kept", rotates)
		}
		if !rotates {
			if got.Kind != "token" || s.State(server) != "signedIn" || revoked != 0 {
				t.Errorf("a refresh token that still works: %s, %s", got.Kind, s.State(server))
			}
			continue
		}
		if got.Kind != "needsSignIn" || s.State(server) != "needsSignIn" || revoked != 1 {
			t.Errorf("%s, %s, %d revoked", got.Kind, s.State(server), revoked)
		}
		// The disk keeps the spent token when it takes no write: it is not sent again.
		if err := s.Keep(server, "x", plan, Client{ID: "c"}, Tokens{Access: "a", Refresh: "first", ExpiresIn: &soon}); err != nil {
			t.Fatal(err)
		}
		if got := s.BearerFor(t.Context(), server, "x"); got.Kind != "needsSignIn" || sent != 1 {
			t.Errorf("a spent token: %s, sent %d times", got.Kind, sent)
		}
	}
}

// #7203: the note that a server needs a sign-in is at most 4 KiB, as it is declared.
func TestAServerThatNeedsASignInIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	s := &SignIns{Home: t.TempDir()}
	s.MarkNeedsSignIn("https://mcp.example", "x", "https://mcp.example/"+strings.Repeat("m", needsSignInBytes))
	b, err := json.Marshal(s.read().NeedsSignIn)
	if err != nil || len(s.read().NeedsSignIn) != 1 || len(b) > needsSignInBytes {
		t.Fatalf("%d bytes: %v", len(b), err)
	}
	decl, err := os.ReadFile("signins.go")
	if err != nil || !strings.Contains(string(decl), "4KiB a server that needs one") {
		t.Fatalf("the declaration does not say 4 KiB: %v", err)
	}
}
