package agentsd

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
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
	card := theCard(t, d)
	if card.Mode.URL == nil || card.Mode.URL.Value != "https://auth.openai.com/codex/device" ||
		card.Message == nil || !strings.Contains(*card.Message, "ABCD-EFGH1") {
		t.Fatalf("%+v", card)
	}
	// Again while it is open: the same card.
	d.raiseSignInCard(id, "codex") // returns at once: a card is open
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
	waitFor(t, "signed in", func() bool {
		_, err = d.answerRequest(answer, nil)
		return err == nil || !strings.Contains(err.Error(), "still waiting")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.pendingElicitations()) != 0 {
		t.Fatal("the card stayed")
	}
	if !d.ownCodexSignIn() {
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
	card := theCard(t, d)
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

func TestExpiredCodexCardIsReplacedWithoutAnotherTurn(t *testing.T) {
	fakeRuntimes(t)
	fakeSignInTools(t)
	setFor(t, &headlessSignInTime, 800*time.Millisecond)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asAServer(t)
	id := startAgent(t, c, 1, t.TempDir(), "")
	d.raiseSignInCard(id, "codex")
	first := theCard(t, d)

	var replacement wire.ElicitationRequest
	waitFor(t, "a replacement device-code card", func() bool {
		for _, card := range d.pendingElicitations() {
			if card.ID != first.ID {
				replacement = card
				return true
			}
		}
		return false
	})
	if replacement.Mode.URL == nil || replacement.Mode.URL.Value != first.Mode.URL.Value {
		t.Fatalf("replacement card %+v, first card %+v", replacement, first)
	}
	for _, card := range d.pendingElicitations() {
		if card.ID == first.ID {
			t.Fatal("expired card stayed open")
		}
	}
}

// theCard waits for the one card the daemon shows.
func theCard(t *testing.T, d *Daemon) wire.ElicitationRequest {
	t.Helper()
	var card wire.ElicitationRequest
	waitFor(t, "shown the card", func() bool {
		got := d.pendingElicitations()
		if len(got) > 0 {
			card = got[0]
		}
		return len(got) > 0
	})
	return card
}

// #7506: a server that still uses the Mac's Claude shows a card with the link and a field for
// the code; the code signs it in only after one check turn passes, and then the lend closes.
func TestAServersClaudeCardChecksBeforeTheLendCloses(t *testing.T) {
	fakeRuntimes(t)
	home := fakeSignInTools(t)
	root := t.TempDir()
	d, c := start(t, root)
	defer d.Shutdown()
	asAServer(t)
	id := startAgent(t, c, 1, t.TempDir(), "")
	d.SetTunneler(&fakeTunnels{})

	d.raiseSignInCard(id, "claude")
	if len(d.pendingElicitations()) != 0 {
		t.Fatal("a card for a Claude with no lend")
	}
	lend(t, d, "claude", "agents-relay-claude")
	d.raiseSignInCard(id, "claude")
	card := theCard(t, d)
	if card.Mode.Form == nil || len(card.Mode.Form.Value.Properties) != 1 || card.Message == nil ||
		!strings.Contains(*card.Message, "https://claude.ai/oauth/authorize") {
		t.Fatalf("%+v", card)
	}
	answer := func(code string) error {
		_, err := d.answerRequest(wire.RequestsAnswer{ID: card.ID, Answer: wire.RequestAnswer{Form: &wire.RequestFormAnswer{
			Action: wire.AnswerElicitationRequestActionAccept, Content: map[string]wire.JSONValue{signInCodeField: wire.JSONValue(`"` + code + `"`)}}}}, nil)
		return err
	}
	// The check turn fails: nothing is kept, the lend stays.
	setFor(t, &claudeCheckTurn, func(context.Context, []string) error { return errors.New("401 Invalid bearer token") })
	err := answer("good")
	if err == nil || !strings.Contains(err.Error(), "check turn") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a failed check: %v", err)
	}
	if d.storedClaudeSignIn() != "" || d.relayFor("claude") == nil {
		t.Fatal("a failed check closed the lend or kept the sign-in")
	}
	// It passes, with the server's own token alone.
	var env []string
	setFor(t, &claudeCheckTurn, func(_ context.Context, e []string) error { env = e; return nil })
	// The failed code is used up: the next turn shows a new card in the old one's place.
	d.raiseSignInCard(id, "claude")
	waitFor(t, "a new card", func() bool {
		got := d.pendingElicitations()
		if len(got) == 1 && got[0].ID != card.ID {
			card = got[0]
			return true
		}
		return false
	})
	if err := answer("good"); err != nil {
		t.Fatal(err)
	}
	if !hasVariable(env, "CLAUDE_CODE_OAUTH_TOKEN") || hasVariable(env, "ANTHROPIC_BASE_URL") {
		t.Fatalf("the check turn: %q", env)
	}
	if d.storedClaudeSignIn() == "" || d.relayFor("claude") != nil || len(d.pendingElicitations()) != 0 {
		t.Fatal("not signed in on the server after a passing check")
	}
	assertNoClaudeSignInLeak(t, []string{root, home}, d.claudeSignInFile())
}

func assertNoClaudeSignInLeak(t *testing.T, roots []string, signInFile string) {
	t.Helper()
	for _, dir := range roots {
		files, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || path == signInFile || strings.HasSuffix(path, ".local/bin/claude") {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			if b, err := files.ReadFile(rel); err == nil && (bytes.Contains(b, []byte("sk-ant-oat01-abc_DEF-123")) || bytes.Contains(b, []byte(`"good"`))) {
				t.Errorf("the token or the code is in %s", path)
			}
			return nil
		})
		if err := files.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
