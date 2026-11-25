package agentsd

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The bound of an account's home (#6707): a runtime's own conversations in
// <root>/runtimes/accounts/<runtime>/<id> go accountHistoryAge after they were last written,
// and the oldest go first while those of one home are over accountHistoryBytes (1 MB a
// session, 100 sessions a week, 12 weeks: #6677). The home is the runtime's, so only the
// files that are known to be one conversation's go (runtimeFilePatterns): never its sign-in,
// its configuration, its databases or what nobody here can tell. The history of a session
// that is not archived stays, so it can go on.
const (
	accountHistoryAge   = store.MaxAge
	accountHistoryBytes = 1200 << 20
	// A conversation written this lately may be running, also one the app did not start.
	accountHistoryBusy = 24 * time.Hour
	// The first line of a Codex rollout, with its base instructions, is about 20 KB.
	rolloutHeadBytes = 1 << 20
)

var (
	conversationUUID = regexp.MustCompile(`[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}`)
	// conversationFile is the name of a conversation's file or folder, as runtimeFilePatterns
	// finds it. Claude's projects/<folder>/memory is not one.
	conversationFile = regexp.MustCompile(`^(?:rollout-.*-)?(` + conversationUUID.String() + `)(?:\.jsonl|\.txt|-.*\.json)?$`)
)

// accountConversation is one conversation in an account's home, with its helpers'.
type accountConversation struct {
	ids   []string
	paths []string
	bytes int64
	at    time.Time
	keep  bool // it could not be read in full
}

// sweepAccountHomes holds each account's home to its bound. It looks at the folders, not the
// accounts, so a home left by a removal that failed is bounded too.
func (d *Daemon) sweepAccountHomes(now time.Time) {
	var needed func(id string) bool
	for runtimeID := range accountKinds {
		dir := filepath.Join(d.Store.Loc.Root, "runtimes", "accounts", runtimeID)
		homes, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, h := range homes {
			if !h.IsDir() || d.ctx.Err() != nil {
				continue
			}
			if needed == nil {
				if needed, err = d.neededConversations(); err != nil {
					log.Printf("retention: account homes left as they are: %v", err)
					return
				}
			}
			home := filepath.Join(dir, h.Name())
			n, left := sweepAccountHome(runtimeID, home, accountHistoryBytes, now, needed)
			if n > 0 {
				log.Printf("retention: removed %d conversations past the bound of %s", n, home)
			}
			if left > accountHistoryBytes {
				log.Printf("retention: %s keeps %d MB of conversations, over its %d MB: the rest are in use or of sessions not archived",
					home, left>>20, accountHistoryBytes>>20)
			}
		}
	}
}

// neededConversations says whether a session that is not archived needs a conversation. It
// asks for the session's state at each call, as a person may unarchive one meanwhile. A
// record that could not be read may be of such a session, so each ID in it is needed.
func (d *Daemon) neededConversations() (func(id string) bool, error) {
	owners, known, unread := map[string][]wire.UUID{}, map[string]bool{}, map[string]bool{}
	for _, a := range d.Store.Agents() {
		known[strings.ToUpper(string(a.ID))] = true
		if a.RuntimeSessionID != nil {
			id := strings.ToLower(*a.RuntimeSessionID)
			owners[id] = append(owners[id], a.ID)
		}
	}
	entries, err := os.ReadDir(d.Store.Loc.Agents())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if known[strings.ToUpper(e.Name())] {
			continue
		}
		b, err := os.ReadFile(d.Store.Loc.Record(e.Name()))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, id := range conversationUUID.FindAllString(strings.ToLower(string(b)), -1) {
			unread[id] = true
		}
	}
	return func(id string) bool {
		return unread[id] || slices.ContainsFunc(owners[id], func(agent wire.UUID) bool {
			a := d.Store.Agent(agent)
			return a != nil && a.State != wire.AgentStateArchived
		})
	}, nil
}

// sweepAccountHome removes the conversations of home that are past their age, then the oldest
// until it is at most limit bytes; it says how many, and how many bytes are left.
func sweepAccountHome(runtimeID, home string, limit int64, now time.Time, needed func(id string) bool) (removed int, left int64) {
	all := accountConversations(runtimeID, home)
	for _, c := range all {
		left += c.bytes
	}
	slices.SortFunc(all, func(a, b *accountConversation) int { return a.at.Compare(b.at) })
	for _, c := range all {
		age := now.Sub(c.at)
		if age < accountHistoryAge && left <= limit {
			break
		}
		if c.keep || age < accountHistoryBusy || slices.ContainsFunc(c.ids, needed) {
			continue
		}
		gone := true
		for _, p := range c.paths {
			if err := os.RemoveAll(p); err != nil {
				log.Printf("retention: %v", err)
				gone = false
			}
		}
		if gone {
			left -= c.bytes
			removed++
		}
	}
	return removed, left
}

// accountConversations is each conversation in home. A folder that is a link is not looked in:
// what it leads to may be the person's own.
func accountConversations(runtimeID, home string) []*accountConversation {
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil
	}
	byID := map[string]*accountConversation{}
	seen := map[string]bool{}
	for _, pattern := range runtimeFilePatterns(runtimeID, "*", []string{accountKinds[runtimeID].variable + "=" + resolved}) {
		matches, _ := filepath.Glob(pattern)
		for _, p := range matches {
			name := conversationFile.FindStringSubmatch(filepath.Base(p))
			if name == nil || seen[p] {
				continue
			}
			seen[p] = true
			if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err != nil || dir != filepath.Dir(p) {
				continue
			}
			c := byID[name[1]]
			if c == nil {
				c = &accountConversation{ids: []string{name[1]}}
				byID[name[1]] = c
			}
			c.add(p)
		}
	}
	if runtimeID == "codex" {
		joinRollouts(byID)
	}
	all := make([]*accountConversation, 0, len(byID))
	joined := map[*accountConversation]bool{}
	for _, c := range byID {
		if !joined[c] {
			joined[c] = true
			all = append(all, c)
		}
	}
	return all
}

// add counts a file, or a folder and all in it.
func (c *accountConversation) add(path string) {
	c.paths = append(c.paths, path)
	if err := filepath.WalkDir(path, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !e.IsDir() {
			c.bytes += info.Size()
		}
		if info.ModTime().After(c.at) {
			c.at = info.ModTime()
		}
		return nil
	}); err != nil {
		c.keep = true
	}
}

// joinRollouts makes one conversation of the Codex rollouts that name each other in their
// first line: a helper's names its session's, and a fork's what it was forked from.
func joinRollouts(byID map[string]*accountConversation) {
	for _, id := range slices.Collect(maps.Keys(byID)) {
		c := byID[id]
		for _, p := range slices.Clone(c.paths) {
			line, err := rolloutHead(p)
			if err != nil {
				c.keep = true
				continue
			}
			for _, named := range conversationUUID.FindAllString(line, -1) {
				c.name(named, byID)
			}
		}
	}
}

// name joins c with the conversation of an ID that one of its rollouts names.
func (c *accountConversation) name(id string, byID map[string]*accountConversation) {
	other := byID[id]
	switch {
	case other == c:
	case other != nil:
		c.join(other)
		for _, i := range other.ids {
			if byID[i] == other {
				byID[i] = c
			}
		}
	case !slices.Contains(c.ids, id):
		// Its rollout is gone, or never was: a session may still need it.
		c.ids = append(c.ids, id)
	}
}

// join makes other a part of c.
func (c *accountConversation) join(other *accountConversation) {
	c.ids = append(c.ids, other.ids...)
	c.paths = append(c.paths, other.paths...)
	c.bytes += other.bytes
	c.keep = c.keep || other.keep
	if other.at.After(c.at) {
		c.at = other.at
	}
}

// rolloutHead is the first line of a rollout.
func rolloutHead(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, rolloutHeadBytes)).ReadString('\n')
	return line, err
}
