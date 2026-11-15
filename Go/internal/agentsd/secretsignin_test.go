package agentsd

import (
	"testing"
)

// #5361: step 7 of a machine's set-up asks for the key a person has in Secrets for a
// runtime. A name with Ask before use off is given with its value; one with it on is named
// and never given; a name not set is only looked for.
func TestSignInKeyGivesAKeyThatDoesNotAsk(t *testing.T) {
	t.Setenv("AGENTS_PERSONAL_HOME", t.TempDir())
	d, _ := serveWith(t, fixedPolicy{role: RoleControl, ok: true})
	const plain, flagged = "plain-5361-not-real", "flagged-5361-not-real"
	if err := d.saveSecret("OPENAI_API_KEY", plain, false); err != nil {
		t.Fatal(err)
	}
	if err := d.saveSecret("XAI_API_KEY", flagged, true); err != nil {
		t.Fatal(err)
	}

	got, err := d.signInKey("codex")
	if err != nil || got.Name == nil || *got.Name != "OPENAI_API_KEY" || got.Value == nil || *got.Value != plain {
		t.Fatalf("codex: %+v %v", got, err)
	}
	got, err = d.signInKey("grok")
	if err != nil || got.Value != nil || len(got.Refused) != 1 || got.Refused[0] != "XAI_API_KEY" {
		t.Fatalf("a flagged key was not refused by name: %+v %v", got, err)
	}
	got, err = d.signInKey("claude")
	if err != nil || got.Value != nil || len(got.Refused) != 0 || len(got.Looked) != 2 {
		t.Fatalf("claude has none set: %+v %v", got, err)
	}
	if _, err := d.signInKey("nothing"); err == nil {
		t.Fatal("an unknown runtime was answered")
	}
}
