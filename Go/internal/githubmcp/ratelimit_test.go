package githubmcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// GitHub's rate limits (#6163): GraphQL says a used-up limit with 200, and no call goes to a
// resource before its reset.

// limitedAPI is a GitHub whose GraphQL limit is used up until reset, and whose REST still
// answers. It counts the calls that reach it by resource.
func limitedAPI(t *testing.T, reset time.Time) (*RealGitHub, *atomic.Int32, *atomic.Int32) {
	var graphql, core atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			graphql.Add(1)
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			_, _ = io.WriteString(w, `{"errors":[{"type":"RATE_LIMIT","code":"graphql_rate_limit",`+
				`"message":"API rate limit already exceeded for user ID 1."}]}`)
			return
		}
		core.Add(1)
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(server.Close)
	g := NewRealGitHub(server.URL, func() string { return "token" }, func(context.Context) (string, error) { return repo, nil })
	return g, &graphql, &core
}

func TestAGraphQLRateLimitIsRateLimitedUntilTheReset(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute)
	g, graphql, _ := limitedAPI(t, reset)

	_, err := g.BlockedPRs(context.Background(), repo)
	var gh *GitHubError
	if !errors.As(err, &gh) || gh.Kind != RateLimited {
		t.Fatalf("a 200 with RATE_LIMIT: %v", err)
	}
	if left := time.Duration(gh.RetryAfterMs) * time.Millisecond; left < 9*time.Minute || left > 10*time.Minute+time.Second {
		t.Fatalf("retry after %v, want until the reset", left)
	}
	rpc := eventsError(err)
	if rpc.Code != -32013 || rpc.Data["retryAfterMs"] == nil {
		t.Fatalf("events error: %+v", rpc)
	}
	equal(t, graphql.Load(), int32(1), "the first call reaches GitHub")
}

func TestNoCallGoesToAResourceBeforeItsReset(t *testing.T) {
	g, graphql, core := limitedAPI(t, time.Now().Add(10*time.Minute))
	ctx := context.Background()
	if _, err := g.BlockedPRs(ctx, repo); err == nil {
		t.Fatal("not rate limited")
	}

	// Other GraphQL readers, through both ways in, wait for the reset too.
	_, err := g.PRReviews(ctx, repo, "", 0)
	var gh *GitHubError
	if !errors.As(err, &gh) || gh.Kind != RateLimited || gh.RetryAfterMs < 9*60_000 {
		t.Fatalf("a second call before the reset: %v", err)
	}
	var into struct{}
	if err := g.graphql(ctx, "{ viewer { login } }", nil, &into); !errors.As(err, &gh) || gh.Kind != RateLimited {
		t.Fatalf("graphql() before the reset: %v", err)
	}
	equal(t, graphql.Load(), int32(1), "no call before the reset reaches GitHub")

	// REST is another resource, with a limit of its own.
	var out []any
	if err := g.get(ctx, "repos/"+repo+"/pulls", false, &out); err != nil {
		t.Fatalf("REST while GraphQL is limited: %v", err)
	}
	equal(t, core.Load(), int32(1), "REST still reaches GitHub")
}

func TestAResourceOpensAgainAtItsReset(t *testing.T) {
	g, graphql, _ := limitedAPI(t, time.Now().Add(-time.Second))
	ctx := context.Background()
	for range 2 {
		_, _ = g.BlockedPRs(ctx, repo)
	}
	// A reset already past is closed for the least time (1 s), not for ever.
	key := gateKey("token", "graphql")
	if g.limits.closed(key, time.Now().Add(2*time.Second)) != nil {
		t.Fatal("still closed after the reset")
	}
	equal(t, graphql.Load(), int32(1), "within the least wait, the second call is not sent")
}

func TestARESTRateLimitShutsCoreForEveryCaller(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit"}`)
	}))
	t.Cleanup(server.Close)
	g := NewRealGitHub(server.URL, func() string { return "token" }, nil)
	ctx := context.Background()
	var out []any
	for range 3 {
		err := g.get(ctx, "repos/"+repo+"/pulls", false, &out)
		var gh *GitHubError
		if !errors.As(err, &gh) || gh.Kind != RateLimited || gh.RetryAfterMs > 30_000 {
			t.Fatalf("429: %v", err)
		}
	}
	equal(t, hits.Load(), int32(1), "one call reaches GitHub")

	// Another token has limits of its own.
	g.Token = func() string { return "other" }
	_ = g.get(ctx, "repos/"+repo+"/pulls", false, &out)
	equal(t, hits.Load(), int32(2), "another token's call is sent")
}

func TestALastCallThatSucceedsShutsTheResource(t *testing.T) {
	var hits atomic.Int32
	reset := time.Now().Add(5 * time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"a"}}}`)
	}))
	t.Cleanup(server.Close)
	g := NewRealGitHub(server.URL, func() string { return "token" }, nil)
	var into struct {
		Viewer struct{ Login string } `json:"viewer"`
	}
	if err := g.graphql(context.Background(), "{ viewer { login } }", nil, &into); err != nil || into.Viewer.Login != "a" {
		t.Fatalf("the last call's answer is good: %v %+v", err, into)
	}
	err := g.graphql(context.Background(), "{ viewer { login } }", nil, &into)
	var gh *GitHubError
	if !errors.As(err, &gh) || gh.Kind != RateLimited {
		t.Fatalf("the next call: %v", err)
	}
	equal(t, hits.Load(), int32(1), "the next call is not sent")
}

func TestAnAnswerThatNamesRateLimitIsNotOne(t *testing.T) {
	equal(t, graphqlRateLimited([]byte(`{"data":{"title":"RATE_LIMIT is read as a server error"}}`)), false, "a title")
	equal(t, graphqlRateLimited([]byte(`{"errors":[{"type":"NOT_FOUND","message":"RATE_LIMIT"}]}`)), false, "another error")
	equal(t, graphqlRateLimited([]byte(`{"errors":[{"code":"graphql_rate_limit"}]}`)), true, "the code alone")
	equal(t, rateResource("graphql"), "graphql", "graphql")
	equal(t, rateResource("search/issues?q=x"), "search", "search")
	equal(t, rateResource("repos/a/b/pulls?state=open"), "core", "REST")
	if strings.Contains(gateKey("secret-token", "graphql"), "secret-token") {
		t.Fatal("a gate key holds the token")
	}
}
