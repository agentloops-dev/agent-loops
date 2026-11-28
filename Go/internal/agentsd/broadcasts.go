package agentsd

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Broadcasts (#6954, epic #6951, gate 1 on #6951): a workflow with agent: labelled sends its
// words to the sessions of its folder that match its to:. With deliver: next-turn (the
// default) nothing is queued: each turn's start adds the active broadcasts that the session
// matches and has not read (broadcastTurnText), so no idle session is started. steer and wake
// send at once, to 20 sessions at most; the rest hear it at their next turn. A broadcast ends
// by until:, for: (24 h by default) and the rest of broadcastend.go (#6955), and an ended one
// goes a day later.

const (
	labelledMode         = "labelled"
	broadcastsFile       = "broadcasts.json"
	broadcastsMost       = 50
	broadcastReadsMost   = 200
	broadcastTextMost    = 4000
	broadcastRecordMost  = 32 << 10
	broadcastFor         = 24 * time.Hour
	broadcastKeptEnded   = 24 * time.Hour
	broadcastSendMost    = 20
	broadcastsListed     = 10
	broadcastNotToldMost = 10
)

// broadcastRead is a session that was told, and the revision it was told.
type broadcastRead struct {
	SessionID wire.UUID `json:"sessionID"`
	Revision  int64     `json:"revision"`
	ReadAt    wire.Date `json:"readAt"`
	// EndToldAt is when it was told that the broadcast ended (#6955).
	EndToldAt *wire.Date `json:"endToldAt,omitzero"`
}

// broadcast is one in broadcasts.json: one per workflow, folder and match values.
type broadcast struct {
	ID         wire.UUID            `json:"id"`
	Folder     string               `json:"folder"`
	WorkflowID string               `json:"workflowID"`
	Name       string               `json:"name"`
	MatchKey   string               `json:"matchKey,omitzero"`
	Text       string               `json:"text"`
	EndText    string               `json:"endText,omitzero"`
	Level      *string              `json:"level,omitzero"`
	Revision   int64                `json:"revision"`
	To         wire.WorkflowTo      `json:"to"`
	Deliver    wire.WorkflowDeliver `json:"deliver"`
	StartedAt  wire.Date            `json:"startedAt"`
	StartedBy  string               `json:"startedBy"`
	EndsAt     wire.Date            `json:"endsAt"`
	EndedAt    *wire.Date           `json:"endedAt,omitzero"`
	EndedBy    *string              `json:"endedBy,omitzero"`
	Reads      []broadcastRead      `json:"reads"`
	// StartEvent and Volume are what started it, and HasUntil whether an event ends it, so
	// the first disk look after a restart can end it (#6955).
	StartEvent string `json:"startEvent,omitzero"`
	Volume     string `json:"volume,omitzero"`
	HasUntil   bool   `json:"hasUntil,omitzero"`
	// Unreached is the sessions not told because 200 had read it.
	Unreached int64 `json:"unreached,omitzero"`
}

func (b *broadcast) active(at time.Time) bool { return b.EndedAt == nil && at.Before(b.EndsAt.Time()) }

func (b *broadcast) readBy(id wire.UUID) bool {
	for _, r := range b.Reads {
		if r.SessionID == id && r.Revision >= b.Revision {
			return true
		}
	}
	return false
}

// broadcastBook is the daemon's broadcasts: the file, and a copy in memory for the turn's start.
type broadcastBook struct {
	broadcastMu   sync.Mutex
	broadcastFile *store.JSONFile[broadcast]
	// broadcastCopy is the file as last written or read; nil before the first read.
	broadcastCopy map[string]broadcast
	// diskRechecked is whether the first disk look since the start has ended the broadcasts
	// that ended while the daemon was away (#6955).
	diskRechecked bool
}

func (d *Daemon) openBroadcasts() *store.JSONFile[broadcast] {
	return smallFile(d, store.Spec{Name: broadcastsFile, Kind: "key-value", Scope: "machine",
		Bounds: store.Bounds{MaxRecordBytes: broadcastRecordMost, MaxCount: broadcastsMost,
			MaxAge: broadcastFor*7 + broadcastKeptEnded, Action: store.Refuse}},
		func() string { return filepath.Join(d.Store.Loc.Root, broadcastsFile) },
		func(b *broadcast) *wire.Date { return &b.StartedAt })
}

// broadcastsLocked is the broadcasts, read once. Called with broadcastMu held.
func (d *Daemon) broadcastsLocked() map[string]broadcast {
	if d.broadcastCopy == nil {
		m, err := d.broadcastFile.Read()
		if err != nil || m == nil {
			m = map[string]broadcast{}
		}
		d.broadcastCopy = m
	}
	return d.broadcastCopy
}

// updateBroadcastsLocked changes the file and the copy. Called with broadcastMu held.
func (d *Daemon) updateBroadcastsLocked(change func(map[string]broadcast) error) error {
	var kept map[string]broadcast
	err := d.broadcastFile.Update(func(m map[string]broadcast) error {
		if err := change(m); err != nil {
			return err
		}
		kept = m
		return nil
	})
	if err == nil {
		d.broadcastCopy = make(map[string]broadcast, len(kept))
		for k, v := range kept {
			d.broadcastCopy[k] = v
		}
	} else {
		d.broadcastCopy = nil
	}
	return err
}

// isLabelled says whether a workflow is agent: labelled: it is the one with to:.
func isLabelled(w wire.Workflow) bool { return w.To != nil }

func deliverOf(w wire.Workflow) wire.WorkflowDeliver {
	if w.Deliver == nil {
		return wire.WorkflowDeliverNextTurn
	}
	return *w.Deliver
}

// broadcastMatches is to: against a session (the spike #6952): its folder, every label, none
// of not-labels, the runtime that ran it (also on Auto), and its account (default is none).
func broadcastMatches(to wire.WorkflowTo, folder string, a *wire.Agent) bool {
	if a == nil || a.State == wire.AgentStateArchived || folderPath(a) != folder {
		return false
	}
	has := func(label string) bool {
		for _, l := range a.Labels {
			if strings.EqualFold(strings.TrimSpace(l.Value), label) {
				return true
			}
		}
		return false
	}
	for _, l := range to.Labels {
		if !has(l) {
			return false
		}
	}
	for _, l := range to.NotLabels {
		if has(l) {
			return false
		}
	}
	if to.Runtime != nil && !strings.EqualFold(*to.Runtime, a.RuntimeID) {
		return false
	}
	if to.Account != nil {
		if strings.EqualFold(*to.Account, "default") {
			return a.AccountID == nil || *a.AccountID == ""
		}
		return a.AccountID != nil && *a.AccountID == *to.Account
	}
	return true
}

// toWords is to: as the history says it: "labelled a, b".
func toWords(to wire.WorkflowTo) string {
	words := "labelled " + strings.Join(to.Labels, ", ")
	if len(to.NotLabels) > 0 {
		words += ", not " + strings.Join(to.NotLabels, ", ")
	}
	if to.Runtime != nil {
		words += ", on " + *to.Runtime
	}
	if to.Account != nil {
		words += ", account " + *to.Account
	}
	return words
}

// broadcastText is what the sessions are told: who sent it and why, then the file's words.
func (d *Daemon) broadcastText(w wire.Workflow, fr fireRequest) string {
	why := ""
	var e *wire.Event
	if fr.causingEvent != nil {
		e = d.event(*fr.causingEvent)
	}
	switch {
	case e != nil:
		why = " because of the event " + e.Name + ": " + e.Sentence + eventDetails(*e)
	case fr.byAgent != nil:
		why = " because the agent " + d.agentWords(*fr.byAgent) + " ran it"
	case fr.byHand:
		why = " because the person ran it by hand, with Run now"
	case fr.trigger.Schedule != nil:
		why = " because of its schedule"
	}
	return "(From the app: the workflow \"" + w.Name + "\" sends this to the sessions " + toWords(*w.To) + why +
		". Nobody waits for an answer.)\n\n" + strings.TrimSpace(w.Prompt)
}

// fireBroadcast is a labelled workflow's run, once fire has weighed it: a broadcast, and the
// sessions told at once for steer and wake.
func (d *Daemon) fireBroadcast(fr fireRequest, w wire.Workflow) {
	refuse := func(why string) {
		d.recordOutcome(fr, w.Name, wire.WorkflowOutcome{Refused: &wire.WorkflowOutcomeRefused{
			Value: wire.WorkflowRefusal{Unreadable: &wire.WorkflowRefusalUnreadable{Value: why}}, At: now()}}, nil)
	}
	if !d.labelledWorkflowsOn() {
		refuse(errLabelledWorkflowsOff.Message)
		return
	}
	text := d.broadcastText(w, fr)
	if n := utf8.RuneCountInString(text); n > broadcastTextMost {
		refuse(fmt.Sprintf("its words are %d characters, and a labelled workflow sends at most %d", n, broadcastTextMost))
		return
	}
	b, raised, why := d.startBroadcast(fr, w, text)
	if why != "" {
		refuse(why)
		return
	}
	if !raised {
		d.scheduleBroadcastEnd(b)
		// An until: of a server's event is subscribed to now that it is active.
		d.kickMCPEvents()
	}
	told, later, notTold := d.deliverBroadcast(b)
	d.noteBroadcastRan(fr, w, b, told, later, notTold)
}

// startBroadcast keeps a new broadcast, or says why it cannot. A start of an active one with
// a worse level replaces its words, so its readers read it again: raised.
func (d *Daemon) startBroadcast(fr fireRequest, w wire.Workflow, text string) (b broadcast, raised bool, why string) {
	at := nowFunc()
	by := "person"
	if !fr.byHand {
		by = "event"
	}
	if fr.byAgent != nil {
		by = "agent"
	}
	b = broadcast{ID: store.NewUUID(), Folder: fr.folder, WorkflowID: fr.id, Name: w.Name, Text: text, Revision: 1,
		EndText: broadcastEndText(w), To: *w.To, Deliver: deliverOf(w), StartedAt: wire.DateOf(at), StartedBy: by,
		EndsAt: wire.DateOf(at.Add(broadcastForOf(w))), Reads: []broadcastRead{}, HasUntil: len(w.Until) > 0}
	if fr.event != nil {
		b.MatchKey, b.StartEvent, b.Volume = broadcastMatchKey(w.Match, fr.event), fr.event.Name, fr.event.Details["volume"]
		if l := fr.event.Details["level"]; l != "" {
			b.Level = &l
		}
	}
	d.broadcastMu.Lock()
	defer d.broadcastMu.Unlock()
	err := d.updateBroadcastsLocked(func(m map[string]broadcast) error {
		dropEndedBroadcasts(m, at)
		for k, o := range m {
			if o.Folder != b.Folder || o.WorkflowID != b.WorkflowID || o.MatchKey != b.MatchKey || !o.active(at) {
				continue
			}
			if b.Level != nil && worseLevel(o.Level, *b.Level) {
				o.Text, o.Level, o.Revision = b.Text, b.Level, o.Revision+1
				m[k], b, raised = o, o, true
				return nil
			}
			why = "it is still active, until " + clock(o.EndsAt) + ", so a second start changes nothing"
			return errBroadcastRefused
		}
		if len(m) >= broadcastsMost && !evictEndedBroadcast(m) {
			why = fmt.Sprintf("%d broadcasts are active on this machine, the most there can be", broadcastsMost)
			return errBroadcastRefused
		}
		m[string(b.ID)] = b
		return nil
	})
	if err != nil && why == "" {
		why = "its broadcast could not be kept: " + err.Error()
	}
	return b, raised, why
}

var errBroadcastRefused = &Error{Code: CodeNotSupported, Message: "refused"}

// dropEndedBroadcasts drops each broadcast that ended a day ago. One whose time is up is
// ended by endDueBroadcasts, which says so on the log.
func dropEndedBroadcasts(m map[string]broadcast, at time.Time) {
	for k, b := range m {
		if b.EndedAt != nil && at.Sub(b.EndedAt.Time()) >= broadcastKeptEnded {
			delete(m, k)
		}
	}
}

// evictEndedBroadcast drops the broadcast that ended first, to make room.
func evictEndedBroadcast(m map[string]broadcast) bool {
	key := ""
	for k, b := range m {
		if b.EndedAt != nil && (key == "" || b.EndedAt.Time().Before(m[key].EndedAt.Time())) {
			key = k
		}
	}
	if key == "" {
		return false
	}
	delete(m, key)
	return true
}

// broadcastNotTold is a matching session not told now, and why.
type broadcastNotTold struct {
	id  wire.UUID
	why string
}

// deliverBroadcast tells the matching sessions at once, for steer and wake. It is the
// sessions told, those that hear it at their next turn, and those not told now and why.
func (d *Daemon) deliverBroadcast(b broadcast) (told []wire.UUID, later int, notTold []broadcastNotTold) {
	var matching []wire.Agent
	for _, a := range d.Store.Agents() {
		if broadcastMatches(b.To, b.Folder, &a) {
			matching = append(matching, a)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].ID < matching[j].ID })
	for i := range matching {
		a := &matching[i]
		why := d.broadcastNowRefusal(b, a, len(told))
		if why == "" {
			if why = d.sendBroadcastNow(b, a); why == "" {
				told = append(told, a.ID)
				continue
			}
		}
		later++
		if why != "next-turn" {
			notTold = append(notTold, broadcastNotTold{id: a.ID, why: why})
		}
	}
	return told, later, notTold
}

// broadcastNowRefusal is why a session is not told at once: "next-turn" when it hears it at
// its next turn as asked, else the reason, which it also hears then.
func (d *Daemon) broadcastNowRefusal(b broadcast, a *wire.Agent, sent int) string {
	switch {
	case b.Deliver == wire.WorkflowDeliverNextTurn:
		return "next-turn"
	case a.State == wire.AgentStateStopped:
		return "it is stopped, so it hears it at its next turn"
	case a.State == wire.AgentStateWaitingOnUser:
		return "it waits for an answer, so it hears it at its next turn"
	case a.State == wire.AgentStateQueued || a.State == wire.AgentStateStarting:
		return "it is starting, so it hears it at its first turn"
	case sent >= broadcastSendMost:
		return fmt.Sprintf("%d sessions were told at once already, the most, so it hears it at its next turn", broadcastSendMost)
	}
	return ""
}

// sendBroadcastNow tells one session at once: into its running turn, or, for wake, as a new
// turn of an idle session. It marks the read first, so the turn's start does not tell it
// again, and takes it back when the words did not go. It is "" when told, else why not.
func (d *Daemon) sendBroadcastNow(b broadcast, a *wire.Agent) string {
	marked, was := d.markBroadcastRead(b.ID, a.ID)
	if !marked {
		return "it had read it already"
	}
	why := d.sendBroadcastWords(b, a)
	if why != "" {
		d.unmarkBroadcastRead(b.ID, a.ID, was)
	}
	return why
}

func (d *Daemon) sendBroadcastWords(b broadcast, a *wire.Agent) string {
	l := d.live(a.ID)
	l.mu.Lock()
	s, inTurn := l.session, l.inTurn
	l.mu.Unlock()
	if inTurn && s != nil && s.SupportsSteering() {
		ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
		defer cancel()
		bs := []wire.ContentBlock{textBlock(b.Text)}
		outcome, err := s.Steer(ctx, acpBlocks(bs))
		if err == nil && (outcome == "injected" || outcome == "startedNewTurn") {
			from := wire.PromptOriginApp
			d.record(a.ID, wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: b.Text, From: &from}})
			return ""
		}
		if b.Deliver == wire.WorkflowDeliverSteer {
			return "its runtime did not take the words in its turn, so it hears them at its next turn"
		}
	}
	if inTurn {
		return "its runtime cannot take words in the middle of a turn, so it hears them at its next turn"
	}
	if b.Deliver != wire.WorkflowDeliverWake {
		return "next-turn"
	}
	if err := d.promptFromApp(a.ID, b.Text); err != nil {
		_, msg := errMessage(err)
		return msg
	}
	return ""
}

// markBroadcastRead records that a session was told, unless it was or 200 were. False when
// it is not to be told. was is the revision it had read before a worse level, else 0.
func (d *Daemon) markBroadcastRead(id, session wire.UUID) (marked bool, was int64) {
	d.broadcastMu.Lock()
	defer d.broadcastMu.Unlock()
	_ = d.updateBroadcastsLocked(func(m map[string]broadcast) error {
		b, ok := m[string(id)]
		if !ok || b.readBy(session) {
			return nil
		}
		if i := readIndex(b.Reads, session); i >= 0 {
			was = b.Reads[i].Revision
			b.Reads[i].Revision, b.Reads[i].ReadAt = b.Revision, now()
			m[string(id)], marked = b, true
			return nil
		}
		if len(b.Reads) >= broadcastReadsMost {
			b.Unreached++
			m[string(id)] = b
			return nil
		}
		b.Reads = append(b.Reads, broadcastRead{SessionID: session, Revision: b.Revision, ReadAt: now()})
		m[string(id)], marked = b, true
		return nil
	})
	return marked, was
}

// unmarkBroadcastRead takes a read back: to the revision it had read, or out with none.
func (d *Daemon) unmarkBroadcastRead(id, session wire.UUID, was int64) {
	d.broadcastMu.Lock()
	defer d.broadcastMu.Unlock()
	_ = d.updateBroadcastsLocked(func(m map[string]broadcast) error {
		b, ok := m[string(id)]
		if !ok {
			return nil
		}
		kept := b.Reads[:0]
		for _, r := range b.Reads {
			if r.SessionID == session && was > 0 {
				r.Revision = was
			}
			if r.SessionID != session || was > 0 {
				kept = append(kept, r)
			}
		}
		b.Reads = kept
		m[string(id)] = b
		return nil
	})
}

// noteBroadcastRan keeps the fire as the workflow's last, so its cooldown counts from it, and
// says on the log who got it and who did not and why.
func (d *Daemon) noteBroadcastRan(fr fireRequest, w wire.Workflow, b broadcast, told []wire.UUID, later int, notTold []broadcastNotTold) {
	d.workflowMu.Lock()
	st := d.stateLocked(fr.folder, fr.id, true)
	t := now()
	cause := wire.WorkflowCause{Trigger: &wire.WorkflowCauseTrigger{Value: fr.trigger}}
	if fr.byHand {
		cause = wire.WorkflowCause{ByHand: &struct{}{}}
	}
	st.LastFiredAt, st.LastFiredBy = &t, &cause
	// A broadcast has no agent, so it is no outcome of ran; an earlier refusal is over.
	st.LastOutcome, st.LastCausingEvent, st.DismissedFailure = nil, fr.causingEvent, nil
	d.saveRecordsLocked(false)
	d.workflowMu.Unlock()
	sentence, details := d.broadcastHistory(w, b, told, later, notTold)
	details["workflow"] = fr.id
	d.raise(wire.EventDraft{Name: "workflow.ran", At: now(), Scope: folderScope(fr.folder), Sentence: sentence,
		Details: details, ChainDepth: fr.depth + 1})
	if fr.causingEvent != nil {
		d.addConsequence(wire.Consequence{Fired: &wire.ConsequenceFired{WorkflowID: fr.id, Folder: store.FolderURL(fr.folder)}}, *fr.causingEvent)
	}
	d.tellWorkflow(fr.folder, fr.id)
}

// broadcastHistory is the run's history: the sentence and the details of workflow.ran.
func (d *Daemon) broadcastHistory(w wire.Workflow, b broadcast, told []wire.UUID, later int, notTold []broadcastNotTold) (string, map[string]string) {
	sentence := "Workflow " + w.Name + " sent its words to the sessions " + toWords(b.To) + " ("
	switch b.Deliver {
	case wire.WorkflowDeliverNextTurn:
		sentence += fmt.Sprintf("%d hear them at their next turn", later)
	default:
		sentence += fmt.Sprintf("%d told now, %d at their next turn", len(told), later)
	}
	sentence += ", until " + clock(b.EndsAt) + ")."
	var ids, reasons []string
	for _, id := range told {
		ids = append(ids, string(id))
	}
	for i, n := range notTold {
		if i == broadcastNotToldMost {
			reasons = append(reasons, fmt.Sprintf("and %d more", len(notTold)-i))
			break
		}
		reasons = append(reasons, d.titleOf(n.id)+" ("+string(n.id)+"): "+n.why)
	}
	if len(reasons) > 0 {
		sentence += " Not told now: " + strings.Join(reasons, "; ") + "."
	}
	details := map[string]string{"broadcast": string(b.ID), "deliver": string(b.Deliver), "told": fmt.Sprint(len(told)),
		"next_turn": fmt.Sprint(later), "ends_at": b.EndsAt.Time().UTC().Format(time.RFC3339)}
	if len(ids) > 0 {
		details["told_sessions"] = strings.Join(ids, ",")
	}
	if len(reasons) > 0 {
		details["not_told"] = strings.Join(reasons, "; ")
	}
	return sentence, details
}

// broadcastTurnText is what a turn's start tells the session: each active broadcast that it
// matches and has not read, now marked read. "" when there is none.
func (d *Daemon) broadcastTurnText(id wire.UUID) string {
	a := d.Store.Agent(id)
	if a == nil {
		return ""
	}
	folder := folderPath(a)
	// A workflow turned off or archived in its file ends its broadcasts before they are given.
	d.endBroadcastsOfStoppedWorkflows()
	at := nowFunc()
	d.broadcastMu.Lock()
	defer d.broadcastMu.Unlock()
	due := false
	for _, b := range d.broadcastsLocked() {
		if b.endDue(id, at) || b.active(at) && !b.readBy(id) && broadcastMatches(b.To, folder, a) {
			due = true
			break
		}
	}
	if !due {
		return ""
	}
	var texts []string
	_ = d.updateBroadcastsLocked(func(m map[string]broadcast) error {
		texts = takeBroadcasts(m, a, folder, at)
		return nil
	})
	return strings.Join(texts, "\n\n")
}

// takeBroadcasts marks each due broadcast read by a, and is their words, the oldest first: the
// end words of each that ended after a read it, once, then the words of each active one.
func takeBroadcasts(m map[string]broadcast, a *wire.Agent, folder string, at time.Time) []string {
	var due, ended []broadcast
	for _, b := range m {
		switch {
		case b.endDue(a.ID, at):
			ended = append(ended, b)
		case b.active(at) && !b.readBy(a.ID) && broadcastMatches(b.To, folder, a):
			due = append(due, b)
		}
	}
	oldest := func(bs []broadcast) func(i, j int) bool {
		return func(i, j int) bool { return bs[i].StartedAt.Time().Before(bs[j].StartedAt.Time()) }
	}
	sort.Slice(ended, oldest(ended))
	sort.Slice(due, oldest(due))
	var texts []string
	for _, b := range ended {
		for i := range b.Reads {
			if b.Reads[i].SessionID == a.ID {
				told := wire.DateOf(at)
				b.Reads[i].EndToldAt = &told
			}
		}
		if b.EndText != "" {
			texts = append(texts, b.EndText)
		}
		m[string(b.ID)] = b
	}
	for _, b := range due {
		if i := readIndex(b.Reads, a.ID); i >= 0 {
			// A worse level replaced the words: it reads the new revision.
			b.Reads[i].Revision, b.Reads[i].ReadAt = b.Revision, wire.DateOf(at)
			texts = append(texts, b.Text)
		} else if len(b.Reads) >= broadcastReadsMost {
			b.Unreached++
		} else {
			b.Reads = append(b.Reads, broadcastRead{SessionID: a.ID, Revision: b.Revision, ReadAt: wire.DateOf(at)})
			texts = append(texts, b.Text)
		}
		m[string(b.ID)] = b
	}
	return texts
}

func readIndex(reads []broadcastRead, id wire.UUID) int {
	for i, r := range reads {
		if r.SessionID == id {
			return i
		}
	}
	return -1
}

// broadcastStatuses is a workflow's active broadcasts for its summary, the newest first.
func (d *Daemon) broadcastStatuses(folder, workflowID string) []wire.WorkflowBroadcastStatus {
	at := nowFunc()
	d.broadcastMu.Lock()
	var mine []broadcast
	for _, b := range d.broadcastsLocked() {
		if b.Folder == folder && b.WorkflowID == workflowID && b.active(at) {
			mine = append(mine, b)
		}
	}
	d.broadcastMu.Unlock()
	if len(mine) == 0 {
		return nil
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].StartedAt.Time().After(mine[j].StartedAt.Time()) })
	if len(mine) > broadcastsListed {
		mine = mine[:broadcastsListed]
	}
	agents := d.Store.Agents()
	out := make([]wire.WorkflowBroadcastStatus, 0, len(mine))
	for _, b := range mine {
		reachable := int64(0)
		for i := range agents {
			if broadcastMatches(b.To, folder, &agents[i]) {
				reachable++
			}
		}
		s := wire.WorkflowBroadcastStatus{ID: b.ID, StartedAt: b.StartedAt, EndsAt: b.EndsAt,
			Reachable: reachable, Read: int64(len(b.Reads)), Level: b.Level}
		if b.MatchKey != "" {
			k := b.MatchKey
			s.MatchKey = &k
		}
		out = append(out, s)
	}
	return out
}

// sweepBroadcasts is the hourly sweep's part: it ends those whose time is up, drops those
// ended a day ago, and forgets the reads of sessions that are gone.
func (d *Daemon) sweepBroadcasts(at time.Time) {
	if d.broadcastFile == nil || d.Store == nil {
		return
	}
	d.endDueBroadcasts()
	d.endBroadcastsOfStoppedWorkflows()
	d.broadcastMu.Lock()
	defer d.broadcastMu.Unlock()
	_ = d.updateBroadcastsLocked(func(m map[string]broadcast) error {
		dropEndedBroadcasts(m, at)
		for k, b := range m {
			kept := b.Reads[:0]
			for _, r := range b.Reads {
				if d.Store.Agent(r.SessionID) != nil {
					kept = append(kept, r)
				}
			}
			b.Reads = kept
			m[k] = b
		}
		return nil
	})
}
