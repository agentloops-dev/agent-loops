package agentsd

import (
	"context"
	"log"
	"os"
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

// signInCodeField is the one field of Claude's sign-in card.
const signInCodeField = "code"

// signInCards are the open cards of a sign-in made here: request ID to sign-in ID.
type signInCardState struct {
	cards   map[wire.UUID]signInCard
	raising map[string]bool
	// dismissed are the runtimes whose card the person dismissed: not raised again until restart.
	dismissed map[string]bool
	// signedOut are the runtimes whose own sign-in on this server ended (#7472): expired or
	// revoked. Their card is Sign in again until a sign-in is made here.
	signedOut map[string]bool
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
	gone, out := d.signInCards.dismissed[runtimeID], d.signInCards.signedOut[runtimeID]
	d.headlessMu.Unlock()
	ends, endsSoon := d.claudeSignInEnds()
	if runtimeID == "claude" && !out && !endsSoon && (d.relayFor("claude") == nil || d.storedClaudeSignIn() != "" || ownClaudeSignIn()) {
		return // no lend to leave, or a sign-in of its own already (#7506)
	}
	if gone {
		return
	}
	var stale []wire.UUID
	d.headlessMu.Lock()
	for req, card := range d.signInCards.cards {
		if d.headless[card.signInID] != nil {
			d.headlessMu.Unlock()
			return
		}
		stale = append(stale, req) // its sign-in ran out or was stopped
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
		for _, req := range stale {
			d.forgetSignInCard(req)
			d.endRequest(req, wire.RequestStateDropped, nil, wire.RequestEndedWhyAskerEnded, nil)
			d.formDown(agent, req)
		}
		step, err := d.startHeadlessSignIn(d.ctx, runtimeID)
		if err != nil {
			log.Printf("sign-in card: %s: %v", runtimeID, err)
			return
		}
		code := ""
		if step.UserCode != nil {
			code = *step.UserCode
		}
		msg := "Sign Codex in on this server, so it works when the Mac is away. Open " + string(step.URL) +
			" in any browser and enter the code " + code + ", then choose Accept."
		if out {
			msg = signedOutTitle("Codex", d.ownName()) + ": its sign-in on this server ended. To sign in again, open " +
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
				title = signedOutTitle("Claude", d.ownName())
				msg = "Its sign-in on this server ended. To sign in again, open " + string(step.URL) +
					" in any browser, sign in, and paste the code it gives."
			case endsSoon:
				// Its token lasts a year and does not renew (spec 7469 Q1): asked 30 days ahead.
				title = "Sign Claude in again on " + d.ownName()
				msg = "Its sign-in on this server ends on " + ends.Format("2 January 2006") + ". To sign in again now, open " +
					string(step.URL) + " in any browser, sign in, and paste the code it gives."
			}
			field := "Code from the sign-in page"
			mode = wire.ElicitationRequestMode{Form: &wire.ElicitationRequestModeForm{Value: wire.ElicitationSchema{Title: &title,
				Properties: []wire.ElicitationSchemaProperty{{Name: signInCodeField, Title: &field, IsRequired: true,
					Kind: wire.ElicitationSchemaPropertyKind{String: &wire.ElicitationSchemaPropertyKindString{}}}}}}}
		}
		req := wire.ElicitationRequest{ID: store.NewUUID(), AgentID: agent, Message: &msg,
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
	d.headlessMu.Lock()
	if d.signInCards.signedOut == nil {
		d.signInCards.signedOut = map[string]bool{}
	}
	d.signInCards.signedOut[a.RuntimeID] = true
	delete(d.signInCards.dismissed, a.RuntimeID) // a new end asks again
	d.headlessMu.Unlock()
	d.record(a.ID, notice("error", signedOutTitle(runtimeName(a.RuntimeID), d.ownName()),
		"Its own sign-in on this server ended: it expired or was revoked. Sign in again on the card, then send the message again."))
	if rt := runtimeByID(a.RuntimeID); rt != nil {
		Broadcast(d, wire.NotifyRuntimeChanged, d.statusOf(rt))
	}
	d.raiseSignInCard(a.ID, a.RuntimeID)
	return true
}

// signedOutHere says whether runtimeID's own sign-in on this server ended (#7472).
func (d *Daemon) signedOutHere(runtimeID string) bool {
	d.headlessMu.Lock()
	defer d.headlessMu.Unlock()
	return d.signInCards.signedOut[runtimeID]
}

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
