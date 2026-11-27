package agentsd

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Requests (#3863, spec 3862): what an agent asks a person for, kept on disk so a restart
// does not lose it. One file a record, <root>/requests/<id>.json. ask_question, ask_secret
// and hand_to_person open one in showForm; the answer or the form taken down ends it, and
// the end raises request.answered or request.dropped, which wakes every session that waits
// on it (wait_for_event with where: {request: <id>}).

// The bound (spec 3862 §1, #6718): an ended record goes after requestKeepFor, and at most
// requestFileLimit records are kept, the oldest ended ones going first. An open one is a
// person's: it is never evicted, a new one past the limit is refused, and at requestMaxAge
// it is dropped and its asker told.
var (
	requestKeepFor   = 14 * 24 * time.Hour
	requestFileLimit = 1000
)

type requestStore struct {
	mu   sync.Mutex
	byID map[wire.UUID]*wire.Request
	// held is the open form requests whose call this daemon holds (#6475): never lost
	// forms, also in the instant after an answer takes the form from its live and before
	// it ends the request, or a second answer at once would be taken as a lost form's.
	held map[wire.UUID]bool
	// timer brings back the deferred requests whose time has come (#4233).
	timer *time.Timer
}

func (d *Daemon) requestsDir() string { return filepath.Join(d.Store.Loc.Root, "requests") }

// loadRequests reads the records at start and prunes them; the hourly sweep does after that.
func (d *Daemon) loadRequests() {
	d.requests.mu.Lock()
	d.requests.byID = map[wire.UUID]*wire.Request{}
	records := d.requestRecords()
	for cursor := ""; ; {
		ids, next, err := records.List(cursor, requestPage)
		if err != nil {
			log.Printf("requests: %v", err)
		}
		for _, id := range ids {
			b, err := records.Get(id)
			var r wire.Request
			if err != nil || json.Unmarshal(b, &r) != nil || string(r.ID) != id {
				log.Printf("requests: skipped unreadable %s.json", id)
				continue
			}
			d.requests.byID[r.ID] = &r
		}
		if next == "" {
			break
		}
		cursor = next
	}
	d.requests.mu.Unlock()
	d.pruneRequests(time.Now())
	d.armRequestTimer() // one due while the host was down comes back at once
}

// sweepRequests holds the requests to their bounds, in the hourly sweep.
func (d *Daemon) sweepRequests(at time.Time) {
	d.dropGoneAskers()
	d.expireRequests(at)
	d.pruneRequests(at)
	d.requestRecords().Expire(at.Add(-requestMaxAge), func(id string) bool { return d.request(wire.UUID(id)) != nil })
}

// expireRequests drops each open request that is requestMaxAge old: its card goes, the chat
// keeps a line, and the session that asked starts again once to hear it (Alex, #6718). A
// forge item still listed opens as a new request in the next forge sweep.
func (d *Daemon) expireRequests(at time.Time) {
	d.requests.mu.Lock()
	for _, r := range d.requests.byID {
		if r.AskedAt.Time().After(at) { // a clock that went back: its age starts now
			r.AskedAt = wire.DateOf(at)
			d.saveRequestLocked(r)
		}
	}
	d.requests.mu.Unlock()
	for _, r := range d.listRequests(wire.RequestsListStateOpen, "") {
		if at.Sub(r.AskedAt.Time()) < requestMaxAge {
			return // the oldest first
		}
		d.expireRequest(r)
	}
}

func (d *Daemon) expireRequest(r wire.Request) {
	var l *live
	var pe *pendingElicitation
	for _, each := range d.everyLive() {
		if each.elicitationFor(r.ID) != nil {
			if l, pe = each, each.take(r.ID); pe == nil {
				return // being answered
			}
			break
		}
	}
	if pe == nil && d.heldForm(r.ID) {
		return // being answered
	}
	waits := r.AskedBy != nil && d.waitsOnDrop(*r.AskedBy, r.ID)
	if !d.endRequest(r.ID, wire.RequestStateDropped, nil, wire.RequestEndedWhyExpired, nil) {
		return
	}
	d.secretUseEnded(r.ID)
	if r.AskedBy == nil {
		return
	}
	asker, title := *r.AskedBy, clip(r.Title, 200)
	d.record(asker, runtimeNote("Nobody answered “"+title+"” in 90 days, so it expired."))
	if r.Detail.Elicitation != nil {
		d.formDown(asker, r.ID)
	}
	words := "Your request \"" + title + "\" expired: nobody answered it in 90 days. Ask again if you still need it, or end your turn."
	switch {
	case pe != nil && pe.left && pe.late != nil:
		d.goJob("late form answer", func() { pe.late(words) })
		return
	case pe != nil && !pe.left:
		d.moveIf(l.id, wire.AgentStateWaitingOnUser, wire.AgentStateRunning)
		pe.reply <- cancelled()
	case waits:
		return // request.dropped starts it
	}
	if err := d.promptFromApp(asker, words); err != nil {
		log.Printf("agent %s: request expired: %v", asker, err)
	}
}

// waitsOnDrop is whether agent waits for the event that a drop of request id raises.
func (d *Daemon) waitsOnDrop(agent, id wire.UUID) bool {
	a := d.Store.Agent(agent)
	if a == nil || a.EventWait == nil || a.EventWait.Ending != nil {
		return false
	}
	for _, p := range a.EventWait.Patterns {
		if p.Name == "request.dropped" && strings.EqualFold(p.Filters["request"], string(id)) {
			return true
		}
	}
	return false
}

// requestRefusedLocked is why a new request is not opened, or nil: the open ones are a
// person's, so none is evicted to make room.
func (d *Daemon) requestRefusedLocked(r *wire.Request) error {
	open := 0
	for _, each := range d.requests.byID {
		if each.State == wire.RequestStateOpen {
			open++
		}
	}
	if open >= requestFileLimit {
		return Invalid("This machine has %d open requests already, the most it keeps. Nothing was asked: the person answers or drops some first.", open)
	}
	if b, _ := json.Marshal(r); len(b) > requestMaxNew {
		return Invalid("Nothing was asked: the request is %d KB, and the most is %d KB. Ask with fewer words.", len(b)>>10, requestMaxNew>>10)
	}
	return nil
}

// openRequestLocked keeps a new request, on disk too, unless it is refused.
func (d *Daemon) openRequestLocked(r *wire.Request) (wire.Request, error) {
	if err := d.requestRefusedLocked(r); err != nil {
		return wire.Request{}, err
	}
	if d.requests.byID == nil {
		d.requests.byID = map[wire.UUID]*wire.Request{}
	}
	d.requests.byID[r.ID] = r
	d.saveRequestLocked(r)
	return copyRequest(r), nil
}

// formRefused is why a form cannot be a request, before it is shown.
func (d *Daemon) formRefused(req wire.ElicitationRequest) error {
	r := d.formRequest(req)
	if r == nil {
		return nil
	}
	d.requests.mu.Lock()
	defer d.requests.mu.Unlock()
	return d.requestRefusedLocked(r)
}

// dropGoneAskers drops the open requests whose asker has gone.
func (d *Daemon) dropGoneAskers() {
	for _, r := range d.listRequests(wire.RequestsListStateOpen, "") {
		if r.AskedBy == nil {
			continue
		}
		switch a := d.Store.Agent(*r.AskedBy); {
		case a == nil:
			d.endRequest(r.ID, wire.RequestStateDropped, nil, wire.RequestEndedWhyAskerEnded, nil)
		case a.State == wire.AgentStateArchived:
			d.endRequest(r.ID, wire.RequestStateDropped, nil, wire.RequestEndedWhyFolderArchived, nil)
		}
	}
}

// pruneRequests drops the open requests whose asker has gone, then keeps the bounds of the
// ended ones.
func (d *Daemon) pruneRequests(at time.Time) {
	d.dropGoneAskers()
	d.requests.mu.Lock()
	defer d.requests.mu.Unlock()
	d.pruneEndedLocked(at)
}

func (d *Daemon) pruneEndedLocked(at time.Time) {
	var ended []*wire.Request
	for _, r := range d.requests.byID {
		if r.State != wire.RequestStateOpen {
			ended = append(ended, r)
		}
	}
	sort.Slice(ended, func(i, j int) bool { return endedAt(ended[i]) < endedAt(ended[j]) })
	over := len(d.requests.byID) - requestFileLimit
	for _, r := range ended {
		if over <= 0 && at.Sub(endedAt(r).Time()) < requestKeepFor && at.Sub(r.AskedAt.Time()) < requestMaxAge {
			continue
		}
		d.removeRequestLocked(r.ID)
		over--
	}
}

func endedAt(r *wire.Request) wire.Date {
	if r.AnsweredAt != nil {
		return *r.AnsweredAt
	}
	return r.AskedAt
}

func (d *Daemon) removeRequestLocked(id wire.UUID) {
	delete(d.requests.byID, id)
	if err := d.requestRecords().Delete(string(id)); err != nil {
		log.Printf("requests: %v", err)
	}
}

func (d *Daemon) saveRequestLocked(r *wire.Request) {
	if err := d.putRequestLocked(r); err != nil {
		log.Printf("requests: %s: %v", r.ID, err)
	}
}

func (d *Daemon) putRequestLocked(r *wire.Request) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := d.requestRecords().Put(string(r.ID), b); err != nil {
		return err
	}
	// Past the high-water mark the ended ones go now, not in the hourly sweep.
	if len(d.requests.byID) >= 2*requestFileLimit {
		d.pruneEndedLocked(nowFunc())
	}
	return nil
}

// listRequests is requests/list: oldest first.
func (d *Daemon) listRequests(state wire.RequestsListState, folder string) []wire.Request {
	d.requests.mu.Lock()
	out := []wire.Request{}
	for _, r := range d.requests.byID {
		open := r.State == wire.RequestStateOpen
		if state == wire.RequestsListStateOpen && !open || state == wire.RequestsListStateEnded && open {
			continue
		}
		if folder != "" && r.Folder != folder {
			continue
		}
		out = append(out, copyRequest(r))
	}
	d.requests.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].AskedAt < out[j].AskedAt })
	return out
}

// lostForms is the forms of the open requests whose call a restart took (#4990): an
// elicitation that no live agent holds, asked by a session that is not archived. With agent
// set, that session's only. Oldest first.
func (d *Daemon) lostForms(agent wire.UUID) []wire.ElicitationRequest {
	out := []wire.ElicitationRequest{}
	for _, r := range d.listRequests(wire.RequestsListStateOpen, "") {
		if r.Detail.Elicitation == nil || r.AskedBy == nil || agent != "" && *r.AskedBy != agent {
			continue
		}
		if a := d.Store.Agent(*r.AskedBy); a == nil || a.State == wire.AgentStateArchived {
			continue
		}
		if d.heldForm(r.ID) || d.liveFormOf(r.ID) != nil {
			continue
		}
		out = append(out, *r.Detail.Elicitation)
	}
	return out
}

// dropRequestsOf drops every open request a session asked, held or lost, as it is archived
// (#4990): halt takes down only the forms held in memory.
func (d *Daemon) dropRequestsOf(agent wire.UUID, why wire.RequestEndedWhy) {
	for _, r := range d.listRequests(wire.RequestsListStateOpen, "") {
		if r.AskedBy == nil || *r.AskedBy != agent {
			continue
		}
		if d.endRequest(r.ID, wire.RequestStateDropped, nil, why, nil) && r.Detail.Elicitation != nil {
			d.formDown(agent, r.ID)
		}
	}
}

func copyRequest(r *wire.Request) wire.Request {
	c := *r
	c.Waiters = append([]wire.RequestWaiter{}, r.Waiters...)
	if r.Forge != nil {
		f := *r.Forge
		f.Checklist = append([]wire.RequestChecklistLine{}, r.Forge.Checklist...)
		c.Forge = &f
	}
	if r.Deferred != nil {
		def := *r.Deferred
		c.Deferred = &def
	}
	return c
}

func (d *Daemon) request(id wire.UUID) *wire.Request {
	d.requests.mu.Lock()
	defer d.requests.mu.Unlock()
	if r := d.requests.byID[id]; r != nil {
		c := copyRequest(r)
		return &c
	}
	return nil
}

// formRequest is the record for a form an agent shows: a question, a secret or a step. It is
// nil for a form that gets none. keepFormRequest keeps it and requestOpened says it.
func (d *Daemon) formRequest(req wire.ElicitationRequest) *wire.Request {
	a := d.Store.Agent(req.AgentID)
	if a == nil || a.Purpose != nil {
		return nil
	}
	kind, source, title := wire.RequestKindDecide, wire.RequestSourceQuestion, "A question"
	if req.Message != nil && strings.TrimSpace(*req.Message) != "" {
		title = strings.TrimSpace(*req.Message)
	}
	switch {
	case req.Secret != nil:
		kind, source, title = wire.RequestKindSecret, wire.RequestSourceSecret, "The secret "+req.Secret.Name
	case req.HandOff != nil:
		kind, source, title = wire.RequestKindStep, wire.RequestSourceHandOff, req.HandOff.Step
	}
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	at := now()
	asker := req.AgentID
	return &wire.Request{ID: req.ID, Kind: kind, Source: source, State: wire.RequestStateOpen, Title: title,
		Detail: wire.RequestDetail{Elicitation: &req}, Folder: string(store.FolderURL(folderPath(a))), AskedBy: &asker,
		Waiters: []wire.RequestWaiter{{Agent: asker, Since: at}}, AskedAt: at}
}

// keepFormRequest keeps a form's request, on disk too, and is a copy of it. showForm calls it
// with the agent's lock held, as it shows the form (#5974): an answer that came between the
// two found no request to end, and the request opened after it stayed open for ever.
func (d *Daemon) keepFormRequest(r *wire.Request) wire.Request {
	d.requests.mu.Lock()
	defer d.requests.mu.Unlock()
	if d.requests.byID == nil {
		d.requests.byID = map[wire.UUID]*wire.Request{}
	}
	d.requests.byID[r.ID] = r
	d.saveRequestLocked(r)
	return copyRequest(r)
}

// holdFormRequest marks form request id as one whose call this daemon holds
// (requestStore.held), until the request ends.
func (d *Daemon) holdFormRequest(id wire.UUID) {
	d.requests.mu.Lock()
	defer d.requests.mu.Unlock()
	if d.requests.held == nil {
		d.requests.held = map[wire.UUID]bool{}
	}
	d.requests.held[id] = true
}

// requestOpened tells the clients and raises request.opened, once the agent's lock is let go.
// The clients get the request as it is now: an answer that came first ended it already.
func (d *Daemon) requestOpened(opened wire.Request) {
	c := opened
	if cur := d.request(opened.ID); cur != nil {
		c = *cur
	}
	Broadcast(d, wire.NotifyRequestsChanged, wire.RequestUpdate{Request: c})
	d.raiseRequestEvent("request.opened", opened, nil, nil)
}

// addRequestWaiter is a session that joins an open request with wait_for_event.
func (d *Daemon) addRequestWaiter(id, agent wire.UUID) {
	d.requests.mu.Lock()
	r := d.requests.byID[id]
	if r == nil || r.State != wire.RequestStateOpen {
		d.requests.mu.Unlock()
		return
	}
	for _, w := range r.Waiters {
		if w.Agent == agent {
			d.requests.mu.Unlock()
			return
		}
	}
	if len(r.Waiters) >= requestMaxWaiters { // it waits all the same; it is only not listed
		d.requests.mu.Unlock()
		return
	}
	r.Waiters = append(r.Waiters, wire.RequestWaiter{Agent: agent, Since: now()})
	d.saveRequestLocked(r)
	c := copyRequest(r)
	d.requests.mu.Unlock()
	Broadcast(d, wire.NotifyRequestsChanged, wire.RequestUpdate{Request: c})
}

// endRequest answers or drops an open request, once: its end raises the event that wakes
// its waiters. It is false when the request had ended already, or is not here. What the
// answer does is done already (a grant, a merge on the forge), so it ends here also when the
// disk refuses the record.
func (d *Daemon) endRequest(id wire.UUID, state wire.RequestState, answer *string, why wire.RequestEndedWhy, by *wire.AuditActor) bool {
	ended, err := d.endRequestKept(id, state, answer, why, by, false)
	if err != nil {
		log.Printf("requests: %s: %v", id, err)
	}
	return ended
}

// answerFormRequest is endRequest for a form's answer. One that the disk refuses is not told:
// it is an error and the request stays open, for it would be open after a restart (#7185).
func (d *Daemon) answerFormRequest(id wire.UUID, answer *string, by *wire.AuditActor) (bool, error) {
	ended, err := d.endRequestKept(id, wire.RequestStateAnswered, answer, "", by, true)
	if err != nil {
		return false, Invalid("The answer was not kept, so it was not given. Answer again. (%v)", err)
	}
	return ended, nil
}

// fitAnswerLocked cuts an ended request's answer to the room its record has, measured as it is
// stored, where a character can be 6 bytes. requestMaxNew leaves room for an answer always.
func fitAnswerLocked(r *wire.Request) {
	if r.Answer == nil {
		return
	}
	fits := func() bool {
		b, err := json.Marshal(r)
		return err != nil || len(b) <= requestMaxBytes
	}
	whole := *r.Answer
	cutTo := func(n int) string { return strings.ToValidUTF8(whole[:n], "") + "…" }
	if len(whole) > requestMaxAnswer {
		r.Answer = new(cutTo(requestMaxAnswer))
	}
	if fits() {
		return
	}
	over := sort.Search(min(len(whole), requestMaxAnswer), func(n int) bool {
		r.Answer = new(cutTo(n))
		return !fits()
	})
	r.Answer = new(cutTo(max(0, over-1)))
}

// endRequestKept ends an open request. With mustKeep, one that the disk refuses stays open.
func (d *Daemon) endRequestKept(id wire.UUID, state wire.RequestState, answer *string, why wire.RequestEndedWhy, by *wire.AuditActor, mustKeep bool) (bool, error) {
	d.requests.mu.Lock()
	r := d.requests.byID[id]
	if r == nil || r.State != wire.RequestStateOpen {
		delete(d.requests.held, id)
		d.requests.mu.Unlock()
		return false, nil
	}
	was := *r
	at := now()
	r.State, r.AnsweredAt, r.AnsweredBy = state, &at, by
	r.Deferred = nil // an end clears Later (#4233)
	if state == wire.RequestStateAnswered {
		r.Answer = answer
		fitAnswerLocked(r)
		answer = r.Answer
	} else {
		r.DroppedWhy = &why
	}
	err := d.putRequestLocked(r)
	if err != nil && mustKeep {
		*r = was
		d.requests.mu.Unlock()
		return false, err
	}
	delete(d.requests.held, id)
	c := copyRequest(r)
	d.requests.mu.Unlock()
	Broadcast(d, wire.NotifyRequestsChanged, wire.RequestUpdate{Request: c})
	if state == wire.RequestStateAnswered {
		d.raiseRequestEvent("request.answered", c, map[string]string{"answer": deref(answer), "answered_by": actorName(by)}, by)
	} else {
		d.raiseRequestEvent("request.dropped", c, map[string]string{"why": string(why)}, by)
	}
	if c.Detail.SecretUse != nil {
		d.reconsider() // its banner goes (#4017)
	}
	return true, err
}

func actorName(a *wire.AuditActor) string {
	switch {
	case a == nil:
		return ""
	case a.UserName != nil && *a.UserName != "":
		return *a.UserName
	}
	return a.User
}

func (d *Daemon) raiseRequestEvent(name string, r wire.Request, extra map[string]string, by *wire.AuditActor) {
	details := map[string]string{"request": string(r.ID), "kind": string(r.Kind), "source": string(r.Source), "title": r.Title}
	if r.AskedBy != nil {
		details["agent"] = string(*r.AskedBy)
	}
	forgeRequestDetails(r, details)
	for k, v := range extra {
		if v != "" {
			details[k] = v
		}
	}
	sentence := map[string]string{"request.opened": "A request was opened: ", "request.answered": "A request was answered: ",
		"request.dropped": "A request was dropped: ", "request.deferred": "A request was deferred: "}[name] + r.Title
	d.raiseAs(wire.EventDraft{Name: name, At: now(), Scope: folderScope(store.FolderPath(wire.URL(r.Folder))), Sentence: sentence, Details: details}, by)
}

// formAnswerSummary is the answer's summary on the record: never a secret's value.
func formAnswerSummary(action wire.AnswerElicitationRequestAction, said []wire.ElicitationAnswer) *string {
	s := answeredSummary(action)
	if len(said) > 0 {
		var parts []string
		for _, a := range said {
			parts = append(parts, a.Answer)
		}
		s += ": " + strings.Join(parts, "; ")
	}
	return &s
}

// answerRequest is requests/answer. A form still held goes the way elicitations/answer
// goes; one whose call went with a restart starts its session again with the answer.
func (d *Daemon) answerRequest(r wire.RequestsAnswer, by *wire.AuditActor) (wire.RequestUpdate, error) {
	rec := d.request(r.ID)
	if rec == nil {
		return wire.RequestUpdate{}, &Error{Code: wire.FailureNoSuchAgent, Message: "There is no such request."}
	}
	if rec.State != wire.RequestStateOpen {
		return wire.RequestUpdate{}, &Error{Code: wire.FailureNoSuchAgent, Message: "That request has ended already.", Data: wire.RequestUpdate{Request: *rec}}
	}
	form := r.Answer.Form
	if form == nil || rec.Detail.Elicitation == nil {
		return wire.RequestUpdate{}, Invalid("This request takes a form answer.")
	}
	d.answeredBy(r.ID, by)
	err := d.answerElicitation(wire.AnswerElicitationRequest{RequestID: r.ID, Action: form.Action, Content: form.Content})
	if e, ok := err.(*Error); ok && e.Code == wire.FailureNoSuchAgent && d.liveFormOf(r.ID) == nil {
		err = d.answerLostForm(*rec.Detail.Elicitation, form.Action, form.Content, by)
	}
	if err != nil {
		return wire.RequestUpdate{}, err
	}
	if after := d.request(r.ID); after != nil {
		return wire.RequestUpdate{Request: *after}, nil
	}
	return wire.RequestUpdate{Request: *rec}, nil
}

// answeredBy notes who answers a request, for the record the answer path ends.
func (d *Daemon) answeredBy(id wire.UUID, by *wire.AuditActor) {
	d.actorMu.Lock()
	defer d.actorMu.Unlock()
	if d.requestActors == nil {
		d.requestActors = map[wire.UUID]*wire.AuditActor{}
	}
	d.requestActors[id] = by
}

func (d *Daemon) takeAnsweredBy(id wire.UUID) *wire.AuditActor {
	d.actorMu.Lock()
	defer d.actorMu.Unlock()
	a := d.requestActors[id]
	delete(d.requestActors, id)
	return a
}

func (d *Daemon) liveFormOf(id wire.UUID) *pendingElicitation {
	for _, l := range d.everyLive() {
		if pe := l.elicitationFor(id); pe != nil {
			return pe
		}
	}
	return nil
}

// heldForm is whether this daemon holds the form request id's call (requestStore.held).
func (d *Daemon) heldForm(id wire.UUID) bool {
	d.requests.mu.Lock()
	defer d.requests.mu.Unlock()
	return d.requests.held[id]
}

// answerLostForm answers a form whose held call a restart took: as for a form left open
// (#1641), the answer starts the session again with a prompt.
func (d *Daemon) answerLostForm(req wire.ElicitationRequest, action wire.AnswerElicitationRequestAction, content map[string]wire.JSONValue, by *wire.AuditActor) error {
	if action == "" {
		action = wire.AnswerElicitationRequestActionCancel
	}
	if content == nil {
		content = map[string]wire.JSONValue{}
	}
	var said []wire.ElicitationAnswer
	switch {
	case req.Secret != nil:
		sessionOnly := askedSessionOnly(content)
		if action == wire.AnswerElicitationRequestActionAccept {
			if err := d.saveAskedSecret(req.AgentID, *req.Secret, content); err != nil {
				return err
			}
		}
		content = map[string]wire.JSONValue{}
		said = secretAnswered(*req.Secret, action, sessionOnly)
	case action == wire.AnswerElicitationRequestActionAccept && req.Mode.Form != nil:
		if why := problems(req.Mode.Form.Value, content); len(why) > 0 {
			return Invalid("%s", strings.Join(why, ". "))
		}
		said = answers(req.Mode.Form.Value, content, req.Message)
	}
	// A card for a sign-in made here (#7471): Accept is Done, and the agent is not prompted.
	signIn := d.signInCardOf(req.ID)
	if signIn != "" && action == wire.AnswerElicitationRequestActionAccept {
		if err := d.answerSignInCard(signIn, action); err != nil {
			return err
		}
	}
	if d.heldForm(req.ID) {
		// Not lost: an answer that came first is ending it (#6475).
		return &Error{Code: wire.FailureNoSuchAgent, Message: "That form has already been answered."}
	}
	if ended, err := d.answerFormRequest(req.ID, formAnswerSummary(action, said), by); err != nil {
		return err
	} else if !ended {
		return &Error{Code: wire.FailureNoSuchAgent, Message: "That request has ended already."}
	}
	d.takeAnsweredBy(req.ID)
	d.formDown(req.AgentID, req.ID) // a lost form's card is drawn (#4990): it goes now
	d.record(req.AgentID, wire.TranscriptEntryKind{ElicitationAnswered: &wire.TranscriptEntryKindElicitationAnswered{ID: req.ID,
		Summary: answeredSummary(action), Answers: said}})
	if signIn != "" {
		if action != wire.AnswerElicitationRequestActionAccept {
			d.answerSignInCard(signIn, action) //nolint:errcheck // a stop has nothing to report
		}
		d.forgetSignInCard(req.ID)
		return nil
	}
	// A dismissed lost form (#5295): the person's next message is the reply.
	if action == wire.AnswerElicitationRequestActionCancel {
		return nil
	}
	if err := d.promptFromApp(req.AgentID, d.formAnsweredWords(req, action, content, said, "")); err != nil {
		log.Printf("agent %s: request answer: %v", req.AgentID, err)
	}
	return nil
}

// dropRequest is requests/drop: a form still held is taken down, and its agent hears cancel.
func (d *Daemon) dropRequest(r wire.RequestsDrop, by *wire.AuditActor) (wire.RequestUpdate, error) {
	rec := d.request(r.ID)
	if rec == nil {
		return wire.RequestUpdate{}, &Error{Code: wire.FailureNoSuchAgent, Message: "There is no such request."}
	}
	why := wire.RequestEndedWhyByPerson
	if r.Why != nil {
		why = wire.RequestEndedWhy(*r.Why)
	}
	if !d.endRequest(r.ID, wire.RequestStateDropped, nil, why, by) {
		return wire.RequestUpdate{}, &Error{Code: wire.FailureNoSuchAgent, Message: "That request has ended already.", Data: wire.RequestUpdate{Request: *rec}}
	}
	if d.liveFormOf(r.ID) != nil {
		// The request is dropped already; a form answered meanwhile has nothing left to cancel.
		if err := d.answerElicitation(wire.AnswerElicitationRequest{RequestID: r.ID, Action: wire.AnswerElicitationRequestActionCancel}); err != nil {
			log.Printf("requests: drop %s: could not cancel its form: %v", r.ID, err)
		}
	}
	d.secretUseEnded(r.ID)
	after := d.request(r.ID)
	return wire.RequestUpdate{Request: *after}, nil
}

func (d *Daemon) registerRequests() {
	On(d, wire.CallRequestsList, func(ctx context.Context, c *Client, r wire.RequestsList) (wire.RequestsListResult, error) {
		state := wire.RequestsListStateOpen
		if r.State != nil {
			state = *r.State
		}
		return wire.RequestsListResult{Requests: d.listRequests(state, deref(r.Folder))}, nil
	})
	On(d, wire.CallRequestsAnswer, func(ctx context.Context, c *Client, r wire.RequestsAnswer) (wire.RequestUpdate, error) {
		if rec := d.request(r.ID); rec != nil && rec.Forge != nil && rec.State == wire.RequestStateOpen {
			return d.answerForgeRequest(ctx, *rec, r.Answer, c.actor())
		}
		if rec := d.request(r.ID); rec != nil && rec.Detail.SecretUse != nil && rec.State == wire.RequestStateOpen {
			if !askBeforeUseAllowed(c) {
				return wire.RequestUpdate{}, &Error{Code: CodeNotPermitted, Message: "Only a person's own client approves the use of a secret."}
			}
			return d.answerSecretUse(*rec, r.Answer.SecretUse, c.actor())
		}
		return d.answerRequest(r, c.actor())
	})
	On(d, wire.CallRequestsDrop, func(ctx context.Context, c *Client, r wire.RequestsDrop) (wire.RequestUpdate, error) {
		return d.dropRequest(r, c.actor())
	})
	d.registerRequestDefer()
}
