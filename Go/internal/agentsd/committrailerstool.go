package agentsd

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// commit_trailers (#6362, epic #6365): the app's tool over git/trailers, for the session that
// calls it. An agent writes its lines, as they come, as the last paragraph of a commit message
// or of its pull request's squash message (gh pr merge --auto --squash --body-file), so it
// types no trailer by hand. A script on the socket is a stranger and cannot ask git/trailers
// (roles.go); the session's own token can.

// commitTrailersTimeout bounds one call: git/trailers makes at most two forge reads, each
// bounded by trailerForgeWait.
const commitTrailersTimeout = 30 * time.Second

// commitTrailersTool is commit_trailers: the lines, one a line, or one sentence when there are none.
func (d *Daemon) commitTrailersTool(caller wire.UUID, args map[string]json.RawMessage) (string, error) {
	r := wire.GitTrailersRequest{Session: caller}
	if _, given := args["pr"]; given && string(args["pr"]) != "null" {
		n, ok := argInt(args, "pr")
		if !ok || n <= 0 {
			return "", Invalid("No trailers: pr is a pull request's number, such as 6362.")
		}
		pr := int64(n)
		r.Pr = &pr
	}
	ctx, cancel := context.WithTimeout(d.ctx, commitTrailersTimeout)
	defer cancel()
	t, err := d.gitTrailers(ctx, r)
	if err != nil {
		return "", toolWords(err, "git/trailers: ")
	}
	if len(t.Lines) == 0 {
		return "", Invalid("No trailers: this folder's commitTrailers rule turns every trailer off, or none has a value. Write no trailer block.")
	}
	return strings.Join(t.Lines, "\n"), nil
}
