package agentsd

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Sessions on another machine (#2927). A session an agent here started on another machine
// with start_session (#2887) is in that machine's Store, not this one, so this host keeps a
// record of it in <root>/elsewhere.json: the machine, its id, and the starter's folder,
// whose helper limits count it. list_sessions, read_session, message_session,
// wait_for_event on agent.ended, stop_session and archive_session reach it through the control plane
// (machines/sessionCall), which sends that machine's agents/fromStarter; that machine
// answers only the agent that started it (#1695, #1728), from its own record of who did.
// When it ends, that machine says so (machines/sessionEnded), the control plane tells this
// one (agents/endedElsewhere), and the waits on it close. A host offline then is not told:
// its agent's next status, read or wait asks.
//
// Sessions of the project on another machine (#3277). Any agent may read, message and wait
// on a session of its project on another of the person's machines, found with
// machines/projectSessions; its call carries the project, and that machine answers when the
// session's folder is in it. Stop and archive stay the starter's. A wait on one is kept in
// Watched here and in Watchers there, so its end comes back as a starter's does.

const (
	sessionCallMethod  = "machines/sessionCall"
	sessionEndedMethod = "machines/sessionEnded"
	// elsewhereMessageMost and elsewhereNameMost are the most characters kept of an ending's message
	// and of a title or a name, so that a long one does not refuse its entry.
	elsewhereMessageMost = 4 << 10
	elsewhereNameMost    = 512
	// elsewhereHeardEvery is how often an entry heard of again, with no change, is written (#2977).
	elsewhereHeardEvery = 24 * time.Hour
)

// The lists of elsewhere.json: what an entry's key begins with.
const (
	// Sessions agents here started on other machines. One archived there is dropped: it holds
	// no place.
	elsewhereStarted = "started"
	// Who on another machine started a session here.
	elsewhereFrom = "from"
	// Sessions of the project on other machines that agents here wait on, not started by them
	// (#3277); one is dropped once heard to have ended.
	elsewhereWatched = "watched"
	// Who on other machines waits on a session here, not its starter (#3277); told once when
	// it ends.
	elsewhereWatcher = "watcher"
)

// elsewhereEntry is one entry of elsewhere.json: There in the lists started and watched, Here in
// from and watcher.
type elsewhereEntry struct {
	There *startedThere `json:"there,omitempty"`
	Here  *startedHere  `json:"here,omitempty"`
	// Heard is when the two machines last spoke of the session, which the entry ages from, so
	// one in use stays: each end of a turn and each call by its starter is heard.
	Heard wire.Date `json:"heard"`
}

type startedThere struct {
	Machine string    `json:"machine"`
	Session wire.UUID `json:"session"`
	Starter wire.UUID `json:"starter"`
	// Folder is the starter's, whose limits count it; There is the folder on Machine.
	Folder string    `json:"folder"`
	There  string    `json:"there"`
	Title  string    `json:"title"`
	At     wire.Date `json:"at"`
	// Ended is how it ended, as last heard; nil while it goes on.
	Ended *wire.WaitEndingHow `json:"ended,omitempty"`
}

type startedHere struct {
	Session wire.UUID        `json:"session"`
	From    wire.StartedFrom `json:"from"`
}

func (d *Daemon) elsewherePath() string { return filepath.Join(d.Store.Loc.Root, "elsewhere.json") }

// openElsewhere opens the record, which evicts the ones heard of the longest ago. It is the
// small-file type: a few entries, read and written whole.
func (d *Daemon) openElsewhere() *store.JSONFile[elsewhereEntry] {
	// The lists this file held before #6722, in use when the host is updated. They are read
	// first, because Update sets a file in that shape aside.
	var old struct {
		Started  []startedThere `json:"started"`
		From     []startedHere  `json:"from"`
		Watched  []startedThere `json:"watched"`
		Watchers []startedHere  `json:"watchers"`
	}
	if raw, err := os.ReadFile(d.elsewherePath()); err == nil {
		_ = diskjson.Unmarshal(raw, &old)
	}
	f := smallFile(d, store.Spec{Name: "elsewhere.json", Kind: "key-value", Scope: "person",
		Bounds: store.Bounds{MaxRecordBytes: 64 << 10, MaxCount: 800, MaxAge: store.MaxAge, Action: store.EvictOldest}},
		d.elsewherePath, func(e *elsewhereEntry) *wire.Date { return &e.Heard })
	if len(old.Started)+len(old.From)+len(old.Watched)+len(old.Watchers) == 0 {
		return f
	}
	if err := f.Update(func(m map[string]elsewhereEntry) error {
		for _, s := range old.Started {
			putThere(m, elsewhereStarted, s)
		}
		for _, s := range old.Watched {
			putThere(m, elsewhereWatched, s)
		}
		for _, h := range old.From {
			putHere(m, elsewhereFrom, h)
		}
		for _, h := range old.Watchers {
			putHere(m, elsewhereWatcher, h)
		}
		return nil
	}); err != nil {
		log.Printf("elsewhere: could not carry over the sessions on other machines: %v", err)
	}
	return f
}

func thereKey(list string, session, starter wire.UUID) string {
	return list + "|" + string(session) + "|" + string(starter)
}

func putThere(m map[string]elsewhereEntry, list string, s startedThere) {
	s.Title, s.Ended = cut(s.Title, elsewhereNameMost), keptEnding(s.Ended)
	m[thereKey(list, s.Session, s.Starter)] = elsewhereEntry{There: &s, Heard: now()}
}

func putHere(m map[string]elsewhereEntry, list string, h startedHere) {
	h.From.Who = cut(h.From.Who, elsewhereNameMost)
	key := list + "|" + string(h.Session)
	if list == elsewhereWatcher {
		key += "|" + string(h.From.Agent) + "|" + strings.ToLower(h.From.Machine)
	}
	m[key] = elsewhereEntry{Here: &h, Heard: now()}
}

// keptEnding is an ending with its message cut to what an entry keeps.
func keptEnding(how *wire.WaitEndingHow) *wire.WaitEndingHow {
	if how == nil || how.Finished == nil || how.Finished.Message == nil || len(*how.Finished.Message) <= elsewhereMessageMost {
		return how
	}
	finished := *how.Finished
	finished.Message = new(cut(*finished.Message, elsewhereMessageMost))
	return &wire.WaitEndingHow{Finished: &finished}
}

// readElsewhereFile is the entries of one list, the oldest first.
func (d *Daemon) readElsewhereFile(list string) []elsewhereEntry {
	entries, err := d.elsewhere.Read()
	if err != nil {
		log.Printf("elsewhere: could not read %s: %v", d.elsewherePath(), err)
	}
	var out []elsewhereEntry
	for key, e := range entries {
		// An entry with neither half is not one this host wrote.
		if name, _, _ := strings.Cut(key, "|"); name == list && (e.There != nil || e.Here != nil) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b elsewhereEntry) int {
		if a.There != nil && b.There != nil {
			return cmp.Or(cmp.Compare(a.There.At, b.There.At), cmp.Compare(a.There.Session, b.There.Session))
		}
		return cmp.Compare(a.Heard, b.Heard)
	})
	return out
}

// updateElsewhere changes the entries, then drops those of sessions that are over.
func (d *Daemon) updateElsewhere(change func(map[string]elsewhereEntry)) {
	if err := d.elsewhere.Update(func(m map[string]elsewhereEntry) error {
		change(m)
		for key, e := range m {
			over := e.There != nil && e.There.Ended != nil &&
				(e.There.Ended.Archived != nil || strings.HasPrefix(key, elsewhereWatched+"|"))
			if over || e.Here != nil && d.Store.Agent(e.Here.Session) == nil {
				delete(m, key)
			}
		}
		return nil
	}); err != nil {
		log.Printf("elsewhere: could not keep %s: %v", d.elsewherePath(), err)
	}
}

// heardAgain dates an entry that was heard of again, if it is time to write that.
func heardAgain(m map[string]elsewhereEntry, key string) {
	if e, ok := m[key]; ok && now().Time().Sub(e.Heard.Time()) >= elsewhereHeardEvery {
		e.Heard = now()
		m[key] = e
	}
}

// noteWatched keeps a session of the project on another machine that an agent here waits
// on (#3277), once per waiter.
func (d *Daemon) noteWatched(s startedThere) {
	d.updateElsewhere(func(m map[string]elsewhereEntry) { putThere(m, elsewhereWatched, s) })
}

// noteWatcher keeps who on another machine waits on a session here (#3277), once each.
func (d *Daemon) noteWatcher(id wire.UUID, from wire.StartedFrom) {
	d.updateElsewhere(func(m map[string]elsewhereEntry) {
		putHere(m, elsewhereWatcher, startedHere{Session: id, From: from})
	})
}

// takeWatchers is who on other machines waits on a session here, each told only once.
func (d *Daemon) takeWatchers(id wire.UUID) []wire.StartedFrom {
	var out []wire.StartedFrom
	d.updateElsewhere(func(m map[string]elsewhereEntry) {
		for key, e := range m {
			if e.Here != nil && strings.HasPrefix(key, elsewhereWatcher+"|"+string(id)+"|") {
				out = append(out, e.Here.From)
				delete(m, key)
			}
		}
	})
	return out
}

// noteStartedThere keeps a session an agent here started on another machine.
func (d *Daemon) noteStartedThere(s startedThere) {
	d.updateElsewhere(func(m map[string]elsewhereEntry) { putThere(m, elsewhereStarted, s) })
}

// noteStartedHere keeps who on another machine started a session here.
func (d *Daemon) noteStartedHere(id wire.UUID, from wire.StartedFrom) {
	d.updateElsewhere(func(m map[string]elsewhereEntry) { putHere(m, elsewhereFrom, startedHere{Session: id, From: from}) })
}

// heardFromStarter says the machine that started a session here spoke of it, or was told of it.
func (d *Daemon) heardFromStarter(id wire.UUID) {
	d.updateElsewhere(func(m map[string]elsewhereEntry) { heardAgain(m, elsewhereFrom+"|"+string(id)) })
}

// startedFromOf is who on another machine started a session here, or nil.
func (d *Daemon) startedFromOf(id wire.UUID) *wire.StartedFrom {
	for _, e := range d.readElsewhereFile(elsewhereFrom) {
		if e.Here != nil && e.Here.Session == id {
			return &e.Here.From
		}
	}
	return nil
}

// theres is the sessions on other machines in one list, the oldest first.
func (d *Daemon) theres(list string) []startedThere {
	var out []startedThere
	for _, e := range d.readElsewhereFile(list) {
		if e.There != nil {
			out = append(out, *e.There)
		}
	}
	return out
}

// startedThereBy is the sessions starter started on other machines.
func (d *Daemon) startedThereBy(starter wire.UUID) []startedThere {
	return slices.DeleteFunc(d.theres(elsewhereStarted), func(s startedThere) bool { return s.Starter != starter })
}

// watchedBy is the sessions of the project on other machines that waiter waits on (#3277).
func (d *Daemon) watchedBy(waiter wire.UUID) []startedThere {
	return slices.DeleteFunc(d.theres(elsewhereWatched), func(s startedThere) bool { return s.Starter != waiter })
}

// thereNamed is the session on another machine value names, by id or exact title, of the
// ones starter started or waits on in its project (#3277); nil when none is.
func (d *Daemon) thereNamed(starter wire.UUID, value string) *startedThere {
	value = strings.TrimSpace(value)
	var titled []startedThere
	for _, s := range append(d.startedThereBy(starter), d.watchedBy(starter)...) {
		if strings.EqualFold(string(s.Session), value) {
			return &s
		}
		if s.Title == value {
			titled = append(titled, s)
		}
	}
	if len(titled) == 1 {
		return &titled[0]
	}
	return nil
}

// heardThere is what the session's machine said of it: its title, and how it ended, nil
// while it goes on. An archived one is dropped, and the waits on an ended one close.
// elsewhere.json is written only when what was heard changes a record (#2977), or once a day to
// say it was heard: a status probe of a session that goes on as before costs no write.
func (d *Daemon) heardThere(session wire.UUID, machine, title string, ended *wire.WaitEndingHow) {
	d.updateElsewhere(func(m map[string]elsewhereEntry) {
		for key, e := range m {
			s := e.There
			if s == nil || s.Session != session {
				continue
			}
			if machine != "" {
				s.Machine = machine
			}
			if title != "" {
				s.Title = cut(title, elsewhereNameMost)
			}
			s.Ended = keptEnding(ended)
			heardAgain(m, key)
		}
	})
	if ended != nil {
		d.closeWaits(session, *ended)
	}
}

// thereAgents is the sessions on other machines that count in folder's helper limits, as
// records helpersIn can count: running until heard to have ended, and not while their
// machine was last listed as offline (#5976).
func (d *Daemon) thereAgents(folder string) []wire.Agent {
	var out []wire.Agent
	for _, s := range d.theres(elsewhereStarted) {
		if s.Folder != folder {
			continue
		}
		title, starter := s.Title+" (on "+s.Machine+")", s.Starter
		a := wire.Agent{ID: s.Session, Title: &title, CreatedAt: s.At, StartedByAgent: &starter, State: wire.AgentStateRunning}
		switch {
		case s.Ended != nil:
			a.State = wire.AgentStateFinished
		case d.thereMachineDown(s.Machine):
			a.State = wire.AgentStateStopped
		}
		out = append(out, a)
	}
	return out
}

// statusProbePatience bounds thereStatus's live ask (P20261030-1): list_sessions and
// askThereWaits each ask every session on another machine, so one slow machine must not hold
// up the rest — it falls back to "as last heard" instead, as it already does on any error.
const statusProbePatience = 3 * time.Second

// callThere is one agents/fromStarter on a session caller started on another machine,
// through the control plane, and what it said is kept. patience is 0 to wait as long as the
// control plane's own patience for the op allows, or a shorter bound for a status probe.
func (d *Daemon) callThere(caller wire.UUID, s *startedThere, call wire.SessionCall, patience time.Duration) (wire.SessionCallReply, error) {
	var reply wire.SessionCallReply
	call.From, call.Session = wire.StartedFrom{Agent: caller}, s.Session
	params := wire.MachineSessionCall{Host: s.Machine, Call: call}
	var out json.RawMessage
	var err error
	if patience > 0 {
		out, err = d.askWorkspacesPatient(caller, sessionCallMethod, params, patience)
	} else {
		out, err = d.askWorkspaces(caller, sessionCallMethod, params)
	}
	if failureCode(err) == wire.FailureNoSuchHost {
		if gone, ok := d.machineGone(s, call.Op); ok {
			return gone, nil
		}
	}
	if err != nil {
		return reply, err
	}
	if json.Unmarshal(out, &reply) != nil {
		return reply, &Error{Code: wire.FailureNotSupported, Message: "Nothing was done: the control plane's answer could not be read."}
	}
	d.heardThere(s.Session, "", deref(reply.Title), reply.Ended)
	return reply, nil
}

// thereOp is a call that says only its words: read and stop.
func (d *Daemon) thereOp(caller wire.UUID, s *startedThere, op string) (string, error) {
	reply, err := d.callThere(caller, s, wire.SessionCall{Op: op}, 0)
	if err != nil {
		return "", err
	}
	return reply.Said, nil
}

// thereStatus is how a session on another machine stands, asked now, with a short patience
// of its own (P20261030-1); what was last heard, and why, when its machine can't say quickly.
func (d *Daemon) thereStatus(caller wire.UUID, s startedThere) string {
	reply, err := d.callThere(caller, &s, wire.SessionCall{Op: "status"}, statusProbePatience)
	if err == nil {
		return reply.Said
	}
	why := err.Error()
	if e, ok := err.(*Error); ok {
		why = e.Message
	}
	last := "working"
	if s.Ended != nil {
		e := wire.WaitEnding{How: *s.Ended}
		last = endingSummary(&e)
	}
	return last + ", as last heard (" + strings.TrimSuffix(why, ".") + ")"
}

// askThereWaits asks how each session on another machine that an agent here waits on
// stands, so a wait on one that ended while this host was away closes. Asked at once
// (P20261030-1), not one after another: one slow machine must not hold up every other
// session's check.
func (d *Daemon) askThereWaits() {
	// A host with no store has no waits to ask about (#5806): a job must not panic.
	if d.Store == nil {
		return
	}
	var wg sync.WaitGroup
	for _, a := range d.Store.Agents() {
		if !isOpenBlock(a.Report) || a.Report.Block == nil {
			continue
		}
		for _, w := range a.Report.Block.Waits {
			s := d.thereNamed(a.ID, string(w.AgentID))
			if s == nil || w.Ending != nil || d.Store.Agent(w.AgentID) != nil {
				continue
			}
			caller, there := a.ID, *s
			wg.Add(1)
			d.goJob("status of a waited-on session elsewhere", func() {
				defer wg.Done()
				d.thereStatus(caller, there)
			})
		}
	}
	wg.Wait()
}

// listThere is list_sessions started_by me's lines for the sessions on other machines, each
// asked at once (P20261030-1): one slow machine must not make list_sessions wait for every
// other session in turn.
func (d *Daemon) listThere(caller wire.UUID) []string {
	started := d.startedThereBy(caller)
	lines := make([]string, len(started))
	var wg sync.WaitGroup
	for i, s := range started {
		i, s := i, s
		wg.Add(1)
		d.goJob("status of a session elsewhere", func() {
			defer wg.Done()
			lines[i] = "- " + string(s.Session) + ": " + quotedTitle(&s.Title) + " on " + s.Machine + ", in " + s.There + " — " + d.thereStatus(caller, s)
		})
	}
	wg.Wait()
	return lines
}

// waitThere is the session on another machine a wait on agent.ended names, as a record waitOn
// can wait on, or nil when name is none of caller's.
func (d *Daemon) waitThere(name string, caller wire.UUID) (*wire.Agent, error) {
	s := d.thereNamed(caller, name)
	if s == nil {
		return nil, nil
	}
	reply, err := d.callThere(caller, s, wire.SessionCall{Op: "status"}, 0)
	ended := s.Ended
	if err == nil {
		ended = reply.Ended
	}
	if ended != nil {
		e := wire.WaitEnding{How: *ended}
		return nil, &endedAlready{id: s.Session, title: s.Title, there: " on " + s.Machine, how: endingSummary(&e)}
	}
	title := s.Title
	return &wire.Agent{ID: s.Session, Title: &title}, nil
}

// messageThere is message_session to a session caller started on another machine.
func (d *Daemon) messageThere(me *wire.Agent, s *startedThere, text string, replace bool) (string, error) {
	if !replace && !d.spendMessage(me.ID) {
		return "", refusedMessage(fmt.Sprintf("this session has sent %d messages in the last hour, the most it may.", messagesPerHour))
	}
	reply, err := d.callThere(me.ID, s, wire.SessionCall{Op: "message", Message: &text, Replace: &replace}, 0)
	if err != nil {
		if e, ok := err.(*Error); ok && !strings.HasPrefix(e.Message, "Nothing") {
			return "", refusedMessage(e.Message)
		}
		return "", err
	}
	return reply.Said, nil
}

// spendMessage counts one message_session against the sender's hour, false past it.
func (d *Daemon) spendMessage(caller wire.UUID) bool {
	t := time.Now()
	d.messageMu.Lock()
	defer d.messageMu.Unlock()
	var sent []time.Time
	for _, at := range d.messagesSent[caller] {
		if t.Sub(at) < time.Hour {
			sent = append(sent, at)
		}
	}
	if d.messagesSent == nil {
		d.messagesSent = map[wire.UUID][]time.Time{}
	}
	if len(sent) >= messagesPerHour {
		d.messagesSent[caller] = sent
		return false
	}
	d.messagesSent[caller] = append(sent, t)
	return true
}

// endedElsewhere is agents/endedElsewhere: a session an agent here started on another
// machine has ended.
func (d *Daemon) endedElsewhere(r wire.SessionEnded) error {
	known := false
	for _, s := range append(d.startedThereBy(r.Starter), d.watchedBy(r.Starter)...) {
		known = known || s.Session == r.Session
	}
	if !known {
		return &Error{Code: wire.FailureNotYours, Message: "No agent here started that session."}
	}
	d.heardThere(r.Session, r.Machine, deref(r.Title), &r.How)
	return nil
}

// tellStarter says to the machine whose agent started a session here that it has ended,
// off the call that ended it.
func (d *Daemon) tellStarter(id wire.UUID, how wire.WaitEndingHow) {
	if d.askControl.Load() == nil {
		return
	}
	// And those of the project on other machines that wait on it (#3277).
	tell := d.takeWatchers(id)
	if from := d.startedFromOf(id); from != nil {
		d.heardFromStarter(id)
		tell = append(tell, *from)
	}
	var title *string
	if a := d.Store.Agent(id); a != nil {
		title = a.Title
	}
	for _, from := range tell {
		ended := wire.SessionEnded{Machine: from.Machine, Session: id, Starter: from.Agent, How: how, Title: title}
		d.goJob("tell the starter", func() {
			if _, err := d.askWorkspaces(id, sessionEndedMethod, ended); err != nil {
				log.Printf("agent %s: tell %s it ended: %v", id, from.Machine, err)
			}
		})
	}
}

// fromStarter is agents/fromStarter: the agent on another machine that started a session
// here reads, messages, stops or archives it, or asks how it stands. Only that agent.
func (d *Daemon) fromStarter(r wire.SessionCall) (wire.SessionCallReply, error) {
	var reply wire.SessionCallReply
	from := d.startedFromOf(r.Session)
	t := d.Store.Agent(r.Session)
	starter := from != nil && t != nil && from.Agent == r.From.Agent && strings.EqualFold(from.Machine, r.From.Machine)
	// A session of the asker's project (#3277): status, read and message, not stop or archive.
	member := !starter && t != nil && r.Project != nil && *r.Project != "" && projectOfFolder(folderPath(t)) == *r.Project
	if !starter && !member {
		return reply, &Error{Code: wire.FailureNotYours, Message: "Nothing was done: you did not start that session, or it is not there any more."}
	}
	if starter {
		d.heardFromStarter(t.ID)
	}
	if member && (r.Op == "stop" || r.Op == "archive") {
		return reply, &Error{Code: wire.FailureNotYours, Message: "Nothing changed: you did not start that session. " +
			"You can only stop or archive sessions you started (also with manage_workflows run) and the sessions they started."}
	}
	if member {
		from = &r.From
	}
	name := quotedTitle(t.Title)
	reply.Title = t.Title
	switch r.Op {
	case "status":
		reply.Said = helperStatus(t, nil)
		if member && r.Watch != nil && *r.Watch && d.hasEnded(t) == "" {
			d.noteWatcher(t.ID, r.From)
		}
	case "read":
		entries, _, err := d.Store.Transcript(t.ID, 0)
		if err != nil {
			reply.Said = sessionUnavailable
		} else {
			reply.Said = history(t, entries, historyHeader(t, r.From.Agent))
		}
	case "message":
		said, err := d.messageFromStarter(t, *from, deref(r.Message), r.Replace != nil && *r.Replace, member)
		if err != nil {
			return reply, err
		}
		reply.Said = said
	case "stop":
		if !d.stopHelper(t) {
			reply.Said = name + " had already stopped; nothing changed."
		} else {
			reply.Said = "Stopped " + name + ", freeing its running place; it keeps its other place until it is archived."
		}
	case "archive":
		reply.Said = d.archiveForStarter(t, reasonLine(deref(r.Reason)))
	default:
		return reply, Invalid("Nothing was done: %s is not something a starter can ask.", r.Op)
	}
	if t = d.Store.Agent(r.Session); t != nil && d.hasEnded(t) != "" {
		reply.Ended = waitEndingHow(t)
		if reply.Ended == nil {
			reply.Ended = &wire.WaitEndingHow{Finished: &wire.WaitEndingHowFinished{}}
		}
	}
	return reply, nil
}

// archiveForStarter is archive_session from the starter on another machine, as for a
// helper here: archived now when this folder lets agents and it is not running, else the
// person approves it.
func (d *Daemon) archiveForStarter(t *wire.Agent, why *string) string {
	name := quotedTitle(t.Title)
	may := d.agentsMayArchive(folderPath(t))
	if may && !helperBusy(t) {
		d.sayArchiveWhy(t.ID, why)
		if err := d.archiveBy(t.ID, wire.AgentArchivedReasonByAgent); err != nil {
			return "Could not archive " + name + ": " + err.Error()
		}
		return "Archived " + name + ", freeing its place; the person can unarchive it."
	}
	if r := t.ArchiveRequest; r != nil && (r.Requested != nil || r.WhenTurnEnds != nil) {
		return name + " already waits for the person to approve archiving it; nothing changed."
	}
	working := inFlight(t.State)
	d.requestArchive(t.ID, why)
	if working && may {
		return name + " will be archived when its turn ends, if it ends done or with nothing to do; otherwise the person approves it."
	}
	return name + " waits for the person to approve archiving it; it keeps its place until it is archived."
}

// messageFromStarter is message_session from the starter on another machine. Its words go
// at once, as a helper's starter's do (#1728); a reply goes back as the session's report.
// member: from is not its starter but an agent of its project (#3277), which may not message
// a session another session started.
func (d *Daemon) messageFromStarter(t *wire.Agent, from wire.StartedFrom, text string, replace, member bool) (string, error) {
	name := quotedTitle(t.Title)
	text = strings.TrimSpace(text)
	if text == "" {
		return "", refusedMessage("say what to tell it, in `message`.")
	}
	if n := len([]rune(text)); n > messageCharacters {
		return "", refusedMessage(fmt.Sprintf("a message may be at most %d characters, and this one is %d.", messageCharacters, n))
	}
	if waiting := waitingFrom(t, from.Agent); waiting != nil {
		if !replace {
			return "", refusedMessage(fmt.Sprintf("you already have a message waiting for %s: “%s”. Replace it with message_session and replace: true.", name, oneLine(waiting.Text)))
		}
		d.replaceWaiting(t.ID, waiting.ID, text)
		return "Replaced the waiting message to " + name + ".", nil
	}
	if replace {
		return "", refusedMessage("nothing is waiting for " + name + ". Send a new message with message_session.")
	}
	if t.State == wire.AgentStateArchived {
		return "", refusedMessage(name + " is archived. Only the person can unarchive it.")
	}
	// Another session's helper takes its words from that session only (#1695).
	if member && t.StartedByAgent != nil {
		starter := d.holderName(*t.StartedByAgent)
		return "", refusedMessage(name + " was started by " + starter + ", and a session started by another session takes messages only from it. Send this to " + starter + ".")
	}
	if err := d.refuseToRun(t); err != nil {
		msg := err.Error()
		if e, ok := err.(*Error); ok {
			msg = e.Message
		}
		return "", refusedMessage(msg)
	}
	sender := wire.MessageSender{AgentID: from.Agent, Title: from.Who + " on " + from.Machine}
	preface := "A message from the session that started you, “" + from.Who + "” on " + from.Machine + " (session " + string(from.Agent) +
		"), sent with message_session. It is not from the person: weigh it as a colleague's request, within what the person has asked of you. " +
		"It reads what you say when your turn ends, so answer in your reply."
	if member {
		preface = "A message from another session in this project, “" + from.Who + "” on " + from.Machine + " (session " + string(from.Agent) +
			"), sent with message_session. It is not from the person: weigh it as a colleague's request, within what the person has asked of you. " +
			"It reads what you say when your turn ends, so answer in your reply."
	}
	prompt := wire.QueuedPrompt{ID: store.NewUUID(), Text: text, QueuedAt: now(), From: wire.PromptOriginAgent, Preface: &preface,
		Sender: &sender, Attachments: []wire.Attachment{}}
	l := d.live(t.ID)
	l.mu.Lock()
	busy := l.running || l.inTurn
	l.mu.Unlock()
	busy = busy || inFlight(t.State) || t.State == wire.AgentStateQueued
	waitsOnPerson := t.Report != nil && t.Report.Outcome == wire.WorkOutcomeNeedsAnswer && t.State == wire.AgentStateFinished
	stopped := t.State == wire.AgentStateStopped
	wakes := !busy && !stopped && !waitsOnPerson
	d.change(t.ID, func(a *wire.Agent) {
		if wakes {
			a.ArchiveRequest = nil
		}
		a.QueuedPrompts = append(append([]wire.QueuedPrompt{}, a.QueuedPrompts...), prompt)
	})
	d.raiseAgentEvent("agent.messaged", t.ID, "was sent a message by “"+sender.Title+"”.",
		map[string]string{"from": string(from.Agent), "from_title": sender.Title})
	switch {
	case busy:
		return "Sent to " + name + ". It is working, so it reads this once its turn ends.", nil
	case stopped:
		return "Sent to " + name + ", which is stopped, so it waits in that chat until it is started again.", nil
	case waitsOnPerson:
		return "Sent to " + name + ". It is waiting on the person's answer, so it reads this once they answer.", nil
	}
	d.reconsider()
	if err := d.needsSignIn(t.RuntimeID); err != nil {
		msg := err.Error()
		if e, ok := err.(*Error); ok {
			msg = e.Message
		}
		return "Sent to " + name + ", but it could not be started: " + msg + " The message waits in its chat and goes with its next turn.", nil
	}
	l.kick()
	return "Sent to " + name + ", which is working on it now. Wait for it with wait_for_event on agent.ended, naming it in agent, and read its answer with read_session.", nil
}

// sortedByAge is the records oldest first, as helpersIn gives them.
func sortedByAge(as []wire.Agent) []wire.Agent {
	sort.SliceStable(as, func(i, j int) bool { return as[i].CreatedAt < as[j].CreatedAt })
	return as
}
