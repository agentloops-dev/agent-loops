package agentsd

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Continue on another account (#7459). A chat whose allowance ran out goes on, as the same
// conversation, on another account of the same runtime that the person chose: the app never
// changes a session's account by itself. The runtime keeps the conversation in the account's
// own folder (Claude's CLAUDE_CONFIG_DIR, Codex's CODEX_HOME), so its record is moved from
// the old account's folder to the new one's: only the files runtimeFilePatterns names for this
// conversation, which are what Delete erases, never a sign-in. When there is no record to
// move, a new session on that account reads this one instead, as Continue in new session does.

func (d *Daemon) registerContinueOnAccount() {
	On(d, wire.CallAgentsContinueOnAccount, func(ctx context.Context, c *Client, r wire.ContinueOnAccountRequest) (wire.UUID, error) {
		return d.continueOnAccount(r)
	})
}

// continueOnAccount is agents/continueOnAccount: the session that goes on, this one or the
// new one.
func (d *Daemon) continueOnAccount(r wire.ContinueOnAccountRequest) (wire.UUID, error) {
	target := accountIDOrNil(r.AccountID)
	a := d.Store.Agent(r.AgentID)
	if a == nil {
		return "", noSuchAgent()
	}
	// A retry of a try that already went on answers as the first did: this session, now on
	// the account; or the new session it started.
	if r.RequestID != nil {
		if sameAccount(a.AccountID, target) {
			return a.ID, nil
		}
		for _, s := range d.Store.Agents() {
			if s.StartRequestID != nil && *s.StartRequestID == *r.RequestID {
				return s.ID, nil
			}
		}
	}
	if !pausedBySpentAllowance(a) {
		return "", &Error{Code: CodeInvalidParams, Message: "Only a chat whose allowance ran out can continue on another account."}
	}
	if sameAccount(a.AccountID, target) {
		return "", &Error{Code: CodeInvalidParams, Message: "This chat is on that account already."}
	}
	var account *wire.AccountProfile
	if target != nil {
		if account = d.accountProfile(a.RuntimeID, *target); account == nil {
			return "", &Error{Code: CodeInvalidParams, Message: runtimeName(a.RuntimeID) + " has no account " + *target + "."}
		}
	}
	name := runtimeName(a.RuntimeID) + " · " + ownSignInName
	if account != nil {
		name = runtimeName(a.RuntimeID) + " · " + account.Name
	}
	if err := d.accountRefusal(a.RuntimeID, account); err != nil {
		return "", err
	}
	if d.allowanceOut(accountKey(a.RuntimeID, target)) {
		return "", &Error{Code: CodeInvalidParams, Message: name + "’s allowance is out too."}
	}
	// The runtime is let go first: a runtime still open on the old account would write the
	// conversation there again.
	d.endRuntimesOf([]wire.UUID{a.ID})
	if moved := d.moveConversation(a, account); !moved {
		return d.continueInNewSessionOn(a, target, r.RequestID)
	}
	changed := d.change(a.ID, func(s *wire.Agent) {
		s.AccountID = target
		s.ContinueWhenBack = nil
	})
	if changed == nil {
		return "", noSuchAgent()
	}
	d.record(a.ID, runtimeNote("Continued on "+name+"."))
	if err := d.enqueue(a.ID, wire.QueuedPrompt{ID: store.NewUUID(), Text: continueOnAccountWords, QueuedAt: now(), From: wire.PromptOriginApp}, false); err != nil {
		return "", err
	}
	return a.ID, nil
}

// ownSignInName is the runtime's own sign-in in the account menus (AccountProfile.defaultName).
const ownSignInName = "Your Own Sign-In"

const continueOnAccountWords = "You are on another account now. Continue where you left off."

// continueInNewSessionOn is the way on when the conversation cannot move: a new session on
// the runtime and the account chosen, in the same worktree or folder, that reads this one;
// then this one is archived.
func (d *Daemon) continueInNewSessionOn(a *wire.Agent, account *string, requestID *wire.UUID) (wire.UUID, error) {
	start := wire.StartRequest{RuntimeID: a.RuntimeID, AccountID: account, Cwd: a.Cwd, Prompt: continueInNewSessionPrompt(a),
		Attachments: []wire.Attachment{}, StartOptions: a.StartOptions, AdditionalDirectories: a.AdditionalDirectories,
		McpServers: a.McpServers, Sandbox: a.SandboxOverride, RequestID: requestID}
	if start.StartOptions.Values == nil {
		start.StartOptions.Values = map[string]wire.JSONValue{}
	}
	if start.StartOptions.ExtraArguments == nil {
		start.StartOptions.ExtraArguments = []string{}
	}
	if a.Worktree != nil {
		start.Cwd = a.Worktree.Folder
		start.Worktree = &wire.WorktreeChoice{Existing: &wire.WorktreeChoiceExisting{Value: a.Worktree.Root}}
	}
	id, err := d.start(start)
	if err != nil {
		return "", err
	}
	if a.Title != nil && strings.TrimSpace(*a.Title) != "" {
		d.change(id, func(s *wire.Agent) { s.Title, s.TitleIsPersons = a.Title, a.TitleIsPersons })
	}
	if err := d.archive(a.ID); err != nil {
		return "", err
	}
	return id, nil
}

// allowanceOut is whether the credential's allowance is out now.
func (d *Daemon) allowanceOut(key string) bool {
	d.allowMu.Lock()
	defer d.allowMu.Unlock()
	s, ok := d.allowancesLocked()[key]
	return ok && s.Status.Out != nil
}

func accountIDOrNil(id string) *string {
	if id == "" || id == ownSignIn {
		return nil
	}
	return &id
}

func sameAccount(a, b *string) bool {
	return (a == nil || *a == "") == (b == nil || *b == "") && (a == nil || b == nil || *a == *b)
}

// moveConversation moves the runtime's record of the agent's conversation from the folder of
// its account to the folder of account, and is false when there is none to move: a runtime
// whose files are not known, no conversation yet, or no record where the runtime keeps it.
func (d *Daemon) moveConversation(a *wire.Agent, account *wire.AccountProfile) bool {
	if !hasRuntimeCopy(a) || !sessionIDShape.MatchString(*a.RuntimeSessionID) {
		return false
	}
	rt := runtimeByID(a.RuntimeID)
	if rt == nil {
		return false
	}
	old, err := d.accountFor(a)
	if err != nil {
		return false
	}
	from, err := d.launchAs(rt, d.Store.Loc.Root, old)
	if err != nil {
		return false
	}
	to, err := d.launchAs(rt, d.Store.Loc.Root, account)
	if err != nil {
		return false
	}
	if err := moveRuntimeFiles(a.RuntimeID, *a.RuntimeSessionID, from.Env, to.Env); err != nil {
		log.Printf("agent %s: its conversation did not move to the other account: %v", a.ID, err)
		return false
	}
	return true
}

var errNoConversation = errors.New("no record of the conversation")

// moveRuntimeFiles moves a conversation's files from the runtime's home in from to its home in
// to, at the same place under each. The first pattern is the conversation itself; without it
// nothing moves. Each is copied before any is removed, so a failed copy leaves the old account
// as it was.
func moveRuntimeFiles(runtimeID, sessionID string, from, to []string) error {
	src := runtimeFilePatterns(runtimeID, sessionID, from)
	if len(src) == 0 {
		return errNoConversation
	}
	srcHome, dstHome := runtimeHome(runtimeID, from), runtimeHome(runtimeID, to)
	if srcHome == "" || dstHome == "" {
		return errNoConversation
	}
	if filepath.Clean(srcHome) == filepath.Clean(dstHome) {
		return nil
	}
	main, _ := filepath.Glob(src[0])
	if len(main) == 0 {
		return errNoConversation
	}
	var moved []string
	for _, p := range src {
		matches, _ := filepath.Glob(p)
		moved = append(moved, matches...)
	}
	for _, m := range moved {
		rel, err := filepath.Rel(srcHome, m)
		if err != nil || strings.HasPrefix(rel, "..") {
			return errNoConversation
		}
		if err := copyTree(m, filepath.Join(dstHome, rel)); err != nil {
			return err
		}
	}
	for _, m := range moved {
		if err := os.RemoveAll(m); err != nil {
			log.Printf("continue on account: the old copy %s stays: %v", m, err)
		}
	}
	return nil
}

// runtimeHome is the folder the runtime keeps its conversations in, in the home env names:
// only for a runtime that can have more than one account (accountKinds).
func runtimeHome(runtimeID string, env []string) string {
	kind, ok := accountKinds[runtimeID]
	if !ok {
		return ""
	}
	return envHome(env, kind.variable, kind.defaultFolder)
}

// copyTree copies a file, or a folder and what is in it, to dst, readable only by its
// owner. A link is not followed or copied.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		out := filepath.Join(dst, rel)
		switch {
		case e.IsDir():
			return os.MkdirAll(out, 0o700)
		case !e.Type().IsRegular():
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return err
		}
		return copyPrivateFile(path, out)
	})
}

// copyPrivateFile copies one file through a temporary name, so a half copy is never the record.
func copyPrivateFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".moving"
	// store-bounds: not a store: the runtime's own record of a conversation, moved to another account's folder.
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
