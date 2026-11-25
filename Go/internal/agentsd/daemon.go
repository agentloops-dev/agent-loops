package agentsd

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/acp"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/browser"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/repoalias"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Daemon is one host: its root, the clients connected to it, and what it serves them.
type Daemon struct {
	// assessMu guards the files of the assessments (assessmentFiles).
	assessMu sync.Mutex

	// smallFiles are the bounded small files (smallFile), each swept hourly; set in newWith.
	smallFiles []interface {
		Name() string
		Path() string
		Sweep() (int, error)
	}
	// runsFile is workflow-runs.json; workflowMu guards what it is written from.
	runsFile *store.JSONFile[workflowRun]
	// removedConnectors is the copies of the personal connectors removed (#2195).
	removedConnectors *store.JSONFile[removedConnector]
	dismissedLeads    *store.JSONFile[dismissedLead]
	repoAliases       *store.JSONFile[repoalias.Alias]
	erased            *store.JSONFile[wire.Date]

	ctx context.Context
	// opts are the daemon's own (#5926): set by New, and changed only by a test, as a test
	// once changed the process's environment while its daemon ran.
	opts     atomic.Pointer[startOptions]
	Store    *store.Store
	handlers map[string]Handler

	// requests are what agents asked people for (#3863), and who answers each, by id.
	requests      requestStore
	requestActors map[wire.UUID]*wire.AuditActor

	// accountOps is each account's last operation, and each secret's last read (#4157).
	accountOps *store.JSONFile[wire.AccountLastOp]
	// secretOps is each secret's last operation, and the reads waiting for the audit (#4156).
	secretOps secretOpBook

	// erasures is the queue of runtime copies of deleted agents to erase (#1001).
	erasures eraser

	// Tunneler opens tunnels through the control plane, to a host that lends a sign-in;
	// nil when this host has no control plane.
	Tunneler Tunneler

	// browser is the machine's browser (#3533): one Chromium, started on first use.
	browser *browser.Machine
	// bw is who watches the machine's browser, and their screencast (#3528).
	bw browserWatch

	// The machine providers this machine carries (#3398), made at the first ask.
	carriedOnce sync.Once
	carriedHeld *carriedProviders

	// machineMoves is the moves to another machine agents asked for, made when their turn
	// ends (move_session, #3717), by agent.
	machineMoves sync.Map
	// moves are the moves to another machine under way, whoever asked (#4641).
	moves movesUnderWay

	// largeChat asks the retention sweep to trim conversations before the hour (#6706).
	largeChat chan struct{}

	mu      sync.Mutex
	clients map[int64]*Client
	unknown map[string]int
	lives   map[wire.UUID]*live
	relays  map[string]*relay
	sent    map[wire.UUID]bool
	// watches is each watched agent folder's inotify, and interests who watches it.
	watches   map[string]*folderWatch
	interests map[string][]interest
	shells    map[shellKey]*shell
	// agentMu orders every read-change-save of an agent record.
	agentMu sync.Mutex
	pinState
	viewCountBook
	reviewBook
	deployBook
	attachmentBook
	settingsState
	userScopeState
	sessionSecretsState
	sealedSecretsState
	secretUseState
	driveApprovalState
	terminalState
	secretProxyState
	lockState
	selfUpdateState
	quitState
	processBook
	capacityBook
	warmBook
	leaseState
	eventBook
	actorBook
	appToolsState
	gitCredentialState
	screenDriveState
	handOffWindowState
	browserDriveState
	browserTabState
	hostUserState
	userSuspendState
	machinePinState
	hostedPool
	eventWaitState
	messageState
	helperQueueState
	headlessSignInState
	costBook
	featureBook
	folderChangedState
	agentTellState
	workflowBook
	allowanceBook
	accountBook
	mcpBook
	readyMadeFetching
	viewBook
	mcpHealthBook
	mcpEventsBook
	mergeArchiveBook
	restartBook
	adoptedBook
	machineBook
	sleepBook
	hostStatusBook
	// elsewhere is the sessions on other machines, and those here from them (#2927).
	elsewhere *store.JSONFile[elsewhereEntry]
	idleBook
	turnBook
	findBook
	optionBook
	timeBook
	reportBook
	lineageBook
	// The build timings (#5177).
	ciTimingsBook
	// The queue of each repository's CI, for the ci lease (#5678).
	ciQueueBook
	ciSyncBook
	// The control plane's copy of the deploys (#5496).
	deploySyncBook
	// The events this machine publishes on the bus (#4729).
	busBook
	// Subscriptions on the bus, and the bursts debounce holds (#4730).
	busSubBook
	debounceBook
	projectBook
	projectToolsBook
	achievementBook
	afterTurnState
	attentionState
	liveTileState
	worktreeState
	recoveryState
	forgottenState
	backupState
	folderState
	installBook
	guardedStateHolder
	macState
	folderWatchState
	loopBook
	deviceBook
	localBook
	connectionBook
	botBook
	setupBook
	forgeBook
	timelineBook
	orgPolicyState
	orgRouteState
	teamPeopleCache
	// pageEdits is what the person changed on live pages since each agent's last turn.
	pageEdits pageEdits
	// undoNotes is the files put back by Undo These Changes since each agent's last turn (#3437).
	undoNotes  undoNotes
	policyOnce sync.Once
	policy     SocketPolicy
	// SettingsDir is the person's settings on this host (#1033):
	// <root>/settings, or $HOME/.agents/settings for a server, as the Swift host's --serve.
	SettingsDir string
	// RaiseByHand lets events/raise make an event: an unreleased build on a scratch root.
	RaiseByHand bool
	// DiskOverride lets the root's disk-free-override say each volume's free space
	// (#195, #4851): an unreleased build on a scratch root, as RaiseByHand.
	DiskOverride bool
	// HostKey keeps this machine's private key: control-host-key in the root unless the
	// command sets the platform's store (controllink.HostKeyFor, the Keychain on a Mac).
	HostKey controllink.HostKeyStore
	// PersonKey keeps the person key (#2957, research R1): person-key in the root unless the
	// command sets the platform's store (controllink.PersonKeyFor, the Keychain on a Mac).
	// Only the keeper signs with it.
	PersonKey controllink.PersonKeyStore
	yourPolicyBook
	jobBook
}

// New opens the root and registers every method this host serves.
// startOptions is what one daemon has from its start (#5926), before New starts its first
// goroutine, in place of what the process's environment and package variables say: tests run
// daemons side by side in one process, each with its own.
type startOptions struct {
	// namedHome is the personal home AGENTS_PERSONAL_HOME names, "" when none is named.
	namedHome string
	// locate finds a runtime in place of locateFor; nil is locateFor. A test's starts a fake.
	locate func(*runtimeDef) (string, []string, []string)
	// runtimeEnv goes into each runtime's environment, after the rest.
	runtimeEnv []string
	// attach, when set, reaches each runtime in place of starting its process (acp.Launch.Attach):
	// a test's fake runtime in the test's own process (#6143).
	attach func(acp.Launch) (acp.Attached, error)
	// diskFree, when set, is each volume's free space in place of what the disk says, from
	// its real reading: a test's daemon reads a disk with room (#6390), so a full Mac puts no
	// disk notice in its Needs you; a disk test sets its own.
	diskFree func(wire.DiskReading) int64
}

// startOpts are the daemon's start options now: none for a Daemon made without New.
func (d *Daemon) startOpts() startOptions {
	if o := d.opts.Load(); o != nil {
		return *o
	}
	return startOptions{}
}

// newDiskFree is the diskFree of a daemon that New makes: nil, the disk's own reading, in
// the product. The package's tests set it once, before any test runs, to a disk with room
// (#6497), so a test's restart by New reads no full Mac either.
var newDiskFree func(wire.DiskReading) int64

// New is a daemon on root, with the personal home AGENTS_PERSONAL_HOME names.
func New(ctx context.Context, root string) (*Daemon, error) {
	return newWith(ctx, root, startOptions{namedHome: os.Getenv("AGENTS_PERSONAL_HOME"), diskFree: newDiskFree})
}

// newWith is New with its options given, not read from the process.
func newWith(ctx context.Context, root string, opts startOptions) (*Daemon, error) {
	st, err := store.Open(root)
	if err != nil {
		return nil, err
	}
	// Shutdown ends this ctx last, so a goroutine that waits on d.ctx ends with the host (#5653).
	ctx, cancel := context.WithCancel(ctx)
	d := &Daemon{ctx: ctx, largeChat: make(chan struct{}, 1), Store: st, handlers: map[string]Handler{}, clients: map[int64]*Client{}, unknown: map[string]int{},
		lives: map[wire.UUID]*live{}, relays: map[string]*relay{}, sent: map[wire.UUID]bool{},
		watches: map[string]*folderWatch{}, interests: map[string][]interest{}, shells: map[shellKey]*shell{},
		SettingsDir: filepath.Join(st.Loc.Root, "settings"), HostKey: controllink.HostKeyFile{Root: st.Loc.Root},
		PersonKey: controllink.PersonKeyFile{Root: st.Loc.Root}}
	d.dismissedLeads = d.openDismissedLeads()
	d.removedConnectors = d.openRemovedConnectors()
	d.repoAliases = d.openRepoAliases()
	d.erased = d.openErased()
	d.deployStore = d.openDeploys()
	d.releaseDone = d.openReleaseTags()
	d.secretOps.pending = d.openSecretOpsPending()
	d.accountOps = d.openAccountOps()
	d.elsewhere = d.openElsewhere()
	d.dismissed = d.openReviewMarks("review-dismissals.json")
	d.opened = d.openReviewMarks("review-opened.json")
	d.lockSeen = d.openLockSeen()
	d.runsFile = d.openWorkflowRuns()
	d.opts.Store(&opts)
	d.jobCancel = cancel
	st.OnGap = d.tellEntry
	st.OnLarge = func() {
		select {
		case d.largeChat <- struct{}{}:
		default:
		}
	}
	// The sweep of roots that are gone finds this root's own folders by this (#3746).
	if err := store.MarkOwnFolders(st.Loc); err != nil {
		log.Printf("store: could not mark this root's cache and log folders: %v", err)
	}
	d.setToolsHome()
	d.register()
	d.startBrowser()
	d.loadAttention()
	d.loadRepoAliases()
	d.loadEvents()
	d.loadRequests()
	d.beginMCPApprovals()
	d.recover()
	// A session archived or deleted while the host was not running loses its secrets (#3548).
	d.sweepSessionSecrets()
	d.startErasures()
	// How long nothing ran, and the waits open over it (#1219): before anything below can
	// raise an event that wakes one, so its wake says so.
	d.noteRestart()
	d.resumeEventWaits()
	// The background processes the last host's runtimes left running (#4577).
	d.adoptBackground()
	// A lease's line and the waits for it agree again (#2361).
	d.reconcileLineWaits()
	d.resumeBlocks()
	d.resumeContinues()
	d.checkEveryQueue()
	// A place another machine of the project frees (#3281).
	d.checkProjectQueues()
	// Levels kept by a host before this one, or with Auto settings since changed (#5730).
	d.rematchLevels()
	d.restoreRuns()
	d.startWorkflowClock()
	// Each folder's one watch (#173), which the first workflow scan starts too.
	d.kickFolderWatches()
	d.goJob("folder watches end", func() { <-ctx.Done(); d.stopFolderWatches() })
	d.checkAchievementsSoon()
	d.countMergedPRsEveryHour()
	d.checkReleaseTagsEveryHour()
	d.saveFeaturesEveryMinute()
	d.watchDisk()
	// The machine's capacity, ok, near or full (#4578).
	d.watchCapacity()
	// The counts on the servers' list view rows (#2033).
	d.startViewCounts()
	// The pull requests waiting for the person, in For Review (#3330).
	d.startReviews()
	// The forge items that ask the person for something, as requests (#3865).
	d.startForgeRequests()
	// The deploys waiting for the person, in Needs You (#3587).
	d.startDeploys()
	d.installDefaultToolsets()
	// Each app worktree's hooks folder, as this build writes it (#6529).
	d.refreshTrailerHooks()
	return d, nil
}

func (d *Daemon) addClient(c *Client) {
	d.mu.Lock()
	d.clients[c.ID] = c
	d.mu.Unlock()
}

func (d *Daemon) removeClient(c *Client) {
	d.mu.Lock()
	delete(d.clients, c.ID)
	d.mu.Unlock()
	d.forgetWatches(c)
	d.forgetPresence(c)
	d.unwatchBrowser(c)
}

// clientsToTell is every client, or none once Shutdown has begun (#3828): what is said as
// the host goes is not heard, and a client that reads nothing would hold each write that
// follows a turn up to its own limit, past Shutdown's wait for them.
func (d *Daemon) clientsToTell() []*Client {
	if d.shuttingDown() {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cs := make([]*Client, 0, len(d.clients))
	for _, c := range d.clients {
		cs = append(cs, c)
	}
	return cs
}

// Broadcast sends a notification to every client.
func Broadcast[P any](d *Daemon, n wire.Notification[P], params P) {
	p, err := json.Marshal(params)
	if err != nil {
		log.Printf("encode %s: %v", n.Name, err)
		return
	}
	b, _ := json.Marshal(message{JSONRPC: "2.0", Method: n.Name, Params: p})
	cs := d.clientsToTell()
	// Those on a control-plane uplink: one frame per uplink, naming who hears it (#167).
	routed := map[*controllink.Uplink][]fanned{}
	for _, c := range cs {
		if f, ok := c.conn.(controllink.FanOuter); ok {
			if up, n, yes := f.FanOutRoute(); yes {
				routed[up] = append(routed[up], fanned{n: n, c: c, tails: f.FanOutLock()})
				continue
			}
		}
		c.writeUnasked(string(b))
	}
	for up, fs := range routed {
		fanOut(up, fs, string(b))
	}
}

// fanned is a client a broadcast reaches in a fan-out frame: its channel's number, and whether
// the control plane reads the frame's lock members.
type fanned struct {
	n     int
	c     *Client
	tails bool
}

// fanOut writes one frame for the clients on an uplink. Each client with a checked channel
// gets its own lock member in the frame (#3541): one HMAC each, and the message once. Their
// writes are held until the frame is written, so the counts go out in order.
func fanOut(up *controllink.Uplink, fs []fanned, line string) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].n < fs[j].n })
	for _, f := range fs {
		f.c.lock.write.Lock()
	}
	defer func() {
		for _, f := range fs {
			f.c.lock.write.Unlock()
		}
	}()
	ns := make([]int, 0, len(fs))
	tails := make([]string, 0, len(fs))
	locked := false
	for _, f := range fs {
		ch := f.c.lock.get()
		switch {
		case ch == nil:
			tails = append(tails, "")
		case !f.tails:
			// A control plane that reads no lock members: this one channel by channel.
			_ = f.c.conn.Write(ch.Seal(line))
			continue
		default:
			tails = append(tails, ch.Tail(line))
			locked = true
		}
		ns = append(ns, f.n)
	}
	if len(ns) == 0 {
		return
	}
	if !locked {
		tails = nil
	}
	_ = up.WriteFanOut(ns, tails, line)
}

// Shutdown ends what the host runs: every runtime it started.
func (d *Daemon) Shutdown() {
	// First, so a stop that goes wrong below still counts as a stop (#1219).
	stoppedAt := d.noteCleanStop()
	// No turn starts, nothing new follows one, and nothing more goes to a client unasked,
	// before the steps below wait for the clock's and the loops' goroutines: a loop's write
	// in line for a client that reads nothing waited, with each write before it, the
	// connection's limit (#6603).
	d.closeAfterTurns()
	d.machineMu.Lock()
	if d.machineStop != nil {
		d.machineStop()
		d.machineStop = nil
	}
	d.machineMu.Unlock()
	// No tick fires a workflow, or scans, once the host is going.
	d.stopWorkflowClock()
	// Nor any of the timer loops: the merged pull requests, the release tags and the rest (#3522).
	d.stopLoops()
	d.stopIdleTimers()
	// Nothing more is routed or written down once the host is going.
	d.attentionMu.Lock()
	d.presences = nil
	for _, t := range d.settling {
		t.Stop()
	}
	d.attentionMu.Unlock()
	// The turns stopped here record their endings and what follows them before the files
	// they write are closed (#1272).
	// What the runtimes leave running is watched again after the restart (#4577).
	d.handBackgroundOver()
	d.stopAll()
	d.waitAfterTurns(afterTurnLimit)
	d.noteCutOffAgents(stoppedAt)
	d.closeReport()
	d.closeLineage()
	d.clearPickUps()
	d.stopMCPEvents()
	d.stopEvents()
	d.stopAppTools()
	d.stopScreenDrives()
	d.allowMu.Lock()
	d.allowStopped = true
	d.allowMu.Unlock()
	d.stopEventWaits()
	d.stopRequestTimer()
	d.stopShells()
	d.stopBrowserDrives()
	d.browser.Shutdown()
	d.stopFolderWatches()
	d.stopPins()
	d.stopSignInRelays()
	d.stopModelRelay()
	// The last chance for chat lines the disk refused: they live only in memory (#3245).
	d.writeHeld()
	// The secret reads still counting, for the next start to send to the audit (#6464).
	d.keepSecretOpsForNextStart()
	// The archived agents' summaries, so the next start trusts them all (#4653).
	d.Store.FlushSummaries()
	// Last: every goroutine the host started has ended, so none writes into the root or
	// reads a setting after this returns (#5653).
	d.waitJobs(jobLimit)
}

// once is true the first time a sendID is seen: a window sends a prompt again for 30 s
// until it hears it was taken, and it must run once.
func (d *Daemon) once(sendID *wire.UUID) bool {
	if sendID == nil {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sent[*sendID] {
		return false
	}
	if len(d.sent) > 4096 {
		d.sent = map[wire.UUID]bool{}
	}
	d.sent[*sendID] = true
	return true
}
