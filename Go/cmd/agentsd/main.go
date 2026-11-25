// agentsd is the host daemon (#1136, #1138): it joins a control plane with the flags the
// Swift agentsd had, on the same root, on Linux and on a Mac. The Swift one went in #1314.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/redact"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/buildinfo"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
	"github.com/agentloops-dev/agent-loops/Go/internal/rolllog"
	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// logLine writes each line as DaemonLog does: the time in UTC to the second, then the words,
// with any known token shape redacted (#1001).
type logLine struct{ w io.Writer }

func (l logLine) Write(p []byte) (int, error) {
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05Z") + " "
	words, _ := redact.Bytes(p)
	if _, err := l.w.Write(append([]byte(stamp), words...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func setLog(w io.Writer) {
	log.SetFlags(0)
	log.SetOutput(logLine{w})
}

func main() {
	setLog(os.Stderr)
	if subcommand(os.Args[1:]) {
		return
	}
	cl := parse(os.Args[1:])
	if err := os.MkdirAll(cl.root, 0o700); err != nil {
		log.Fatal(err)
	}
	loc := store.At(cl.root)
	if err := os.MkdirAll(loc.Logs, 0o700); err != nil {
		log.Fatal(err)
	}
	if cl.detach {
		detach(loc)
		return
	}
	if !isTerminal() {
		// Detached or under systemd: the log is the root's daemon.log, where the Swift
		// host keeps it (StoreLocations.log), rolled at 4 MB (#1555) and kept 7 days (#6735). A panic lands in it too.
		// store-bounds: kind=log; size=8MiB; age=7d; applied=on write, at open and every 30 s (#6735)
		if f, err := rolllog.Open(loc.Log(), rolllog.Limit, true); err == nil {
			setLog(f)
		}
	}
	lock, err := takeLock(loc.Lock())
	if err != nil {
		log.Fatalf("another agentsd holds %s: %v", loc.Lock(), err)
	}
	defer lock.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dumpStacksOnSignal(ctx)
	d, err := agentsd.New(ctx, cl.root)
	if err != nil {
		log.Fatal(err)
	}
	d.Quit = stop // daemon/quit
	configure(d, cl, loc)
	d.StartWake()
	// No secrets.env (#4177): one found here, from before the update or written by hand, is
	// sealed into sealed.json and removed, before any call can read it.
	d.MoveSecretsFile()
	// The old sign-up passwords in the Keychain (#5043), copied into the store for fill_secret.
	safe.Go("signup-keychain", d.MoveSignupKeychain)
	safe.Go("socket", func() {
		if err := d.ServeSocket(ctx, loc.Socket()); err != nil && ctx.Err() == nil {
			log.Printf("socket: %v", err)
		}
	})
	if cl.controlNetwork() {
		join(ctx, cl, d)
	}
	log.Printf("agentsd %s on %s, root %s", buildinfo.String(), platform(), cl.root)
	d.WaitForMembership()
	startWork(d)
	<-ctx.Done()
	log.Printf("shutting down")
	d.Shutdown()
	if d.RestartWanted() {
		// A self-update put a new copy in bin/current (#5788): the service or the
		// supervisor starts it.
		_ = lock.Close()
		log.Printf("restarting to run the update")
		os.Exit(agentsd.ExitRestart)
	}
}

// subcommand runs the command named first in args, when there is one, and says whether it
// did: supervise and version return, the others exit with their own status.
func subcommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "supervise":
		supervise(args[1:])
		return true
	case "credential":
		// An agent's git and gh asking for the GitHub App's token (#3926).
		os.Exit(agentsd.CredentialCommand(args[1:], os.Getenv, os.Stdin, os.Stdout))
	case "secret":
		// agents-secret NAME: a secret that does not ask, through this host (#4178); from a
		// terminal, with no session's grant, once a person allows it (#4179).
		os.Exit(agentsd.SecretCommand(args[1:], os.Getenv, secretSocket(), os.Stdout, os.Stderr))
	case "trailers":
		// agents-trailers MSGFILE: an app worktree's commit-msg hook writes the session's
		// trailer block (#6372). It never refuses a commit.
		os.Exit(agentsd.TrailersCommand(args[1:], os.Getenv, os.Stderr))
	case "install":
		// The one installer (#5356): steps 3 to 6 of a machine's set-up, on this machine.
		os.Exit(installCommand(args[1:]))
	case "lock":
		os.Exit(lockCommand(args[1:]))
	case "--version", "version":
		fmt.Println(buildinfo.String())
		return true
	}
	return false
}

// configure gives the daemon this machine's keys, the person's home and the policy it
// follows, before any connection can read them.
func configure(d *agentsd.Daemon, cl commandLine, loc store.Locations) {
	// This machine's key: in a Keychain item only agentsd reads on a Mac (the root's own
	// service, so a scratch root has its own), control-host-key on Linux.
	d.HostKey = controllink.HostKeyFor(cl.root)
	// The person key (#2957): the same per-root Keychain pattern, a distinct item.
	d.PersonKey = controllink.PersonKeyFor(cl.root)
	// events/raise, as a debug build on a scratch root has it: never on a root a person
	// uses, nor in a release.
	d.RaiseByHand = buildinfo.Version == "dev" && !ordinaryRoot(cl.root)
	d.DiskOverride = d.RaiseByHand
	if home := personalHome(cl); home != "" {
		// The person's settings are in their home: a server's
		// (Daemon.swift's --serve), and the ordinary root's (StoreLocations.personalHome).
		d.SettingsDir = filepath.Join(home, ".agents", "settings")
		d.PersonalHome = home
	}
	// Your layer (#2957), read or made once, before any connection below can read it from
	// inside another lock (cost.go's costMu) at the same time as a concurrent write takes
	// yoursMu.
	d.WarmYourPolicy()
	// A root that joined follows its organisation's policy from its first call, also when
	// started without the control network (#998, FR-013): only its control plane's key
	// checks the policy it keeps. join sets it again after a new enrolment.
	d.SetPin(machinePin(cl))
	loadMembership(d, loc)
}

// loadMembership takes the membership kept in the root, when the pin admits it.
func loadMembership(d *agentsd.Daemon, loc store.Locations) {
	member, err := controllink.LoadMembership(loc.Root)
	if err != nil || member.Host == "" {
		return
	}
	if err := d.Pin().AdmitsMember(member); err != nil {
		log.Printf("pin: the membership kept here is for another control plane, and is not used: %v", err)
		return
	}
	d.SetControlKey(member.ControlKey)
	d.SetMember(member)
}

// startWork starts what a host does once it runs.
func startWork(d *agentsd.Daemon) {
	// The restart itself, for a wait or a workflow that names it (#1219), and from now on
	// a word once a minute that this host is alive.
	d.SayRestarted()
	d.PickUpAfterRestart()
	d.StartConnections()
	d.StartSetupReports()
	d.StartRetention()
	d.StartBackups()
	d.StartProjects()
	d.StartMCPHealth()
	d.StartMCPEvents()
	d.StartRepoAliases()
	d.StartCITimings()
	d.StartMachineWatch()
	d.StartSleepWatch()
}

// commandLine is DaemonCommandLine: the flags the install script and ssh pass.
type commandLine struct {
	root     string
	serve    bool
	detach   bool
	network  bool
	code     string
	hostName string
	// web and webPort: the page to serve on loopback, when the control plane runs elsewhere
	// (#1951). Agents Host gives them only then.
	web     string
	webPort int
	// pinAddress, pinKey and mustJoin: the Mac's managed preferences, as Agents Host gives
	// them (#998, FR-025). /etc/agents/policy.json, when there is one, wins.
	pinAddress string
	pinKey     string
	mustJoin   bool
	args       []string
}

func parse(args []string) commandLine {
	cl := commandLine{args: args}
	value := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, flag+"=") {
				return strings.TrimPrefix(a, flag+"=")
			}
		}
		return ""
	}
	for _, a := range args {
		switch a {
		case "--serve":
			cl.serve = true
		case "--detach":
			cl.detach = true
		case "--control-network":
			cl.network = true
		case "--must-join":
			cl.mustJoin = true
		}
	}
	cl.root = value("--root")
	cl.code = value("--control")
	if cl.code == "" {
		cl.code = value("--control-code")
	}
	cl.hostName = value("--host-name")
	cl.pinAddress = value("--pin-address")
	cl.pinKey = value("--pin-key")
	cl.web = value("--web")
	cl.webPort, _ = strconv.Atoi(value("--web-port"))
	if cl.root == "" {
		cl.root = os.Getenv("AGENTS_ROOT")
	}
	if cl.root == "" {
		// The ordinary root, Application Support/Agents, as Foundation has it on this OS.
		home, _ := os.UserHomeDir()
		cl.root = store.StandardRoot(home)
	}
	if strings.HasPrefix(cl.root, "~/") {
		home, _ := os.UserHomeDir()
		cl.root = filepath.Join(home, cl.root[2:])
	}
	cl.root, _ = filepath.Abs(cl.root)
	return cl
}

// machinePin is the pin (#998, US7): /etc/agents/policy.json when root owns it and nobody
// else can write it, or else the managed preferences Agents Host gave. Read once, at start.
func machinePin(cl commandLine) *agentsd.MachinePin {
	pin, err := agentsd.ReadPinFile(agentsd.PinFile, 0)
	if err != nil {
		log.Printf("pin: %v", err)
	}
	if pin != nil {
		return pin
	}
	return agentsd.PinFromFlags(cl.pinAddress, cl.pinKey, cl.mustJoin)
}

func (cl commandLine) controlNetwork() bool { return cl.code != "" || cl.network }

// detach starts this host again in a session of its own, under its supervisor, told
// everything but --detach, and returns at once (037: the ssh that asked can finish).
func detach(loc store.Locations) {
	// A host that holds the root already is left as it is (#3348): a second start, over
	// ssh or by hand, would leave a supervisor starting a host that can't take the lock,
	// again and again. One stopping (install.sh kills the old one first) is waited for.
	if running(loc.Lock(), 5*time.Second) {
		fmt.Println("agentsd is running already")
		return
	}
	var args []string
	for _, a := range os.Args[1:] {
		if a != "--detach" {
			args = append(args, a)
		}
	}
	logf, _ := os.OpenFile(loc.Log(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	self, _ := os.Executable()
	cmd := exec.Command(self, append([]string{"supervise"}, args...)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("agentsd started under its supervisor, pid %d\n", cmd.Process.Pid)
}

// running is whether another agentsd holds the lock file, after waiting up to patience
// for it to let go.
func running(file string, patience time.Duration) bool {
	f, err := os.OpenFile(file, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	for until := time.Now().Add(patience); ; {
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // the deferred Close lets it go too
			return false
		}
		if time.Now().After(until) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// takeLock is DaemonLock: an exclusive flock on daemon.lock, holding this pid, so two
// hosts never share a root. The install script stops a host by the pid in it.
func takeLock(file string) (*os.File, error) {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	// The lock is what keeps a second host out; the pid is only for the install script, so a
	// failed write is logged, not fatal.
	if err := f.Truncate(0); err != nil {
		log.Printf("%s: %v", file, err)
	} else if _, err := f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0); err != nil {
		log.Printf("%s: %v", file, err)
	}
	return f, nil
}

// join enrols with a code once (control-join-code, or --control), then keeps the uplink.
func join(ctx context.Context, cl commandLine, d *agentsd.Daemon) {
	loc := d.Store.Loc
	hello := machineHello(cl, d)
	codeFile := filepath.Join(loc.Root, "control-join-code")
	code := joinCode(cl, codeFile)
	private, err := controllink.HostPrivateKey(d.HostKey)
	if err != nil {
		log.Fatal(err)
	}
	pin := d.Pin()
	member, err := controllink.LoadMembership(loc.Root)
	if err == nil && member.Host != "" && pin.AdmitsMember(member) != nil {
		// Joined another control plane before the pin: it is not dialled (FR-027).
		member = nil
	}
	if code == "" && (member == nil || member.Host == "") {
		var asked bool
		if member, asked = askIfAsked(ctx, loc.Root, pin, private, hello); asked && member == nil {
			return
		}
	}
	if code != "" || member == nil || member.Host == "" {
		if member = enrolWithCode(ctx, loc.Root, code, codeFile, pin, private, hello); member == nil {
			return
		}
	}
	if err := pin.AdmitsMember(member); err != nil {
		// An approved ask that turned out to be another control plane's key.
		log.Printf("pin: the control plane joined is not the one required, and is not used: %v", err)
		return
	}
	// The organisation's policy is checked with the control key (#998).
	d.SetControlKey(member.ControlKey)
	d.SetMember(member)
	keepUplink(ctx, cl, d, member, private, hello)
}

// machineHello is what this machine tells the control plane about itself.
func machineHello(cl commandLine, d *agentsd.Daemon) controllink.MachineHello {
	name := cl.hostName
	if name == "" {
		name, _ = os.Hostname()
	}
	capabilities := d.Capabilities()
	hello := controllink.MachineHello{Version: buildinfo.Version, Platform: platform(), MachineID: machine.ID(),
		Implementation: new(buildinfo.Implementation), Capabilities: capabilities, Maturity: wire.MaturityOf(capabilities)}
	if name != "" {
		hello.Name = &name
	}
	return hello
}

// joinCode is the code to enrol with: --control, else the one in codeFile.
func joinCode(cl commandLine, codeFile string) string {
	if cl.code != "" {
		return cl.code
	}
	if b, err := os.ReadFile(codeFile); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// askIfAsked asks to join with no code when control-join-ask says to, and says whether it
// did; a nil membership after an ask means not to join.
func askIfAsked(ctx context.Context, root string, pin *agentsd.MachinePin, private []byte, hello controllink.MachineHello) (*controllink.Membership, bool) {
	askFile := filepath.Join(root, "control-join-ask")
	b, err := os.ReadFile(askFile)
	if err != nil {
		return nil, false
	}
	var ask controllink.JoinAsk
	if err := json.Unmarshal(b, &ask); err != nil {
		log.Fatalf("control-join-ask: %v", err)
	}
	if err := pin.Admits(nil, ask.URL); err != nil {
		log.Printf("pin: not asking to join %s: %v", ask.URL, err)
		return nil, true
	}
	member := askToJoin(ctx, root, ask, private, hello)
	if member == nil {
		return nil, true
	}
	os.Remove(askFile)
	os.Remove(filepath.Join(root, "control-join-shown"))
	return member, true
}

// enrolWithCode enrols with code, when the pin admits its control plane, and removes the
// code's file once it is used.
func enrolWithCode(ctx context.Context, root, code, codeFile string, pin *agentsd.MachinePin, private []byte, hello controllink.MachineHello) *controllink.Membership {
	if code == "" {
		log.Printf("no membership in the root and no code to enrol with: not joining a control plane")
		return nil
	}
	member := enrol(ctx, code, codeFile, func(ctx context.Context, c *controllink.Code) (*controllink.Membership, error) {
		// A code for another control plane is refused here, before it is used (FR-027).
		if err := pin.Admits(c.ControlKey, c.URL); err != nil {
			return nil, err
		}
		return controllink.EnrollAs(ctx, root, c, private, hello, controllink.RolesOf(root))
	})
	if member == nil {
		return nil
	}
	os.Remove(codeFile)
	log.Printf("enrolled as host %s with %s", member.Host, member.Name)
	return member
}

// keepUplink connects this host to its control plane, and to the window and browser on
// this Mac, and dials again for as long as ctx lasts.
func keepUplink(ctx context.Context, cl commandLine, d *agentsd.Daemon, member *controllink.Membership, private []byte, hello controllink.MachineHello) {
	loc := d.Store.Loc
	up := &controllink.Uplink{Root: loc.Root, Member: member, Private: private, Hello: hello, Accept: d.Accept,
		Lend: d.LendingPort, Rotated: func(key []byte) { d.ControlKeyRotated(key); d.MoveLocal(key) }}
	controlOnly := controllink.ControlOnly(loc.Root)
	if controlOnly {
		// The control role alone (#7314): the link is for the store copy and the handover.
		up.Accept, up.Lend = agentsd.RefuseAgentWork, nil
	}
	d.Tunneler = up
	// The needs it shows nowhere go to the control plane, offered again on each return (#1367).
	up.OnChange = func(connected bool, problem string) {
		writeJoinStatus(loc.Root, connected, problem)
		d.ControlAnswered(connected, false)
		safe.Go("attention uplink", func() { d.ControlUplinkChanged(connected) })
		if connected {
			d.ShareBotKey()
		}
	}
	// A machine the person removed keeps no key of their bot's (#3950).
	up.OnRefused = func(reason string) {
		if reason == controllink.Forgotten {
			d.ControlAnswered(false, true)
			d.BotMachineRemoved()
		}
	}
	up.Told = d.Told
	up.Asked = d.Asked
	if controlOnly {
		up.Asked = d.AskedControlOnly
	}
	// A window on this Mac reaches this host directly (#1118).
	d.StartLocal(ctx, member, private)
	// And a browser on it, through the page this host serves (#1951).
	if cl.web != "" {
		d.StartPage(ctx, private, cl.web, cl.webPort)
	}
	d.SetControl(up.Tell)
	d.SetControlAsk(up.Ask)
	d.SetControlCapabilities(up.ControlCapabilities)
	// What a capability gates goes when the control plane says it, not an hour later (#5410).
	up.OnCapabilities = func([]string) { safe.Go("control capabilities", d.ControlCapabilitiesKnown) }
	// Its agents and workflows wait for the first answer: a host the control plane forgot
	// starts none (#4985). The last run's answer counts until then.
	d.AwaitMembership(strings.HasSuffix(joinProblem(loc.Root), controllink.Refuse(controllink.Forgotten).Error()))
	safe.Go("uplink", func() {
		// A panic in the uplink would leave the host up but offline: dial again.
		for ctx.Err() == nil && safe.Catch("uplink", func() { up.Run(ctx) }) != nil {
			time.Sleep(time.Second)
		}
	})
}

// askToJoin asks to join with no code (#994) until the person approves it, asking again
// when a request is denied or runs out, or the control plane can't be reached.
func askToJoin(ctx context.Context, root string, ask controllink.JoinAsk, private []byte, hello controllink.MachineHello) *controllink.Membership {
	for {
		member, err := controllink.AskToJoin(ctx, root, ask, private, hello, func(code string) {
			if err := os.WriteFile(filepath.Join(root, "control-join-shown"), []byte(code+"\n"), 0o600); err != nil {
				log.Printf("control-join-shown: %v", err)
			}
			log.Printf("asking to join %s as %s: approve it on a client; the code is %s", ask.URL, ask.Person, code)
		})
		if err == nil {
			log.Printf("joined as host %s with %s, by approval", member.Host, member.Name) //nolint:gosec // G706: the membership the control plane gave, one line, and logLine redacts it
			return member
		}
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("could not join: %v; asking again in 30 s", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(30 * time.Second):
		}
	}
}

// platform is Daemon.platform: "Linux arm64" or "Linux x86-64".
func platform() string {
	name := "Linux"
	if runtime.GOOS == "darwin" {
		name = "macOS"
	}
	if runtime.GOARCH == "arm64" {
		return name + " arm64"
	}
	return name + " x86-64"
}

// personalHome is StoreLocations.personalHome: AGENTS_PERSONAL_HOME first, then the
// account's home for a server (--serve) or the ordinary root, and none for any other root,
// which keeps its settings in <root>/settings and never writes the real ~/.agents.
func personalHome(cl commandLine) string {
	if named := os.Getenv("AGENTS_PERSONAL_HOME"); named != "" {
		return named
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if cl.serve || store.IsStandard(cl.root) {
		return home
	}
	return ""
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && isTerminalFile(fi)
}

// isTerminalFile is a character device other than /dev/null: launchd gives the Mac's host
// /dev/null, a character device too, and its log went nowhere (#1392).
func isTerminalFile(fi os.FileInfo) bool {
	if fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

// secretSocket is the daemon.sock that a terminal's agents-secret asks (#4179): AGENTS_ROOT's,
// else the ordinary root's, else an installed server's when only it has one.
func secretSocket() string {
	sock := store.At(parse(nil).root).Socket()
	if os.Getenv("AGENTS_ROOT") != "" {
		return sock
	}
	if _, err := os.Stat(sock); err != nil {
		home, _ := os.UserHomeDir()
		server := store.At(filepath.Join(home, ".agents-server", "root")).Socket()
		if _, err := os.Stat(server); err == nil {
			return server
		}
	}
	return sock
}

// ordinaryRoot is StoreLocations.isStandard: the root a person's host uses, where an
// invented event would be a lie (Application Support/Agents, or an installed server's).
func ordinaryRoot(root string) bool {
	home, _ := os.UserHomeDir()
	return store.IsStandard(root) || filepath.Clean(root) == filepath.Join(home, ".agents-server", "root")
}
