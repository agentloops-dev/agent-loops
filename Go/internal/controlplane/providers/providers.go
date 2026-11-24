package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/workspacecommands"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// MachineRecords is what Providers needs of ControlRecords; records.Records has it.
type MachineRecords interface {
	Hosts() []records.MachineRecord
	Host(id string) *records.MachineRecord
	SaveHost(ctx context.Context, h records.MachineRecord) error
}

// Router is what Providers, Kept and Drafts need of ControlRouter. The service's router
// implements it.
type Router interface {
	// Online says whether a host's uplink is up (HostState.isOnline).
	Online(host string) bool
	// HostState is the state machines/list shows for a host, its workspace laid over it.
	HostState(host string) string
	// SetWorkspace is ControlRouter.setWorkspace: a linked host's workspace changed, or the
	// link went (nil). Said to every client in control/hostChanged when it differs, and laid
	// over the host's state with Overlay.
	SetWorkspace(host string, ws *MachineWorkspace)
	// BroadcastControl tells every client session a control notification.
	BroadcastControl(method string, params any)
	// DeliverKept is ControlRouter.deliverKept: a request sent to a host on a channel of
	// its own, bound to the client that asked, and its reply. Nil when the host is not here,
	// goes before it answers, or takes longer than patience.
	DeliverKept(method string, params json.RawMessage, client, kind, host string, patience time.Duration) *Reply
	// DeliverSigned is DeliverKept for a request its sender signed (#3492): its params and
	// signature go as they came.
	DeliverSigned(d wire.LockDelivery, client, kind, host string, patience time.Duration) *Reply
	// TellControl tells one client session a control notification; false when it has gone.
	TellControl(session, method string, params any) bool
	// HasSession says a client session is still here.
	HasSession(session string) bool
	// Holds says this copy has the host's real uplink.
	Holds(host string) bool
	// Ask is a request of a host on its channel 0, which only the control plane writes:
	// its result, or nil when it is not here, goes or takes longer than patience (#3398).
	Ask(host, method string, params any, patience time.Duration) json.RawMessage
	// HostCan says a host said the capability in its machine/hello (#1748).
	HostCan(host, capability string) bool
}

// Methods is what Providers needs of ControlMethods.
type Methods interface {
	// HostRow is one machines/list row, as it stands now (DaemonAPI.ControlMachine).
	HostRow(h records.MachineRecord) any
	// RemoveHost is machines/remove, as that client asked it.
	RemoveHost(host string, from Caller) error
}

// Timing is WorkspaceProviders.Timing.
type Timing struct {
	// How often every provider is listed (FR-004).
	ListEvery time.Duration
	// How long a started workspace's agentsd has to connect before it is started over ssh.
	SSHAfter time.Duration
	// How long a start has, in all, before it is given up (US1 AS3).
	GiveUp time.Duration
	// How often a start looks for the host's connection.
	LookEvery time.Duration
	Calls     CallTiming
	// What one minute of a stop when idle lasts: a minute, but shorter in tests.
	IdleMinute time.Duration
	// How often stopped workspaces are looked at for a wake-up call that is due (#1004).
	WakeLookEvery time.Duration
}

// StandardTiming is WorkspaceProviders.Timing.standard.
var StandardTiming = Timing{ListEvery: 300 * time.Second, SSHAfter: 120 * time.Second, GiveUp: 600 * time.Second,
	LookEvery: time.Second, Calls: StandardCalls, IdleMinute: time.Minute, WakeLookEvery: 15 * time.Second}

// Hooks is WorkspaceProviders.Hooks: what the executable does for it, what needs ssh,
// processes or the tunnels.
type Hooks struct {
	// SSHStart starts a host's agentsd over ssh, as an install leaves it, without installing.
	// renewKey: its workspace's host key is made again at each start, and this start was
	// the control plane's own, so the key it is offered is taken and pinned again (#1701).
	SSHStart func(host records.MachineRecord, destination string, renewKey bool) error
	// OpenTunnel opens the host's reverse tunnel now, if it has one.
	OpenTunnel func(host string)
	// Stopped: the host's workspace was stopped. Its connection, if one is still here, is
	// old (a tunnel can hold a stopped one open for a minute), and is closed, so a Start
	// waits for a new one (#1701). Nil does nothing.
	Stopped func(host string)
	// InstalledOver is the ssh destination the host was installed over, when its tunnel
	// keeps it (#965); "" for none.
	InstalledOver func(host string) string
	// HostKey is the ssh host key to keep on a host's link, in known_hosts form, for its
	// ssh start (#3379): "" with no needsTrust when its tunnel keeps one already. needsTrust
	// is the fingerprint of a key nobody trusts yet, kept only once trust is that one. Nil
	// keeps none.
	HostKey func(host, destination, trust string) (knownHosts, needsTrust string, err error)
	// Connect is a connection to a provider, as its record says: through the machine that
	// carries it (Carried, #3398). secrets are only those an add or an update was
	// given, sent with each call and kept by neither side; nil for a kept provider.
	Connect func(record ProviderRecord, secrets map[string]string) (Provider, error)
	// HostCommand is the one line that installs a host with a new host code, for a new
	// workspace's first boot (US5), and the host id the machine joins as with it (#5765).
	HostCommand func(person string) (command, host string, err error)
	// SetupStep keeps a step of a made machine's set-up (#5765, epic #5353): 1, create
	// machine, as the provider makes it, and 2, join machine, until it joins. Nil keeps none.
	SetupStep func(host string, report wire.MachineSetupReport)
	// MadeFor keeps, in the store, the workspace a made machine's host id is for, as soon as
	// the provider has made it (#6075): the host that joins as that id is linked to it by its
	// code, also after the control plane restarted and lost linkWhenJoined's wait. Nil keeps
	// none.
	MadeFor func(host, provider, workspace string)
	// Setup is a made machine's set-up as it stands (#6073), for its row while it is made and
	// until it joins. Nil, or nil for a host, shows none.
	Setup func(host string) *wire.MachineSetup
	// BackUp makes a backup of the host, and waits for it, before its workspace is deleted
	// (#1815). False when the host can't make one: it is offline, or older than backups.
	// Nil makes none.
	BackUp func(host string, caller Caller) (bool, error)
	// OwnerOf is the user who owns a host (#994): an agent reaches only its own user's.
	OwnerOf func(host string) string
	// IsAdmin says a user is an admin (#1761). Nil: everyone is.
	IsAdmin func(user string) bool
	// InGroup says a user is in a group (#1761). Nil: everyone is.
	InGroup func(user, group string) bool
	// GroupName is a group's name, for a refusal; "" for none.
	GroupName func(group string) string
	// Suspended says a person's org provider suspended them (#1002, D13): their machines
	// are not started, by hand, for use or for a schedule. Nil: nobody is.
	Suspended func(person string) bool
	// ProjectFolders is the absolute paths of each machine's folders in a project, by
	// machine id, as the machines joined them (#3277). Nil: no machine has any.
	ProjectFolders func(project string) map[string][]string
	// ProjectSessions is projects/sessions for an agent's person (#3277). Nil: refused.
	ProjectSessions func(request wire.ProjectSessionsRequest, caller Caller) (wire.ProjectSessions, error)
	// HasTool says this machine has a CLI on its PATH (#3367). Nil: exec.LookPath.
	HasTool func(name string) bool
}

// Config is what NewProviders is given.
type Config struct {
	Store   store.Store
	Records MachineRecords
	Router  Router
	Methods Methods
	Timing  Timing
	Hooks   Hooks
	Log     func(string)
}

// listed is WorkspaceProviders.Listed: a provider's last list.
type listed struct {
	workspaces []MachineInfo
	listedAt   *time.Time
	// False once a list has failed after a good one.
	current bool
	problem string
	// code is the last failure's, when a client can act on it (#6072): disconnected.
	code string
	// Which list it is, as lists counted them.
	asked uint64
}

// deletedWorkspace is a workspace deleted here, by its provider's id and its own (#6079).
type deletedWorkspace struct{ provider, workspace string }

// deletedKeep is how long a deleted workspace is left out of its provider's lists at most
// (#6079): until a list no longer has it, or this long, in case the delete did not happen.
const deletedKeep = 10 * time.Minute

type activityKind int

const (
	starting activityKind = iota + 1
	stopping
	// deleting: its backup is being made before its workspace is deleted (#1815).
	deleting
)

// activity is WorkspaceProviders.Activity: a start or stop under way, for one host.
type activity struct {
	kind  activityKind
	phase string
	since time.Time
}

type heldProvider struct {
	record ProviderRecord
	etag   string
}

// joining is WorkspaceProviders.Joining: a workspace made here that installs on its first
// boot, until it joins and is linked.
type joining struct {
	provider  string
	workspace string
	// host is the id it joins as, whose set-up step 2 fails when it does not join (#5765).
	host string
	// What it clones once it has joined (#1122), as the client that made it.
	clones []string
	caller *Caller
}

// makingRow is a make its provider has not answered yet (#6073): its name, and the host id
// it joins as.
type makingRow struct {
	name string
	host string
}

// notJoinedMake is a machine New Machine made that the control plane gave up waiting for
// (#4610): its row says so, with Delete, until it is deleted or a host is linked to it.
type notJoinedMake struct {
	provider, workspace, name, reason string
}

// keptNotJoined is how many makes that never joined are kept: the oldest goes first.
const keptNotJoined = 20

// ClonePatience is WorkspaceProviders.clonePatience: how long one clone on a new host may
// take before the next is sent.
const ClonePatience = time.Hour

// Providers is WorkspaceProviders (#691, plan.md): the providers' records, their last
// lists, the links from hosts to workspaces, and every start and stop, one at a time per
// workspace. What it learns it lays over each linked host's state in the router
// (SetWorkspace). It runs only on a control plane with one copy (research R4).
type Providers struct {
	store   store.Store
	records MachineRecords
	router  Router
	methods Methods
	timing  Timing
	hooks   Hooks
	log     func(string)

	mu sync.Mutex
	// Held while changed() reads the records and tells the router, so two at once can
	// never leave the older overlay there (#1296).
	publishing sync.Mutex
	providers  map[string]heldProvider
	// The providers being added, which count for mostProviders (#6762).
	adding      map[string]bool
	connections map[string]Provider
	listed      map[string]*listed
	// Counts the lists asked for, so a slow one never replaces a later one (#1443).
	lists      uint64
	activities map[string]*activity
	// Why each host's last start or stop failed, until the next one.
	problems map[string]string
	kept     *Kept
	// What each host last said about whether it is idle (US4).
	statuses map[string]MachineStatus
	// Each linked host's stop when idle, counting down while it stays idle.
	idleTimers map[string]*time.Timer
	// Stops under way because a host was idle, by how many minutes.
	idleStopping map[string]int
	// "Stopped by Agents after 30 minutes idle", until it is started again.
	stoppedBy map[string]string
	// Codespaces whose host was started over ssh since they were last seen not running
	// (#3348): one try each time one starts.
	broughtBack map[string]bool
	joining     map[string]*joining
	// Makes that failed, by provider (#3972).
	failedMakes map[string][]failedMake
	// The makes a provider has not answered yet (#6073), by provider: each one's row, with its
	// set-up, from the press of Make.
	makingNow map[string][]makingRow
	// The makes the control plane gave up waiting for (#4610), the oldest first.
	notJoined []notJoinedMake
	// The machines installing Agents in a codespace for Add as Machine… (#3497), by how
	// many installs: only their machine/installProgress is said to clients.
	installing map[string]int
	// The OAuth connections started and not finished yet (#6072), by state.
	oauth oauthStarts
	// The hosts whose codespaces their carrying machines are asked to keep awake (#4637).
	awake map[string]keptAwake
	// One make at a time on each provider, by its id (#4706): seven makes sent in one
	// second all failed in 3 s, and GitHub asks for one user's requests one after another.
	making map[string]*sync.Mutex
	// The workspaces deleted here, until when each is left out of its provider's lists
	// (#6079): a provider such as DigitalOcean deletes after it answers, so its next list
	// can still have the workspace.
	deleted  map[deletedWorkspace]time.Time
	done     chan struct{}
	stopOnce sync.Once
}

// NewProviders is WorkspaceProviders.init.
func NewProviders(cfg Config) *Providers {
	if cfg.Log == nil {
		cfg.Log = func(string) {}
	}
	if cfg.Timing.IdleMinute == 0 {
		cfg.Timing.IdleMinute = time.Minute
	}
	if cfg.Timing.WakeLookEvery == 0 {
		cfg.Timing.WakeLookEvery = 15 * time.Second
	}
	if cfg.Timing.Calls == (CallTiming{}) {
		cfg.Timing.Calls = StandardCalls
	}
	h := &cfg.Hooks
	if h.SSHStart == nil {
		h.SSHStart = func(records.MachineRecord, string, bool) error {
			return providerFailure("This control plane can’t start agentsd over ssh.")
		}
	}
	if h.OpenTunnel == nil {
		h.OpenTunnel = func(string) {}
	}
	if h.InstalledOver == nil {
		h.InstalledOver = func(string) string { return "" }
	}
	if h.HostCommand == nil {
		h.HostCommand = func(string) (string, string, error) {
			return "", "", providerFailure("This control plane can’t give out host codes.")
		}
	}
	if h.SetupStep == nil {
		h.SetupStep = func(string, wire.MachineSetupReport) {}
	}
	if h.Setup == nil {
		h.Setup = func(string) *wire.MachineSetup { return nil }
	}
	if h.MadeFor == nil {
		h.MadeFor = func(string, string, string) {}
	}
	if h.OwnerOf == nil {
		h.OwnerOf = func(string) string { return "" }
	}
	return &Providers{store: cfg.Store, records: cfg.Records, router: cfg.Router, methods: cfg.Methods,
		timing: cfg.Timing, hooks: cfg.Hooks, log: cfg.Log,
		providers: map[string]heldProvider{}, adding: map[string]bool{}, connections: map[string]Provider{}, listed: map[string]*listed{},
		activities: map[string]*activity{}, problems: map[string]string{}, statuses: map[string]MachineStatus{},
		idleTimers: map[string]*time.Timer{}, idleStopping: map[string]int{}, stoppedBy: map[string]string{},
		broughtBack: map[string]bool{}, joining: map[string]*joining{}, failedMakes: map[string][]failedMake{}, makingNow: map[string][]makingRow{}, making: map[string]*sync.Mutex{},
		deleted: map[deletedWorkspace]time.Time{}, done: make(chan struct{})}
}

// KeepWith keeps prompts and starts for this control plane's stopped hosts (US3).
func (p *Providers) KeepWith(kept *Kept) {
	p.mu.Lock()
	p.kept = kept
	p.mu.Unlock()
}

// Kept is the kept requests, or nil.
func (p *Providers) Kept() *Kept {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kept
}

// BusyHost is WorkspaceProviders.busyHost: the host linked to a provider's workspace, by
// name, when agents are working there; "" when none. A writer cannot ask to stop it
// (#858, FR-020).
func (p *Providers) BusyHost(provider, workspace string) string {
	if provider == "" {
		return ""
	}
	for _, h := range p.records.Hosts() {
		link := LinkOf(&h)
		if link == nil || link.Provider != provider || link.Workspace != workspace {
			continue
		}
		p.mu.Lock()
		running := len(p.statuses[h.ID].Running)
		p.mu.Unlock()
		if running > 0 {
			return h.Name
		}
	}
	return ""
}

// Start is WorkspaceProviders.start: reads the providers, lists each, and lists again
// every ListEvery (FR-004).
func (p *Providers) Start() {
	p.load()
	if kept := p.Kept(); kept != nil {
		kept.Load()
	}
	p.Refresh("")
	go p.every(p.timing.ListEvery, func() { p.Refresh("") })
	go p.every(p.timing.WakeLookEvery, func() {
		p.wakeUps(time.Now())
		p.keepAwake(time.Now())
	})
}

func (p *Providers) every(interval time.Duration, do func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
			do()
		}
	}
}

// sleep waits d, or says false when the providers stopped.
func (p *Providers) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.done:
		return false
	case <-t.C:
		return true
	}
}

// Stop is WorkspaceProviders.stop: the lists stop, and every connection ends.
func (p *Providers) Stop() {
	p.stopOnce.Do(func() { close(p.done) })
	p.mu.Lock()
	connections := p.connections
	p.connections = map[string]Provider{}
	for _, t := range p.idleTimers {
		t.Stop()
	}
	p.idleTimers = map[string]*time.Timer{}
	p.mu.Unlock()
	for _, c := range connections {
		c.End()
	}
}

func (p *Providers) load() {
	ctx := context.Background()
	keys, err := p.store.List(ctx, ProvidersPrefix)
	if err != nil {
		return
	}
	for _, key := range keys {
		object, err := p.store.Get(ctx, key.Key)
		if err != nil || object == nil {
			continue
		}
		var record ProviderRecord
		if json.Unmarshal(object.Data, &record) != nil || (record.Forgotten != nil && *record.Forgotten) {
			continue
		}
		if !knownKind(record.Kind) {
			// An MCP server provider, a kind that was removed (#6623): its record stays as it is.
			p.log(fmt.Sprintf("provider: %s is kind %s, which this build no longer runs; not loaded", record.ID, record.Kind))
			continue
		}
		p.mu.Lock()
		p.providers[record.ID] = heldProvider{record, object.ETag}
		p.mu.Unlock()
		p.loadFailed(record.ID)
	}
}

// MARK: WorkspaceHandling

// Answer is WorkspaceHandling.answer: one of WorkspaceMethods, from a client.
func (p *Providers) Answer(method string, params json.RawMessage, from Caller) (any, error) {
	return p.answer(method, params, from, "client "+shortID(from.Client))
}

// HostSaid answers machine/machines, machine/status and machine/installProgress, which a host says
// on its channel 0, as ControlMethods.hostSaid does for them.
func (p *Providers) HostSaid(host, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodHostMachines:
		var call MachineWorkspaceCall
		if err := requireParams(params, &call, "MachineWorkspaceCall", "agent", "method", "params"); err != nil {
			return nil, err
		}
		if !AgentWorkspaceMethods[call.Method] {
			return nil, fail(CodeNotPermitted, "Agent Loops can’t ask that of the control plane.")
		}
		return p.AgentCall(host, call.Agent, call.Method, call.Params, call.Deliver)
	case MethodHostStatus:
		var status MachineStatus
		if requireParams(params, &status, "MachineStatus", "idle", "running") == nil {
			p.Heard(host, status)
		}
		return map[string]any{}, nil
	case wire.NotifyMachineInstallProgress.Name:
		var step wire.ControlInstallProgress
		if requireParams(params, &step, "ControlInstallProgress", "name", "step") == nil {
			p.installProgress(host, step)
		}
		return map[string]any{}, nil
	}
	return nil, methodNotFound(method)
}

// AgentCall is WorkspaceHandling.agentCall: an agent on host asked through its workspace
// tools (US6), one of AgentWorkspaceMethods, logged as that agent's.
// deliver is what the other machine is to get, as this machine signed it (#3492, D7).
func (p *Providers) AgentCall(host, agent, method string, params json.RawMessage, deliver *wire.LockDelivery) (any, error) {
	hostName := host
	if h := p.records.Host(host); h != nil {
		hostName = h.Name
	}
	// No client: the host's own channel.
	caller := Caller{Session: records.NewUUID(), Client: records.NewUUID(), Kind: "unknown", Person: p.hooks.OwnerOf(host)}
	if method == MethodWorkspacesAgent {
		return p.forAgent(caller.Person), nil
	}
	var given map[string]any
	if decodeJSON(params, &given) != nil || given == nil {
		return nil, invalid("Those parameters aren’t an object.")
	}
	if err := p.agentHost(given, caller.Person); err != nil {
		return nil, err
	}
	switch method {
	case MethodSessionOnMachine:
		return p.startSessionOn(hostName, agent, rawOf(given), caller, deliver)
	case MethodSessionCall:
		return p.sessionCall(host, hostName, agent, rawOf(given), caller, deliver)
	case MethodSessionEnded:
		return p.sessionEnded(hostName, rawOf(given), caller, deliver)
	case MethodSendFolder:
		return p.sendFolder(hostName, agent, rawOf(given), caller, deliver)
	case MethodBotKey:
		return p.botKey(host, hostName, rawOf(given), caller, deliver)
	case MethodProjectFolders:
		return p.projectFolders(rawOf(given), caller)
	case MethodProjectSessions:
		return p.projectSessions(host, rawOf(given), caller)
	}
	if method == MethodProvidersCreate || method == MethodProvidersOptions || method == MethodProvidersDeleteWorkspace {
		if err := p.agentProvider(given); err != nil {
			return nil, err
		}
	}
	// An agent deletes this way only a failed make's row (#6078) or a machine with no host
	// that its person is shown (#6080): delete_machine deletes a machine by its host, with
	// its backup.
	if method == MethodProvidersDeleteWorkspace {
		id, _ := str(given["id"])
		workspace, _ := str(given["workspace"])
		if _, failed := p.failedOwner(id, workspace); !failed && !p.agentSees(caller.Person, given) {
			return nil, fail(CodeNoSuchHost, "No machine is called %s.", workspace)
		}
	}
	return p.answer(method, rawOf(given), caller, "agent "+agent+" on "+hostName)
}

// agentHost writes the host given names by its id, as an agent names hosts as it saw
// them listed: by name, or by id. Another user's machine is not there for this agent
// (FR-038).
func (p *Providers) agentHost(given map[string]any, person string) error {
	if named, ok := str(given["host"]); ok && p.records.Host(named) == nil {
		found := ""
		for _, h := range p.records.Hosts() {
			if strings.EqualFold(h.Name, named) {
				found = h.ID
				break
			}
		}
		if found == "" {
			return fail(CodeNoSuchHost, "No machine is called %s.", named)
		}
		given["host"] = found
	}
	if named, ok := str(given["host"]); ok && p.hooks.OwnerOf(named) != person {
		return fail(CodeNoSuchHost, "No machine is called %s.", named)
	}
	return nil
}

// agentProvider writes the provider given names by its id, as an agent names providers
// as it saw them listed: by name, or by id.
func (p *Providers) agentProvider(given map[string]any) error {
	named, ok := str(given["id"])
	if !ok {
		return nil
	}
	p.mu.Lock()
	_, known := p.providers[named]
	found := ""
	if !known {
		for _, held := range p.providers {
			if strings.EqualFold(held.record.Name, named) {
				found = held.record.ID
				break
			}
		}
	}
	p.mu.Unlock()
	if known {
		return nil
	}
	if found == "" {
		return invalid("No provider is called %s.", named)
	}
	given["id"] = found
	return nil
}

// agentSees says whether the workspace given names is one of person's machines with no
// host, as forAgent lists it (#6080).
func (p *Providers) agentSees(person string, given map[string]any) bool {
	id, _ := str(given["id"])
	workspace, _ := str(given["workspace"])
	for _, provider := range p.forAgent(person).Providers {
		if string(provider.ID) != id {
			continue
		}
		for _, w := range provider.Workspaces {
			if w.ID == workspace && w.Host == nil {
				return true
			}
		}
	}
	return false
}

// forAgent is what an agent is shown (US6 AS1): every provider's workspaces and their
// hosts, and what a new one could be, never a secret or its name.
func (p *Providers) forAgent(person string) AgentMachines {
	hosts := p.records.Hosts()
	out := AgentMachines{Providers: []AgentProvider{}}
	for _, summary := range p.SummariesFor(person) {
		// Only the providers its user may start machines from (#1761).
		if summary.MayUse != nil && !*summary.MayUse {
			continue
		}
		canMake, canDelete := contains(summary.Capabilities, "create"), contains(summary.Capabilities, "delete")
		var options any
		if canMake {
			options, _ = p.providerCall(string(summary.ID), func(c Provider) (any, error) { return c.Options() })
		}
		workspaces := []AgentWorkspace{}
		for _, w := range summary.Workspaces {
			aw := AgentWorkspace{ID: w.ID, Name: w.Name, State: w.State, Cost: w.Cost, Reason: w.Reason}
			for _, h := range hosts {
				if w.Host != nil && *w.Host != "" && h.ID == string(*w.Host) {
					aw.Host = &h.Name
					aw.HostState = new(p.router.HostState(h.ID))
				}
			}
			workspaces = append(workspaces, aw)
		}
		out.Providers = append(out.Providers, AgentProvider{ID: summary.ID, Name: summary.Name, CanMake: canMake,
			CanDelete: canDelete, Options: jsonValue(options), Workspaces: workspaces})
	}
	// Every machine of the person's, made by a provider or not, for start_session (#2887).
	out.Hosts = []AgentHost{}
	for _, h := range hosts {
		if p.hooks.OwnerOf(h.ID) == person {
			out.Hosts = append(out.Hosts, AgentHost{Name: h.Name, State: p.router.HostState(h.ID)})
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p *Providers) answer(method string, params json.RawMessage, caller Caller, who string) (any, error) {
	if err := p.refuse(method, params, caller); err != nil {
		return nil, err
	}
	switch method {
	case MethodProvidersList:
		// As last listed: a page that opens asks for providers/refresh too (FR-004).
		return map[string]any{"providers": p.SummariesFor(caller.Person)}, nil
	case MethodProvidersRefresh:
		var request providerRequest
		// The params are optional: none, or ones that do not read, are the defaults.
		_ = decodeJSON(params, &request)
		go p.Refresh(string(deref(request.ID)))
		return map[string]any{}, nil
	case MethodProvidersAdd, MethodProvidersUpdate:
		return p.answerSave(params, method == MethodProvidersUpdate, caller)
	case MethodProvidersOAuthStart:
		var request wire.ProviderOAuthStart
		if err := requireParams(params, &request, "ProviderOAuthStart", "preset"); err != nil {
			return nil, err
		}
		return p.oauthStart(request, caller)
	case MethodProvidersOAuthFinish:
		var request wire.ProviderOAuthFinish
		if err := requireParams(params, &request, "ProviderOAuthFinish"); err != nil {
			return nil, err
		}
		return p.oauthFinish(request, caller)
	case MethodProvidersMakeDefault:
		return p.answerProviderID(params, func(id string) error { return p.makeDefault(caller.Person, id) })
	case MethodProvidersRemove:
		return p.answerProviderID(params, p.remove)
	case MethodHostsLink:
		return p.answerLink(params, caller)
	case MethodHostsUnlink:
		var request hostRequest
		if err := requireParams(params, &request, "MachineRequest", "host"); err != nil {
			return nil, err
		}
		return p.changeLink(string(request.Host), func(h *records.MachineRecord) { setLink(h, nil) }, nil)
	case MethodHostsSetOptions:
		return p.answerSetOptions(params)
	case MethodHostsStart:
		var request hostRequest
		if err := requireParams(params, &request, "MachineRequest", "host"); err != nil {
			return nil, err
		}
		return p.begin(starting, string(request.Host), who)
	case MethodHostsStop:
		return p.answerStop(params, who)
	case MethodProvidersOptions:
		var request providerRequest
		if err := requireParams(params, &request, "ProviderRequest"); err != nil {
			return nil, err
		}
		return p.newMachineForm(caller.Person, string(deref(request.ID)))
	case MethodProvidersCreate:
		var request ProviderCreate
		if err := requireParams(params, &request, "ProviderCreate"); err != nil {
			return nil, err
		}
		return p.create(request, who, &caller)
	case MethodProvidersDeleteWorkspace:
		return p.answerDeleteWorkspace(params, caller, who)
	case MethodHostsDelete:
		var request hostRequest
		if err := requireParams(params, &request, "MachineRequest", "host"); err != nil {
			return nil, err
		}
		return map[string]any{}, p.deleteMachine(string(request.Host), who, caller)
	case MethodHostsKeep:
		return p.answerKeep(params, caller)
	case MethodHostsListKept, MethodHostsDropKept, MethodHostsRetryKept:
		kept := p.Kept()
		if kept == nil {
			return map[string]any{"kept": []any{}}, nil
		}
		return kept.Answer(method, params, caller.Person)
	}
	return nil, methodNotFound(method)
}

// answerSave is providers/add and providers/update.
func (p *Providers) answerSave(params json.RawMessage, updating bool, caller Caller) (any, error) {
	var request ProviderAdd
	if err := requireParams(params, &request, "ProviderAdd", "name", "kind"); err != nil {
		return nil, err
	}
	if !knownKind(request.Kind) {
		return nil, invalid("Those parameters are not a ProviderAdd.")
	}
	return p.save(request, updating, caller)
}

// answerProviderID reads a ProviderRequest that must name a provider, and does work on it.
func (p *Providers) answerProviderID(params json.RawMessage, work func(id string) error) (any, error) {
	var request providerRequest
	if err := requireParams(params, &request, "ProviderRequest"); err != nil {
		return nil, err
	}
	if deref(request.ID) == "" {
		return nil, invalid("Which provider?")
	}
	return map[string]any{}, work(string(*request.ID))
}

// answerLink is hosts/link.
func (p *Providers) answerLink(params json.RawMessage, caller Caller) (any, error) {
	var request MachineLink
	if err := requireParams(params, &request, "MachineLink", "host", "provider", "workspace"); err != nil {
		return nil, err
	}
	if !p.mayLink(caller.Person, string(request.Provider), request.Workspace) {
		return nil, invalid("There is no such workspace.")
	}
	return p.link(request)
}

// answerSetOptions is hosts/setOptions.
func (p *Providers) answerSetOptions(params json.RawMessage) (any, error) {
	var options MachineOptions
	if err := requireParams(params, &options, "MachineOptions", "host"); err != nil {
		return nil, err
	}
	return p.changeLink(string(options.Host), func(h *records.MachineRecord) {
		link := LinkOf(h)
		if link == nil {
			return
		}
		applyOptions(link, options)
		setLink(h, link)
	}, func() { p.countIdle(string(options.Host)) })
}

// applyOptions sets on link each option that options gives.
func applyOptions(link *WorkspaceLink, options MachineOptions) {
	if options.StartWhenUsed != nil {
		link.StartWhenUsed = options.StartWhenUsed
	}
	if options.StopWhenIdleMinutes != nil {
		if *options.StopWhenIdleMinutes > 0 {
			link.StopWhenIdleMinutes = intOf(options.StopWhenIdleMinutes)
		} else {
			link.StopWhenIdleMinutes = nil
		}
	}
	if options.StartForSchedules != nil {
		link.StartForSchedules = options.StartForSchedules
	}
}

// answerStop is hosts/stop: asked first while agents are working there (US4 AS4).
func (p *Providers) answerStop(params json.RawMessage, who string) (any, error) {
	var request MachineStop
	if err := requireParams(params, &request, "MachineStop", "host"); err != nil {
		return nil, err
	}
	host := string(request.Host)
	p.mu.Lock()
	running := p.statuses[host].Running
	p.mu.Unlock()
	if (request.ConfirmRunning == nil || !*request.ConfirmRunning) && len(running) > 0 && p.router.Online(host) {
		name := host
		if h := p.records.Host(host); h != nil {
			name = h.Name
		}
		verb := "are"
		if len(running) == 1 {
			verb = "is"
		}
		f := fail(CodeAgentsWorking, "Stop %s? %s %s working there.", name, andList(running), verb)
		f.Data = rawOf(map[string]any{"running": running})
		return nil, f
	}
	return p.begin(stopping, host, who)
}

// answerDeleteWorkspace is providers/deleteWorkspace.
func (p *Providers) answerDeleteWorkspace(params json.RawMessage, caller Caller, who string) (any, error) {
	var request wire.ProviderWorkspaceRequest
	if err := requireParams(params, &request, "ProviderWorkspaceRequest", "id", "workspace"); err != nil {
		return nil, err
	}
	if _, failed := p.failedOwner(string(request.ID), request.Workspace); failed {
		return map[string]any{}, p.deleteFailed(string(request.ID), request.Workspace, caller.Person, who)
	}
	return map[string]any{}, p.deleteWorkspace(string(request.ID), request.Workspace, who)
}

// answerKeep is hosts/keep.
func (p *Providers) answerKeep(params json.RawMessage, caller Caller) (any, error) {
	var request KeepRequest
	if err := requireParams(params, &request, "KeepRequest", "host", "method", "params"); err != nil {
		return nil, err
	}
	kept := p.Kept()
	var answer *Failure
	if kept != nil {
		answer = kept.Keep(string(request.Host), request.Method, request.Params, caller, request.Signed)
	}
	if answer == nil {
		name := "That machine"
		if h := p.records.Host(string(request.Host)); h != nil {
			name = h.Name
		}
		return nil, fail(CodeHostOffline, "%s is offline.", name)
	}
	return nil, answer
}

// MARK: Providers

// Summaries is WorkspaceProviders.summaries: every provider for the Providers page, by name.
func (p *Providers) Summaries() []ProviderSummary {
	linked := map[string]string{}
	for _, h := range p.records.Hosts() {
		if link := LinkOf(&h); link != nil {
			linked[link.Provider+"/"+link.Workspace] = h.ID
		}
	}
	p.mu.Lock()
	held := make([]heldProvider, 0, len(p.providers))
	for _, h := range p.providers {
		held = append(held, h)
	}
	lists := map[string]listed{}
	for id, l := range p.listed {
		lists[id] = *l
	}
	failed := map[string][]failedMake{}
	for id, f := range p.failedMakes {
		failed[id] = append([]failedMake{}, f...)
	}
	// The makes New Machine still waits for, and the ones it gave up on (#4610).
	joiningNow := map[string]string{}
	for _, j := range p.joining {
		joiningNow[j.provider+"/"+j.workspace] = j.host
	}
	makingNow := map[string][]makingRow{}
	for id, rows := range p.makingNow {
		makingNow[id] = append([]makingRow{}, rows...)
	}
	notJoinedNow := map[string]string{}
	for _, n := range p.notJoined {
		notJoinedNow[n.provider+"/"+n.workspace] = n.reason
	}
	p.mu.Unlock()
	sort.Slice(held, func(i, j int) bool { return held[i].record.Name < held[j].record.Name })
	out := []ProviderSummary{}
	for _, h := range held {
		r := h.record
		l, ok := lists[r.ID]
		if !ok {
			l = listed{current: true}
		}
		workspaces := summaryWorkspaces(r.ID, l.workspaces, linked, joiningNow, notJoinedNow, p.hooks.Setup)
		for _, m := range makingNow[r.ID] {
			workspaces = append(workspaces, MachineInfo{ID: "making-" + m.name, Name: m.name, State: "starting",
				Joining: new(true), Setup: p.hooks.Setup(m.host)})
		}
		for _, f := range failed[r.ID] {
			workspaces = append(workspaces, f.info)
		}
		out = append(out, p.summary(r, l, workspaces))
	}
	return out
}

// summaryWorkspaces is a provider's listed workspaces with their hosts, and, for one with
// no host, whether New Machine still waits for it or gave up on it (#4610), and how its
// set-up stands while it waits (#6073).
func summaryWorkspaces(provider string, listed []MachineInfo, linked map[string]string, joiningNow map[string]string,
	notJoinedNow map[string]string, setup func(host string) *wire.MachineSetup) []MachineInfo {
	workspaces := []MachineInfo{}
	for _, w := range listed {
		key := provider + "/" + w.ID
		w.Host = optional(wire.MachineRef(linked[key]))
		if w.Host == nil {
			if host, ok := joiningNow[key]; ok {
				w.Joining = new(true)
				if host != "" {
					w.Setup = setup(host)
				}
			}
			if why, ok := notJoinedNow[key]; ok {
				w.NotJoined = &why
			}
		}
		workspaces = append(workspaces, w)
	}
	return workspaces
}

// summary is one provider's row for the Providers page.
func (p *Providers) summary(r ProviderRecord, l listed, workspaces []MachineInfo) ProviderSummary {
	// Its machine keeps each one it was given (#3398).
	secrets := map[string]bool{}
	for _, name := range r.SecretNames {
		secrets[name] = true
	}
	caps := r.Capabilities
	if caps == nil {
		caps = []string{}
	}
	s := ProviderSummary{ID: wire.ProviderID(r.ID), Name: r.Name, Kind: r.Kind, Secrets: secrets, Capabilities: caps,
		Contract: int64(r.Contract), Preset: optional(r.preset()), Workspaces: workspaces,
		Current: l.current, Problem: optional(l.problem), Group: r.Group, Oauth: summaryOAuth(r, l)}
	if machine := deref(r.Machine); machine != "" {
		s.Machine = optional(wire.MachineRef(machine))
		if h := p.records.Host(machine); h != nil {
			s.MachineName = optional(h.Name)
		}
		if !p.router.Online(machine) {
			yes := true
			s.MachineOffline = &yes
		}
	}
	// A carried codespace is reached over ssh from its machine, not from here (#3398).
	if tool := sshTool(r.preset()); tool != "" && !(r.preset() == "codespaces-api" && deref(r.Machine) != "") && !p.hasTool(tool) {
		s.SshToolMissing = &tool
	}
	if l.listedAt != nil {
		s.ListedAt = dateOf(*l.listedAt)
	}
	return s
}

// save adds or changes a provider: connected, checked and listed before it is kept
// (US2 AS1–2).
func (p *Providers) save(request ProviderAdd, updating bool, caller Caller) (ProviderSummary, error) {
	return p.saveConnected(request, updating, caller, nil)
}

// connectedSave is what an OAuth connection (#6072) adds to a save: the secrets its
// machine keeps already, by name, and whose account they are for.
type connectedSave struct {
	// id is the provider's: its machine keeps the tokens under it.
	id      string
	machine string
	names   []string
	oauth   *OAuthConnection
}

// saveConnected is save, for a provider whose machine keeps an OAuth connection's tokens
// already when connected is not nil.
func (p *Providers) saveConnected(request ProviderAdd, updating bool, caller Caller, connected *connectedSave) (ProviderSummary, error) {
	id, etag, heldBefore, err := p.saving(request, updating)
	if err != nil {
		return ProviderSummary{}, err
	}
	if connected != nil {
		id = connected.id
	}
	release, err := p.reserve(id, updating)
	if err != nil {
		return ProviderSummary{}, err
	}
	defer release()
	var machine string
	if connected != nil {
		machine = connected.machine
	} else if machine, err = p.carrier(request, heldBefore, caller); err != nil {
		return ProviderSummary{}, err
	}
	// An update keeps the commands it leaves out, as it keeps the secrets.
	if heldBefore != nil && request.Kind == heldBefore.Kind && request.Commands == nil {
		request.Commands = jsonValue(heldBefore.Commands)
	}
	given, names, err := savedSecrets(request, heldBefore, machine)
	if err != nil {
		return ProviderSummary{}, err
	}
	record := ProviderRecord{ID: id, Name: trimSpaces(request.Name), Kind: request.Kind, SecretNames: names, Contract: 1,
		Machine: &machine}
	switch {
	case connected != nil:
		record.OAuth = connected.oauth
		record.SecretNames = unionSorted(names, connected.names)
	case heldBefore != nil && heldBefore.OAuth != nil && !pastesToken(request):
		record.OAuth = heldBefore.OAuth
	case heldBefore != nil && heldBefore.OAuth != nil:
		// A pasted token replaces the connection: its machine forgets the rest of it.
		record.SecretNames = withoutOAuthNames(names)
	}
	record.Commands = valueOf(request.Commands)
	if problem := record.Problem(); problem != "" {
		return ProviderSummary{}, invalid("%s", problem)
	}
	// Checked with what it was given, laid over what its machine keeps, before either keeps
	// anything.
	trying, err := p.hooks.Connect(record, given)
	var checked Checked
	if err == nil {
		checked, err = trying.Check()
		trying.End()
	}
	if err != nil {
		return ProviderSummary{}, invalid("%s", errorWords(err))
	}
	record.Capabilities = checked.Capabilities
	if record.Capabilities == nil {
		record.Capabilities = []string{}
	}
	record.Contract = checked.Contract
	if record.Contract == 0 {
		record.Contract = 1
	}
	record.Rev = 1
	if heldBefore != nil {
		record.Rev = heldBefore.Rev + 1
		record.Group = heldBefore.Group
		record.AddedAt = heldBefore.AddedAt
	} else {
		record.AddedAt = new(records.Now())
	}
	data, err := encodeWithin(record, heldBefore)
	if err != nil {
		return ProviderSummary{}, err
	}
	if len(given) > 0 {
		if _, err := Ask(p.router, machine, p.hostName(machine), wire.CallProvidersKeepSecrets.Name,
			wire.ProviderSecretsKeep{Provider: wire.ProviderID(id), Secrets: given}, time.Minute); err != nil {
			return ProviderSummary{}, invalid("%s", errorWords(err))
		}
	}
	condition := store.Absent
	if etag != "" {
		condition = store.Matching(etag)
	}
	newTag, err := p.store.Put(context.Background(), ProviderKey(id), data, condition)
	if err != nil {
		return ProviderSummary{}, err
	}
	if heldBefore != nil && deref(heldBefore.Machine) != "" && deref(heldBefore.Machine) != machine {
		p.forget(*heldBefore)
	}
	connection, err := p.hooks.Connect(record, nil)
	if err != nil {
		return ProviderSummary{}, err
	}
	now := time.Now()
	p.mu.Lock()
	p.providers[id] = heldProvider{record, newTag}
	old := p.connections[id]
	p.connections[id] = connection
	p.lists++
	p.listed[id] = &listed{workspaces: checked.Workspaces, listedAt: &now, current: true, asked: p.lists}
	p.mu.Unlock()
	if old != nil {
		old.End()
	}
	p.logSaved(record, updating, len(checked.Workspaces))
	p.changed()
	return p.savedSummary(id)
}

// savedSummary is the summary of the provider that was just kept.
func (p *Providers) savedSummary(id string) (ProviderSummary, error) {
	for _, s := range p.Summaries() {
		if string(s.ID) == id {
			return s, nil
		}
	}
	return ProviderSummary{}, invalid("There is no such provider.")
}

// saving is the id of the provider that request adds or changes, and, when it changes
// one, its etag and record as they are kept. A name another provider has is refused.
func (p *Providers) saving(request ProviderAdd, updating bool) (id, etag string, heldBefore *ProviderRecord, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if updating {
		held, ok := p.providers[string(deref(request.ID))]
		if deref(request.ID) == "" || !ok {
			return "", "", nil, invalid("There is no such provider.")
		}
		id, etag = string(*request.ID), held.etag
		r := held.record
		heldBefore = &r
	} else {
		id = MakeProviderID(request.Name)
	}
	for _, h := range p.providers {
		if h.record.ID != id && h.record.Name == trimSpaces(request.Name) {
			return "", "", nil, invalid("There is already a provider called %s.", request.Name)
		}
	}
	return id, etag, heldBefore, nil
}

// savedSecrets is the secrets request gives, which go on to its machine, and the names of
// all its secrets, which the control plane keeps. A provider that moves to another machine
// must be given each of its secrets again.
func savedSecrets(request ProviderAdd, before *ProviderRecord, machine string) (map[string]string, []string, error) {
	given := map[string]string{}
	named := map[string]bool{}
	for k, v := range request.Secrets {
		if v != "" {
			given[k] = v
			named[k] = true
		}
	}
	if before != nil {
		moved := deref(before.Machine) != machine
		for _, k := range before.SecretNames {
			if moved && !named[k] {
				return nil, nil, invalid("Give its %s again: its secrets stay on the machine that carried it.", k)
			}
			named[k] = true
		}
	}
	names := make([]string, 0, len(named))
	for k := range named {
		names = append(names, k)
	}
	sort.Strings(names)
	return given, names, nil
}

// logSaved logs a provider that was added or changed.
func (p *Providers) logSaved(record ProviderRecord, updating bool, workspaces int) {
	verb := "added"
	if updating {
		verb = "changed"
	}
	extra := ""
	if preset := record.preset(); preset != "" {
		extra += ", preset " + preset
	}
	p.log(fmt.Sprintf("provider: %s %s (%s%s), %d workspaces", verb, record.ID, record.Kind, extra, workspaces))
}

// remove is a provider going; its hosts stay, unlinked (US2 AS5).
func (p *Providers) remove(id string) error {
	p.mu.Lock()
	held, ok := p.providers[id]
	p.mu.Unlock()
	if !ok {
		return invalid("There is no such provider.")
	}
	data, err := records.Encode(held.record.tombstone())
	if err != nil {
		return err
	}
	if _, err := p.store.Put(context.Background(), ProviderKey(id), data, store.Matching(held.etag)); err != nil {
		return err
	}
	p.mu.Lock()
	delete(p.providers, id)
	delete(p.listed, id)
	for d := range p.deleted {
		if d.provider == id {
			delete(p.deleted, d)
		}
	}
	connection := p.connections[id]
	delete(p.connections, id)
	p.mu.Unlock()
	if connection != nil {
		connection.End()
	}
	p.forget(held.record)
	p.forgetFailed(id)
	for _, h := range p.records.Hosts() {
		if link := LinkOf(&h); link != nil && link.Provider == id {
			setLink(&h, nil)
			if err := p.records.SaveHost(context.Background(), h); err != nil {
				p.log("provider " + id + ": " + h.Name + " could not be unlinked: " + err.Error())
			}
		}
	}
	p.log("provider: removed " + id)
	p.changed()
	return nil
}

// Refresh is WorkspaceProviders.refresh: lists one provider, or every one ("" for every);
// a failed list keeps the last, marked not current.
func (p *Providers) Refresh(only string) {
	var ids []string
	if only != "" {
		ids = []string{only}
	} else {
		p.mu.Lock()
		for id := range p.providers {
			ids = append(ids, id)
		}
		p.mu.Unlock()
	}
	type result struct {
		id         string
		workspaces []MachineInfo
		err        error
		asked      uint64
	}
	results := make(chan result)
	count := 0
	for _, id := range ids {
		connection, err := p.connection(id)
		if err != nil {
			continue
		}
		count++
		p.mu.Lock()
		p.lists++
		asked := p.lists
		p.mu.Unlock()
		go func() {
			workspaces, err := connection.List()
			results <- result{id, workspaces, err, asked}
		}()
	}
	for range count {
		r := <-results
		p.mu.Lock()
		// A list asked for before the one shown says what was, not what is: a stop's list
		// replaced by the older one of a link or a poll said the workspace was running.
		if _, ok := p.providers[r.id]; ok && (p.listed[r.id] == nil || p.listed[r.id].asked < r.asked) {
			l := p.listed[r.id]
			if l == nil {
				l = &listed{current: true}
			}
			if r.err == nil {
				now := time.Now()
				l = &listed{workspaces: p.leaveOutDeletedLocked(r.id, r.workspaces, now), listedAt: &now, current: true, asked: r.asked}
			} else {
				l.current = l.listedAt == nil
				l.problem = errorWords(r.err)
				l.code = failureCode(r.err)
				l.asked = r.asked
			}
			p.listed[r.id] = l
		}
		p.mu.Unlock()
	}
	p.changed()
	p.bringBack()
}

// sshTool is the CLI an API preset's machines are reached through over ssh, as the ssh
// ProxyCommand: gh for a codespace, coder for a Coder workspace; "" for none.
func sshTool(preset string) string {
	switch preset {
	case "codespaces-api":
		return "gh"
	case "coder-api":
		return "coder"
	}
	return ""
}

// hasTool is Hooks.HasTool, else whether exec finds the CLI.
func (p *Providers) hasTool(name string) bool {
	if p.hooks.HasTool != nil {
		return p.hooks.HasTool(name)
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// startsNoHost is whether a preset's workspaces never start their host by themselves: a
// codespace has no systemd, so after a stop and a start its host is down (#3348).
func startsNoHost(preset string) bool { return preset == "codespaces" || preset == "codespaces-api" }

// bringBack starts over ssh the host of each linked codespace that a list says is
// running, while its host is offline and no start or stop is under way (#3348): one
// started from GitHub, not from Agents, or whose start here ended before its host came
// back. One try each time it starts: a codespace listed not running may be tried again.
func (p *Providers) bringBack() {
	type due struct {
		host        records.MachineRecord
		link        WorkspaceLink
		destination string
	}
	var all []due
	for _, h := range p.records.Hosts() {
		link := LinkOf(&h)
		if link == nil {
			continue
		}
		p.mu.Lock()
		held, known := p.providers[link.Provider]
		w := p.workspaceLocked(*link)
		busy := p.activities[h.ID] != nil
		if !known || !startsNoHost(held.record.preset()) || w == nil {
			p.mu.Unlock()
			continue
		}
		if w.State != string(workspacecommands.Running) {
			delete(p.broughtBack, h.ID)
			p.mu.Unlock()
			continue
		}
		tried := p.broughtBack[h.ID]
		p.mu.Unlock()
		if tried || busy || p.router.Online(h.ID) || p.suspendedOwner(h.ID) {
			continue
		}
		destination := p.sshDestination(*link, h.ID)
		if destination == "" {
			continue
		}
		p.mu.Lock()
		p.broughtBack[h.ID] = true
		p.mu.Unlock()
		all = append(all, due{h, *link, destination})
	}
	for _, d := range all {
		go func() {
			p.hooks.OpenTunnel(d.host.ID)
			if err := p.sshStart(d.host, d.link, d.destination, false); err != nil {
				p.log(fmt.Sprintf("workspace: ssh start %s, running with its host offline: failed: %s", d.host.Name, errorWords(err)))
				return
			}
			p.log(fmt.Sprintf("workspace: ssh start %s, running with its host offline: ok", d.host.Name))
		}()
	}
}

// connection is a provider's connection, made when there is none.
func (p *Providers) connection(id string) (Provider, error) {
	p.mu.Lock()
	held, ok := p.connections[id]
	record, known := p.providers[id]
	p.mu.Unlock()
	if ok {
		return held, nil
	}
	if !known {
		return nil, providerFailure("That provider was removed.")
	}
	made, err := p.hooks.Connect(record.record, nil)
	if err != nil {
		var failure *ProviderFailure
		if errors.As(err, &failure) {
			p.mu.Lock()
			l := p.listed[id]
			if l == nil {
				l = &listed{}
			}
			l.current = false
			l.problem = failure.Words
			p.listed[id] = l
			p.mu.Unlock()
		}
		return nil, err
	}
	p.mu.Lock()
	if other, ok := p.connections[id]; ok {
		p.mu.Unlock()
		made.End()
		return other, nil
	}
	p.connections[id] = made
	p.mu.Unlock()
	return made, nil
}

// MARK: Links

// mayLink says the person may link the workspace to a machine: an admin, any; a member,
// only one they are shown, on none of the machines or on one of their own.
func (p *Providers) mayLink(person, provider, workspace string) bool {
	if p.isAdmin(person) || p.hooks.OwnerOf == nil {
		return true
	}
	for _, h := range p.records.Hosts() {
		if link := LinkOf(&h); link != nil && link.Provider == provider && link.Workspace == workspace &&
			p.hooks.OwnerOf(h.ID) != person {
			return false
		}
	}
	for _, s := range p.SummariesFor(person) {
		if string(s.ID) != provider {
			continue
		}
		for _, w := range s.Workspaces {
			if w.ID == workspace {
				return true
			}
		}
	}
	return false
}

func (p *Providers) link(request MachineLink) (any, error) {
	return p.linkKeying(request, true)
}

// linkKeying is link; with keyed false it asks for no host key and keeps none, as for a key
// that can't be fetched (LinkJoined, #6581).
func (p *Providers) linkKeying(request MachineLink, keyed bool) (any, error) {
	host, provider := string(request.Host), string(request.Provider)
	p.mu.Lock()
	_, ok := p.providers[provider]
	p.mu.Unlock()
	if !ok {
		return nil, invalid("There is no such provider.")
	}
	knownHosts := ""
	if keyed {
		var err error
		knownHosts, err = p.linkHostKey(host, WorkspaceLink{Provider: provider, Workspace: request.Workspace}, deref(request.Trust))
		if err != nil {
			return nil, err
		}
	}
	p.forgetNotJoined(provider, request.Workspace)
	// One host per workspace: linking moves the link.
	for _, other := range p.records.Hosts() {
		link := LinkOf(&other)
		if other.ID != host && link != nil && link.Provider == provider && link.Workspace == request.Workspace {
			setLink(&other, nil)
			if err := p.records.SaveHost(context.Background(), other); err != nil {
				return nil, err
			}
		}
	}
	return p.changeLink(host, func(h *records.MachineRecord) {
		had := LinkOf(h)
		link := &WorkspaceLink{Provider: provider, Workspace: request.Workspace}
		on := true
		switch {
		case request.StartWhenUsed != nil:
			link.StartWhenUsed = request.StartWhenUsed
		case had != nil && had.StartWhenUsed != nil:
			link.StartWhenUsed = had.StartWhenUsed
		default:
			link.StartWhenUsed = &on
		}
		switch {
		case request.StopWhenIdleMinutes != nil:
			if *request.StopWhenIdleMinutes > 0 {
				link.StopWhenIdleMinutes = intOf(request.StopWhenIdleMinutes)
			}
		case had == nil:
			minutes := DefaultIdleMinutes
			link.StopWhenIdleMinutes = &minutes
		default:
			link.StopWhenIdleMinutes = had.StopWhenIdleMinutes
		}
		// The host's schedule and the switch are the host's, whatever it is linked to.
		if had != nil {
			link.StartForSchedules = had.StartForSchedules
			link.Schedule = had.Schedule
		}
		link.KnownHosts = knownHosts
		if knownHosts == "" && had != nil && had.Provider == provider && had.Workspace == request.Workspace {
			link.KnownHosts = had.KnownHosts
		}
		setLink(h, link)
	}, func() {
		// Listed again, in the background: the last list can be from before an install
		// woke the workspace (#965).
		go p.Refresh(provider)
	})
}

// linkHostKey is the ssh host key a link keeps for its host's ssh start (#3379): a host
// that joined with the command was never reached over ssh, so no key is trusted for it, and
// its ssh start would fail. A new key is refused hostKeyToTrust, with its fingerprint, until
// trust is that one. A workspace that gives no ssh destination, or that can't be reached
// now, keeps none: its ssh start checks as before.
func (p *Providers) linkHostKey(host string, link WorkspaceLink, trust string) (string, error) {
	if p.hooks.HostKey == nil {
		return "", nil
	}
	if had := LinkOf(p.records.Host(host)); had != nil && had.KnownHosts != "" &&
		had.Provider == link.Provider && had.Workspace == link.Workspace {
		return "", nil
	}
	destination := p.sshDestination(link, host)
	if destination == "" {
		return "", nil
	}
	knownHosts, needsTrust, err := p.hostKey(host, link, destination, trust)
	if err != nil {
		p.log(fmt.Sprintf("workspace: link %s: no host key kept: %s", host, errorWords(err)))
		return "", nil
	}
	if needsTrust != "" {
		f := fail(CodeHostKeyToTrust, "This machine’s host key is new to the control plane: %s. Trust it only if it is the one the machine shows.", needsTrust)
		f.Data = rawOf(map[string]any{"fingerprint": needsTrust})
		return "", f
	}
	return knownHosts, nil
}

func (p *Providers) changeLink(host string, change func(*records.MachineRecord), after func()) (any, error) {
	record := p.records.Host(host)
	if record == nil {
		return nil, fail(CodeNoSuchHost, "No machine is called %s.", host)
	}
	change(record)
	if err := p.records.SaveHost(context.Background(), *record); err != nil {
		return nil, err
	}
	if after != nil {
		after()
	}
	saved := record
	if h := p.records.Host(host); h != nil {
		saved = h
	}
	if link := LinkOf(saved); link != nil {
		p.log(fmt.Sprintf("workspace: %s linked to %s/%s", saved.Name, link.Provider, link.Workspace))
	} else {
		p.log(fmt.Sprintf("workspace: %s unlinked", saved.Name))
	}
	p.changed()
	return p.methods.HostRow(*saved), nil
}

// MARK: Starts and stops

// Keepable is WorkspaceProviders.keepable: the host's name when a prompt or start for it
// is to be kept (FR-020), "" when not. It is linked with start when used on, is not
// online, and its workspace is stopped or starting.
func (p *Providers) Keepable(host string) string {
	record := p.records.Host(host)
	link := LinkOf(record)
	if link == nil || (link.StartWhenUsed != nil && !*link.StartWhenUsed) {
		return ""
	}
	p.mu.Lock()
	_, known := p.providers[link.Provider]
	a := p.activities[host]
	p.mu.Unlock()
	if !known || p.router.Online(host) || (a != nil && a.kind == stopping) {
		return ""
	}
	if shown := p.overlay(*record); shown != nil && (deref(shown.HostState) == "stopped" || deref(shown.HostState) == "starting") {
		return record.Name
	}
	return ""
}

// StartForUse is WorkspaceProviders.startForUse: starts a host's workspace for a kept
// request, or joins the start under way.
func (p *Providers) StartForUse(host, by string) {
	if _, err := p.begin(starting, host, by); err != nil {
		p.log("workspace: " + host + " could not be started for " + by + ": " + errorWords(err))
	}
}

// StopFor stops (never deletes) each started workspace of a person's machines, so they
// cost nothing while the person is suspended (#1002, D13, FR-018). Their disks, agents and
// branches stay; they are started again by hand, once the suspension ends.
func (p *Providers) StopFor(person, who string) {
	if p.hooks.OwnerOf == nil {
		return
	}
	for _, h := range p.records.Hosts() {
		if LinkOf(&h) == nil || p.hooks.OwnerOf(h.ID) != person {
			continue
		}
		if shown := p.overlay(h); shown != nil && deref(shown.HostState) == "stopped" && !p.router.Online(h.ID) {
			continue
		}
		if _, err := p.begin(stopping, h.ID, who); err != nil {
			p.log(fmt.Sprintf("workspace: stop %s for %s: refused: %s", h.Name, who, errorWords(err)))
		}
	}
}

// suspendedOwner says the host's person is suspended, so it is not started.
func (p *Providers) suspendedOwner(host string) bool {
	return p.hooks.Suspended != nil && p.hooks.OwnerOf != nil && p.hooks.Suspended(p.hooks.OwnerOf(host))
}

func (p *Providers) begin(kind activityKind, host, who string) (any, error) {
	record := p.records.Host(host)
	if record == nil {
		return nil, fail(CodeNoSuchHost, "No machine is called %s.", host)
	}
	link := LinkOf(record)
	p.mu.Lock()
	known := false
	if link != nil {
		_, known = p.providers[link.Provider]
	}
	if link == nil || !known {
		p.mu.Unlock()
		return nil, fail(CodeNotLinked, "%s isn’t linked to a workspace.", record.Name)
	}
	if kind == starting && p.suspendedOwner(host) {
		p.mu.Unlock()
		return nil, fail(CodeProviderFailed, "%s is not started: its person's organisation turned their access off.", record.Name)
	}
	// One start or stop at a time per workspace (US1 AS4): a second press of the same joins
	// the first; the other is refused, never dropped without a word.
	if under := p.activities[host]; under != nil && under.kind != kind {
		p.mu.Unlock()
		if under.kind == starting {
			return nil, fail(CodeProviderFailed, "%s is starting. Stop it once it has started.", record.Name)
		}
		if under.kind == deleting {
			return nil, fail(CodeProviderFailed, "%s is being backed up, to be deleted.", record.Name)
		}
		return nil, fail(CodeProviderFailed, "%s is stopping. Start it once it has stopped.", record.Name)
	}
	fresh := p.activities[host] == nil
	if fresh {
		phase := "stopping"
		if kind == starting {
			phase = "provider"
		}
		p.activities[host] = &activity{kind: kind, phase: phase, since: time.Now()}
		delete(p.problems, host)
		delete(p.stoppedBy, host)
		if t := p.idleTimers[host]; t != nil {
			t.Stop()
			delete(p.idleTimers, host)
		}
	}
	p.mu.Unlock()
	if fresh {
		p.changed()
	}
	if h := p.records.Host(host); h != nil {
		record = h
	}
	// The row is read before the run begins: a fast provider can finish it first, and the
	// answer to a stop said online (#1443).
	row := p.methods.HostRow(*record)
	if fresh {
		go p.run(kind, host, *link, who)
	}
	return row, nil
}

func (p *Providers) run(kind activityKind, host string, link WorkspaceLink, who string) {
	began := time.Now()
	name := host
	if h := p.records.Host(host); h != nil {
		name = h.Name
	}
	verb := "stop"
	if kind == starting {
		verb = "start"
	}
	what := fmt.Sprintf("%s (%s/%s)", name, link.Provider, link.Workspace)
	connection, err := p.connection(link.Provider)
	if err == nil {
		if kind == starting {
			_, err = connection.Start(link.Workspace)
		} else {
			_, err = connection.Stop(link.Workspace)
		}
	}
	if err != nil {
		reason := errorWords(err)
		p.log(fmt.Sprintf("workspace: %s %s by %s: failed: %s", verb, what, who, reason))
		if kind == starting {
			p.finish(host, "Couldn’t start "+name+": "+reason)
		} else {
			p.finish(host, "Couldn’t stop "+name+": "+reason)
		}
		return
	}
	p.Refresh(link.Provider)
	if kind == stopping {
		p.log(fmt.Sprintf("workspace: stop %s by %s: ok in %s", what, who, secondsSince(began)))
		p.stopped(host)
		return
	}
	p.waitToConnect(host, link, name, what, who, began)
}

// stopped ends a stop that the provider did. It is said on its row and strip until it is
// started again (US4 AS1).
func (p *Providers) stopped(host string) {
	if p.hooks.Stopped != nil {
		p.hooks.Stopped(host)
	}
	p.mu.Lock()
	if minutes, ok := p.idleStopping[host]; ok {
		delete(p.idleStopping, host)
		p.stoppedBy[host] = "Stopped by Agent Loops after " + minuteWords(minutes) + " idle"
	}
	delete(p.statuses, host)
	p.mu.Unlock()
	p.finish(host, "")
}

// waitToConnect ends a start that the provider did: now its agentsd has to connect (US1
// AS2), and, when it does not, it is started over ssh once.
func (p *Providers) waitToConnect(host string, link WorkspaceLink, name, what, who string, began time.Time) {
	p.setPhase(host, "connecting")
	p.hooks.OpenTunnel(host)
	sshTried := false
	for time.Since(began) < p.timing.GiveUp {
		if p.router.Online(host) {
			p.log(fmt.Sprintf("workspace: start %s by %s: ok in %s", what, who, secondsSince(began)))
			// How long it takes, so a wake-up call starts it early enough (#1004 FR-004).
			took := int(time.Since(began).Seconds())
			p.changeLinkQuietly(host, func(l *WorkspaceLink) { l.MeasuredStartSeconds = &took })
			// Done as it connects, so a Stop pressed the moment it shows online is heard.
			p.finish(host, "")
			p.Refresh(link.Provider)
			return
		}
		if !sshTried && time.Since(began) >= p.timing.SSHAfter {
			var ok bool
			if sshTried, ok = p.startOverSSH(host, link, name, what, who); !ok {
				return
			}
		}
		if !p.sleep(p.timing.LookEvery) {
			return
		}
	}
	p.didntConnect(host, link, name, what, who)
}

// startOverSSH starts the agentsd of a started workspace that did not connect over ssh,
// when the provider gives a destination. tried says whether it did; ok is false when the
// start failed, and then the start has ended with why (#960).
func (p *Providers) startOverSSH(host string, link WorkspaceLink, name, what, who string) (tried, ok bool) {
	record := p.records.Host(host)
	if record == nil {
		return false, true
	}
	destination := p.sshDestination(link, host)
	if destination == "" {
		return false, true
	}
	p.setPhase(host, "sshStart")
	w := p.workspace(link)
	renew := w != nil && w.HostKeyRenews != nil && *w.HostKeyRenews
	if err := p.sshStart(*record, link, destination, renew); err != nil {
		// Nothing else will bring it online, so the start ends now, with why (#960).
		reason := errorWords(err)
		p.log(fmt.Sprintf("workspace: ssh start %s: failed: %s", name, reason))
		p.log(fmt.Sprintf("workspace: start %s by %s: failed: ssh start failed", what, who))
		p.finish(host, name+" started, but its agentsd didn’t connect, and starting it over ssh failed. "+reason)
		return true, false
	}
	p.log(fmt.Sprintf("workspace: ssh start %s: ok", name))
	return true, true
}

// didntConnect ends a start whose agentsd did not connect in time.
func (p *Providers) didntConnect(host string, link WorkspaceLink, name, what, who string) {
	minutes := durationWords(p.timing.GiveUp)
	var problem string
	if p.sshDestination(link, host) == "" {
		problem = name + " started, but its agentsd didn’t connect in " + minutes +
			", and the provider gives no ssh destination to start it with. Make agentsd start on boot (loginctl enable-linger)."
	} else {
		problem = name + " didn’t connect in " + minutes + "."
	}
	p.log(fmt.Sprintf("workspace: start %s by %s: failed: didn't connect in %s", what, who, minutes))
	p.finish(host, problem)
}

// MARK: Making and deleting (US5)

var hostNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,62}$`)

// create is WorkspaceProviders.create (FR-040, FR-041): a workspace made with a one-time
// install command and host code for its first boot, so it joins by itself and is linked.
// A provider that cannot run it says so, and the person adds it over ssh.
//
// With no provider, name or options, it takes the person's default provider, the next
// machine-N and the provider's defaults (#3972). A make that fails stays as a failed row.
func (p *Providers) create(request ProviderCreate, who string, caller *Caller) (any, error) {
	person := ""
	if caller != nil {
		person = caller.Person
	}
	id := string(deref(request.ID))
	if id == "" {
		if id = p.defaultProvider(person); id == "" {
			return nil, invalid("There is no machine provider you may make machines with. Add one in Settings ▸ Machine Providers.")
		}
	}
	p.mu.Lock()
	held, ok := p.providers[id]
	p.mu.Unlock()
	if !ok {
		return nil, invalid("There is no such provider.")
	}
	provider := held.record
	name, err := p.newMachineName(deref(request.Name), person, id)
	if err != nil {
		return nil, err
	}
	// The install line holds a host code: passed on, never logged.
	// The workspace's machine is the starter's (FR-028).
	command, madeHost, err := p.hooks.HostCommand(person)
	if err != nil {
		var f *Failure
		if errors.As(err, &f) {
			return nil, f
		}
		return nil, fail(CodeInternalError, "%s", errorWords(err))
	}
	firstBoot := command + " " + shellQuoted(name)
	began := time.Now()
	// Options left out are what New Machine's form shows (#6686): the person's last choices
	// on this provider (#3979) over the form's defaults.
	form := p.formOf(id)
	options := valueOf(request.Options)
	if options == nil {
		options = form.values(p.lastOptions(person, id))
	}
	// Kept as given, before a repository is put in them.
	used := valueOf(rawOf(options))
	options, clones, err := p.withRepository(id, trimSpaces(deref(request.Repository)), options)
	if err != nil {
		return nil, err
	}
	var made *MachineInfo
	var runs bool
	var said []string
	// Its row, with its set-up, until the provider answers (#6073): a client that opens now
	// sees it being made.
	p.addMaking(id, name, madeHost)
	defer p.dropMaking(id, name)
	// Step 1 of its set-up (#5765), from the press of Make: waiting for an earlier make on the
	// same provider is part of it.
	p.setupStep(madeHost, wire.MachineSetupStepIDCreate, wire.MachineSetupStateRunning,
		"Asking "+provider.Name+" for "+name, "")
	turn := p.makeTurn(id)
	turn.Lock()
	_, err = p.providerCall(id, func(c Provider) (any, error) {
		var err error
		if saying, ok := c.(Saying); ok {
			made, runs, said, err = saying.CreateSaying(name, options, &firstBoot)
		} else {
			made, runs, err = c.Create(name, options, &firstBoot)
		}
		return nil, err
	})
	turn.Unlock()
	if err != nil {
		p.log(fmt.Sprintf("workspace: create %s at %s by %s: failed: %s", name, provider.Name, who, errorWords(err)))
		p.failed(id, name, person, errorWords(err))
		p.setupStep(madeHost, wire.MachineSetupStepIDCreate, wire.MachineSetupStateFailed, "", errorWords(err))
		return nil, err
	}
	// What the provider said it did (#6073), else where and how long.
	madeWords := "Made at " + provider.Name + " in " + secondsSince(began)
	if len(said) > 0 {
		madeWords = strings.Join(said, " ")
	}
	p.setupStep(madeHost, wire.MachineSetupStepIDCreate, wire.MachineSetupStateDone, madeWords, "")
	if runs {
		p.setupStep(madeHost, wire.MachineSetupStepIDJoin, wire.MachineSetupStateRunning,
			"Waiting for "+name+" to start and join", "")
	}
	p.madeOn(person, id, used, form)
	p.usedName(person, name)
	p.Refresh(id)
	found := p.listedMade(id, made, name)
	if found == nil {
		return nil, fail(CodeProviderFailed, "%s made %s, but doesn’t list it yet. Refresh in a minute.", provider.Name, name)
	}
	joiningWords := ""
	if runs {
		joiningWords = ", joining on first boot"
	}
	p.log(fmt.Sprintf("workspace: create %s (%s/%s) by %s: ok in %s%s", name, id, found.ID, who,
		secondsSince(began), joiningWords))
	if runs {
		if madeHost != "" {
			p.hooks.MadeFor(madeHost, id, found.ID)
		}
		for _, c := range request.Clones {
			if c = trimSpaces(c); c != "" {
				clones = append(clones, c)
			}
		}
		p.mu.Lock()
		p.joining[name] = &joining{provider: id, workspace: found.ID, host: madeHost, clones: clones, caller: caller}
		p.mu.Unlock()
		go p.linkWhenJoined(name)
	}
	return ProviderCreated{Workspace: *found, Joining: runs}, nil
}

// newMachineName is the name a new machine is made with: the one given, or else the
// person's next machine-N (#3972). It is refused when it is not a host name, or a host
// has it.
func (p *Providers) newMachineName(given, person, provider string) (string, error) {
	name := strings.TrimSpace(given)
	if name == "" {
		var err error
		if name, err = p.nextName(person, provider); err != nil {
			return "", err
		}
	}
	if !hostNamePattern.MatchString(name) {
		return "", invalid("A workspace’s name is letters, digits and dashes, and starts with a letter.")
	}
	for _, h := range p.records.Hosts() {
		if h.Name == name {
			return "", invalid("There is already a host called %s.", name)
		}
	}
	return name, nil
}

// withRepository puts a repository (#1809) in a make: the provider makes the workspace
// from it when its form asks for one (Codespaces), and otherwise the host clones it first
// once it joins.
func (p *Providers) withRepository(provider, repository string, options any) (any, []string, error) {
	if repository == "" {
		return options, nil, nil
	}
	path, ok := repositoryPath(repository)
	if !ok {
		return nil, nil, invalid("A repository is an https address, such as https://github.com/owner/name.")
	}
	if !p.formAsksForRepository(provider) {
		return options, []string{repository}, nil
	}
	chosen, _ := options.(map[string]any)
	if chosen == nil {
		chosen = map[string]any{}
	}
	if _, given := chosen["repository"]; !given {
		chosen["repository"] = path
	}
	return chosen, nil, nil
}

// listedMade is the workspace a make made, as the provider lists it now: by the id the
// make gave, or else by its name. With an id the provider does not list yet, it is the
// one the make gave; with neither, nil.
func (p *Providers) listedMade(provider string, made *MachineInfo, name string) *MachineInfo {
	p.mu.Lock()
	var listedNow []MachineInfo
	if l := p.listed[provider]; l != nil {
		listedNow = l.workspaces
	}
	p.mu.Unlock()
	for i := range listedNow {
		if made != nil && listedNow[i].ID == made.ID || made == nil && (listedNow[i].Name == name || listedNow[i].ID == name) {
			return &listedNow[i]
		}
	}
	return made
}

// makeTurn is the provider's make lock (#4706): a make waits for the one before it on the
// same provider, so a burst of New Machine presses reaches the provider one at a time, not
// all at once. GitHub's REST API asks for one user's requests one after another, and its
// secondary rate limit refuses many made at the same time.
func (p *Providers) makeTurn(provider string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.making == nil {
		p.making = map[string]*sync.Mutex{}
	}
	turn := p.making[provider]
	if turn == nil {
		turn = &sync.Mutex{}
		p.making[provider] = turn
	}
	return turn
}

// repositoryPath is an https git address's owner/name path, such as alexec/agents for
// https://github.com/alexec/agents.git; false for any other address.
func repositoryPath(address string) (string, bool) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" {
		return "", false
	}
	path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	if !strings.Contains(path, "/") || strings.Contains(path, "//") {
		return "", false
	}
	return path, true
}

// formAsksForRepository is whether the provider's New Machine form has `repository`: it
// makes the workspace from the repository itself, as Codespaces does.
func (p *Providers) formAsksForRepository(id string) bool {
	schema, err := p.providerCall(id, func(c Provider) (any, error) { return c.Options() })
	if err != nil {
		return false
	}
	form, _ := valueOf(rawOf(schema)).(map[string]any)
	properties, _ := form["properties"].(map[string]any)
	_, ok := properties["repository"]
	return ok
}

// LinkJoined links a host that joined with a host code an install made for a workspace
// (#3871): by the code, not by the host's name, which the host can choose for itself, and
// with no client open. A host that is linked to it already keeps its options.
func (p *Providers) LinkJoined(host, provider, workspace, trust string) {
	if link := LinkOf(p.records.Host(host)); link != nil && link.Provider == provider && link.Workspace == workspace {
		return
	}
	request := MachineLink{Host: wire.MachineRef(host), Provider: wire.ProviderID(provider), Workspace: workspace}
	if trust != "" {
		request.Trust = &trust
	}
	_, err := p.link(request)
	var f *Failure
	if errors.As(err, &f) && f.Code == CodeHostKeyToTrust && trust == "" {
		// It joined with the code its workspace was made with, so it is that workspace's
		// host; only its ssh host key is new, and no person is asked here (#6581). It is
		// linked with no key, and the key is checked when ssh is first needed. A key a person
		// trusted at an install, and that differs now, still stops the link.
		p.log(fmt.Sprintf("workspace: %s joined for %s/%s; its host key is new, so the link keeps none", host, provider, workspace))
		_, err = p.linkKeying(request, false)
	}
	if err != nil {
		p.log(fmt.Sprintf("workspace: %s joined for %s/%s, but wasn't linked: %s", host, provider, workspace, errorWords(err)))
	}
}

// linkWhenJoined links a made workspace's host once it has joined, within the start's time,
// and clones its repositories. The join itself links a host whose code names its workspace
// (MadeFor, #6075), so this wait, kept only in memory, is needed for the link only when the
// control plane keeps no set-up; it still does the clones.
func (p *Providers) linkWhenJoined(name string) {
	began := time.Now()
	var last *joining
	started := false
	for time.Since(began) < p.timing.GiveUp {
		p.mu.Lock()
		wanted := p.joining[name]
		p.mu.Unlock()
		if wanted == nil {
			return
		}
		last = wanted
		// Its provider lists it running (#6073): it boots and installs, then joins.
		if !started && p.listedState(wanted.provider, wanted.workspace) == "running" {
			started = true
			p.setupStep(wanted.host, wire.MachineSetupStepIDJoin, wire.MachineSetupStateRunning,
				"Started "+name+"; installing Agent Loops on it, then it joins", "")
		}
		for _, h := range p.records.Hosts() {
			ours := (wanted.host != "" && h.ID == wanted.host) || h.Name == name
			if !ours {
				continue
			}
			// Linked to this workspace already: by its code, as it joined (#6075).
			link := LinkOf(&h)
			if link != nil && (link.Provider != wanted.provider || link.Workspace != wanted.workspace) {
				continue
			}
			p.mu.Lock()
			delete(p.joining, name)
			p.mu.Unlock()
			p.LinkJoined(h.ID, wanted.provider, wanted.workspace, "")
			p.clone(wanted.clones, h.ID, name, wanted.caller)
			return
		}
		if !p.sleep(p.timing.LookEvery) {
			return
		}
	}
	p.mu.Lock()
	_, still := p.joining[name]
	delete(p.joining, name)
	providerName := ""
	if last != nil {
		if held, ok := p.providers[last.provider]; ok {
			providerName = held.record.Name
		}
	}
	p.mu.Unlock()
	if still && last != nil {
		waited := durationWords(p.timing.GiveUp)
		p.log(fmt.Sprintf("workspace: %s was made but didn't join in %s", name, waited))
		// Said to every client (#4610): the row says so, with Delete, not only the log.
		reason := fmt.Sprintf("Made at %s, but it didn’t join in %s. Delete it, or check its install and register it by hand.",
			providerName, waited)
		p.gaveUp(last.provider, last.workspace, name, reason)
		p.setupStep(last.host, wire.MachineSetupStepIDJoin, wire.MachineSetupStateFailed, "", reason)
	}
}

// listedState is a workspace's state as its provider last listed it, or "".
func (p *Providers) listedState(provider, workspace string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.listed[provider]; ok {
		for _, w := range l.workspaces {
			if w.ID == workspace {
				return w.State
			}
		}
	}
	return ""
}

// setupStep says one step of a made machine's set-up (#5765), when it has a host id.
func (p *Providers) setupStep(host string, step wire.MachineSetupStepID, state wire.MachineSetupState, detail, problem string) {
	if host == "" {
		return
	}
	report := wire.MachineSetupReport{Step: step, State: state}
	if detail != "" {
		report.Detail = &detail
	}
	if problem != "" {
		report.Error = &problem
	}
	p.hooks.SetupStep(host, report)
	// A machine being made has no row of its own yet: its provider's row shows the step (#6073).
	if p.records.Host(host) == nil {
		p.changed()
	}
}

// addMaking shows a make its provider has not answered yet (#6073).
func (p *Providers) addMaking(provider, name, host string) {
	p.mu.Lock()
	p.makingNow[provider] = append(p.makingNow[provider], makingRow{name: name, host: host})
	p.mu.Unlock()
}

// dropMaking takes the row away once the provider answered: the listed one, joining, or the
// failed one has it now.
func (p *Providers) dropMaking(provider, name string) {
	p.mu.Lock()
	rows := p.makingNow[provider]
	kept := rows[:0]
	for _, r := range rows {
		if r.name != name {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		delete(p.makingNow, provider)
	} else {
		p.makingNow[provider] = kept
	}
	p.mu.Unlock()
	p.changed()
}

// gaveUp keeps a make the control plane stopped waiting for (#4610), so that its row says
// so until it is deleted or a host is linked to it.
func (p *Providers) gaveUp(provider, workspace, name, reason string) {
	p.mu.Lock()
	kept := append(p.notJoined, notJoinedMake{provider: provider, workspace: workspace, name: name, reason: reason})
	if len(kept) > keptNotJoined {
		kept = kept[len(kept)-keptNotJoined:]
	}
	p.notJoined = kept
	p.mu.Unlock()
	p.changed()
}

// forgetNotJoined drops a give-up for a workspace that is linked or deleted (#4610).
func (p *Providers) forgetNotJoined(provider, workspace string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.notJoined[:0:0]
	for _, n := range p.notJoined {
		if n.provider != provider || n.workspace != workspace {
			kept = append(kept, n)
		}
	}
	p.notJoined = kept
}

// deleteWorkspace is providers/deleteWorkspace (#4610): Delete on a machine New Machine
// made that never joined. The provider deletes the workspace. A workspace a machine is
// linked to is deleted by machines/deleteMachine instead, with the machine's backup.
func (p *Providers) deleteWorkspace(id, workspace, who string) error {
	p.mu.Lock()
	_, ok := p.providers[id]
	p.mu.Unlock()
	if !ok {
		return invalid("There is no such provider.")
	}
	for _, h := range p.records.Hosts() {
		if link := LinkOf(&h); link != nil && link.Provider == id && link.Workspace == workspace {
			return invalid("%s is the machine %s. Delete the machine instead.", workspace, h.Name)
		}
	}
	what := fmt.Sprintf("%s/%s", id, workspace)
	if err := p.deleteAt(id, workspace, what, who); err != nil {
		p.log(fmt.Sprintf("workspace: delete %s by %s: failed: %s", what, who, errorWords(err)))
		return err
	}
	p.mu.Lock()
	for name, j := range p.joining {
		if j.provider == id && j.workspace == workspace {
			delete(p.joining, name)
		}
	}
	p.mu.Unlock()
	p.forgetNotJoined(id, workspace)
	p.log(fmt.Sprintf("workspace: delete %s by %s: ok", what, who))
	p.Refresh(id)
	return nil
}

// clone is WorkspaceProviders.clone (#1122): each folder sent to the new host as File ▸
// Clone Folder sends it, one after another, as the client that made the workspace. The
// URLs are not logged: one may hold a token.
func (p *Providers) clone(urls []string, host, name string, caller *Caller) {
	client, kind := records.NewUUID(), "mac"
	if caller != nil {
		client, kind = caller.Client, caller.Kind
	}
	for i, url := range urls {
		reply := p.router.DeliverKept(MethodFoldersClone, rawOf(map[string]string{"url": url}), client, kind, host, ClonePatience)
		outcome := "not answered"
		switch {
		case reply != nil && reply.Error != nil:
			// Its code only: the host's words may name the URL.
			outcome = fmt.Sprintf("failed (%d)", reply.Error.Code)
		case reply != nil:
			outcome = "ok"
		}
		p.log(fmt.Sprintf("workspace: %s clone %d of %d: %s", name, i+1, len(urls), outcome))
	}
}

// deleteMachine deletes a linked host's workspace, then removes the host as Remove does
// (FR-042).
func (p *Providers) deleteMachine(host, who string, caller Caller) error {
	record := p.records.Host(host)
	if record == nil {
		return fail(CodeNoSuchHost, "No machine is called %s.", host)
	}
	link := LinkOf(record)
	p.mu.Lock()
	known := false
	if link != nil {
		_, known = p.providers[link.Provider]
	}
	busy := p.activities[host] != nil
	p.mu.Unlock()
	if link == nil || !known {
		return fail(CodeNotLinked, "%s isn’t linked to a workspace.", record.Name)
	}
	if busy {
		return fail(CodeProviderFailed, "%s is starting or stopping. Delete it once that is done.", record.Name)
	}
	what := fmt.Sprintf("%s (%s/%s)", record.Name, link.Provider, link.Workspace)
	// A machine is the only copy of its agents, so one that is online backs up first, and
	// is deleted once that is done (#1815). It takes minutes, so the answer is at once and
	// the row says it is going; a backup that fails leaves it, with the reason. One that is
	// offline is deleted now: its newest backup is the one in the store.
	if p.hooks.BackUp != nil && p.router.Online(host) {
		p.mu.Lock()
		if p.activities[host] != nil {
			p.mu.Unlock()
			return fail(CodeProviderFailed, "%s is starting or stopping. Delete it once that is done.", record.Name)
		}
		p.activities[host] = &activity{kind: deleting, phase: "backing up", since: time.Now()}
		delete(p.problems, host)
		p.mu.Unlock()
		p.changed()
		go func() {
			made, err := p.hooks.BackUp(host, caller)
			if err != nil {
				p.log(fmt.Sprintf("workspace: delete %s by %s: the backup failed: %s", what, who, errorWords(err)))
				p.finish(host, "Couldn’t back up "+record.Name+", so it wasn’t deleted: "+errorWords(err))
				return
			}
			if !made {
				p.log(fmt.Sprintf("workspace: delete %s by %s: no backup, the host can't make one", what, who))
			}
			if err := p.removeWorkspace(host, *link, what, who, caller); err != nil {
				p.finish(host, "Couldn’t delete "+record.Name+": "+errorWords(err))
				return
			}
			p.finish(host, "")
		}()
		return nil
	}
	if p.hooks.BackUp != nil {
		p.log(fmt.Sprintf("workspace: delete %s by %s: offline, so no backup first", what, who))
	}
	return p.removeWorkspace(host, *link, what, who, caller)
}

// DeleteForRemoval deletes a removed person's started machine through its provider
// (FR-029). There is no backup first: the admin was shown its unpushed work before they
// confirmed. The host's record is the caller's to remove, at once; this may take a while,
// so the caller runs it in the background.
func (p *Providers) DeleteForRemoval(link WorkspaceLink, what, who string) error {
	if err := p.deleteAt(link.Provider, link.Workspace, what, who); err != nil {
		p.log(fmt.Sprintf("workspace: delete %s (%s/%s), as %s was removed: failed: %s", what, link.Provider, link.Workspace, who, errorWords(err)))
		return err
	}
	p.log(fmt.Sprintf("workspace: delete %s (%s/%s), as %s was removed: ok", what, link.Provider, link.Workspace, who))
	p.Refresh(link.Provider)
	return nil
}

// removeWorkspace is the delete itself: the provider's, then the host's record.
func (p *Providers) removeWorkspace(host string, link WorkspaceLink, what, who string, caller Caller) error {
	if err := p.deleteAt(link.Provider, link.Workspace, what, who); err != nil {
		p.log(fmt.Sprintf("workspace: delete %s by %s: failed: %s", what, who, errorWords(err)))
		return err
	}
	p.log(fmt.Sprintf("workspace: delete %s by %s: ok", what, who))
	if err := p.methods.RemoveHost(host, caller); err != nil {
		return err
	}
	p.Refresh(link.Provider)
	return nil
}

// deleteAt has the provider delete a workspace (#6079). A delete that fails while the
// provider no longer lists the workspace is done: a slow provider's first delete took it,
// and its row stayed. Once done, the workspace leaves the provider's last list at once,
// and later lists until one no longer has it.
func (p *Providers) deleteAt(provider, workspace, what, who string) error {
	_, err := p.providerCall(provider, func(c Provider) (any, error) { return nil, c.Delete(workspace) })
	if err != nil {
		if !p.unlisted(provider, workspace) {
			return err
		}
		p.log(fmt.Sprintf("workspace: delete %s by %s: it was gone already (%s)", what, who, errorWords(err)))
	}
	p.mu.Lock()
	p.deleted[deletedWorkspace{provider, workspace}] = time.Now().Add(deletedKeep)
	if l := p.listed[provider]; l != nil {
		l.workspaces = without(l.workspaces, workspace)
	}
	p.mu.Unlock()
	p.changed()
	return nil
}

// unlisted is whether a new list of the provider says the workspace is not there; false
// when the list fails.
func (p *Providers) unlisted(provider, workspace string) bool {
	connection, err := p.connection(provider)
	if err != nil {
		return false
	}
	workspaces, err := connection.List()
	if err != nil {
		return false
	}
	return len(without(workspaces, workspace)) == len(workspaces)
}

// leaveOutDeletedLocked is a list without the workspaces deleted here that it still has
// (#6079). A deleted workspace the list no longer has, or one deleted more than
// deletedKeep ago, is forgotten. p.mu is held.
func (p *Providers) leaveOutDeletedLocked(provider string, workspaces []MachineInfo, now time.Time) []MachineInfo {
	for d, until := range p.deleted {
		if d.provider != provider {
			continue
		}
		kept := without(workspaces, d.workspace)
		if len(kept) == len(workspaces) || now.After(until) {
			delete(p.deleted, d)
			continue
		}
		workspaces = kept
	}
	return workspaces
}

// without is a new list of the workspaces but the one with this id.
func without(workspaces []MachineInfo, id string) []MachineInfo {
	kept := make([]MachineInfo, 0, len(workspaces))
	for _, w := range workspaces {
		if w.ID != id {
			kept = append(kept, w)
		}
	}
	return kept
}

// providerCall is a provider call, its failure in the provider's words.
func (p *Providers) providerCall(id string, work func(Provider) (any, error)) (any, error) {
	connection, err := p.connection(id)
	var out any
	if err == nil {
		out, err = work(connection)
	}
	var failure *ProviderFailure
	if errors.As(err, &failure) {
		return nil, fail(CodeProviderFailed, "%s", failure.Words)
	}
	return out, err
}

// shellQuoted is WorkspaceProviders.shellQuoted.
func shellQuoted(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

// MARK: Stop when idle (US4)

// Heard is WorkspaceHandling.heard: a host said whether it is idle (machine/status).
func (p *Providers) Heard(host string, status MachineStatus) {
	p.mu.Lock()
	was, hadStatus := p.statuses[host]
	p.statuses[host] = status
	p.mu.Unlock()
	// Kept on its link, for while it is stopped (#1004 FR-003). A host from before #1004
	// says nothing, and is left as it was (FR-011).
	if schedule := schedule(status.Schedule); schedule != nil {
		if link := LinkOf(p.records.Host(host)); link != nil && !link.Schedule.equal(schedule) {
			p.changeLinkQuietly(host, func(l *WorkspaceLink) { l.Schedule = schedule })
			p.changed()
		}
	}
	// Busy again cancels the count; still idle leaves it running (US4 AS2).
	p.mu.Lock()
	timer := p.idleTimers[host]
	if !status.Idle && timer != nil {
		timer.Stop()
		delete(p.idleTimers, host)
	}
	p.mu.Unlock()
	if status.Idle && (!hadStatus || !was.Idle || timer == nil) {
		p.countIdle(host)
	}
	// Kept awake as soon as it works, and let sleep as soon as it is idle (#4637).
	if !hadStatus || was.Idle != status.Idle {
		p.keepAwake(time.Now())
	}
}

// countIdle starts counting down a linked host's stop when idle, from now, if it is idle,
// online and has one; otherwise it stops counting.
func (p *Providers) countIdle(host string) {
	p.mu.Lock()
	if t := p.idleTimers[host]; t != nil {
		t.Stop()
		delete(p.idleTimers, host)
	}
	idle := p.statuses[host].Idle
	busy := p.activities[host] != nil
	p.mu.Unlock()
	link := LinkOf(p.records.Host(host))
	if !idle || link == nil || link.StopWhenIdleMinutes == nil || *link.StopWhenIdleMinutes <= 0 || busy ||
		!p.router.Online(host) {
		return
	}
	minutes := *link.StopWhenIdleMinutes
	// The timer is made and kept under the lock, and its function reads it under the lock,
	// so a short idle time cannot fire before `timer` is set.
	var timer *time.Timer
	p.mu.Lock()
	timer = time.AfterFunc(p.timing.IdleMinute*time.Duration(minutes), func() {
		p.mu.Lock()
		t := timer
		p.mu.Unlock()
		p.idleFor(host, minutes, t)
	})
	if old := p.idleTimers[host]; old != nil {
		old.Stop()
	}
	p.idleTimers[host] = timer
	p.mu.Unlock()
}

func (p *Providers) idleFor(host string, minutes int, timer *time.Timer) {
	p.mu.Lock()
	if p.idleTimers[host] != timer {
		p.mu.Unlock()
		return
	}
	delete(p.idleTimers, host)
	idle := p.statuses[host].Idle
	busy := p.activities[host] != nil
	p.mu.Unlock()
	// Still idle, still online, and still set to this (US4 AS1).
	link := LinkOf(p.records.Host(host))
	if !idle || busy || !p.router.Online(host) || link == nil || link.StopWhenIdleMinutes == nil ||
		*link.StopWhenIdleMinutes != minutes {
		return
	}
	// Its next scheduled workflow is due within its idle time, or it was started for it: it
	// stays up for the run, and counts again (#1004 FR-005).
	if keptUp(*link, p.timing.IdleMinute*time.Duration(minutes), time.Now()) && link.Schedule != nil && link.Schedule.At != nil {
		workflow := link.Schedule.Workflow
		if workflow == "" {
			workflow = "a workflow"
		}
		p.log(fmt.Sprintf("workspace: %s idle %d min, kept up for %s at %s", host, minutes, workflow,
			link.Schedule.At.UTC().Format(time.RFC3339)))
		p.countIdle(host)
		return
	}
	p.mu.Lock()
	p.idleStopping[host] = minutes
	p.mu.Unlock()
	if _, err := p.begin(stopping, host, fmt.Sprintf("idle (%d min)", minutes)); err != nil {
		p.mu.Lock()
		delete(p.idleStopping, host)
		p.mu.Unlock()
		p.log(fmt.Sprintf("workspace: stop %s by idle (%d min): refused: %s", host, minutes, errorWords(err)))
	}
}

// MARK: Wake-up calls (#1004)

// lead is WorkspaceProviders.lead: how long before a due time a stopped workspace is
// started, its last measured start and 2 minutes, or 5 minutes when none was measured.
func lead(link WorkspaceLink) time.Duration {
	if link.MeasuredStartSeconds != nil {
		return time.Duration(*link.MeasuredStartSeconds+120) * time.Second
	}
	return 300 * time.Second
}

// keptUp is WorkspaceProviders.keptUp: whether its next scheduled workflow keeps it up
// though it is idle (FR-005).
func keptUp(link WorkspaceLink, idleFor time.Duration, now time.Time) bool {
	if link.Schedule == nil || link.Schedule.At == nil || !link.Schedule.At.After(now) {
		return false
	}
	at := *link.Schedule.At
	return at.Sub(now) <= idleFor || sameTime(link.WokeFor, &at)
}

// wakeAt is WorkspaceProviders.wakeAt: when a wake-up call would start it for its next
// scheduled workflow, with the switch on and a due time still to come that it was not
// already started for.
func wakeAt(link WorkspaceLink, now time.Time) *time.Time {
	if (link.StartForSchedules != nil && !*link.StartForSchedules) || link.Schedule == nil || link.Schedule.At == nil ||
		!link.Schedule.At.After(now) || sameTime(link.WokeFor, link.Schedule.At) {
		return nil
	}
	t := link.Schedule.At.Add(-lead(link))
	return &t
}

// wakeUps starts each stopped workspace whose wake-up call is due (FR-004, FR-006).
func (p *Providers) wakeUps(now time.Time) {
	for _, h := range p.records.Hosts() {
		link := LinkOf(&h)
		if link == nil {
			continue
		}
		p.mu.Lock()
		_, known := p.providers[link.Provider]
		busy := p.activities[h.ID] != nil
		p.mu.Unlock()
		wake := wakeAt(*link, now)
		if !known || wake == nil || wake.After(now) || busy {
			continue
		}
		if shown := p.overlay(h); shown == nil || deref(shown.HostState) != "stopped" || p.router.Online(h.ID) {
			continue
		}
		at := *link.Schedule.At
		// Marked before the start, so a restart or a reconnect never starts it twice for it.
		p.changeLinkQuietly(h.ID, func(l *WorkspaceLink) { l.WokeFor = &at })
		workflow := link.Schedule.Workflow
		if workflow == "" {
			workflow = "a workflow"
		}
		p.log(fmt.Sprintf("workspace: wake-up call for %s: %s due at %s", h.Name, workflow, at.UTC().Format(time.RFC3339)))
		if _, err := p.begin(starting, h.ID, "schedule ("+workflow+")"); err != nil {
			p.log("workspace: wake-up call for " + h.Name + " failed: " + errorWords(err))
		}
	}
}

// changeLinkQuietly changes a host's link and saves it, without telling clients.
func (p *Providers) changeLinkQuietly(host string, change func(*WorkspaceLink)) {
	record := p.records.Host(host)
	link := LinkOf(record)
	if link == nil {
		return
	}
	before := LinkOf(record)
	change(link)
	if link.equal(before) {
		return
	}
	setLink(record, link)
	if err := p.records.SaveHost(context.Background(), *record); err != nil {
		p.log(fmt.Sprintf("workspace: couldn't save %s's link: %v", record.Name, err))
	}
}

func (p *Providers) setPhase(host, phase string) {
	p.mu.Lock()
	if a := p.activities[host]; a != nil {
		a.phase = phase
	}
	p.mu.Unlock()
	p.changed()
}

func (p *Providers) finish(host, problem string) {
	p.mu.Lock()
	var kind activityKind
	if a := p.activities[host]; a != nil {
		kind = a.kind
	}
	delete(p.activities, host)
	delete(p.idleStopping, host)
	if problem == "" {
		delete(p.problems, host)
	} else {
		p.problems[host] = problem
	}
	kept := p.kept
	p.mu.Unlock()
	p.changed()
	if kind == starting && problem != "" && kept != nil {
		kept.StartFailed(host, problem)
	}
	// A host says it is idle as it connects, while the start is still under way, and is not
	// counted then: count now, or a host started and left idle is never stopped.
	if kind == starting && problem == "" {
		p.countIdle(host)
	}
}

// Asleep is whether the host's workspace is stopped, or being stopped, so nothing should
// dial it (#864): a stop is under way, or no start is and its provider lists it stopped or
// stopping. A host with no link is never asleep.
func (p *Providers) Asleep(host string) bool {
	record := p.records.Host(host)
	if record == nil {
		return false
	}
	link := LinkOf(record)
	if link == nil {
		return false
	}
	p.mu.Lock()
	a := p.activities[host]
	found := p.workspaceLocked(*link)
	p.mu.Unlock()
	if a != nil {
		// One being backed up to be deleted is awake: its backup goes over its link.
		return a.kind == stopping
	}
	return found != nil && (found.State == "stopped" || found.State == "stopping")
}

// Listed is the ssh destination the host's workspace is listed with now, as one word ssh
// takes, or "" when it has no link or its listing gives none. Its tunnel dials it (#1691):
// a VM can have a new address at each start.
func (p *Providers) Listed(host string) string {
	record := p.records.Host(host)
	if record == nil {
		return ""
	}
	link := LinkOf(record)
	if link == nil {
		return ""
	}
	if w := p.workspace(*link); w != nil && deref(w.SSH) != "" {
		return SSHDestination(*w.SSH)
	}
	return ""
}

// sshDestination is where agentsd is started over ssh: the provider's destination, or the
// one the host was installed over (#965).
func (p *Providers) sshDestination(link WorkspaceLink, host string) string {
	if w := p.workspace(link); w != nil && deref(w.SSH) != "" {
		return *w.SSH
	}
	return p.hooks.InstalledOver(host)
}

func (p *Providers) workspace(link WorkspaceLink) *MachineInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.workspaceLocked(link)
}

func (p *Providers) workspaceLocked(link WorkspaceLink) *MachineInfo {
	l := p.listed[link.Provider]
	if l == nil {
		return nil
	}
	for i := range l.workspaces {
		if l.workspaces[i].ID == link.Workspace {
			w := l.workspaces[i]
			return &w
		}
	}
	return nil
}

// MARK: What clients are told

// changed lays each linked host's workspace over its state, and tells clients the providers.
func (p *Providers) changed() {
	p.publishing.Lock()
	defer p.publishing.Unlock()
	for _, h := range p.records.Hosts() {
		p.router.SetWorkspace(h.ID, p.overlay(h))
	}
	p.tellProviders()
}

// Workspace is a host's workspace as it is laid over its state now, or nil.
func (p *Providers) Workspace(host string) *MachineWorkspace {
	if h := p.records.Host(host); h != nil {
		return p.overlay(*h)
	}
	return nil
}

// overlay is WorkspaceProviders.overlay.
func (p *Providers) overlay(record records.MachineRecord) *MachineWorkspace {
	link := LinkOf(&record)
	if link == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	provider, ok := p.providers[link.Provider]
	if !ok {
		return nil
	}
	l := p.listed[link.Provider]
	found := p.workspaceLocked(*link)
	state := "unknown"
	if found != nil {
		state = found.State
	}
	a := p.activities[record.ID]
	now := time.Now()
	var hostState string
	switch {
	case a != nil && a.kind == starting:
		hostState = "starting"
	case a != nil:
		hostState = "stopping"
	case state == "stopped" || state == "starting" || state == "stopping":
		hostState = state
	}
	problem := p.problems[record.ID]
	ws := &MachineWorkspace{Provider: wire.ProviderID(link.Provider), ProviderName: provider.record.Name, Workspace: link.Workspace,
		WorkspaceName: link.Workspace, WorkspaceState: state, HostState: optional(hostState), Problem: optional(problem),
		// A running one whose start failed can be started again: its agentsd is what is down (#960).
		CanStart:      a == nil && (state != "running" && state != "starting" || state == "running" && problem != ""),
		CanStop:       a == nil && (state == "running" || state == "starting" || state == "unknown"),
		StartWhenUsed: link.StartWhenUsed == nil || *link.StartWhenUsed, StopWhenIdleMinutes: int64Of(link.StopWhenIdleMinutes),
		Current: l != nil && l.current}
	if found != nil {
		ws.WorkspaceName = found.Name
		ws.Cost = found.Cost
		ws.IdleTimeoutMinutes = found.IdleTimeoutMinutes
	}
	if a != nil {
		ws.Since = dateOf(a.since)
		// A start's steps, and a delete's backup (#1815).
		if a.kind == starting || a.kind == deleting {
			ws.Phase = optional(a.phase)
		}
	}
	if hostState == "stopped" {
		ws.StoppedBy = optional(p.stoppedBy[record.ID])
	}
	on := link.StartForSchedules == nil || *link.StartForSchedules
	tells := link.Schedule != nil
	ws.StartForSchedules, ws.TellsSchedule = &on, &tells
	if s := link.Schedule; s != nil && s.At != nil && s.At.After(now) {
		ws.NextWorkflow = optional(s.Workflow)
		ws.NextWorkflowAt = dateOf(*s.At)
	}
	// Also while it runs, so Stop's confirmation can say when it starts next (FR-010).
	if wake := wakeAt(*link, now); wake != nil {
		ws.WakeAt = dateOf(*wake)
	}
	return ws
}

// MARK: Helpers

// SSHDestination is WorkspaceProviders.sshDestination(_:): an ssh destination as a
// provider may give it, alex@127.0.0.1 -p 2222, as one word ssh takes,
// ssh://alex@127.0.0.1:2222.
func SSHDestination(given string) string {
	words := strings.Fields(given)
	if len(words) != 3 || words[1] != "-p" {
		return given
	}
	port, err := strconv.Atoi(words[2])
	if err != nil {
		return given
	}
	return fmt.Sprintf("ssh://%s:%d", words[0], port)
}

// SayHostKeyUnknown is WorkspaceProviders.say for HostProblem.hostKeyUnknown: a host key
// to trust is trusted with ssh itself, as there is no install here to show it. Every other
// problem is said as MachineInstall.say says it.
func SayHostKeyUnknown(destination string) string {
	return "Its host key isn’t in your known_hosts. Check and trust it with “ssh " + strings.Join(strings.Fields(destination), " ") +
		"”, then start it again."
}

func secondsSince(t time.Time) string { return fmt.Sprintf("%ds", int(time.Since(t).Seconds())) }

// durationWords is WorkspaceProviders.words(_ duration:).
func durationWords(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds >= 60 {
		return fmt.Sprintf("%d minutes", seconds/60)
	}
	return fmt.Sprintf("%d seconds", seconds)
}

// minuteWords is WorkspaceProviders.words(minutes:).
func minuteWords(minutes int) string {
	switch {
	case minutes == 60:
		return "1 hour"
	case minutes%60 == 0:
		return fmt.Sprintf("%d hours", minutes/60)
	case minutes == 1:
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

// andList is WorkspaceProviders.list: "Claude (fix-login) and Codex (docs)".
func andList(names []string) string {
	if len(names) <= 1 {
		if len(names) == 1 {
			return names[0]
		}
		return ""
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// errorWords is ProviderDrafts.words: a provider's failure, a refusal's message, or the error.
func errorWords(err error) string {
	var failure *ProviderFailure
	if errors.As(err, &failure) {
		return failure.Words
	}
	var refused *Failure
	if errors.As(err, &refused) {
		return refused.Message
	}
	return err.Error()
}

// trimSpaces is trimmingCharacters(in: .whitespaces): spaces and tabs, not new lines.
func trimSpaces(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) })
}
