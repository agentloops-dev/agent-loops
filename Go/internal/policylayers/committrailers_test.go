package policylayers

import (
	"slices"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func trailers(kv ...string) wire.PolicyRules {
	m := wire.CommitTrailers{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = wire.CommitTrailerValue(kv[i+1])
	}
	return wire.PolicyRules{CommitTrailers: m}
}

// With no layer setting a trailer, each has its default: every record trailer on, Cost off,
// Tokens and Duration on (#6365).
func TestCommitTrailersDefaults(t *testing.T) {
	on := TrailersOn(Resolve([]Layer{{Ref: orgRef}, {Ref: youRef}, {Ref: folderRef}}))
	for _, t0 := range Trailers {
		if want := t0.Key != "Cost"; on[t0.Key] != want {
			t.Errorf("%s: %v, not %v", t0.Key, on[t0.Key], want)
		}
	}
	if len(on) != 10 {
		t.Fatalf("%d keys", len(on))
	}
}

// For a record trailer On is tighter, so a folder's On beats your Off; for a spend trailer Off
// is tighter, so your Off beats a folder's On. Your looser value is kept, not used.
func TestCommitTrailersTighterByKind(t *testing.T) {
	you := Layer{Ref: youRef, Rules: trailers("Made-by", "off", "Tokens", "off", "Cost", "on")}
	folder := Layer{Ref: folderRef, Rules: trailers("Made-by", "on", "Tokens", "on")}
	eff := Resolve([]Layer{{Ref: orgRef}, you, folder})
	on := TrailersOn(eff)
	if !on["Made-by"] || on["Tokens"] || !on["Cost"] {
		t.Fatalf("%v", on)
	}
	made := ruleOf(eff, wire.PolicyRuleNameCommitTrailersMadeBy)
	if made.Decides == nil || made.Decides.Kind != "folder" || !made.Layers[1].Kept || made.Words != "On" {
		t.Fatalf("Made-by: %+v", made)
	}
	tokens := ruleOf(eff, wire.PolicyRuleNameCommitTrailersTokens)
	if tokens.Decides == nil || tokens.Decides.Kind != "you" || !tokens.Layers[2].Kept {
		t.Fatalf("Tokens: %+v", tokens)
	}
	// A key this machine does not know is kept in the layer and has no effect: also Attested,
	// a key until #6545, in a layer written before it.
	for _, key := range []string{"Signed-off-by", "Attested"} {
		unknown := Resolve([]Layer{{Ref: youRef, Rules: trailers(key, "on")}})
		if got := TrailersOn(unknown); len(got) != 10 || got[key] {
			t.Fatalf("%s: %v", key, got)
		}
	}
}

// A cap binds only at the tight value, and its line says which way: "This folder (<name>):
// Made-by is required", "Acme: Tokens is kept out of commits". A record trailer a layer above
// sets Off, or a spend trailer it sets On, is no cap.
func TestCommitTrailersCapLines(t *testing.T) {
	org := Layer{Ref: orgRef, Rules: trailers("Tokens", "off", "Session", "off")}
	folder := Layer{Ref: folderRef, Rules: trailers("Made-by", "on", "Duration", "on")}
	you := Layer{Ref: youRef, Rules: trailers("Made-by", "off")}
	eff := WithCaps(Resolve([]Layer{org, folder, you}), []Layer{org, folder})
	for rule, want := range map[wire.PolicyRuleName]string{
		wire.PolicyRuleNameCommitTrailersMadeBy: "This folder (payments-api): Made-by is required",
		wire.PolicyRuleNameCommitTrailersTokens: "Acme: Tokens is kept out of commits",
	} {
		if r := ruleOf(eff, rule); r.CapLine == nil || *r.CapLine != want {
			t.Errorf("%s: %v, not %q", rule, r.CapLine, want)
		}
	}
	for _, rule := range []wire.PolicyRuleName{wire.PolicyRuleNameCommitTrailersSession, wire.PolicyRuleNameCommitTrailersDuration} {
		if r := ruleOf(eff, rule); r.CapLine != nil || r.Cap != nil {
			t.Errorf("%s capped at its loose value: %s", rule, *r.CapLine)
		}
	}
}

// A change is looser when it removes a tight value or moves it to the loose one.
func TestCommitTrailersLooser(t *testing.T) {
	approved := trailers("Made-by", "on", "Cost", "off", "Session", "off")
	for _, c := range []struct {
		next wire.PolicyRules
		want []wire.PolicyRuleName
	}{
		{trailers("Made-by", "on", "Cost", "off"), nil},
		{trailers("Made-by", "off", "Cost", "off"), []wire.PolicyRuleName{wire.PolicyRuleNameCommitTrailersMadeBy}},
		{trailers("Made-by", "on"), []wire.PolicyRuleName{wire.PolicyRuleNameCommitTrailersCost}},
		{trailers("Made-by", "on", "Cost", "on", "Session", "on"), []wire.PolicyRuleName{wire.PolicyRuleNameCommitTrailersCost}},
	} {
		if got := LooserRules(approved, c.next); !slices.Equal(got, c.want) {
			t.Errorf("%v: %v, not %v", c.next.CommitTrailers, got, c.want)
		}
	}
}

// Validate takes on and off for a known key, keeps an unknown key, and refuses another value.
func TestCommitTrailersValidate(t *testing.T) {
	if _, err := Validate([]byte(`{"commitTrailers":{"Cost":"on","Signed-off-by":"maybe"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate([]byte(`{"commitTrailers":{"Cost":"yes"}}`)); err == nil {
		t.Fatal("an unknown value for Cost was taken")
	}
	if RuleTitle(wire.PolicyRuleNameCommitTrailersCoAuthoredBy) != "Co-authored-by" {
		t.Fatal(RuleTitle(wire.PolicyRuleNameCommitTrailersCoAuthoredBy))
	}
}
