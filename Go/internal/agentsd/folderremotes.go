package agentsd

import (
	"context"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/gitremote"
)

// remoteReadBudget is the time limit of one read of a folder's git remotes.
const remoteReadBudget = 5 * time.Second

// remoteReads is how many times folderRemotes reads before it gives up.
const remoteReads = 2

// folderRemotes is the URLs of folder's git remotes (gitremote.Read). Each read has a time
// limit of its own, and a read that fails is done again once (#6064): one slow git on a
// loaded machine must not end the read. When every read fails, the error says so, and the
// caller tells "not known" from "no remote": a folder is never put on a forge from a read
// that failed.
func folderRemotes(ctx context.Context, folder string) ([]string, error) {
	var err error
	for range remoteReads {
		readCtx, cancel := context.WithTimeout(ctx, remoteReadBudget)
		var urls []string
		urls, err = gitremote.Read(readCtx, folder)
		cancel()
		if err == nil {
			return urls, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, err
}
