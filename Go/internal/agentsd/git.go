package agentsd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// Git, run as the Swift host runs it (GitWorktrees.git, GitChanges.run): no optional
// locks, a deadline, output trimmed, and git's own words when it refuses.

const gitOutputLimit = 16 << 20

type gitError struct {
	message string
	// answered is true when git ran to its own end and said no (an exit status, as "No such
	// remote"), false when it gave no answer: it could not start, a signal or the deadline
	// stopped it (#6257).
	answered bool
	// status is git's exit status when it answered.
	status int
}

func (e *gitError) Error() string { return e.message }

// gitAnswered says whether err is git's own answer, not a git that did not run to its end: a
// read that failed under load (#6257) is not a fact to keep.
func gitAnswered(err error) bool {
	var g *gitError
	return err == nil || errors.As(err, &g) && g.answered
}

// gitSaidNo says whether err is git's own answer with exit status status, as 1 from
// `rev-parse -q --verify` for a ref that is not there.
func gitSaidNo(err error, status int) bool {
	var g *gitError
	return errors.As(err, &g) && g.answered && g.status == status
}

func gitDeadline(args []string) time.Duration {
	if len(args) >= 2 && args[0] == "worktree" {
		switch args[1] {
		case "add":
			return 120 * time.Second
		case "remove":
			return 300 * time.Second
		}
	}
	if len(args) >= 1 && (args[0] == "fetch" || args[0] == "apply" || args[0] == "add" || args[0] == "bundle" || args[0] == "diff") {
		return 600 * time.Second // a sent repository (#1123) can be large
	}
	return 30 * time.Second
}

// gitLocks keeps the worktree commands of one folder to one at a time (OneAtATime).
var gitLocks sync.Map

// git runs git in dir and is its output, trimmed of newlines.
func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, nil, args...)
	return strings.Trim(string(out), "\n"), err
}

func gitRaw(dir string, stdin []byte, args ...string) ([]byte, error) {
	return gitRawEnv(dir, nil, stdin, args...)
}

// gitRawEnv is gitRaw with more of the environment, such as GIT_INDEX_FILE.
func gitRawEnv(dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "worktree" && (len(args) < 2 || args[1] != "list") {
		m, _ := gitLocks.LoadOrStore(filepath.Clean(dir), &sync.Mutex{})
		m.(*sync.Mutex).Lock()
		defer m.(*sync.Mutex).Unlock()
	}
	deadline := gitDeadline(args)
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	var out, errOut bytes.Buffer
	var err error
	for attempt := 1; ; attempt++ {
		out.Reset()
		errOut.Reset()
		cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git itself, with no shell: an argument cannot start another command
		cmd.Dir = dir
		cmd.Env = append(append(os.Environ(), "GIT_OPTIONAL_LOCKS=0"), env...)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		cmd.Stdout = &limited{w: &out, n: gitOutputLimit}
		cmd.Stderr = &errOut
		err = cmd.Run()
		// A git the system did not start (short of memory or processes on a busy machine)
		// did nothing, so it is safe to start again (#6106).
		if err == nil || cmd.ProcessState != nil || ctx.Err() != nil || attempt == gitStartAttempts ||
			errors.Is(err, exec.ErrNotFound) || !isDir(dir) {
			break
		}
		time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, &gitError{message: "Git is not installed on this Mac."}
	}
	if ctx.Err() != nil {
		msg := fmt.Sprintf("git %s was stopped after %s with no end in sight, so nothing else waits on it now.",
			strings.Join(args[:min(2, len(args))], " "), deadline)
		if args[0] == "worktree" {
			msg += " If it left a half-made worktree, `git worktree prune` clears it."
		}
		return out.Bytes(), &gitError{message: msg}
	}
	if err != nil {
		msg := strings.TrimSpace(errOut.String())
		var exit *exec.ExitError
		switch {
		case msg != "":
		case !errors.As(err, &exit):
			msg = "git " + args[0] + " could not start: " + err.Error() + "." // say why (#6106)
		default:
			msg = "git " + args[0] + " failed (" + err.Error() + ")." // a signal, with no words from git
		}
		g := &gitError{message: msg}
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			g.answered, g.status = true, exit.ExitCode()
		}
		return out.Bytes(), g
	}
	return out.Bytes(), nil
}

// gitStartAttempts is how many times git is started when the system does not start it.
const gitStartAttempts = 3

// gitTo runs git in dir with its output written to w, with no limit: a patch to send (#1123).
func gitTo(dir string, w io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitDeadline(args))
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var errOut bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return &gitError{message: msg}
		}
		return &gitError{message: "git " + args[0] + " failed."}
	}
	return nil
}

type limited struct {
	w *bytes.Buffer
	n int
}

func (l *limited) Write(b []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		l.w.Write(b[:min(len(b), room)])
	}
	return len(b), nil
}

type repository struct{ toplevel, commonDir, prefix string }

func (r repository) worktreesFolder() string {
	return filepath.Join(r.toplevel, ".agents", "worktrees")
}

// errNotARepository is git's own answer that a folder is in no repository. Any other
// failure (a deadline, git not starting, git killed) is not that answer, and a person is
// told git's words instead of the false "not in a git repository" (#6106).
var errNotARepository = errors.New("not in a git repository")

// repositoryOf is GitWorktrees.repository: the checkout dir is in, or false, for a caller
// that tells nobody why. A caller that tells a person uses findRepository.
func repositoryOf(dir string) (repository, bool) {
	r, err := findRepository(dir)
	return r, err == nil
}

// findRepository is the checkout dir is in; errNotARepository when git says there is
// none, or git's own failure.
func findRepository(dir string) (repository, error) {
	if !isDir(dir) {
		return repository{}, errNotARepository
	}
	// LC_ALL=C: git's words are read below, so they are in no other language.
	raw, err := gitRawEnv(dir, []string{"LC_ALL=C"}, nil, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--show-prefix")
	out := strings.Trim(string(raw), "\n")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return repository{}, errNotARepository
		}
		return repository{}, err
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return repository{}, &gitError{message: "git rev-parse said too little: " + strconv.Quote(out)}
	}
	r := repository{toplevel: lines[0], commonDir: lines[1]}
	if len(lines) > 2 {
		r.prefix = strings.TrimSuffix(lines[2], "/")
	}
	return r, nil
}

// repositoryWords are a person's words for a findRepository failure: notIn when git says
// the folder is in no repository, else git's own failure after what could not be done.
func repositoryWords(err error, notIn, couldNot string) string {
	if errors.Is(err, errNotARepository) {
		return notIn
	}
	return couldNot + " " + strings.TrimSuffix(err.Error(), ".") + "."
}

type worktreeEntry struct {
	root, head, branch       string
	detached, bare, prunable bool
}

// worktreeList is `git worktree list --porcelain`, bare entries left out.
func worktreeList(dir string) ([]worktreeEntry, error) {
	out, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var all []worktreeEntry
	var cur *worktreeEntry
	for _, l := range strings.Split(out+"\n", "\n") {
		k, v, _ := strings.Cut(l, " ")
		switch k {
		case "worktree":
			all = append(all, worktreeEntry{root: v})
			cur = &all[len(all)-1]
		case "HEAD":
			cur.head = v
		case "branch":
			cur.branch = strings.TrimPrefix(v, "refs/heads/")
		case "detached":
			cur.detached = true
		case "bare":
			cur.bare = true
		case "prunable":
			cur.prunable = true
		}
	}
	var out2 []worktreeEntry
	for _, e := range all {
		if !e.bare {
			out2 = append(out2, e)
		}
	}
	return out2, nil
}

// canonical is a folder's path as folders compare them, for one that may not be there:
// the links of the deepest part that is there resolved.
func canonical(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		if r, err := store.Resolve(p); err == nil {
			return filepath.Join(r, rest)
		}
		if p == "/" || p == "." {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}
