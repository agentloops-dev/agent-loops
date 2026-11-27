package agentsd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7471: a server's Codex signs in on the server: a card with the URL and the code, answered
// from any client, and the Mac's lent sign-in closed once it is made.
func TestAServersCodexSignsInFromACard(t *testing.T) {
	fakeRuntimes(t)
	home := fakeSignInTools(t)
	setFor(t, &codexFinishWait, 200*time.Millisecond)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asAServer(t)
	id := startAgent(t, c, 1, t.TempDir(), "")
	d.SetTunneler(&fakeTunnels{})
	lend(t, d, "codex", "stand-in")

	d.raiseSignInCard(id, "codex")
	var card wire.ElicitationRequest
	for deadline := time.Now().Add(15 * time.Second); card.ID == ""; time.Sleep(20 * time.Millisecond) {
		if got := d.pendingElicitations(); len(got) > 0 {
			card = got[0]
		} else if time.Now().After(deadline) {
			t.Fatal("no card")
		}
	}
	if card.Mode.URL == nil || card.Mode.URL.Value != "https://auth.openai.com/codex/device" ||
		card.Message == nil || !strings.Contains(*card.Message, "ABCD-EFGH1") {
		t.Fatalf("%+v", card)
	}
	// Again while it is open: the same card.
	d.raiseSignInCard(id, "codex")
	time.Sleep(100 * time.Millisecond)
	if got := d.pendingElicitations(); len(got) != 1 {
		t.Fatalf("%d cards", len(got))
	}

	answer := wire.RequestsAnswer{ID: card.ID, Answer: wire.RequestAnswer{Form: &wire.RequestFormAnswer{Action: wire.AnswerElicitationRequestActionAccept}}}
	if _, err := d.answerRequest(answer, nil); err == nil || !strings.Contains(err.Error(), "still waiting") {
		t.Fatalf("Done before the code: %v", err)
	}
	if len(d.pendingElicitations()) != 1 {
		t.Fatal("the card went before the sign-in was made")
	}
	must(t, os.WriteFile(filepath.Join(home, "entered"), nil, 0o600))
	var err error
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err = d.answerRequest(answer, nil); err == nil || !strings.Contains(err.Error(), "still waiting") || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(d.pendingElicitations()) != 0 {
		t.Fatal("the card stayed")
	}
	if !ownCodexSignIn() {
		t.Fatal("not signed in")
	}
	if d.relayFor("codex") != nil {
		t.Fatal("the Mac's lent sign-in stayed open")
	}
	lend(t, d, "codex", "stand-in-2")
	if d.relayFor("codex") != nil {
		t.Fatal("a new lend opened over the server's own sign-in")
	}
	env, err := d.withRuntimeOwnEnv(runtimeByID("codex"), t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(env, "\n"), "codex-relay-home") {
		t.Fatalf("Codex starts on the lend: %q", env)
	}
}

func TestDismissingTheCodexCardStopsTheSignIn(t *testing.T) {
	fakeRuntimes(t)
	fakeSignInTools(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asAServer(t)
	id := startAgent(t, c, 1, t.TempDir(), "")
	d.raiseSignInCard(id, "codex")
	var card wire.ElicitationRequest
	for deadline := time.Now().Add(15 * time.Second); card.ID == ""; time.Sleep(20 * time.Millisecond) {
		if got := d.pendingElicitations(); len(got) > 0 {
			card = got[0]
		} else if time.Now().After(deadline) {
			t.Fatal("no card")
		}
	}
	answer := wire.RequestsAnswer{ID: card.ID, Answer: wire.RequestAnswer{Form: &wire.RequestFormAnswer{Action: wire.AnswerElicitationRequestActionCancel}}}
	if _, err := d.answerRequest(answer, nil); err != nil {
		t.Fatal(err)
	}
	d.headlessMu.Lock()
	n := len(d.headless)
	d.headlessMu.Unlock()
	if n != 0 || len(d.pendingElicitations()) != 0 {
		t.Fatalf("sign-ins %d, cards %d", n, len(d.pendingElicitations()))
	}
}
