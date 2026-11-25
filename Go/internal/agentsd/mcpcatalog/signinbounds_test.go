package mcpcatalog

import (
	"context"
	"errors"
	"fmt"
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
