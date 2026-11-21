// loops is Agent Loops on the command line (epic #6443): a thin client of the control
// plane, as the window is one, that works in the folder of the directory it runs in. It
// grows from agentsctl's pairing (controllink). It keeps one file, the pairing, in
// ~/.config/loops/hosts.json (0600), and no cache: every command reads fresh.
//
//	loops                    continue this folder's newest session, or start one, in the terminal
//	loops "…"                start a session here with this prompt, and follow it
//	loops -c ["…"]           continue the newest session here (with a prompt)
//	loops -r <id|title> ["…"]  continue one session
//	loops pair '<code>'      pair this terminal with a control plane
//	loops unpair             forget the pairing, here and on the control plane
//	loops status             the pairing, this machine and the folder here
//	loops ls [--all]         this folder's sessions: 20, or with --all every one
//	loops folder add [dir]   add this directory (or dir) as a folder
//	loops -p "…" [--json]    print mode for scripts: start a session here, follow it to the
//	                         end, print the last reply (#6447)
//	loops --detach "…"       start a session here, print its id and exit
//
// Exit codes (Stage 3): 0 done; 1 an error; 2 not in a folder; 3 a card waits and
// --on-ask=fail; 4 timed out; 5 not paired.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The exit codes a script reads (Stage 3 of #6443).
const (
	exitOK       = 0
	exitError    = 1
	exitNoFolder = 2
	exitAsked    = 3 // a card waits and --on-ask=fail: print mode (#6447)
	exitTimedOut = 4 // --timeout: print mode (#6447)
	exitNotPair  = 5
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
	why := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		why = "timed out"
	}
	return &exit{code: exitError, msg: fmt.Sprintf("Cannot reach %s (%s).\nNothing changed. Try again, or run: loops status", what, why)}
}

var errNotPaired = &exit{code: exitNotPair, msg: "This terminal is not paired with a control plane.\n" +
	"Get a code: Agent Loops ▸ Settings ▸ Control plane ▸ Clients ▸ Pair a Mac… (or the web page)\n" +
	"Then:       loops pair '<code>'"}

// exitCodes is what the usage says of each code.
var exitCodes = []struct {
	code int
	what string
}{
	{exitOK, "done"}, {exitError, "an error"}, {exitNoFolder, "not in a folder"},
	{exitAsked, "a card waits for an answer (--on-ask=fail)"}, {exitTimedOut, "timed out"}, {exitNotPair, "not paired"},
}

func usageText() string {
	codes := ""
	for _, c := range exitCodes {
		codes += fmt.Sprintf("  %d  %s\n", c.code, c.what)
	}
	return usage + "\nExit codes:\n" + codes
}

const usage = `loops: Agent Loops in a terminal

  loops                    continue this folder's newest session, or start one
  loops "…"                start a session here with this prompt, and follow it
  loops -c ["…"]           continue the newest session here (with a prompt)
  loops -r <id|title> ["…"]  continue one session (an id as loops ls shows it, or its title)
  loops pair '<code>'      pair this terminal with a control plane
  loops unpair             forget the pairing, here and on the control plane
  loops status             the pairing, this machine and the folder here
  loops ls [--all]         this folder's sessions (20; --all for every one, archived too)
  loops folder add [dir]   add this directory as a folder
  loops -p "…"             print mode: start a session here, follow it to the end, print
                           the agent's last reply (the prompt may come on stdin)
  loops --detach "…"       start a session here, print its id and exit at once

Flags:
  --folder <name|path>     work in this folder, not the one of the current directory
  --machine <name>         the folder's machine, when it is not this computer
  --control <host>         the control plane, when this terminal is paired with several
  --runtime <id>           the runtime a new session starts on (claude, codex, …); Auto by default

In the chat: ⏎ sends, ⌥⏎ adds a line, ^C stops the turn (twice leaves), ^D leaves; the
session goes on. A card (a question, a permission) is answered here or on any client.
Colour only on a terminal; NO_COLOR turns it off, LOOPS_THEME=light|dark picks the palette.

Print mode (-p, --detach):
  --json                   one JSON object per line: the session first, the end last
  --on-ask=wait|fail       a card waits: wait for an answer on any client (the default),
                           or end with exit 3; loops never answers a card itself
  --timeout <duration>     end with exit 4 after this long (as 10m); the session goes on
`

// options are the flags, which may come before or after the command.
type options struct {
	folder, machine, control string
	all                      bool
	// cont (-c), resume (-r) and runtime are the chat's (#6446).
	cont            bool
	resume, runtime string
	// Print mode (#6447).
	print, detach, json bool
	onAsk               string
	timeout             time.Duration
	// controlName is the paired control plane's host name, for the words.
	controlName string
}

// errHelp is -h or --help among the flags: the usage, on stdout, with exit 0.
var errHelp = errors.New("help")

func parse(args []string) (options, []string, error) {
	var o options
	fs := flag.NewFlagSet("loops", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.folder, "folder", "", "")
	fs.StringVar(&o.machine, "machine", "", "")
	fs.StringVar(&o.control, "control", "", "")
	fs.BoolVar(&o.all, "all", false, "")
	fs.BoolVar(&o.cont, "c", false, "")
	fs.BoolVar(&o.cont, "continue", false, "")
	fs.StringVar(&o.resume, "r", "", "")
	fs.StringVar(&o.resume, "resume", "", "")
	fs.StringVar(&o.runtime, "runtime", "", "")
	fs.BoolVar(&o.print, "p", false, "")
	fs.BoolVar(&o.print, "print", false, "")
	fs.BoolVar(&o.detach, "detach", false, "")
	fs.BoolVar(&o.json, "json", false, "")
	fs.StringVar(&o.onAsk, "on-ask", "", "")
	fs.DurationVar(&o.timeout, "timeout", 0, "")
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return o, nil, errHelp
			}
			return o, nil, &exit{code: exitError, msg: err.Error() + "\nSee: loops --help"}
		}
		args = fs.Args()
		if len(args) == 0 {
			return o, rest, nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	err := run(ctx, os.Args[1:], os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(codeOf(err))
}

func run(ctx context.Context, args []string, w io.Writer) error {
	o, rest, err := parse(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(w, usageText())
		return nil
	}
	if err != nil {
		return err
	}
	if err := checkPrintFlags(o); err != nil {
		return err
	}
	if o.print || o.detach {
		return printMode(ctx, w, o, rest)
	}
	cmd := ""
	if len(rest) > 0 {
		cmd = rest[0]
	}
	if o.cont || o.resume != "" || !isCommand(cmd) {
		return chat(ctx, o, chatArgs{text: strings.Join(rest, " "), cont: o.cont, resume: o.resume})
	}
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(w, usageText())
		return nil
	case "pair":
		if len(rest) != 2 {
			return &exit{code: exitError, msg: "loops pair '<code>'"}
		}
		return pair(ctx, w, rest[1])
	case "unpair":
		return unpair(ctx, w, o)
	case "status":
		return status(ctx, w, o)
	case "ls":
		return ls(ctx, w, o)
	case "folder":
		if len(rest) < 2 || rest[1] != "add" || len(rest) > 3 {
			return &exit{code: exitError, msg: "loops folder add [dir]"}
		}
		dir := ""
		if len(rest) == 3 {
			dir = rest[2]
		}
		return folderAdd(ctx, w, o, dir)
	}
	return &exit{code: exitError, msg: usageText()}
}

// isCommand says a first word is one of loops' own commands, not a prompt.
func isCommand(word string) bool {
	switch word {
	case "help", "-h", "--help", "pair", "unpair", "status", "ls", "folder":
		return true
	}
	return false
}

// dial is the paired control plane's socket; it keeps a key the control plane rotated.
func dial(ctx context.Context, o *options) (*controllink.Session, error) {
	h, err := readHosts()
	if err != nil {
		return nil, &exit{code: exitError, msg: err.Error()}
	}
	m := h.pick(o.control)
	if m == nil {
		if o.control != "" {
			return nil, &exit{code: exitNotPair, msg: fmt.Sprintf("This terminal is not paired with %s.\nThen: loops pair '<code>'", o.control)}
		}
		return nil, errNotPaired
	}
	o.controlName = hostOf(m.URL)
	key := string(m.ControlKey)
	s, err := controllink.DialClient(ctx, m)
	if err != nil {
		return nil, unreachable(o.controlName, err)
	}
	if string(m.ControlKey) != key {
		if err := h.write(); err != nil {
			fmt.Fprintf(os.Stderr, "loops: could not keep the control plane's new key: %v\n", err)
		}
	}
	return s, nil
}

// connect dials and finds the place: the machine and the folder of the current directory.
func connect(ctx context.Context, o options) (*controllink.Session, place, error) {
	s, err := dial(ctx, &o)
	if err != nil {
		return nil, place{}, err
	}
	dir, err := os.Getwd()
	if err != nil {
		s.Close()
		return nil, place{}, err
	}
	p, err := locate(ctx, s, o, dir)
	if err != nil {
		s.Close()
		return nil, place{}, err
	}
	return s, p, nil
}

func pair(ctx context.Context, w io.Writer, text string) error {
	code, err := controllink.ParseCode(text)
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("That is not a pairing code: %v", err)}
	}
	hostname, _ := os.Hostname()
	name := "loops"
	if hostname != "" {
		name = "loops on " + hostname
	}
	// A terminal pairs as a Mac window does: the control plane has no kind for a CLI, and
	// the kind is only the icon its Devices list shows.
	m, err := controllink.PairClientAs(ctx, code, name, string(wire.ClientKindMac))
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Could not pair with %s: %v", hostOf(code.URL), err)}
	}
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	h.put(*m)
	if err := h.write(); err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Paired, but could not keep the pairing in %s: %v", hostsFile(), err)}
	}
	fmt.Fprintf(w, "Paired with %s as %q.\nTry: loops status\n", hostOf(m.URL), name)
	return nil
}

func unpair(ctx context.Context, w io.Writer, o options) error {
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	m := h.pick(o.control)
	if m == nil {
		return errNotPaired
	}
	u := m.URL
	// The control plane forgets this client too, so its key opens nothing; when it does
	// not answer, the pairing goes here all the same, and its Devices list still shows it.
	forgot := false
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	if s, err := controllink.DialClient(cctx, m); err == nil {
		_, err = s.Call(cctx, "", "clients/forgetSelf", map[string]any{})
		forgot = err == nil
		s.Close()
	}
	cancel()
	h.remove(u)
	if err := h.write(); err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	if forgot {
		fmt.Fprintf(w, "Unpaired from %s.\n", hostOf(u))
	} else {
		fmt.Fprintf(w, "Unpaired here. %s did not answer: forget this terminal in Settings ▸ Control plane ▸ Clients.\n", hostOf(u))
	}
	return nil
}

func status(ctx context.Context, w io.Writer, o options) error {
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	m := h.pick(o.control)
	if m == nil {
		return errNotPaired
	}
	// m.Name is the control plane's own name, from the code, not this terminal's.
	at := hostOf(m.URL)
	if m.Name != "" && m.Name != at {
		at = fmt.Sprintf("%s at %s", m.Name, at)
	}
	fmt.Fprintf(w, "Paired with %s (%s)\n", at, tilde(hostsFile()))
	if len(h.Hosts) > 1 {
		for _, other := range h.Hosts {
			if other.URL != m.URL {
				fmt.Fprintf(w, "Also paired with %s (--control %s)\n", hostOf(other.URL), hostOf(other.URL))
			}
		}
	}
	s, p, err := connect(ctx, o)
	if err != nil {
		return err
	}
	defer s.Close()
	fmt.Fprintf(w, "Machine: %s\n", p.machineName())
	if p.folder == "" {
		fmt.Fprintf(w, "Folder:  none for %s (loops folder add)\n", tilde(p.dir))
		return nil
	}
	fmt.Fprintf(w, "Folder:  %s (%s)\n", p.folderName(), tilde(store.FolderPath(p.folder)))
	return nil
}

func ls(ctx context.Context, w io.Writer, o options) error {
	s, p, err := connect(ctx, o)
	if err != nil {
		return err
	}
	defer s.Close()
	if p.folder == "" {
		return notInFolder(p)
	}
	list, more, err := listSessions(ctx, s, p, o.all)
	if err != nil {
		return err
	}
	printList(w, p, list, more, o.all, time.Now())
	return nil
}

func folderAdd(ctx context.Context, w io.Writer, o options, dir string) error {
	s, err := dial(ctx, &o)
	if err != nil {
		return err
	}
	defer s.Close()
	list, err := machines(ctx, s)
	if err != nil {
		return unreachable(o.controlName, err)
	}
	hostname, _ := os.Hostname()
	m, err := pickMachine(list, o.machine, machine.ID(), hostname)
	if err != nil {
		return err
	}
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	} else if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = filepath.Join(cwd, dir)
	}
	p := place{machine: m, dir: filepath.Clean(dir)}
	r, err := s.Call(ctx, string(m.ID), "folders/add", wire.AddFolderRequest{Folder: store.PathURL(p.dir)})
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Could not add %s on %s: %v", tilde(p.dir), p.machineName(), err)}
	}
	var added wire.FolderSummary
	_ = json.Unmarshal(r, &added)
	p.folder = added.Folder.Folder
	if p.folder == "" {
		p.folder = store.PathURL(p.dir)
	}
	fmt.Fprintf(w, "Added %s on %s.\nStart a session: loops \"what this folder needs\"\n", p.folderName(), p.machineName())
	return nil
}
