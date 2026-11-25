package agentsd

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// What the Send sheet says of untracked files (#7121): build output that no .gitignore names
// goes with the uncommitted changes, so the sheet shows how much there is first. The measure
// is bounded in files and time, so the sheet stays fast in a large tree.

const (
	untrackedFilesLimit = 20_000
	untrackedTimeLimit  = 2 * time.Second
	untrackedLargest    = 3
)

// measureUntracked is what git add -A would take of untracked files in dir's repository, or
// nil when there are none or git does not say: a measure never stops a send.
func measureUntracked(dir string, most int, limit time.Duration) *wire.SendUntracked {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil
	}
	// The untracked folders themselves, then the files: the first names the second.
	roots, cut := untrackedPaths(ctx, top, most, "--directory", "--no-empty-directory")
	isRoot := map[string]bool{}
	for _, r := range roots {
		isRoot[r] = true
	}
	out := &wire.SendUntracked{Largest: []wire.SendUntrackedItem{}, Partial: cut}
	sizes := map[string]int64{}
	paths, cut := untrackedPaths(ctx, top, most)
	out.Partial = out.Partial || cut
	for _, p := range paths {
		if ctx.Err() != nil {
			out.Partial = true
			break
		}
		name, build := untrackedName(p, isRoot)
		out.Files++
		out.BuildOutput = out.BuildOutput || build
		// A link goes as its own text, so Lstat. A nested repository goes as its commit alone.
		if fi, err := os.Lstat(filepath.Join(top, filepath.FromSlash(p))); err == nil && fi.Mode()&fs.ModeDir == 0 {
			out.Size += fi.Size()
			sizes[name] += fi.Size()
		}
	}
	if out.Files == 0 && !out.Partial {
		return nil
	}
	for path, size := range sizes {
		out.Largest = append(out.Largest, wire.SendUntrackedItem{Path: path, Size: size})
	}
	slices.SortFunc(out.Largest, func(a, b wire.SendUntrackedItem) int {
		if a.Size != b.Size {
			return int(min(max(b.Size-a.Size, -1), 1))
		}
		return strings.Compare(a.Path, b.Path)
	})
	out.Largest = out.Largest[:min(untrackedLargest, len(out.Largest))]
	return out
}

// untrackedName is the folder that p counts to: the build output it is in, else the
// untracked folder it is in, else p itself. It also says if it is build output.
func untrackedName(p string, isRoot map[string]bool) (name string, build bool) {
	name = p
	root := false
	for i := range len(p) {
		if p[i] != '/' {
			continue
		}
		folder := p[:i+1]
		if slices.Contains(buildOutputNames, p[strings.LastIndexByte(p[:i], '/')+1:i]) {
			return folder, true
		}
		if !root && isRoot[folder] {
			name, root = folder, true
		}
	}
	return name, false
}

// untrackedPaths is git's untracked, not ignored paths, at most most of them; cut says that
// there can be more: the number or ctx's time ended, or git failed.
func untrackedPaths(ctx context.Context, top string, most int, more ...string) (paths []string, cut bool) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	args := append([]string{"ls-files", "--others", "--exclude-standard", "-z"}, more...)
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git itself, with no shell
	cmd.Dir = top
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	found := &nulPaths{most: most, full: stop}
	cmd.Stdout = found
	err := cmd.Run()
	return found.paths, err != nil || found.cut
}

// nulPaths keeps the first most NUL-ended paths written to it, and calls full at one more.
type nulPaths struct {
	most  int
	full  func()
	rest  []byte
	paths []string
	cut   bool
}

func (n *nulPaths) Write(b []byte) (int, error) {
	if n.cut {
		return len(b), nil
	}
	n.rest = append(n.rest, b...)
	for {
		p, rest, ok := bytes.Cut(n.rest, []byte{0})
		if !ok {
			return len(b), nil
		}
		if len(n.paths) == n.most {
			n.cut, n.rest = true, nil
			n.full()
			return len(b), nil
		}
		n.paths, n.rest = append(n.paths, string(p)), rest
	}
}
