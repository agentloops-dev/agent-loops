package agentsd

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A forge error names the forge once (#6536): the client puts "GitHub: " in front, so the
// message does not, and a request that got no answer says so in words.
func TestForgeErrorWords(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`GitHub: Post "http://127.0.0.1:8963/api/graphql": dial tcp 127.0.0.1:8963: connect: connection refused`,
			"could not be reached (the connection was refused)"},
		{`GitLab: Get "https://gitlab.example/api/v4/issues": dial tcp: lookup gitlab.example: no such host`,
			"could not be reached (no such host)"},
		{`Forgejo: Get "https://code.example/api/v1/repos": net/http: request canceled (Client.Timeout exceeded while awaiting headers)`,
			"could not be reached (it did not answer in time)"},
		{`Azure DevOps: Post "https://dev.azure.com/x": tls: failed to verify certificate: x509: certificate signed by unknown authority`,
			"could not be reached (its certificate is not trusted)"},
		{`Linear: Post "https://api.linear.app/graphql": EOF`, "could not be reached"},
		{"GitHub: API rate limit exceeded", "API rate limit exceeded"},
		{githubmcp.NotConnected, githubmcp.NotConnected},
		{"not found on GitHub", "not found on GitHub"},
	} {
		if got := forgeErrorWords(c.in); got != c.want {
			t.Errorf("forgeErrorWords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// GitHub not answering reads "could not be reached" on a Work list's error, with no forge name in
// the message, from the error RealGitHub really gives.
func TestForgeListErrorOfUnreachableGitHub(t *testing.T) {
	srv := httptest.NewServer(nil)
	api := srv.URL + "/api/"
	srv.Close() // nothing answers at api now
	gh := githubmcp.NewRealGitHub(api, func() string { return "token" }, nil)
	_, err := gh.Search(context.Background(), []string{"o/r"}, githubmcp.SearchFilter{}, "", 10)
	if err == nil {
		t.Fatalf("Search: %v, want GitHub's error", err)
	}
	var out wire.ForgeList
	forgeListFillForgesAndErrors(&out, nil, []forgePage{{forge: "github", err: err}})
	want := wire.ForgeListError{Forge: "github", Message: "could not be reached (the connection was refused)"}
	if len(out.Errors) != 1 || out.Errors[0] != want {
		t.Fatalf("errors %+v, want %+v (from %q)", out.Errors, want, err)
	}
}
