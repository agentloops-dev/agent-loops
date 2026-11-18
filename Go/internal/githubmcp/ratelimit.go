package githubmcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GitHub's rate limits (#6163). GitHub counts each token's calls per resource: core (REST),
// graphql and search. When one call finds a resource's limit used up, every caller of this
// RealGitHub with that token waits until the reset, and no call goes to GitHub before it: a
// call before the reset only uses up more of the secondary limit, and GitHub can ban a token
// that keeps calling.

// rateGates is the time each token's resource opens again.
type rateGates struct {
	mu    sync.Mutex
	until map[string]time.Time
}

// guessedReset is how long a resource stays closed when GitHub says it is rate limited but
// not until when.
const guessedReset = 60 * time.Second

// rateResource is the resource GitHub counts a call to path against.
func rateResource(path string) string {
	path, _, _ = strings.Cut(path, "?")
	switch {
	case path == "graphql":
		return "graphql"
	case strings.HasPrefix(path, "search/"):
		return "search"
	}
	return "core"
}

// gateKey names token's resource with a hash, so the gates hold no token.
func gateKey(token, path string) string {
	sum := sha256.Sum256([]byte(token))
	return string(sum[:8]) + rateResource(path)
}

// closed is the rate limit error for a call to key's resource before its reset, or nil when
// it is open.
func (r *rateGates) closed(key string, now time.Time) *GitHubError {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.until[key]
	if !ok {
		return nil
	}
	if !now.Before(until) {
		delete(r.until, key)
		return nil
	}
	return rateLimited(int(until.Sub(now).Milliseconds()))
}

// close shuts key's resource for ms from now.
func (r *rateGates) close(key string, ms int, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.until == nil {
		r.until = map[string]time.Time{}
	}
	for k, until := range r.until {
		if !now.Before(until) {
			delete(r.until, k)
		}
	}
	until := now.Add(time.Duration(ms) * time.Millisecond)
	if until.After(r.until[key]) {
		r.until[key] = until
	}
}

func rateLimited(ms int) *GitHubError {
	return &GitHubError{Kind: RateLimited, Message: "GitHub rate limit", RetryAfterMs: max(1000, ms)}
}

// answered reads the rate limit in an answer GitHub gave with a 2xx status for path: a
// GraphQL answer with an error of type RATE_LIMIT (GitHub sends it with 200) is a rate limit
// error, and a header that says none is left closes the resource until the reset, though
// this answer is good. It closes key's gate for either.
func (r *rateGates) answered(key, path string, header http.Header, body []byte, now time.Time) *GitHubError {
	if rateResource(path) == "graphql" && graphqlRateLimited(body) {
		ms := retryAfter(header)
		if ms == 0 {
			ms = int(guessedReset.Milliseconds())
		}
		r.close(key, ms, now)
		return rateLimited(ms)
	}
	if header.Get("X-RateLimit-Remaining") == "0" {
		if ms := retryAfter(header); ms > 0 {
			r.close(key, ms, now)
		}
	}
	return nil
}

// graphqlRateLimited says whether a GraphQL answer's errors say its limit is used up.
func graphqlRateLimited(body []byte) bool {
	if !bytes.Contains(body, []byte("RATE_LIMIT")) && !bytes.Contains(body, []byte("graphql_rate_limit")) {
		return false
	}
	var answer struct {
		Errors []struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return false
	}
	for _, e := range answer.Errors {
		if e.Type == "RATE_LIMIT" || e.Type == "RATE_LIMITED" || e.Code == "graphql_rate_limit" {
			return true
		}
	}
	return false
}
