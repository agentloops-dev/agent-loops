// Package gitremote reads the URLs of a folder's git remotes with one git process (#6027).
// Each forge's setup once read them with two processes of its own (`git remote`, then
// `git remote get-url` for each remote), so a folder's forge took ten or more processes in
// one time limit; under load the limit ended, and the folder had no remote.
package gitremote

import (
	"context"
	"os/exec"
	"strings"
)

// URLs is the fetch URL of each of folder's git remotes, origin first, then the others in
// git's order. It is what `git remote get-url` gives for each remote, insteadOf applied.
// A folder that is not in a git repository, or a read that fails, has none.
func URLs(ctx context.Context, folder string) []string {
	out, err := exec.CommandContext(ctx, "git", "-C", folder, "remote", "-v").Output()
	if err != nil {
		return nil
	}
	return Parse(string(out))
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
