// Package gitremote reads the URLs of a folder's git remotes with one git process (#6027).
// Each forge's setup once read them with two processes of its own (`git remote`, then
// `git remote get-url` for each remote), so a folder's forge took ten or more processes in
// one time limit; under load the limit ended, and the folder had no remote.
package gitremote

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// URLs is the fetch URL of each of folder's git remotes, origin first, then the others in
// git's order. It is what `git remote get-url` gives for each remote, insteadOf applied.
// A folder that is not in a git repository, or a read that fails, has none. A caller that
// must not take a failed read for "no remote" uses Read.
func URLs(ctx context.Context, folder string) []string {
	urls, _ := Read(ctx, folder)
	return urls
}

// Read is URLs with the cause when the read fails (#6064). A folder that is not in a git
// repository has no remote and no error: that is a fact about the folder. A read that runs
// out of time, is stopped, or fails for another cause is an error: it says nothing about
// the folder's remotes, so the caller must not take it for "no remote".
func Read(ctx context.Context, folder string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", folder, "remote", "-v")
	out, err := cmd.Output()
	if err == nil {
		return Parse(string(out)), nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("read the git remotes of %s: %w", folder, ctxErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && NotARepository(string(exit.Stderr)) {
		return nil, nil
	}
	if exit != nil && len(exit.Stderr) > 0 {
		return nil, fmt.Errorf("read the git remotes of %s: %w: %s", folder, err, strings.TrimSpace(string(exit.Stderr)))
	}
	return nil, fmt.Errorf("read the git remotes of %s: %w", folder, err)
}

// NotARepository says git's stderr is the refusal of a folder that is not in a git
// repository, or that is not there.
func NotARepository(stderr string) bool {
	return strings.Contains(stderr, "not a git repository") || strings.Contains(stderr, "cannot change to")
}

// Parse reads the output of `git remote -v`: "name<TAB>url (fetch)" and "… (push)" lines.
func Parse(out string) []string {
	var origin, others []string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		name, rest, ok := strings.Cut(line, "\t")
		if !ok || seen[name] {
			continue
		}
		u, ok := strings.CutSuffix(strings.TrimSpace(rest), " (fetch)")
		if !ok || u == "" {
			continue
		}
		seen[name] = true
		if name == "origin" {
			origin = append(origin, u)
		} else {
			others = append(others, u)
		}
	}
	return append(origin, others...)
}
