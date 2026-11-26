package providers

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// MachineSessionStart is DaemonAPI.MachineSessionStart: machines/startSession (#2887).
type MachineSessionStart = wire.MachineSessionStart

// startSessionOn is machines/startSession (#2887): an agent on fromName asked, with
// start_session, for a session on another of its person's machines, which AgentCall has
// found by name and checked is theirs. It goes as that machine's own agents/start, and its
// chat names the agent and machine in the control plane's words, not the asking host's. A
// stopped workspace is kept for and started, as a client's start would be.
func (p *Providers) startSessionOn(fromName, agent string, params json.RawMessage, caller Caller, deliver *wire.LockDelivery) (any, error) {
	var request MachineSessionStart
	if err := requireParams(params, &request, "MachineSessionStart", "host", "start"); err != nil {
		return nil, err
	}
	host, name := request.Host, request.Host
	if h := p.records.Host(host); h != nil {
		name = h.Name
	}
	start := request.Start
	var asker wire.UUID
	if start.StartedFrom != nil {
		asker = start.StartedFrom.Agent
	}
	start.StartedFrom = &wire.StartedFrom{Machine: fromName, Agent: asker, Who: agent}
	p.log(fmt.Sprintf("workspace: agent %s on %s starts a session on %s", agent, fromName, name))
	if deliver != nil && deliver.Method != MethodAgentsStart {
		return nil, invalid("The signed request is not the start.")
	}
	if p.router.Online(host) {
		reply := p.deliverPeer(deliver, MethodAgentsStart, rawOf(start), caller, host, time.Minute)
		if reply == nil {
			return nil, fail(CodeHostOffline, "%s did not answer.", name)
		}
		if reply.Error != nil {
			return nil, reply.Error
		}
		var id wire.UUID
		if json.Unmarshal(reply.Result, &id) != nil || id == "" {
			return nil, fail(CodeHostOffline, "%s did not say which session it started.", name)
		}
		return id, nil
	}
	if kept := p.Kept(); kept != nil {
		if f := kept.Keep(host, MethodAgentsStart, rawOf(start), caller, deliver); f != nil {
			return nil, f
		}
	}
	return nil, fail(CodeHostOffline, "%s is offline.", name)
}

// sessionCall is machines/sessionCall (#2927): an agent on fromName reads, messages, stops
// or archives a session it started on another machine, or asks how it stands. It goes as
// that machine's agents/fromStarter, which answers only the agent that started it; from
// says which, in the control plane's words. With project (#3277), that machine also
// answers for a session of the project, so the asking machine must have a folder of it.
func (p *Providers) sessionCall(from, fromName, agent string, params json.RawMessage, caller Caller, deliver *wire.LockDelivery) (any, error) {
	var request wire.MachineSessionCall
	if err := requireParams(params, &request, "MachineSessionCall", "host", "call"); err != nil {
		return nil, err
	}
	if project := request.Call.Project; project != nil && !p.inProject(from, *project) {
		return nil, fail(CodeNotPermitted, "%s has no folder in that project.", fromName)
	}
	host, name := request.Host, request.Host
	if h := p.records.Host(host); h != nil {
		name = h.Name
	}
	call := request.Call
	call.From = wire.StartedFrom{Machine: fromName, Agent: call.From.Agent, Who: agent}
	if !p.router.Online(host) {
		return nil, fail(CodeHostOffline, "%s is offline.", name)
	}
	if deliver != nil && deliver.Method != MethodAgentsFromStarter {
		return nil, invalid("The signed request is not the call.")
	}
	reply := p.deliverPeer(deliver, MethodAgentsFromStarter, rawOf(call), caller, host, time.Minute)
	if reply == nil {
		return nil, fail(CodeHostOffline, "%s did not answer.", name)
	}
	if reply.Error != nil {
		return nil, reply.Error
	}
	var out wire.SessionCallReply
	if json.Unmarshal(reply.Result, &out) != nil {
		return nil, fail(CodeHostOffline, "%s's answer could not be read.", name)
	}
	return out, nil
}

// sessionEnded is machines/sessionEnded (#2927): a session on fromName that an agent on
// another machine started has ended. That machine is told with agents/endedElsewhere,
// naming fromName as the session's machine; one offline is not, and finds out when its
// agent next asks.
func (p *Providers) sessionEnded(fromName string, params json.RawMessage, caller Caller, deliver *wire.LockDelivery) (any, error) {
	var ended wire.SessionEnded
	if err := requireParams(params, &ended, "SessionEnded", "machine", "session", "starter", "how"); err != nil {
		return nil, err
	}
	host := ""
	for _, h := range p.records.Hosts() {
		if h.ID == ended.Machine || strings.EqualFold(h.Name, ended.Machine) {
			host = h.ID
			break
		}
	}
	// Another user's machine is not there for this one (FR-038).
	if host == "" || p.hooks.OwnerOf(host) != caller.Person {
		return nil, fail(CodeNoSuchHost, "No machine is called %s.", ended.Machine)
	}
	ended.Machine = fromName
	if deliver != nil && deliver.Method != MethodAgentsEndedThere {
		return nil, invalid("The signed request is not the ending.")
	}
	if p.router.Online(host) {
		p.deliverPeer(deliver, MethodAgentsEndedThere, rawOf(ended), caller, host, time.Minute)
	}
	return map[string]any{}, nil
}

// sendFolder is machines/sendFolder: one step of an agent's start_session with send
// (#2929), which sends the branch it is on to another machine first, as the window's Send
// to ‹machine›… does: that machine's folders/receiveState, a part of a file with
// folders/receiveUpload, or folders/receive, which makes the copy. Or one step of its
// bring_back (#2947), the reverse, as Bring Back from ‹machine›… does: that machine's
// folders/sendFacts, folders/sendPack, which makes the bundle, or a part of it with
// folders/sendRead. Or, before a send's bundle, that machine's folders/receiveSeed, which gets
// the commits from the origin (#7119). Or the last step of its move_session (#3717), after the branch and the
// session's export: that machine's agents/moveIn, which has the session go on there. Only an
// online machine takes it; nothing of a send is kept.
func (p *Providers) sendFolder(fromName, agent string, params json.RawMessage, caller Caller, deliver *wire.LockDelivery) (any, error) {
	var request wire.MachineFolderSend
	if err := requireParams(params, &request, "MachineFolderSend", "host"); err != nil {
		return nil, err
	}
	host, name := request.Host, request.Host
	if h := p.records.Host(host); h != nil {
		name = h.Name
	}
	var out wire.MachineFolderSent
	// Each step, and where its answer goes; an upload's answer is nothing.
	method, step, answer, patience := "", any(nil), any(nil), time.Minute
	steps := 0
	if request.State != nil {
		out.State = &wire.ReceiveState{}
		method, step, answer, steps = MethodReceiveState, request.State, out.State, steps+1
	}
	if request.Upload != nil {
		method, step, steps = MethodReceiveUpload, request.Upload, steps+1
	}
	if request.UploadSigned != nil && *request.UploadSigned {
		// The part is only in what the asking machine signed (#7118), so there is no step of
		// the control plane's own to send: it goes as it was signed, or not at all. It is a
		// part of an upload, as upload's is.
		if deliver == nil || deliver.Method != MethodReceiveUpload {
			return nil, invalid("The signed request is not that step.")
		}
		var part wire.ReceiveUploadRequest
		if err := requireParams(json.RawMessage(deliver.Params), &part, "ReceiveUploadRequest", "upload", "offset", "data"); err != nil {
			return nil, err
		}
		method, steps = MethodReceiveUpload, steps+1
	}
	if request.Receive != nil {
		// Fetching the bundle and checking out take a while on a large repository: as long
		// as the asking host waits (controllink's askWait).
		out.Folder = &wire.FolderSummary{}
		method, step, answer, steps, patience = MethodReceive, request.Receive, out.Folder, steps+1, 2*time.Minute
		p.log(fmt.Sprintf("workspace: agent %s on %s sends %s to %s", agent, fromName, request.Receive.Folder, name))
	}
	if request.Facts != nil {
		out.Facts = &wire.SendFacts{}
		method, step, answer, steps = MethodSendFacts, request.Facts, out.Facts, steps+1
	}
	if request.Pack != nil {
		// Making the bundle takes a while on a large repository, as a receive does.
		out.Pack = &wire.SendPack{}
		method, step, answer, steps, patience = MethodSendPack, request.Pack, out.Pack, steps+1, 2*time.Minute
		p.log(fmt.Sprintf("workspace: agent %s on %s brings back %s from %s", agent, fromName, request.Pack.Folder, name))
	}
	if request.Read != nil {
		out.Read = &wire.SendRead{}
		method, step, answer, steps = MethodSendRead, request.Read, out.Read, steps+1
	}
	if request.MoveIn != nil {
		// The machine it leaves is as the control plane knows it, as for agents/start.
		in := *request.MoveIn
		in.From.Machine, in.From.Who = fromName, agent
		method, step, steps = MethodAgentsMoveIn, in, steps+1
		p.log(fmt.Sprintf("workspace: agent %s on %s moves a session to %s, in %s", agent, fromName, name, in.Folder))
	}
	if request.Seed != nil {
		// The machine gets the commits from the folder's origin itself (#7119).
		out.Seed = &wire.ReceiveSeed{}
		method, step, answer, steps = MethodReceiveSeed, request.Seed, out.Seed, steps+1
	}
	if steps != 1 {
		return nil, invalid("Give exactly one of state, upload, uploadSigned, receive, facts, pack, read, moveIn and seed.")
	}
	if !p.router.Online(host) {
		return nil, fail(CodeHostOffline, "%s is offline.", name)
	}
	if deliver != nil && deliver.Method != method {
		return nil, invalid("The signed request is not that step.")
	}
	ended, err := p.partOf(method, host, name, &out)
	if err != nil {
		return nil, err
	}
	defer ended()
	reply := p.deliverPeer(deliver, method, rawOf(step), caller, host, patience)
	if reply == nil {
		return nil, fail(CodeHostOffline, "%s did not answer.", name)
	}
	if reply.Error != nil {
		return nil, reply.Error
	}
	if answer != nil && json.Unmarshal(reply.Result, answer) != nil {
		return nil, fail(CodeHostOffline, "%s's answer could not be read.", name)
	}
	return out, nil
}

// SendPartsAtOnce is the most parts of sends in flight to one machine, and from one machine
// off its lane (#7120): about 22 MB, well under what a link may have waiting (queuedBytes).
const SendPartsAtOnce = 4

// partWait is how long a part waits for a place: with the delivery, under the asking
// machine's own wait (controllink's askWait).
const partWait = 30 * time.Second

// partOf takes a place for a step that carries a part of a file, says in out how many may be
// in flight, and gives what ends it. Another step needs none.
func (p *Providers) partOf(method, host, name string, out *wire.MachineFolderSent) (func(), error) {
	if method != MethodReceiveUpload && method != MethodSendRead {
		return func() {}, nil
	}
	ended, ok := p.partPlace(host)
	if !ok {
		return nil, fail(CodeHostOffline, "%s is busy with other sends.", name)
	}
	// Through another copy its parts would wait in line with the copies' other lines.
	if p.router.HostCan(host, wire.CapFoldersSendParts1) && p.router.Holds(host) {
		out.PartsAtOnce = new(int64(SendPartsAtOnce))
	}
	return ended, nil
}

// partPlace waits for one of host's places for a part, and gives what ends it.
func (p *Providers) partPlace(host string) (func(), bool) {
	p.mu.Lock()
	if p.partPlaces == nil {
		p.partPlaces = map[string]chan struct{}{}
	}
	places := p.partPlaces[host]
	if places == nil {
		places = make(chan struct{}, SendPartsAtOnce)
		p.partPlaces[host] = places
	}
	p.mu.Unlock()
	wait := time.NewTimer(partWait)
	defer wait.Stop()
	select {
	case places <- struct{}{}:
		return func() { <-places }, true
	case <-wait.C:
		return nil, false
	}
}

// IsPart says a machine's call on channel 0 is a part of a send: machines/sendFolder with an
// upload or a read, which another call need not wait for.
func IsPart(method string, params json.RawMessage) bool {
	if method != MethodHostMachines {
		return false
	}
	var call struct {
		Method string `json:"method"`
		Params struct {
			Upload       json.RawMessage `json:"upload"`
			UploadSigned bool            `json:"uploadSigned"`
			Read         json.RawMessage `json:"read"`
		} `json:"params"`
	}
	if json.Unmarshal(params, &call) != nil || call.Method != MethodSendFolder {
		return false
	}
	return call.Params.Upload != nil || call.Params.UploadSigned || call.Params.Read != nil
}

// inProject says host has a folder in project, as it joined it (#3277).
func (p *Providers) inProject(host, project string) bool {
	if p.hooks.ProjectFolders == nil || project == "" {
		return false
	}
	return len(p.hooks.ProjectFolders(project)[host]) > 0
}

// projectFolders is machines/projectFolders (#3277): the project's folders on each of the
// person's machines, so start_session with machine alone starts in the project's folder.
func (p *Providers) projectFolders(params json.RawMessage, caller Caller) (any, error) {
	var request wire.ProjectFoldersRequest
	if err := requireParams(params, &request, "ProjectFoldersRequest", "project"); err != nil {
		return nil, err
	}
	out := wire.ProjectFolders{Machines: []wire.ProjectMachineFolders{}}
	if p.hooks.ProjectFolders == nil {
		return out, nil
	}
	for host, folders := range p.hooks.ProjectFolders(request.Project) {
		// Another user's machine is not there for this agent (FR-038).
		if p.hooks.OwnerOf(host) != caller.Person || len(folders) == 0 {
			continue
		}
		name := host
		if h := p.records.Host(host); h != nil {
			name = h.Name
		}
		out.Machines = append(out.Machines, wire.ProjectMachineFolders{Machine: wire.MachineRef(host), MachineName: name, Folders: folders})
	}
	slices.SortFunc(out.Machines, func(a, b wire.ProjectMachineFolders) int { return strings.Compare(a.MachineName, b.MachineName) })
	return out, nil
}

// previewStart is machines/previewStart (#5876): sessions/previewStart for an agent, the
// pick Auto makes for the project and runtime, with the reason or why none fits.
func (p *Providers) previewStart(params json.RawMessage, caller Caller) (any, error) {
	var request wire.AutoMachineRequest
	if err := requireParams(params, &request, "AutoMachineRequest", "project", "runtimeID"); err != nil {
		return nil, err
	}
	if p.hooks.PreviewStart == nil {
		return nil, fail(CodeNotPermitted, "This control plane cannot pick a machine.")
	}
	return p.hooks.PreviewStart(request, caller)
}

// projectSessions is machines/projectSessions (#3277): projects/sessions for an agent on
// host, which must have a folder of the project.
func (p *Providers) projectSessions(host string, params json.RawMessage, caller Caller) (any, error) {
	var request wire.ProjectSessionsRequest
	if err := requireParams(params, &request, "ProjectSessionsRequest", "project"); err != nil {
		return nil, err
	}
	if p.hooks.ProjectSessions == nil || !p.inProject(host, request.Project) {
		return nil, fail(CodeNotPermitted, "This machine has no folder in that project.")
	}
	return p.hooks.ProjectSessions(request, caller)
}

// deliverPeer delivers a machine's call to another of its person's machines: as the asking
// machine signed it, when it did (#3492, D7), since a machine that holds the lock takes no
// other; else in the control plane's own words, as before.
func (p *Providers) deliverPeer(deliver *wire.LockDelivery, method string, params json.RawMessage, caller Caller, host string, patience time.Duration) *Reply {
	if deliver != nil {
		return p.router.DeliverSigned(*deliver, caller.Client, caller.Kind, host, patience)
	}
	return p.router.DeliverKept(method, params, caller.Client, caller.Kind, host, patience)
}

// botKey is machines/botKey (#3950): one of the person's machines gives the bot's key to
// another of theirs, sealed to that machine's key from the person's lock, or tells it to
// forget the bot. It goes only as the asking machine signed it: this control plane cannot
// open the copy, and has no words of its own to send in its place.
func (p *Providers) botKey(from, fromName string, params json.RawMessage, caller Caller, deliver *wire.LockDelivery) (any, error) {
	var request wire.MachineBotKey
	if err := requireParams(params, &request, "MachineBotKey", "host"); err != nil {
		return nil, err
	}
	method := MethodBotTake
	switch {
	case request.Copy != nil && request.Drop == nil:
	case request.Drop != nil && request.Copy == nil:
		method = MethodBotDrop
	default:
		return nil, invalid("Give exactly one of copy and drop.")
	}
	if deliver == nil || deliver.Method != method {
		return nil, invalid("The bot's key goes only as its machine signed it.")
	}
	host, name := request.Host, request.Host
	if h := p.records.Host(host); h != nil {
		name = h.Name
	}
	if host == from {
		return nil, invalid("That is the asking machine.")
	}
	if !p.router.Online(host) {
		return nil, fail(CodeHostOffline, "%s is offline.", name)
	}
	p.log(fmt.Sprintf("workspace: %s gives the bot's key to %s, sealed", fromName, name))
	reply := p.deliverPeer(deliver, method, nil, caller, host, time.Minute)
	if reply == nil {
		return nil, fail(CodeHostOffline, "%s did not answer.", name)
	}
	if reply.Error != nil {
		return nil, reply.Error
	}
	return map[string]any{}, nil
}
