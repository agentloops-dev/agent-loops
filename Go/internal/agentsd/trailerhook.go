package agentsd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/execfile"
	"github.com/agentloops-dev/agent-loops/Go/internal/policylayers"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The commit-msg hook of an app worktree (#6372, epic #6365): each worktree the app makes gets
// a hooks folder of its own, set in that worktree's own config (extensions.worktreeConfig,
// core.hooksPath with --worktree), so the main checkout and the folder's other worktrees do
// not change. It is a git hook, not a runtime hook: it never holds an agent's tool use.
//
//   - Each hook in the folder runs every hook of the folder's own (core.hooksPath from any
//     other scope, as Husky sets it; else the repository's hooks folder, where lefthook and
//     pre-commit write theirs), read at each run, so a hook the folder adds later still runs.
//   - commit-msg runs the folder's own commit-msg first; when it refuses the commit, that
//     stands. Then, in a session (its grant in the environment), agents-trailers MSGFILE asks
//     agentsd for git/trailers and writes the block as the message's last paragraph: it takes
//     out a trailer the rule turns off and adds what it turns on; other trailers stay.
//   - With no session (a person's own commit) it writes nothing. When agentsd does not answer
//     it leaves the message as it is and prints one line. It never refuses a commit.
//   - `git commit --no-verify` skips it; the squash message (#6362) still applies the rule.

const (
	// trailerHooksDir is the hooks folder's name in the worktree's own git dir
	// (.git/worktrees/<name>/), which goes with the worktree.
	trailerHooksDir = "agents-hooks"
	// trailerHookScript is the one script that each hook's name links to.
	trailerHookScript = "agents-hook"
	// trailersCommandPath is agents-trailers' endpoint, beside agents-secret's, with its grant.
	trailersCommandPath = "/git/trailers"
	// trailersCommandName is the command on a session's PATH, beside agents-secret.
	trailersCommandName = "agents-trailers"
)

// gitHookNames is every hook git runs in a checkout (githooks(5)). The worktree's folder links
// commit-msg, and each of these that the folder's own hooks folder has (#6529), so the
// folder's own still run while core.hooksPath names the worktree's folder. A name the folder
// does not have gets no link: git runs reference-transaction three times for each ref a fetch
// or a rebase updates, and each run of agents-hook starts four processes. The server-side
// hooks never run here.
var gitHookNames = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit",
	"prepare-commit-msg", "commit-msg", "post-commit", "pre-rebase", "post-checkout", "post-merge",
	"pre-push", "post-rewrite", "reference-transaction", "push-to-checkout", "pre-auto-gc",
	"sendemail-validate", "fsmonitor-watchman", "post-index-change", "p4-changelist",
	"p4-prepare-changelist", "p4-post-changelist", "p4-pre-submit",
}

// trailerHookBody is agents-hook. It finds the folder's own hooks folder at each run: the last
// core.hooksPath of a scope other than this worktree's, else the repository's hooks folder.
const trailerHookBody = `#!/bin/sh
# Agent Loops writes this file (#6372). It runs the folder's own hook of the same name, and on
# commit-msg, in an agent's session, writes the commit's trailer block from git/trailers.
# It never refuses a commit of its own. git commit --no-verify skips it.
hook=$(basename "$0")
here=$(cd "$(dirname "$0")" && pwd -P)
own=$(git config --show-scope --get-all core.hooksPath 2>/dev/null | awk -F'\t' '$1 != "worktree" { v = $2 } END { print v }')
case "$own" in
"") own="$(git rev-parse --git-common-dir)/hooks" ;;
"~/"*) own="$HOME/${own#"~/"}" ;;
esac
if [ -x "$own/$hook" ] && [ "$(cd "$own" 2>/dev/null && pwd -P)" != "$here" ]; then
	if [ "$hook" != commit-msg ]; then
		exec "$own/$hook" "$@"
	fi
	"$own/$hook" "$@" || exit $?
fi
[ "$hook" = commit-msg ] || exit 0
[ -n "$AGENTS_SECRET_GRANT" ] || exit 0
if command -v agents-trailers >/dev/null 2>&1; then
	agents-trailers "$1" || true
fi
exit 0
`

// installTrailerHook gives the worktree at root its own hooks folder. It changes the
// repository's config only to turn on extensions.worktreeConfig; it leaves a repository that
// keeps core.bare or core.worktree in its shared config as it is, as git asks there.
func installTrailerHook(root string) error {
	gitDir, err := git(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("no git dir: %w", err)
	}
	common, err := git(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("no common dir: %w", err)
	}
	if filepath.Clean(gitDir) == filepath.Clean(common) {
		return fmt.Errorf("%s is the main checkout, not a worktree", root)
	}
	shared := filepath.Join(common, "config")
	if v, _ := git(root, "config", "--file", shared, "--get", "core.worktree"); v != "" {
		return fmt.Errorf("the repository sets core.worktree in %s", shared)
	}
	if v, _ := git(root, "config", "--file", shared, "--bool", "--get", "core.bare"); v == "true" {
		return fmt.Errorf("the repository is bare")
	}
	dir := filepath.Join(gitDir, trailerHooksDir)
	if err := writeTrailerHooks(dir, trailerHookNames(ownHooksDir(root, common, dir))); err != nil {
		return err
	}
	if v, _ := git(root, "config", "--bool", "--get", "extensions.worktreeConfig"); v != "true" {
		if _, err := git(root, "config", "extensions.worktreeConfig", "true"); err != nil {
			return fmt.Errorf("extensions.worktreeConfig: %w", err)
		}
	}
	if v, _ := git(root, "config", "--worktree", "--get", "core.hooksPath"); v == dir {
		return nil
	}
	if _, err := git(root, "config", "--worktree", "core.hooksPath", dir); err != nil {
		return fmt.Errorf("core.hooksPath: %w", err)
	}
	return nil
}

// ownHooksDir is the folder's own hooks folder, as agents-hook finds it at each run: the last
// core.hooksPath of a scope other than this worktree's, else the repository's hooks folder.
// It is "" when that is the worktree's own folder (mine).
func ownHooksDir(root, common, mine string) string {
	own := ""
	out, _ := git(root, "config", "--show-scope", "--get-all", "core.hooksPath")
	for _, l := range strings.Split(out, "\n") {
		if scope, v, ok := strings.Cut(l, "\t"); ok && scope != "worktree" {
			own = v
		}
	}
	switch {
	case own == "":
		own = filepath.Join(common, "hooks")
	case strings.HasPrefix(own, "~/"):
		home, _ := os.UserHomeDir()
		own = filepath.Join(home, own[2:])
	case !filepath.IsAbs(own):
		own = filepath.Join(root, own) // git runs a hook at the worktree's top
	}
	if filepath.Clean(own) == filepath.Clean(mine) {
		return ""
	}
	return own
}

// trailerHookNames is commit-msg and each hook's name that own has as a program git runs.
func trailerHookNames(own string) map[string]bool {
	names := map[string]bool{"commit-msg": true}
	if own == "" {
		return names
	}
	for _, name := range gitHookNames {
		if fi, err := os.Stat(filepath.Join(own, name)); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			names[name] = true
		}
	}
	return names
}

// writeTrailerHooks writes agents-hook in dir, a link to it for each of names, and takes out
// a link of an older install that names does not have (#6529).
func writeTrailerHooks(dir string, names map[string]bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: git's own hooks folder's mode
		return err
	}
	script := filepath.Join(dir, trailerHookScript)
	if old, err := os.ReadFile(script); err != nil || string(old) != trailerHookBody {
		tmp := script + ".tmp"
		// store-bounds: not a store: an install: the git hook.
		if err := execfile.Write(tmp, []byte(trailerHookBody), 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, script); err != nil {
			return err
		}
	}
	for _, name := range gitHookNames {
		path := filepath.Join(dir, name)
		target, err := os.Readlink(path)
		linked := err == nil && target == trailerHookScript
		if !names[name] {
			if linked {
				_ = os.Remove(path)
			}
			continue
		}
		if linked {
			continue
		}
		_ = os.Remove(path) // a file of an older install, or none
		if err := os.Symlink(trailerHookScript, path); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

// installTrailerHookAt is installTrailerHook for a worktree the app made, with what went wrong
// in the log: a worktree with no hook still works, only its commits have no block.
func installTrailerHookAt(wt *wire.AgentWorktree) {
	if wt == nil || !wt.MadeByApp {
		return
	}
	if err := installTrailerHook(canonical(store.FolderPath(wt.Root))); err != nil {
		log.Printf("commit-msg hook: not in %s: %v", wt.Name, err)
	}
}

// refreshTrailerHooks installs the hook again, off the caller, in each worktree the app made
// that an agent not archived works in: a worktree from before #6529 loses the links of every
// hook's name, and each gets the names its folder's hooks folder has now.
func (d *Daemon) refreshTrailerHooks() {
	var wts []*wire.AgentWorktree
	seen := map[wire.URL]bool{}
	for _, a := range d.Store.Agents() {
		if a.State == wire.AgentStateArchived || a.Worktree == nil || !a.Worktree.MadeByApp || seen[a.Worktree.Root] {
			continue
		}
		seen[a.Worktree.Root] = true
		wts = append(wts, a.Worktree)
	}
	if len(wts) == 0 {
		return
	}
	d.goJob("commit-msg hooks", func() {
		for _, wt := range wts {
			if isDir(store.FolderPath(wt.Root)) {
				installTrailerHookAt(wt)
			}
		}
	})
}

// trailersAnswer is the endpoint's answer to agents-trailers: git/trailers' lines, and the
// keys the rule turns off.
type trailersAnswer struct {
	Lines []string `json:"lines"`
	Off   []string `json:"off"`
}

// serveTrailers answers agents-trailers: the block of the grant's session, as git/trailers
// gives it, and the keys the rule turns off, as JSON.
func (d *Daemon) serveTrailers(w http.ResponseWriter, r *http.Request) {
	agent, ok := d.commandGrantPost(w, r)
	if !ok {
		return
	}
	block, err := d.gitTrailers(r.Context(), wire.GitTrailersRequest{Session: agent})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, err.Error())
		return
	}
	answer := trailersAnswer{Lines: block.Lines, Off: []string{}}
	if a := d.Store.Agent(agent); a != nil {
		on := policylayers.TrailersOn(d.effective(folderPath(a)))
		for _, t := range policylayers.Trailers {
			if !on[t.Key] {
				answer.Off = append(answer.Off, t.Key)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(answer)
}

// TrailersCommand is `agentsd trailers MSGFILE`, run as agents-trailers by the worktree's
// commit-msg hook: it writes the session's trailer block into the message file. It returns 0
// whatever happens, and says in one line on errOut when the message is left as it is.
func TrailersCommand(args []string, getenv func(string) string, errOut io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(errOut, "usage: agents-trailers MSGFILE\n"+
			"Writes the trailer block of this session's commit into the message, as the folder's commitTrailers rule says. The worktree's commit-msg hook runs it.")
		return 0
	}
	say := func(why string) int {
		fmt.Fprintln(errOut, "agents-trailers: the commit has no trailer block: "+why)
		return 0
	}
	base, grant := getenv(secretURLEnv), getenv(secretGrantEnv)
	if base == "" || grant == "" {
		return 0 // not a session's commit
	}
	u, err := url.Parse(base)
	if err != nil {
		return say(err.Error())
	}
	u.Path, u.RawQuery = trailersCommandPath, ""
	ctx, cancel := context.WithTimeout(context.Background(), 3*trailerForgeWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return say(err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+grant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return say("agentsd did not answer (" + err.Error() + ")")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return say(err.Error())
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return say("agentsd does not know this session's grant (the runtime ended or started again)")
		}
		return say("agentsd said " + resp.Status + " " + strings.TrimSpace(string(b)))
	}
	var answer trailersAnswer
	if err := json.Unmarshal(b, &answer); err != nil {
		return say(err.Error())
	}
	path := args[0]
	fi, err := os.Stat(path)
	if err != nil {
		return say(err.Error())
	}
	msg, err := os.ReadFile(path)
	if err != nil {
		return say(err.Error())
	}
	next := rewriteTrailers(string(msg), commentChar(), answer.Lines, answer.Off)
	if next == string(msg) {
		return 0
	}
	// store-bounds: not a store: the commit message file that git gives the hook.
	if err := os.WriteFile(path, []byte(next), fi.Mode().Perm()); err != nil { //nolint:gosec // G703: git names the message file to the hook; it is the file to write
		return say(err.Error())
	}
	return 0
}

// commentChar is the message's comment character: core.commentChar, else '#' (also for auto,
// whose character git chose from the message, almost always '#').
func commentChar() string {
	v, err := git(".", "config", "--get", "core.commentChar")
	if err != nil || v == "" || v == "auto" {
		return "#"
	}
	return v
}

// singleTrailers are the keys with one value a commit: the block's line takes the place of
// one already there. The others (Fixes, Refs, Requested-by, Approved-by) add a line that is
// not there yet.
var singleTrailers = map[string]bool{
	"managed-by": true, "runtime": true, "model": true, "session": true, "epic": true,
	"cost": true, "tokens": true, "duration": true,
}

// runtimesOwnTrailer is the key the hook never adds, changes or removes (#7174): a runtime's
// Co-authored-by is its own, also when an old layer still turns "Co-authored-by" off.
const runtimesOwnTrailer = "co-authored-by"

// rewriteTrailers is msg with the block as its last paragraph: each trailer of a key in off
// taken out (Fixes also takes out Refs), each line of lines added unless it is there, in place
// of a single-valued key's line. Other trailers stay where they are: a runtime's own
// Co-authored-by too, whatever off and lines say, so it and the block end in one last
// paragraph, where git reads both. Comment lines and a
// scissors line at the end (an editor's template, `commit -v`) stay after it, for git's
// cleanup. A message with no words is left as it is, for git to refuse.
func rewriteTrailers(msg, comment string, lines, off []string) string {
	body, tail := splitMessage(msg, comment)
	if len(body) == 0 {
		return msg
	}
	body, kept := cutTrailerBlock(body)
	block := mergeTrailers(kept, lines, off)
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	out := append([]string{}, body...)
	if len(block) > 0 {
		out = append(append(out, ""), block...)
	}
	if len(tail) > 0 && strings.TrimSpace(tail[0]) != "" {
		out = append(out, "")
	}
	out = append(out, tail...)
	return strings.Join(out, "\n") + "\n"
}

// splitMessage is msg's lines, up to the comment lines and the scissors line at its end, and
// those lines.
func splitMessage(msg, comment string) (body, tail []string) {
	all := strings.Split(strings.TrimSuffix(msg, "\n"), "\n")
	scissors := comment + " ------------------------ >8 ------------------------"
	end := len(all)
	for i, l := range all {
		if l == scissors {
			end = i
			break
		}
	}
	for end > 0 && (strings.TrimSpace(all[end-1]) == "" || strings.HasPrefix(all[end-1], comment)) {
		end--
	}
	return all[:end], all[end:]
}

// cutTrailerBlock is body without its last paragraph when that is a trailer block (not the
// subject), and each of its trailers with its continuation lines.
func cutTrailerBlock(body []string) (rest []string, trailers [][]string) {
	start := 0
	for i := len(body) - 1; i >= 0; i-- {
		if strings.TrimSpace(body[i]) == "" {
			start = i + 1
			break
		}
	}
	if start == 0 || !isTrailerBlock(body[start:]) {
		return body, nil
	}
	for _, l := range body[start:] {
		if l[0] == ' ' || l[0] == '\t' {
			trailers[len(trailers)-1] = append(trailers[len(trailers)-1], l)
			continue
		}
		trailers = append(trailers, []string{l})
	}
	return body[:start], trailers
}

// mergeTrailers is the new block: the trailers kept that the rule does not turn off, then
// each of lines that is not there yet. A single-valued key's line stays where it is when it
// is the block's own; another of that key goes.
func mergeTrailers(kept [][]string, lines, off []string) []string {
	drop := map[string]bool{}
	for _, k := range off {
		if strings.EqualFold(k, runtimesOwnTrailer) {
			continue
		}
		drop[strings.ToLower(k)] = true
		if strings.EqualFold(k, "Fixes") {
			drop["refs"] = true
		}
	}
	wanted := map[string]bool{}
	for _, l := range lines {
		if k, v, ok := trailerLine(l); ok {
			wanted[strings.ToLower(k)+":"+v] = true
		}
	}
	block := []string{}
	have := map[string]bool{}
	for _, t := range kept {
		k, v, _ := trailerLine(t[0])
		key := strings.ToLower(k)
		if drop[key] || singleTrailers[key] && !wanted[key+":"+v] && wantsKey(lines, key) {
			continue
		}
		have[key+":"+v] = true
		block = append(block, t...)
	}
	for _, l := range lines {
		k, v, ok := trailerLine(l)
		if !ok || have[strings.ToLower(k)+":"+v] || strings.EqualFold(k, runtimesOwnTrailer) {
			continue
		}
		have[strings.ToLower(k)+":"+v] = true
		block = append(block, l)
	}
	return block
}

// wantsKey says whether lines has a line of key (lower case).
func wantsKey(lines []string, key string) bool {
	for _, l := range lines {
		if k, _, ok := trailerLine(l); ok && strings.ToLower(k) == key {
			return true
		}
	}
	return false
}

// isTrailerBlock says whether every line of para is a trailer, or a continuation of the one
// before it.
func isTrailerBlock(para []string) bool {
	for i, l := range para {
		if l != "" && (l[0] == ' ' || l[0] == '\t') {
			if i == 0 {
				return false
			}
			continue
		}
		if _, _, ok := trailerLine(l); !ok {
			return false
		}
	}
	return len(para) > 0
}

// trailerLine is a "Key: value" line's key and value; ok is false for any other line.
func trailerLine(l string) (key, value string, ok bool) {
	k, v, found := strings.Cut(l, ":")
	if !found || k == "" {
		return "", "", false
	}
	for i, c := range k {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (c != '-' || i == 0) {
			return "", "", false
		}
	}
	return k, strings.TrimSpace(v), true
}

// writeTrailersShim writes bin/agents-trailers: `agentsd trailers`, the agentsd that started
// the session, beside agents-secret.
func writeTrailersShim(bin, agentsd string) error {
	script := "#!/bin/sh\n" +
		"# agents-trailers MSGFILE (#6372): Agent Loops writes this file. The worktree's commit-msg hook runs it to write the commit's trailer block.\n" +
		"exec " + shellQuote(agentsd) + " trailers \"$@\"\n"
	return writeCommand(filepath.Join(bin, trailersCommandName), script)
}
