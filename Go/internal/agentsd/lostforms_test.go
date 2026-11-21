package agentsd

import (
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #4990: a form whose call a restart took keeps its open request, so Needs You lists it.
// The chat draws it again, its card's answer is taken, and an archive drops it at once.

func lostForm(t *testing.T, d *Daemon, asker wire.UUID, msg string) wire.ElicitationRequest {
	t.Helper()
	req := wire.ElicitationRequest{ID: store.NewUUID(), AgentID: asker, Message: &msg, AskedAt: now(),
		Mode: wire.ElicitationRequestMode{Form: &wire.ElicitationRequestModeForm{Value: wire.ElicitationSchema{}}}}
	d.openFormRequest(req) // a request with no live form: what a restart leaves
	return req
}

func pendingIDs(t *testing.T, c *conn, id int) map[wire.UUID]bool {
	t.Helper()
	var got []wire.ElicitationRequest
	json.Unmarshal(c.call(t, id, "elicitations/pending", map[string]any{}), &got)
	out := map[wire.UUID]bool{}
	for _, e := range got {
		out[e.ID] = true
	}
	return out
}

func TestALostFormIsDrawnAndAnswered(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asker := seeded(t, d, t.TempDir(), nil)
	req := lostForm(t, d, asker.ID, "Which voice?")
	if !pendingIDs(t, c, 1)[req.ID] {
		t.Fatal("the lost form is not drawn")
	}
	c.call(t, 2, "elicitations/answer", map[string]any{"requestID": req.ID, "action": "decline"})
	if r := d.request(req.ID); r == nil || r.State != wire.RequestStateAnswered {
		t.Fatalf("%+v", r)
	}
	if pendingIDs(t, c, 3)[req.ID] {
		t.Fatal("an answered form is still drawn")
	}
}

func TestAnArchiveDropsALostForm(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asker := seeded(t, d, t.TempDir(), nil)
	req := lostForm(t, d, asker.ID, "Make a machine?")
	c.call(t, 1, "agents/archive", map[string]any{"agentID": asker.ID})
	if r := d.request(req.ID); r == nil || r.State != wire.RequestStateDropped {
		t.Fatalf("%+v", r)
	}
	if len(listedRequests(t, c, 2, "open")) != 0 || pendingIDs(t, c, 3)[req.ID] {
		t.Fatal("still listed")
	}
}

func TestAMessageInsteadDropsALostQuestion(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	asker := seeded(t, d, t.TempDir(), nil)
	req := lostForm(t, d, asker.ID, "Which colour?")
	left := d.live(asker.ID).dismissQuestions()
	if len(left) != 1 || left[0] != `your question "Which colour?"` {
		t.Fatalf("%q", left)
	}
	if r := d.request(req.ID); r == nil || r.State != wire.RequestStateDropped {
		t.Fatalf("%+v", r)
	}
}

// #6475: an answer that comes while another is taking the same held form, after it has left
// the live and before its request ends, is refused, not taken for a lost form's: one answer
// is recorded, and the session gets no prompt for the second.
func TestASecondAnswerMidTakeIsNotALostFormsAnswer(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	asker := seeded(t, d, t.TempDir(), nil)
	msg := "Which colour?"
	req := wire.ElicitationRequest{ID: store.NewUUID(), AgentID: asker.ID, Message: &msg, AskedAt: now(),
		Mode: wire.ElicitationRequestMode{Form: &wire.ElicitationRequestModeForm{Value: wire.ElicitationSchema{}}}}
	l := d.live(asker.ID)
	l.showForm(req)
	if l.take(req.ID) == nil { // the first answer, mid-step
		t.Fatal("the form is not held")
	}
	if lost := d.lostForms(asker.ID); len(lost) != 0 {
		t.Fatalf("a held form mid-take is listed as lost: %+v", lost)
	}
	err := d.answerElicitation(wire.AnswerElicitationRequest{RequestID: req.ID, Action: wire.AnswerElicitationRequestActionDecline})
	if err == nil {
		t.Fatal("the second answer was taken")
	}
	if r := d.request(req.ID); r == nil || r.State != wire.RequestStateOpen {
		t.Fatalf("the second answer ended the request: %+v", r)
	}
	entries, _, _ := d.Store.Transcript(asker.ID, 0)
	for _, e := range entries {
		if e.Kind.ElicitationAnswered != nil {
			t.Fatalf("the second answer was recorded: %+v", e)
		}
	}
	// The first answer ends it; after that nothing about it is held.
	d.endRequest(req.ID, wire.RequestStateAnswered, nil, "", nil)
	if d.heldForm(req.ID) {
		t.Fatal("an ended request is still held")
	}
}
