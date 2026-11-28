package agentsd

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A server's Codex signs in on its own (#7471, epic #7468, specs/7469-server-own-sign-in).
// When a turn on a server needs Codex and the server has no sign-in of its own, the daemon
// runs `codex login --device-auth` here and shows its URL and one-time code as a card on the
// agent, on every client: the person enters the code in any browser, a phone's too, and
// chooses Accept. Codex keeps its own auth.json on this server and renews it there. Nothing is
// sealed or sent through the control plane, and the Mac's host is not part of it. Once it is
// made, the Mac's lent sign-in is closed and this one wins.

// signInCardReplaced is what the chat says of a sign-in card whose code ran out or went with
// a restart, once a new card takes its place.
const signInCardReplaced = "Replaced by a new sign-in card."

// signInCodeField is the one field of Claude's sign-in card.
const signInCodeField = "code"

// signInCards are the open cards of a sign-in made here: request ID to sign-in ID.
type signInCardState struct {
	cards   map[wire.UUID]signInCard
	raising map[string]bool
	// dismissed are the runtimes whose card the person dismissed: not raised again until restart.
	dismissed map[string]bool
}

type signInCard struct {
	signInID string
	agentID  wire.UUID
}

// signInCardOf is the sign-in a card (a request) is for, or "".
func (d *Daemon) signInCardOf(request wire.UUID) string {
	d.headlessMu.Lock()
	defer d.headlessMu.Unlock()
	return d.signInCards.cards[request].signInID
}

func (d *Daemon) forgetSignInCard(request wire.UUID) {
	d.headlessMu.Lock()
	delete(d.signInCards.cards, request)
	d.headlessMu.Unlock()
}

func (d *Daemon) forgetSignInCardFor(id string) {
	d.headlessMu.Lock()
	for request, card := range d.signInCards.cards {
		if card.signInID == id {
			delete(d.signInCards.cards, request)
		}
	}
	d.headlessMu.Unlock()
}

// replaceExpiredSignInCard starts a fresh device code when Codex expires its current one.
// The normal turn path does not need to run again to raise the card.
func (d *Daemon) replaceExpiredSignInCard(id string) {
	d.headlessMu.Lock()
	var agent wire.UUID
	for _, card := range d.signInCards.cards {
		if card.signInID == id {
			agent = card.agentID
			break
		}
	}
	d.headlessMu.Unlock()
	if agent != "" {
		d.raiseSignInCard(agent, "codex")
	}
}

// raiseSignInCard shows the card for runtimeID's sign-in on agent, in the background, once:
// a card still open for a sign-in that is still running is the card.
func (d *Daemon) raiseSignInCard(agent wire.UUID, runtimeID string) {
	if macToolsHome != "" || runtimeID != "codex" && runtimeID != "claude" {
		return
	}
	d.headlessMu.Lock()
	gone := d.signInCards.dismissed[runtimeID]
	d.headlessMu.Unlock()
	out := d.signedOutHere(runtimeID)
	ends, endsSoon := d.claudeSignInEnds()
	if runtimeID == "claude" && !out && !endsSoon && (d.relayFor("claude") == nil || d.storedClaudeSignIn() != "" || ownClaudeSignIn()) {
		return // no lend to leave, or a sign-in of its own already (#7506)
	}
	if gone {
		return
	}
	type staleCard struct{ agent, req wire.UUID }
	var stale []staleCard
	d.headlessMu.Lock()
	for req, card := range d.signInCards.cards {
		if d.headless[card.signInID] != nil {
			d.headlessMu.Unlock()
			return
		}
		stale = append(stale, staleCard{card.agentID, req}) // its sign-in ran out or was stopped
	}
	if d.signInCards.raising[runtimeID] {
		d.headlessMu.Unlock()
		return
	}
	if d.signInCards.raising == nil {
		d.signInCards.raising = map[string]bool{}
	}
	d.signInCards.raising[runtimeID] = true
	d.headlessMu.Unlock()
	d.goJob("sign-in card", func() {
		defer func() {
			d.headlessMu.Lock()
			delete(d.signInCards.raising, runtimeID)
			d.headlessMu.Unlock()
		}()
		// A card left open by a restart: its sign-in is gone with the daemon.
		for _, form := range d.lostForms("") {
			if form.ElicitationID != nil && *form.ElicitationID == signInElicitation(runtimeID) && d.signInCardOf(form.ID) == "" {
				stale = append(stale, staleCard{form.AgentID, form.ID})
			}
		}
		for _, c := range stale {
			d.forgetSignInCard(c.req)
			if d.endRequest(c.req, wire.RequestStateDropped, nil, wire.RequestEndedWhyAskerEnded, nil) {
				// Said in the chat too, which draws a card asked and never answered (#4990).
				d.record(c.agent, wire.TranscriptEntryKind{ElicitationAnswered: &wire.TranscriptEntryKindElicitationAnswered{ID: c.req,
					Summary: signInCardReplaced}})
			}
			d.formDown(c.agent, c.req)
		}
		step, err := d.startHeadlessSignIn(d.ctx, runtimeID)
		if err != nil {
			log.Printf("sign-in card: %s: %v", runtimeID, err)
			return
		}
		msg, mode := d.signInCardWords(runtimeID, step, out, endsSoon, ends)
		eid := signInElicitation(runtimeID)
		req := wire.ElicitationRequest{ID: store.NewUUID(), ElicitationID: &eid, AgentID: agent, Message: &msg,
			Mode: mode, Scope: wire.ElicitationRequestScopeSession, AskedAt: now()}
		r := d.formRequest(req)
		if r == nil {
			d.cancelSignIn(step.SignInID)
			return
		}
		d.headlessMu.Lock()
		if d.signInCards.cards == nil {
			d.signInCards.cards = map[wire.UUID]signInCard{}
		}
		d.signInCards.cards[req.ID] = signInCard{signInID: step.SignInID, agentID: agent}
		d.headlessMu.Unlock()
		// Not held by a call: it is drawn as a lost form is (#4990), and answered the same way.
		d.record(agent, wire.TranscriptEntryKind{ElicitationAsked: &wire.TranscriptEntryKindElicitationAsked{Value: req}})
		opened := d.keepFormRequest(r)
		d.requestOpened(opened)
		Broadcast(d, wire.NotifyAgentElicitation, wire.ElicitationNotification{AgentID: agent, RequestID: req.ID, Request: &req})
		d.raiseAgentEvent("agent.asked_form", agent, "is asking for "+runtimeByID(runtimeID).Name+" to be signed in on this server.", nil)
	})
}

// signInCardWords is the card's words and its shape: a link for Codex's device code, a form
// with a field for the code for Claude's (#7506); Sign in again when the sign-in here ended
// (#7472), or ends within the warning.
func (d *Daemon) signInCardWords(runtimeID string, step wire.RuntimeSignInStep, out, endsSoon bool, ends time.Time) (string, wire.ElicitationRequestMode) {
	code := ""
	if step.UserCode != nil {
		code = *step.UserCode
	}
	msg := "Sign Codex in on this server, so it works when the Mac is away. Open " + string(step.URL) +
		" in any browser and enter the code " + code + ", then choose Accept."
	if out {
		msg = signedOutTitle("Codex", d.serverName()) + ": its sign-in on this server ended. To sign in again, open " +
			string(step.URL) + " in any browser and enter the code " + code + ", then choose Accept."
	}
	mode := wire.ElicitationRequestMode{URL: &wire.ElicitationRequestModeURL{Value: string(step.URL)}}
	if runtimeID == "claude" {
		// Claude's page gives a code to paste back: a form with the link in its words (#7506).
		msg = "Sign in on this server so it works when the Mac is away. Open " + string(step.URL) +
			" in any browser, sign in, and paste the code it gives."
		title := "Sign Claude in on this server"
		switch {
		case out:
			// Sign in again (#7472): the same sign-in as the first, made on this server, so
			// any client's browser does it and no Mac is needed.
			title = signedOutTitle("Claude", d.serverName())
			msg = "Its sign-in on this server ended. To sign in again, open " + string(step.URL) +
				" in any browser, sign in, and paste the code it gives."
		case endsSoon:
			// Its token lasts a year and does not renew (spec 7469 Q1): asked 30 days ahead.
			title = "Sign Claude in again on " + d.serverName()
			msg = "Its sign-in on this server ends on " + ends.Format("2 January 2006") + ". To sign in again now, open " +
				string(step.URL) + " in any browser, sign in, and paste the code it gives."
		}
		field := "Code from the sign-in page"
		mode = wire.ElicitationRequestMode{Form: &wire.ElicitationRequestModeForm{Value: wire.ElicitationSchema{Title: &title,
			Properties: []wire.ElicitationSchemaProperty{{Name: signInCodeField, Title: &field, IsRequired: true,
				Kind: wire.ElicitationSchemaPropertyKind{String: &wire.ElicitationSchemaPropertyKindString{}}}}}}}
	}
	return msg, mode
}

// serverName is this machine's name as the clients show it: its entry in the person's lock,
// which they approved, else its host name, as it told the control plane when it joined.
// Not ownName, which is the control plane's.
func (d *Daemon) serverName() string {
	if state, _ := d.heldLock(); state != nil {
		if me := d.ownHostID(); me != "" {
			for _, m := range state.Members(wire.LockEntryKindAddMachine) {
				if m.ID == me && m.Name != "" {
					return m.Name
				}
			}
		}
	}
	name, _ := os.Hostname()
	return name
}

// signedOutTitle is the card's and the turn's words for a sign-in that ended (#7472).
func signedOutTitle(runtime, server string) string {
	return runtime + " on " + server + " is signed out"
}

// signInEnded says whether a runtime's typed failure is its sign-in ending: the adapters
// give it the login action (Claude's auth_required, after a 401 or a revoked token).
func signInEnded(f *sessionFailure) bool {
	return f != nil && f.isError() && slices.Contains(f.Actions, "login")
}

// usesOwnSignIn says whether runtimeID runs on a sign-in of this server's own, not the
// Mac's lend and not an added account's.
func (d *Daemon) usesOwnSignIn(a *wire.Agent) bool {
	if macToolsHome != "" || a.AccountID != nil || d.relayFor(a.RuntimeID) != nil {
		return false
	}
	switch a.RuntimeID {
	case "claude":
		return d.storedClaudeSignIn() != "" || ownClaudeSignIn()
	case "codex":
		return d.ownCodexSignIn()
	}
	return false
}

// ownSignInEnded is a turn that failed because this server's own sign-in ended (#7472):
// expired or revoked. It marks the runtime signed out, says so on the agent in place of a
// general error, and raises one Sign in again card. It says whether it was that.
func (d *Daemon) ownSignInEnded(a *wire.Agent) bool {
	if !d.usesOwnSignIn(a) {
		return false
	}
	d.markSignedOut(a.RuntimeID, true)
	d.headlessMu.Lock()
	delete(d.signInCards.dismissed, a.RuntimeID) // a new end asks again
	d.headlessMu.Unlock()
	d.record(a.ID, notice("error", signedOutTitle(runtimeName(a.RuntimeID), d.serverName()),
		"Its own sign-in on this server ended: it expired or was revoked. Sign in again on the card, then send the message again."))
	if rt := runtimeByID(a.RuntimeID); rt != nil {
		Broadcast(d, wire.NotifyRuntimeChanged, d.statusOf(rt))
	}
	d.raiseSignInCard(a.ID, a.RuntimeID)
	return true
}

// signedOutFile marks runtimeID's own sign-in on this server as ended (#7472), so the card
// says Sign in again after a restart too. Empty; one per runtime.
func (d *Daemon) signedOutFile(runtimeID string) string {
	return filepath.Join(d.Store.Loc.Root, "runtimes", runtimeID+"-signed-out")
}

// signedOutHere says whether runtimeID's own sign-in on this server ended (#7472).
func (d *Daemon) signedOutHere(runtimeID string) bool {
	if d.Store == nil {
		return false
	}
	_, err := os.Stat(d.signedOutFile(runtimeID))
	return err == nil
}

// markSignedOut keeps or clears the mark.
func (d *Daemon) markSignedOut(runtimeID string, out bool) {
	if d.Store == nil {
		return
	}
	file := d.signedOutFile(runtimeID)
	if !out {
		_ = os.Remove(file)
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		log.Printf("sign-in: %s signed out: %v", runtimeID, err)
		return
	}
	// store-bounds: kind=state; size=0; count=1 per runtime; age=until signed in; applied=on write (#7472)
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		log.Printf("sign-in: %s signed out: %v", runtimeID, err)
	}
}

// signInElicitation is the ElicitationID of the daemon's sign-in card for runtimeID: a card
// left open by a restart is known by it and closed when a new one is raised.
func signInElicitation(runtimeID string) string { return "agents-sign-in:" + runtimeID }

// claudeTokenLife is how long a `claude setup-token` token lasts (spec 7469 Q1), and
// claudeTokenWarning how long before its end the card asks for a new one.
var claudeTokenLife, claudeTokenWarning = 365 * 24 * time.Hour, 30 * 24 * time.Hour

// claudeSignInEnds is when the token a sign-in made here kept ends, from the day it was
// kept, and whether that is within claudeTokenWarning. An API key does not end.
func (d *Daemon) claudeSignInEnds() (time.Time, bool) {
	if d.Store == nil || !strings.HasPrefix(d.storedClaudeSignIn(), "CLAUDE_CODE_OAUTH_TOKEN=") {
		return time.Time{}, false
	}
	fi, err := os.Stat(d.claudeSignInFile())
	if err != nil {
		return time.Time{}, false
	}
	ends := fi.ModTime().Add(claudeTokenLife)
	return ends, time.Until(ends) < claudeTokenWarning
}

func (d *Daemon) cancelSignIn(id string) {
	cancel := true
	_, _ = d.finishHeadlessSignIn(context.Background(), wire.RuntimeSignInFinish{SignInID: id, Cancel: &cancel})
}

// answerSignInCard is the answer to a sign-in card. Accept is Done: Codex must have seen the
// code, or the card stays open and says so. Anything else stops the sign-in.
func (d *Daemon) answerSignInCard(signInID string, action wire.AnswerElicitationRequestAction, code string) error {
	if action != wire.AnswerElicitationRequestActionAccept {
		d.headlessMu.Lock()
		if s := d.headless[signInID]; s != nil {
			if d.signInCards.dismissed == nil {
				d.signInCards.dismissed = map[string]bool{}
			}
			d.signInCards.dismissed[s.runtimeID] = true
		}
		d.headlessMu.Unlock()
		d.forgetSignInCardFor(signInID)
		d.cancelSignIn(signInID)
		return nil
	}
	fin := wire.RuntimeSignInFinish{SignInID: signInID}
	if code != "" {
		fin.Code = &code
	}
	_, err := d.finishHeadlessSignIn(d.ctx, fin)
	if err == nil {
		d.forgetSignInCardFor(signInID)
	}
	return err
}
