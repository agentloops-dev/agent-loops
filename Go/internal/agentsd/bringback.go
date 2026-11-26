package agentsd

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Bring back from ‹host›… (#1473), the reverse of Send to ‹host›… (#1123): the host packs
// its branches with folders/sendPack, less the commits this Mac has, this host reads the
// parts through the control plane (folderbringback.go, #4640; the window carried them
// before), and folders/bringBack moves this Mac's branches forward to the host's. It never
// overwrites work done here: a branch with commits of its own, or uncommitted changes where
// a branch that moves is checked out, stops it before anything is changed. The host's uncommitted changes come back as a patch only when asked.

func (d *Daemon) bringBack(r wire.BringBackRequest) (wire.BringBack, error) {
	done := wire.BringBack{Moved: []string{}}
	// The uploads go whatever the answer, also when a check below refuses (#1531).
	var bundle, patch string
	var err error
	if r.Bundle != nil {
		if bundle, err = d.incomingFile(*r.Bundle); err != nil {
			return done, err
		}
		defer os.Remove(bundle)
	}
	if r.Patch != nil {
		if patch, err = d.incomingFile(*r.Patch); err != nil {
			return done, err
		}
		defer os.Remove(patch)
	}
	path, err := sendRepository(r.Folder)
	if err != nil {
		return done, err
	}
	patchOn, err := bringBackCheckBranches(r, patch)
	if err != nil {
		return done, err
	}
	if err := d.wholeUploads(bundle, patch); err != nil {
		return done, err
	}
	repo, _ := repositoryOf(path)
	m, _ := receiveLocks.LoadOrStore(canonical(repo.commonDir), &sync.Mutex{})
	m.(*sync.Mutex).Lock()
	defer m.(*sync.Mutex).Unlock()
	here := shortPath(path)

	if bundle != "" {
		if _, err := git(path, "fetch", "-q", "--no-tags", "--", bundle, "+refs/heads/*:refs/agents-back/*"); err != nil {
			if strings.Contains(err.Error(), "prerequisite") {
				return done, Invalid("%s is not what the machine expected. Bring back again. Nothing was changed.", here)
			}
			return done, err
		}
	}
	defer bringBackDropRefs(path)

	// Where each branch is checked out on this Mac, so it moves there with its files.
	checkedOut, err := bringBackCheckedOut(path)
	if err != nil {
		return done, err
	}

	// Every branch moves forward, or nothing moves.
	moves, err := bringBackPlanMoves(path, r.Branches, checkedOut)
	if err != nil {
		return done, err
	}

	if patch != "" {
		if err := bringBackCheckPatch(path, here, patchOn, r.Branches, patch); err != nil {
			return done, err
		}
	}

	if err := bringBackApplyMoves(path, moves, checkedOut, &done); err != nil {
		return done, err
	}
	if patch != "" {
		if err := bringBackApplyPatch(path, patch, &done); err != nil {
			return done, err
		}
	}
	return done, nil
}

// bringBackMove is one branch that moves forward, from the commit it has here (or none) to
// the host's.
type bringBackMove struct{ name, from, to string }

// bringBackCheckBranches checks the branches asked for, and that a patch comes with the branch
// it is on. It returns the branch the patch is on.
func bringBackCheckBranches(r wire.BringBackRequest, patch string) (string, error) {
	if len(r.Branches) == 0 {
		return "", Invalid("Choose a branch to bring back.")
	}
	patchOn := ""
	if r.PatchOn != nil {
		patchOn = *r.PatchOn
	}
	patchSent := false
	for _, b := range r.Branches {
		if !validBranch(b.Name) || len(b.Commit) < 40 || strings.HasPrefix(b.Commit, "-") {
			return "", Invalid("%q is not a branch this Mac can take.", b.Name)
		}
		patchSent = patchSent || b.Name == patchOn
	}
	if patch != "" && !patchSent {
		return "", Invalid("The uncommitted changes come back only with the branch they are on.")
	}
	return patchOn, nil
}

// bringBackDropRefs removes the refs the fetch of the bundle made.
func bringBackDropRefs(path string) {
	if out, _ := git(path, "for-each-ref", "--format=%(refname)", "refs/agents-back"); out != "" {
		for _, ref := range strings.Split(out, "\n") {
			if _, err := git(path, "update-ref", "-d", ref); err != nil {
				log.Printf("bring back: could not remove %s: %v", ref, err) //nolint:gosec // G706: a ref this daemon made
			}
		}
	}
}

// bringBackCheckedOut is the worktree root where each branch of the repository at path is
// checked out.
func bringBackCheckedOut(path string) (map[string]string, error) {
	checkedOut := map[string]string{}
	trees, err := worktreeList(path)
	if err != nil {
		return nil, err
	}
	for _, w := range trees {
		if w.branch != "" && !w.prunable {
			checkedOut[w.branch] = w.root
		}
	}
	return checkedOut, nil
}

// bringBackPlanMoves is the move of each branch that is not at the host's commit yet. It
// refuses when a branch has commits of its own here, or is checked out with uncommitted
// changes, before anything is changed.
func bringBackPlanMoves(path string, branches []wire.SentBranch, checkedOut map[string]string) ([]bringBackMove, error) {
	var moves []bringBackMove
	for _, b := range branches {
		if _, err := git(path, "cat-file", "-e", b.Commit+"^{commit}"); err != nil {
			return nil, Invalid("This Mac does not have the commits of %s. Bring back again. Nothing was changed.", b.Name)
		}
		have, _ := git(path, "rev-parse", "-q", "--verify", "refs/heads/"+b.Name)
		switch {
		case have == b.Commit:
			continue
		case have != "":
			if _, err := git(path, "merge-base", "--is-ancestor", have, b.Commit); err != nil {
				return nil, &Error{Code: wire.FailureFolderInTheWay, Message: b.Name + " has commits of its own on this Mac that the machine does not have. Nothing was changed."}
			}
		}
		if root, ok := checkedOut[b.Name]; ok {
			if status, err := git(root, "status", "--porcelain"); err != nil {
				return nil, err
			} else if status != "" {
				return nil, &Error{Code: wire.FailureFolderInTheWay, Message: b.Name + " is checked out in " + shortPath(root) + " with uncommitted changes. Commit or remove them, then bring back again. Nothing was changed."}
			}
		}
		moves = append(moves, bringBackMove{b.Name, have, b.Commit})
	}
	return moves, nil
}

// bringBackCheckPatch checks that the patch can apply here: its branch is checked out at
// path with no uncommitted changes, and it applies to the commit its branch moves to.
func bringBackCheckPatch(path, here, patchOn string, branches []wire.SentBranch, patch string) error {
	cur, _ := git(path, "symbolic-ref", "-q", "--short", "HEAD")
	if cur != patchOn {
		return Invalid("The uncommitted changes are on %s, and %s has %s checked out. Nothing was changed.", patchOn, here, cur)
	}
	if status, err := git(path, "status", "--porcelain"); err != nil {
		return err
	} else if status != "" {
		return &Error{Code: wire.FailureFolderInTheWay, Message: here + " has uncommitted changes. Commit or remove them to take the machine's, then bring back again. Nothing was changed."}
	}
	if err := patchApplies(path, patchOn, branches, patch); err != nil {
		return Invalid("The machine's uncommitted changes do not apply here: %s Nothing was changed.", err.Error())
	}
	return nil
}

// bringBackApplyMoves moves each branch forward, where it is checked out or by its ref, and
// adds its name to done.Moved.
func bringBackApplyMoves(path string, moves []bringBackMove, checkedOut map[string]string, done *wire.BringBack) error {
	for _, m := range moves {
		if root, ok := checkedOut[m.name]; ok {
			if _, err := git(root, "merge", "-q", "--ff-only", m.to); err != nil {
				return err
			}
		} else {
			args := []string{"update-ref", "-m", "Bring back from host", "refs/heads/" + m.name, m.to}
			if m.from != "" {
				args = append(args, m.from)
			}
			if _, err := git(path, args...); err != nil {
				return err
			}
		}
		done.Moved = append(done.Moved, m.name)
	}
	return nil
}

// bringBackApplyPatch applies the host's uncommitted changes at path, when the patch is not
// empty, and sets done.Applied.
func bringBackApplyPatch(path, patch string, done *wire.BringBack) error {
	if fi, err := os.Stat(patch); err == nil && fi.Size() > 0 {
		if _, err := git(path, "apply", "--binary", "--whitespace=nowarn", patch); err != nil {
			return Invalid("The commits came back, but the uncommitted changes did not apply: %s", err.Error())
		}
		done.Applied = true
	}
	return nil
}

// patchApplies checks the patch against the commit patchOn moves to, in an index of its
// own, so no file or index here changes.
func patchApplies(dir, patchOn string, branches []wire.SentBranch, patch string) error {
	commit := ""
	for _, b := range branches {
		if b.Name == patchOn {
			commit = b.Commit
		}
	}
	// store-bounds: not a store: a temporary git index.
	tmp, err := os.CreateTemp("", "agents-back-index-")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	env := []string{"GIT_INDEX_FILE=" + tmp.Name()}
	if _, err := gitRawEnv(dir, env, nil, "read-tree", commit); err != nil {
		return err
	}
	_, err = gitRawEnv(dir, env, nil, "apply", "--cached", "--check", "--binary", "--whitespace=nowarn", patch)
	return err
}

func (d *Daemon) registerBringBack() {
	On(d, wire.CallFoldersBringBack, func(ctx context.Context, c *Client, r wire.BringBackRequest) (wire.BringBack, error) {
		return d.bringBack(r)
	})
}
