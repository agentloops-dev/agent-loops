package agentsd

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Send to ‹host›… (#1123): a Mac sends its repository here as a git bundle of the branches
// it chose, plus a patch of its uncommitted changes, in parts of at most 16 MB
// (folders/receiveUpload). folders/receive then makes the copy, or brings it up to date.
// It never overwrites work done here: changes or commits of the host's own stop it before
// anything is changed. Changes that the last send put here are not the host's own: a
// fingerprint of them is kept in .git/agents-sent, and the next send replaces them.

const receivePartLimit = 16 << 20

// receiveLimit is the most one upload may hold: a bundle of a large history.
const receiveLimit = 8 << 30

var receiveLocks sync.Map

func (d *Daemon) incomingFolder() string { return filepath.Join(d.Store.Loc.Root, "Incoming") }

func (d *Daemon) incomingFile(id wire.UUID) (string, error) {
	name := string(id)
	if name == "" || strings.ContainsAny(name, "/\\.") {
		return "", Invalid("That is not an upload.")
	}
	return filepath.Join(d.incomingFolder(), name), nil
}

func (d *Daemon) receiveUpload(r wire.ReceiveUploadRequest) error {
	if len(r.Data) > receivePartLimit {
		return Invalid("A part is more than 16 MB.")
	}
	path, err := d.incomingFile(r.Upload)
	if err != nil {
		return err
	}
	if r.Offset+int64(len(r.Data)) > receiveLimit {
		return Invalid("That is more than 8 GB.")
	}
	if r.Offset < 0 {
		return Invalid("That is not a part of an upload.")
	}
	return d.writePart(r, path)
}

// receiveFolder is the folder a send names: absolute, or under the home with ~/.
func receiveFolder(p string) (string, error) {
	path := filepath.Clean(expandHome(strings.TrimSpace(p)))
	if !filepath.IsAbs(path) {
		return "", Invalid("Give a folder that starts with / or ~/.")
	}
	return path, nil
}

// ownTree is the commit dir is on and the tree of everything in it that git does not
// ignore, made with an index of its own so the folder's index is not changed.
func ownTree(dir string) (head, tree string, err error) { return ownTreeOf(dir, true) }

// ownTreeOf is ownTree, without the files git does not track unless untracked (#7121).
func ownTreeOf(dir string, untracked bool) (head, tree string, err error) {
	index, err := git(dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp("", "agents-send-index-")
	if err != nil {
		return "", "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if b, err := os.ReadFile(index); err == nil {
		if err := os.WriteFile(tmp.Name(), b, 0o600); err != nil { //nolint:gosec // G703: our own CreateTemp file
			_ = os.Remove(tmp.Name()) // with no copy, git add makes an index from nothing: slower, the same tree
		}
	} else {
		os.Remove(tmp.Name())
	}
	env := []string{"GIT_INDEX_FILE=" + tmp.Name()}
	add := "-A"
	if !untracked {
		add = "-u"
		// With no copy of the index nothing is tracked, and every file would be a deletion:
		// the commit's files are the tracked ones then.
		if fi, err := os.Stat(tmp.Name()); err != nil || fi.Size() == 0 {
			_ = os.Remove(tmp.Name()) // git reads an empty file as a broken index
			if _, err := gitRawEnv(dir, env, nil, "read-tree", "HEAD"); err != nil {
				return "", "", err
			}
		}
	}
	if _, err := gitRawEnv(dir, env, nil, "add", add); err != nil {
		return "", "", err
	}
	out, err := gitRawEnv(dir, env, nil, "write-tree")
	if err != nil {
		return "", "", err
	}
	// A branch with no commit yet is git's own answer, with no head (exit 1, no words). A git
	// that gave no answer under load, or any other failure, is not "no head" (#6345): an
	// empty head made the send's diff fail with "ambiguous argument ''", and a fingerprint
	// with no head made a sent folder's changes look like its own.
	head, err = git(dir, "rev-parse", "-q", "--verify", "HEAD")
	if err != nil && !gitSaidNo(err, 1) {
		return "", "", err
	}
	return head, strings.TrimSpace(string(out)), nil
}

// worktreeFingerprint is ownTree as one line, for .git/agents-sent.
func worktreeFingerprint(dir string) (string, error) {
	head, tree, err := ownTree(dir)
	return head + " " + tree, err
}

func sentMark(dir string) string {
	p, _ := git(dir, "rev-parse", "--path-format=absolute", "--git-path", "agents-sent")
	return p
}

// changes is whether dir has uncommitted changes, and whether they are the host's own:
// not the ones the last send left.
func changes(dir string) (changed, own bool, err error) {
	status, err := git(dir, "status", "--porcelain")
	if err != nil {
		return false, false, err
	}
	if status == "" {
		return false, false, nil
	}
	mark, _ := os.ReadFile(sentMark(dir))
	now, err := worktreeFingerprint(dir)
	if err != nil {
		return true, true, err
	}
	return true, strings.TrimSpace(string(mark)) != now, nil
}

func (d *Daemon) receiveState(r wire.ReceiveStateRequest) (wire.ReceiveState, error) {
	path, err := receiveFolder(r.Folder)
	if err != nil {
		return wire.ReceiveState{}, err
	}
	s := wire.ReceiveState{Folder: store.FolderURL(path), Heads: []string{}}
	fi, err := os.Stat(path)
	if err != nil {
		return s, nil
	}
	s.Exists = fi.IsDir()
	repo, ok := repositoryOf(path)
	if !ok || canonical(repo.toplevel) != canonical(path) {
		return s, nil
	}
	s.Repository = true
	_, own, err := changes(path)
	if err != nil {
		return s, err
	}
	s.OwnChanges = own
	s.Heads = folderHeads(path)
	return s, nil
}

func validBranch(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") {
		return false
	}
	_, err := git(os.TempDir(), "check-ref-format", "refs/heads/"+name)
	return err == nil
}

func (d *Daemon) receive(r wire.ReceiveRequest) (wire.FolderSummary, error) {
	return d.receiveInto(r, false)
}

// errFolderChanged says that receiveInto left a folder with changes as it is.
var errFolderChanged = errors.New("the folder has changes")

// receiveInto is receive; with keepChanges it does nothing to a folder that has changes,
// the last send's too, and says errFolderChanged (#6890).
func (d *Daemon) receiveInto(r wire.ReceiveRequest, keepChanges bool) (summary wire.FolderSummary, err error) {
	// The uploads go whatever the answer, also when a check below refuses (#1531).
	var bundle, patch string
	if r.Bundle != nil {
		if bundle, err = d.incomingFile(*r.Bundle); err != nil {
			return summary, err
		}
		defer os.Remove(bundle)
	}
	if r.Patch != nil {
		if patch, err = d.incomingFile(*r.Patch); err != nil {
			return summary, err
		}
		defer os.Remove(patch)
	}
	if r.Seed != nil {
		defer d.dropSeed(*r.Seed) // a receive that refuses below uses no seed
	}
	path, err := receiveFolder(r.Folder)
	if err != nil {
		return summary, err
	}
	if err := receiveCheckBranches(r); err != nil {
		return summary, err
	}
	if err := d.wholeUploads(bundle, patch); err != nil {
		return summary, err
	}
	m, _ := receiveLocks.LoadOrStore(path, &sync.Mutex{})
	m.(*sync.Mutex).Lock()
	defer m.(*sync.Mutex).Unlock()
	if keepChanges && isDir(path) {
		if changed, _, err := changes(path); err == nil && changed {
			return summary, errFolderChanged
		}
	}
	return d.receiveIntoLocked(r, path, bundle, patch)
}

// receiveCheckBranches refuses a send with no branch, a branch name or commit this machine
// cannot take, or a branch to check out that is not one of those sent.
func receiveCheckBranches(r wire.ReceiveRequest) error {
	if len(r.Branches) == 0 {
		return Invalid("Choose a branch to send.")
	}
	checkoutSent := false
	for _, b := range r.Branches {
		if !validBranch(b.Name) || len(b.Commit) < 40 || strings.HasPrefix(b.Commit, "-") {
			return Invalid("%q is not a branch this machine can take.", b.Name)
		}
		checkoutSent = checkoutSent || b.Name == r.Checkout
	}
	if !checkoutSent {
		return Invalid("The branch to check out is not one of those sent.")
	}
	return nil
}

// receiveIntoLocked puts the sent branches, origin and changes into the folder at path.
// When it fails, it removes what it made. Called with the folder's receive lock held.
func (d *Daemon) receiveIntoLocked(r wire.ReceiveRequest, path, bundle, patch string) (summary wire.FolderSummary, err error) {
	here := shortPath(path)
	fresh, made, err := receivePrepareFolder(path, here)
	if err != nil {
		return summary, err
	}
	defer func() {
		switch {
		case err == nil:
		case made:
			os.RemoveAll(path)
		case fresh:
			os.RemoveAll(filepath.Join(path, ".git"))
		}
	}()
	staged := ""
	if r.Seed != nil {
		var used func()
		staged, used = d.takeSeed(*r.Seed, path)
		defer used()
	}
	if err := receiveInitOrCheckChanges(path, here, staged, fresh); err != nil {
		return summary, err
	}

	if bundle != "" {
		if err := receiveFetchBundle(path, bundle); err != nil {
			return summary, err
		}
	}
	defer receiveDropSentRefs(path)

	moves, err := receivePlanMoves(path, here, r.Branches)
	if err != nil {
		return summary, err
	}
	if err := receiveApplyMoves(path, fresh, moves, r.Checkout); err != nil {
		return summary, err
	}
	if r.Origin != nil && *r.Origin != "" {
		if err := receiveSetOrigin(path, *r.Origin); err != nil {
			return summary, err
		}
	}
	os.Remove(sentMark(path))
	if patch != "" {
		if err := receiveApplyPatch(path, patch); err != nil {
			return summary, err
		}
	}
	return d.addFolderOf(store.FolderURL(path), false, "")
}

// receivePrepareFolder makes the folder at path when it is not there, and refuses a file or
// a folder with files that is not a Git repository. fresh is true when the folder has no
// repository yet; made is true when this call made the folder.
func receivePrepareFolder(path, here string) (fresh, made bool, err error) {
	if fi, err := os.Stat(path); err != nil {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return false, false, &Error{Code: wire.FailureFolderInTheWay, Message: "Could not make " + here + ": " + err.Error()}
		}
		return true, true, nil
	} else if !fi.IsDir() {
		return false, false, &Error{Code: wire.FailureFolderInTheWay, Message: here + " is a file. Nothing in it was changed."}
	} else if repo, ok := repositoryOf(path); !ok || canonical(repo.toplevel) != canonical(path) {
		if entries, _ := os.ReadDir(path); len(entries) > 0 {
			return false, false, &Error{Code: wire.FailureFolderInTheWay, Message: here + " is already there and is not a Git repository. Nothing in it was changed."}
		}
		return true, false, nil
	}
	return false, false, nil
}

// receiveInitOrCheckChanges makes a repository in a fresh folder, from the one staged by a
// seed when it is not "" (#7119), or refuses a repository that has changes of its own.
func receiveInitOrCheckChanges(path, here, staged string, fresh bool) error {
	if fresh && staged != "" {
		return receiveStartFromSeed(path, staged)
	}
	if fresh {
		_, err := git(path, "init", "-q")
		return err
	}
	_, own, err := changes(path)
	if err != nil {
		return err
	}
	if own {
		return &Error{Code: wire.FailureFolderInTheWay, Message: here + " has changes of its own on this machine. Commit or remove them there, then send again. Nothing was changed."}
	}
	return nil
}

// receiveFetchBundle fetches the bundle's branches into refs/agents-send.
func receiveFetchBundle(path, bundle string) error {
	if _, err := git(path, "fetch", "-q", "--no-tags", "--", bundle, "+refs/heads/*:refs/agents-send/*"); err != nil {
		if strings.Contains(err.Error(), "prerequisite") {
			return Invalid("The copy on this machine is not what the Mac expected. Send again. Nothing was changed.")
		}
		return err
	}
	return nil
}

// receiveDropSentRefs removes every ref under refs/agents-send.
func receiveDropSentRefs(path string) {
	if out, _ := git(path, "for-each-ref", "--format=%(refname)", "refs/agents-send"); out != "" {
		for _, ref := range strings.Split(out, "\n") {
			if _, err := git(path, "update-ref", "-d", ref); err != nil {
				log.Printf("receive: could not remove %s: %v", ref, err) //nolint:gosec // G706: a ref this daemon made
			}
		}
	}
}

// receiveMove is a branch to move: its ref, the commit it has now ("" for a new branch),
// and the commit it moves to.
type receiveMove struct{ ref, from, to string }

// receivePlanMoves is the move of each sent branch. Every branch moves forward, or the send
// stops before any moves.
func receivePlanMoves(path, here string, branches []wire.SentBranch) ([]receiveMove, error) {
	var moves []receiveMove
	for _, b := range branches {
		if _, err := git(path, "cat-file", "-e", b.Commit+"^{commit}"); err != nil {
			return nil, Invalid("This machine does not have the commits of %s. Send again. Nothing was changed.", b.Name)
		}
		ref := "refs/heads/" + b.Name
		have, _ := git(path, "rev-parse", "-q", "--verify", ref)
		switch {
		case have == b.Commit:
		case have == "":
			moves = append(moves, receiveMove{ref, "", b.Commit})
		default:
			if _, err := git(path, "merge-base", "--is-ancestor", have, b.Commit); err != nil {
				return nil, &Error{Code: wire.FailureFolderInTheWay, Message: b.Name + " in " + here + " has commits of its own on this machine. Nothing was changed."}
			}
			moves = append(moves, receiveMove{ref, have, b.Commit})
		}
	}
	return moves, nil
}

// receiveApplyMoves clears what the last send left (when the folder is not fresh), moves
// the branches and checks out the branch to check out.
func receiveApplyMoves(path string, fresh bool, moves []receiveMove, checkout string) error {
	if !fresh {
		// What is left is the last send's changes, or nothing: clear it for this send.
		if _, err := git(path, "reset", "-q", "--hard"); err != nil {
			return err
		}
		if _, err := git(path, "clean", "-fdq"); err != nil {
			return err
		}
	}
	for _, m := range moves {
		args := []string{"update-ref", "-m", "Send to host", m.ref, m.to}
		if m.from != "" {
			args = append(args, m.from)
		}
		if _, err := git(path, args...); err != nil {
			return err
		}
	}
	if _, err := git(path, "symbolic-ref", "HEAD", "refs/heads/"+checkout); err != nil {
		return err
	}
	_, err := git(path, "reset", "-q", "--hard")
	return err
}

// receiveSetOrigin sets the origin remote to origin, or adds it when there is none. The same
// address with this machine's own credential in it stays: a send brings none (#7119).
func receiveSetOrigin(path, origin string) error {
	have, err := git(path, "remote", "get-url", "origin")
	if err == nil && withoutCredential(have) == origin {
		return nil
	}
	if err == nil {
		_, err = git(path, "remote", "set-url", "origin", origin)
	} else {
		_, err = git(path, "remote", "add", "origin", origin)
	}
	return err
}

// receiveApplyPatch applies the uncommitted changes in patch, when it is not empty, and
// marks the folder with its fingerprint.
func receiveApplyPatch(path, patch string) error {
	if fi, err := os.Stat(patch); err == nil && fi.Size() > 0 {
		if _, err := git(path, "apply", "--binary", "--whitespace=nowarn", patch); err != nil {
			return Invalid("The commits arrived, but the uncommitted changes did not apply: %s", err.Error())
		}
		if now, err := worktreeFingerprint(path); err == nil {
			if err := os.WriteFile(sentMark(path), []byte(now+"\n"), 0o600); err != nil {
				log.Printf("receive: could not mark %s as sent: %v", path, err)
			}
		}
	}
	return nil
}

func (d *Daemon) registerReceive() {
	os.RemoveAll(d.incomingFolder())
	if err := os.RemoveAll(d.seedsFolder()); err != nil {
		log.Printf("receive: could not remove old seeds: %v", err)
	}
	On(d, wire.CallFoldersReceiveSeed, func(ctx context.Context, c *Client, r wire.ReceiveSeedRequest) (wire.ReceiveSeed, error) {
		return d.receiveSeed(r)
	})
	On(d, wire.CallFoldersReceiveUpload, func(ctx context.Context, c *Client, r wire.ReceiveUploadRequest) (wire.Empty, error) {
		return wire.Empty{}, d.receiveUpload(r)
	})
	On(d, wire.CallFoldersReceiveState, func(ctx context.Context, c *Client, r wire.ReceiveStateRequest) (wire.ReceiveState, error) {
		return d.receiveState(r)
	})
	On(d, wire.CallFoldersReceive, func(ctx context.Context, c *Client, r wire.ReceiveRequest) (wire.FolderSummary, error) {
		summary, err := d.receive(r)
		if err == nil {
			if path, err := receiveFolder(r.Folder); err == nil {
				_ = d.afterReceive(path) // the send does not wait for the script
			}
		}
		return summary, err
	})
}
