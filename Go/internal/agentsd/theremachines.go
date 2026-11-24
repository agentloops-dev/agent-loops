package agentsd

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The machines of the sessions this machine's agents started elsewhere (#5976). A session
// there counts as running in its starter's folder until this machine hears that it ended,
// and a machine that is off or deleted never says so: its sessions held their running
// places for ever, and stop_session and archive_session on them failed. So the count
// reads the person's machines, at most every projectCountFresh: a session on an offline
// machine is not running, and one whose machine is not there any more has ended ("gone").

type thereMachines struct {
	asking sync.Mutex // one list at a time; those behind it take its answer
	mu     sync.Mutex
	at     time.Time
	// states is each machine's state by its name in lower case, as last listed; nil when
	// the control plane did not list them.
	states map[string]string
}

// notRunningStates are the machine states in which no session there runs.
var notRunningStates = map[string]bool{"offline": true, "failed": true}

// openStartedThere is the sessions this machine's agents started elsewhere that have not
// been heard to end.
func (d *Daemon) openStartedThere() []startedThere {
	return slices.DeleteFunc(d.theres(elsewhereStarted), func(s startedThere) bool { return s.Ended != nil })
}

// checkThereMachines lists the person's machines again when the last list is older than
// projectCountFresh and a session started elsewhere is still open. For each open session
// whose machine is not in the list, it asks that machine: when the control plane says there
// is no such machine, the session is marked gone (callThere). Call it before counting, never
// with queueMu held: it can wait up to projectSessionsAsk, and a status ask for each.
func (d *Daemon) checkThereMachines() {
	if d.askControl.Load() == nil {
		return
	}
	open := d.openStartedThere()
	if len(open) == 0 {
		return
	}
	m := &d.thereMachines
	m.asking.Lock()
	defer m.asking.Unlock()
	m.mu.Lock()
	fresh := !m.at.IsZero() && time.Since(m.at) < projectCountFresh
	m.mu.Unlock()
	if fresh {
		return
	}
	var states map[string]string
	// An older control plane lists no hosts: then nothing is known, and nothing changes.
	if listed, err := d.agentWorkspacesWithin("", projectSessionsAsk); err == nil && listed.Hosts != nil {
		states = map[string]string{}
		for _, h := range listed.Hosts {
			states[strings.ToLower(h.Name)] = h.State
		}
	}
	m.mu.Lock()
	m.at, m.states = time.Now(), states
	m.mu.Unlock()
	if states == nil {
		return
	}
	for _, s := range open {
		if _, listed := states[strings.ToLower(s.Machine)]; !listed {
			// The record may name the machine by its id: only the control plane's own "no
			// such machine" marks it gone.
			_, _ = d.callThere(s.Starter, &s, wire.SessionCall{Op: "status"}, statusProbePatience)
		}
	}
}

// thereMachineDown says machine was last listed in a state in which no session runs.
func (d *Daemon) thereMachineDown(machine string) bool {
	m := &d.thereMachines
	m.mu.Lock()
	defer m.mu.Unlock()
	return notRunningStates[m.states[strings.ToLower(machine)]]
}

// machineGone is callThere's answer when the control plane says s's machine is not there
// any more (#5976): the session has ended with its machine, and stop and archive do what
// is left to do here, which is to mark it so.
func (d *Daemon) machineGone(s *startedThere, op string) (wire.SessionCallReply, bool) {
	how := wire.WaitEndingHow{Gone: &struct{}{}}
	gone := s.Machine + " is not there any more"
	var said string
	switch op {
	case "status":
		said = "gone: " + gone
	case "stop":
		said = "Nothing to stop: " + gone + ", so the session ended with it."
	case "archive":
		how = wire.WaitEndingHow{Archived: &struct{}{}}
		said = "Archived: " + gone + ", so the session ended with it, and it no longer counts here."
	default:
		d.heardThere(s.Session, "", "", &how)
		return wire.SessionCallReply{}, false
	}
	d.heardThere(s.Session, "", "", &how)
	return wire.SessionCallReply{Said: said, Ended: &how}, true
}
