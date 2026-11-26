package hostsetup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/procgroup"
)

// SetupScript is a project's own set-up, in its repository (spec, "The project's set-up script").
const SetupScript = ".agents/setup.sh"

// ScriptTimeout is how long a set-up script may run.
const ScriptTimeout = 10 * time.Minute

// ranMark is the file, in a checkout's .git, that holds the hash of the script that last ran
// there to its end with exit 0 (#7158). A send runs the script again only when its hash is not
// that one. It is one line in each checkout, so it needs no bound of its own.
const ranMark = "agents-setup-ran"

// scriptMu has one script run at a time on this machine, so two never fight over one cache.
var scriptMu sync.Mutex

// tailLines is how many of a script's last lines a failure says.
const tailLines = 5

// scriptEnv is the environment a script runs in: the machine user's basics and the four
// AGENTS_ names, and nothing else, so it holds no credential of the installer's (spec: "No
// credentials").
func scriptEnv(project, machine string, fromPool bool) []string {
	env := []string{"AGENTS_SETUP=1", "AGENTS_PROJECT=" + project, "AGENTS_MACHINE=" + machine, "AGENTS_FROM_POOL=0"}
	if fromPool {
		env[3] = "AGENTS_FROM_POOL=1"
	}
	for _, name := range []string{"HOME", "USER", "LOGNAME", "PATH", "LANG", "LC_ALL", "TERM", "TMPDIR", "SHELL"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// tail keeps the last lines written to it, and each line as it comes.
type tail struct {
	mu    sync.Mutex
	lines []string
	each  func(string)
}

func (t *tail) add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	t.mu.Lock()
	t.lines = append(t.lines, line)
	if len(t.lines) > tailLines {
		t.lines = t.lines[1:]
	}
	t.mu.Unlock()
	if t.each != nil {
		t.each(line)
	}
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// ScriptHash is the hash of project's .agents/setup.sh, and false when it has none.
func ScriptHash(project string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(project, SetupScript))
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

// ranMarkPath is where project's checkout keeps ranMark, or "" when it is not a Git checkout.
func ranMarkPath(project string) string {
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-path", ranMark)
	cmd.Dir = project
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// LastRan is the hash of the script that last ran well in project, or "".
func LastRan(project string) string {
	path := ranMarkPath(project)
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the checkout's own .git
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func markRan(project, hash string) {
	if path := ranMarkPath(project); path != "" {
		// store-bounds: not a store: one line in the checkout's own .git, replaced each run
		_ = os.WriteFile(path, []byte(hash+"\n"), 0o600) // with no mark, the next send runs it again
	}
}

// RunScriptIfChanged is RunScript when project's script is not the one that last ran well
// there: a first send, a changed script, or a script whose last run failed. It says false,
// with no run, for the same script or none.
func RunScriptIfChanged(ctx context.Context, project, machine string, fromPool bool, timeout time.Duration, log io.Writer, progress func(string)) (bool, error) {
	hash, ok := ScriptHash(project)
	if !ok || hash == LastRan(project) {
		return false, nil
	}
	return RunScript(ctx, project, machine, fromPool, timeout, log, progress)
}

// RunScript runs project's .agents/setup.sh with `bash -e`, as this user, in the project's
// folder, after any other run on this machine ends. Its output goes to log and, line by line,
// to progress. It returns false and nil when the project has no script. A non-zero exit, or
// more than timeout, is an error that holds the last lines of output. A run that ends well
// marks the checkout with the script's hash.
func RunScript(ctx context.Context, project, machine string, fromPool bool, timeout time.Duration, log io.Writer, progress func(string)) (bool, error) {
	script := filepath.Join(project, SetupScript)
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		return false, nil
	}
	scriptMu.Lock()
	defer scriptMu.Unlock()
	// The hash is of the text that runs: a send that changes it while this runs runs it again.
	hash, _ := ScriptHash(project)
	if err := runBash(ctx, script, project, scriptEnv(project, machine, fromPool), SetupScript, timeout, log, progress); err != nil {
		return true, err
	}
	if hash != "" {
		markRan(project, hash)
	}
	return true, nil
}

// runBash runs script with `bash -e`, with no stdin, in dir with env, for at most timeout. Its
// output goes to log and, line by line, to progress. A non-zero exit, or more than timeout, is
// an error that holds the last lines of output, naming the script as name.
func runBash(ctx context.Context, script, dir string, env []string, name string, timeout time.Duration, log io.Writer, progress func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-e", script)
	cmd.Dir = dir
	cmd.Env = env
	procgroup.Own(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start bash: %w", err)
	}
	seen := &tail{each: progress}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if log != nil {
			_, _ = log.Write(append(bytes.Clone(sc.Bytes()), '\n'))
		}
		seen.add(sc.Text())
	}
	err = cmd.Wait()
	procgroup.End(cmd)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &ScriptError{Text: fmt.Sprintf("%s ran longer than %s and was stopped.", name, timeout), Last: seen.last()}
	case err != nil:
		return &ScriptError{Text: fmt.Sprintf("%s failed (%v).", name, err), Last: seen.last(), Code: exitCode(err)}
	}
	return nil
}

// ScriptError is a run that did not end well: what happened, the last lines of output, and the
// exit code (-1 when it did not exit).
type ScriptError struct {
	Text, Last string
	Code       int
}

func (e *ScriptError) Error() string {
	if e.Last == "" {
		return e.Text
	}
	return e.Text + "\n" + e.Last
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// UserScriptEnv is the environment the person's own setup.sh runs in (#7155, gate 1 of #7151,
// section 1): the machine user's basics and AGENTS_SETUP, AGENTS_MACHINE and AGENTS_FROM_POOL.
func UserScriptEnv(machine string, fromPool bool) []string {
	env := scriptEnv("", machine, fromPool)
	return append(env[:1:1], env[2:]...) // no AGENTS_PROJECT
}

// RunUserScript runs the person's setup.sh at script as this user, in home, after any other run
// on this machine ends (one at a time, beside no project's script).
func RunUserScript(ctx context.Context, script, home, machine string, fromPool bool, timeout time.Duration, log io.Writer, progress func(string)) error {
	scriptMu.Lock()
	defer scriptMu.Unlock()
	return runBash(ctx, script, home, UserScriptEnv(machine, fromPool), "setup.sh", timeout, log, progress)
}

// OpenScriptLog is the log of project's script in logs, cut at scriptLogMost, and the function
// that closes it. With no file it is nil, and the run still goes on.
func OpenScriptLog(logs, project string) (io.Writer, func()) {
	_ = os.MkdirAll(logs, 0o700)
	// store-bounds: kind=log; count=100 projects; size=1MiB a project; age=7d; applied=on write (the rest is not kept), and hourly sweep
	f, err := os.OpenFile(filepath.Join(logs, "setup-"+filepath.Base(project)+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, func() {}
	}
	return &scriptLog{file: f, left: scriptLogMost}, func() { _ = f.Close() }
}

// UserScriptLog is the file the person's setup.sh writes its output to, in the host's logs
// (#7155): one run's, replaced by the next.
const UserScriptLog = "user-setup.log"

// OpenUserScriptLog is that log, cut at scriptLogMost, and the function that closes it. With no
// file it is nil, and the run still goes on.
func OpenUserScriptLog(logs string) (io.Writer, func()) {
	_ = os.MkdirAll(logs, 0o700)
	// store-bounds: kind=log; count=1; size=1MiB; age=90d; applied=on write (the rest is not kept), replaced by the next run, and hourly sweep
	f, err := os.OpenFile(filepath.Join(logs, UserScriptLog), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, func() {}
	}
	return &scriptLog{file: f, left: scriptLogMost}, func() { _ = f.Close() }
}
