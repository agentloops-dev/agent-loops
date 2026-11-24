// loops is Agent Loops on the command line (epic #6443): a thin client of the control
// plane, as the window is one, that works in the folder of the directory it runs in. It
// grows from agentsctl's pairing (controllink). It keeps one file, the pairing, in
// ~/.config/loops/hosts.json (0600), and no cache: every command reads fresh.
//
//	loops                    choose a recent session in this folder, or start one, in the terminal
//	loops "…"                start a session here with this prompt, and follow it
//	loops -c ["…"]           continue the newest session here (with a prompt)
//	loops -r <id|title> ["…"]  continue one session
//	loops auth login '<code>'    pair this terminal with a control plane
//	loops auth logout            forget the pairing, here and on the control plane
//	loops auth status            the pairing, this machine and the folder here
//	loops sessions list [--all]  this folder's sessions: 20, or with --all every one
//	loops sessions create ["…"] start a session here, and follow it
//	loops sessions follow [<id>] continue the newest session here, or one by id or title
//	loops folders add [dir]      add this directory (or dir) as a folder
//	loops -p "…" [--json]    print mode for scripts: start a session here, follow it to the
//	                         end, print the last reply (#6447)
//	loops --detach "…"       start a session here, print its id and exit
//
// Exit codes (Stage 3): 0 done; 1 an error; 2 not in a folder; 3 a card waits and
// --on-ask=fail; 4 timed out; 5 not paired; 6 the link was lost and did not come back.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
)

// The exit codes a script reads (Stage 3 of #6443).
const (
	exitOK       = 0
	exitError    = 1
	exitNoFolder = 2
	exitAsked    = 3 // a card waits and --on-ask=fail: print mode (#6447)
	exitTimedOut = 4 // --timeout: print mode (#6447)
	exitNotPair  = 5
	exitLinkLost = 6 // the link to the control plane dropped and did not come back (#6478)
)

// exit is an error with the code the program ends with and the words it says.
type exit struct {
	code int
	msg  string
}

func (e *exit) Error() string { return e.msg }

// codeOf is the exit code of an error: its own, else 1.
func codeOf(err error) int {
	if err == nil {
		return exitOK
	}
	var e *exit
	if errors.As(err, &e) {
		return e.code
	}
	return exitError
}

// unreachable is state 9 of the wireframes (exit 1): nothing changed.
func unreachable(what string, err error) error {
	if notInLock(err) {
		return lockRefusal(what, err)
	}
	return &exit{code: exitError, msg: fmt.Sprintf("Cannot reach %s (%s).\nNothing changed. Try again, or run: loops auth status", what, reason(err)) + debugLine(err)}
}

// debug is --debug: an error's raw words go after its short ones (#6479).
var debug bool

// debugLine is the raw error on a line of its own, with --debug; else nothing.
func debugLine(err error) string {
	if !debug {
		return ""
	}
	return "\nDebug: " + err.Error()
}

// reason is a dial error in a few words, as wireframe 9's "(timed out after 10 s)" (#6479).
// An error it does not know keeps its own words.
func reason(err error) string {
	var noAnswer *controllink.NoAnswerError
	var dns *net.DNSError
	switch {
	case notInLock(err):
		return "it takes requests only from your devices, and this terminal is not one yet: run loops auth status to ask to join"
	case errors.As(err, &noAnswer):
		return fmt.Sprintf("timed out after %d s", int(noAnswer.Wait.Round(time.Second).Seconds()))
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "it refused the connection"
	case errors.As(err, &dns) && dns.IsNotFound:
		return "no such host"
	case errors.As(err, &dns):
		return "could not look up the host"
	case errors.Is(err, controllink.ErrNotPinned):
		return "its certificate is not the paired one"
	}
	return err.Error()
}

var errNotPaired = &exit{code: exitNotPair, msg: "This terminal is not paired.\n" +
	"Get a code in Settings ▸ Devices, then: loops auth login '<code>'"}

// options are the flags, which may come before or after the command.
type options struct {
	folder, machine, control string
	all                      bool
	// cont (-c), resume (-r) and runtime are the chat's (#6446).
	cont            bool
	resume, runtime string
	// mode is --mode, the permission level a new session starts at (#6651).
	mode string
	// Print mode (#6447).
	print, detach, json bool
	noInput             bool
	onAsk               string
	timeout             time.Duration
	// controlName is the paired control plane's host name, for the words.
	controlName string
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	err := runTo(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(codeOf(err))
}

// dial is the paired control plane's socket; it keeps a key the control plane rotated. A
// terminal that is not in its machine lock yet asks to join it first (#6563).
func dial(ctx context.Context, o *options) (*controllink.Session, error) {
	mode := joinAsk
	if (o.print || o.detach) && o.onAsk == "fail" {
		mode = joinFail
	}
	s, err := dialWith(ctx, o, mode)
	var e *exit
	if err != nil && !errors.As(err, &e) {
		return nil, unreachable(o.controlName, err)
	}
	return s, err
}

// dialLink is dial with the dial's own error, as a relink says it (#6478). It asks nobody
// to join the lock: a relink only checks its calls with the key the lock names.
func dialLink(ctx context.Context, o *options) (*controllink.Session, error) {
	return dialWith(ctx, o, joinNever)
}

func dialWith(ctx context.Context, o *options, mode joinMode) (*controllink.Session, error) {
	h, err := readHosts()
	if err != nil {
		return nil, &exit{code: exitError, msg: err.Error()}
	}
	m := h.pick(o.control)
	if m == nil {
		if o.control != "" {
			return nil, &exit{code: exitNotPair, msg: fmt.Sprintf("This terminal is not paired with %s.\nThen: loops auth login '<code>'", o.control)}
		}
		return nil, errNotPaired
	}
	o.controlName = hostOf(m.URL)
	key := string(m.ControlKey)
	s, err := controllink.DialClient(ctx, m)
	if err != nil {
		return nil, err
	}
	if string(m.ControlKey) != key {
		if err := h.write(); err != nil {
			fmt.Fprintf(os.Stderr, "loops: could not keep the control plane's new key: %v\n", err)
		}
	}
	if err := useLock(ctx, s, m, h, mode, os.Stderr); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// connect dials and finds the place: the machine and the folder of the current directory.
// It sets o.controlName, for the words.
func connect(ctx context.Context, o *options) (*controllink.Session, place, error) {
	s, err := dial(ctx, o)
	if err != nil {
		return nil, place{}, err
	}
	dir, err := os.Getwd()
	if err != nil {
		s.Close()
		return nil, place{}, err
	}
	p, err := locate(ctx, s, *o, dir)
	if err != nil {
		s.Close()
		return nil, place{}, err
	}
	return s, p, nil
}
