package policylayers

import (
	"encoding/json"
	"fmt"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The commitTrailers rule (#6365): each trailer of an agent's commits is on or off, by its
// key. Each key is a rule of its own in the effective policy (commitTrailers.<key>), and
// "tighter" has one meaning for each key, by its kind:
//
//   - a record trailer (Made-by, Session, Fixes, Epic, Requested-by, Approved-by,
//     Co-authored-by): on is tighter. A folder or an organisation can make a record of agent
//     work required; a person cannot remove it.
//   - a spend trailer (Cost, Tokens, Duration): off is tighter. A folder or an organisation
//     can keep spend out of git history; a person cannot add it there.
//
// A key no layer sets has its default: every record trailer on, Cost off, Tokens and
// Duration on. A key this machine does not know is kept in the layer and has no effect.

// TrailerKind says which way a trailer's rule is tighter.
type TrailerKind int

const (
	// TrailerRecord is a record of agent work: on is tighter.
	TrailerRecord TrailerKind = iota
	// TrailerSpend is what the work cost: off is tighter.
	TrailerSpend
)

// Trailer is one key of the rule, in the order of a commit's block.
type Trailer struct {
	Key     string
	Rule    wire.PolicyRuleName
	Kind    TrailerKind
	Default bool
}

// Trailers is every key this machine knows, in the order the block writes them (the epic's,
// #6365, and policy.schema's PolicyRuleName).
var Trailers = []Trailer{
	{"Made-by", wire.PolicyRuleNameCommitTrailersMadeBy, TrailerRecord, true},
	{"Cost", wire.PolicyRuleNameCommitTrailersCost, TrailerSpend, false},
	{"Tokens", wire.PolicyRuleNameCommitTrailersTokens, TrailerSpend, true},
	{"Duration", wire.PolicyRuleNameCommitTrailersDuration, TrailerSpend, true},
	{"Session", wire.PolicyRuleNameCommitTrailersSession, TrailerRecord, true},
	{"Fixes", wire.PolicyRuleNameCommitTrailersFixes, TrailerRecord, true},
	{"Epic", wire.PolicyRuleNameCommitTrailersEpic, TrailerRecord, true},
	{"Requested-by", wire.PolicyRuleNameCommitTrailersRequestedBy, TrailerRecord, true},
	{"Approved-by", wire.PolicyRuleNameCommitTrailersApprovedBy, TrailerRecord, true},
	{"Co-authored-by", wire.PolicyRuleNameCommitTrailersCoAuthoredBy, TrailerRecord, true},
}

// trailerForRule is the trailer whose rule is name; ok is false for any other rule.
func trailerForRule(name wire.PolicyRuleName) (Trailer, bool) {
	for _, t := range Trailers {
		if t.Rule == name {
			return t, true
		}
	}
	return Trailer{}, false
}

// TrailerOrder ranks a trailer's value, tighter higher: on for a record trailer, off for a
// spend trailer.
func (t Trailer) TrailerOrder(on bool) float64 {
	if on == (t.Kind == TrailerRecord) {
		return 1
	}
	return 0
}

// own is what one layer sets for the trailer; ok is false when it sets nothing, or a value
// Validate would refuse.
func (t Trailer) own(r wire.PolicyRules) (bool, bool) {
	switch r.CommitTrailers[t.Key] {
	case wire.CommitTrailerValueOn:
		return true, true
	case wire.CommitTrailerValueOff:
		return false, true
	}
	return false, false
}

// commitTrailerRules resolves each trailer's rule, in the block's order.
func commitTrailerRules(layers []Layer) []wire.EffectiveRule {
	out := make([]wire.EffectiveRule, 0, len(Trailers))
	for _, t := range Trailers {
		out = append(out, scalar(t.Rule, layers, t.own, t.TrailerOrder, boolWords))
	}
	return out
}

// TrailersOn is each trailer's value in force in eff (Resolve's answer), by its key: the
// tightest value a layer sets, else its default.
func TrailersOn(eff wire.EffectivePolicy) map[string]bool {
	out := make(map[string]bool, len(Trailers))
	for _, t := range Trailers {
		out[t.Key] = t.Default
	}
	for _, r := range eff.Rules {
		t, ok := trailerForRule(r.Rule)
		if !ok || r.Decides == nil {
			continue
		}
		var on bool
		if json.Unmarshal(r.Value, &on) == nil {
			out[t.Key] = on
		}
	}
	return out
}

// trailerCapPhrase is a capped trailer's line after the layer's name: "Made-by is required",
// "Tokens is kept out of commits".
func trailerCapPhrase(t Trailer) string {
	if t.Kind == TrailerRecord {
		return t.Key + " is required"
	}
	return t.Key + " is kept out of commits"
}

// trailerLayerName is how a trailer's cap line names a layer (the wireframes of #6365):
// "This folder (agent-loops)" for a folder, as LayerName for the others.
func trailerLayerName(layer wire.PolicyLayerRef) string {
	if layer.Kind == "folder" {
		return "This folder (" + layer.Name + ")"
	}
	return LayerName(layer)
}

// trailerCapBinds is whether a layer above that sets the trailer to on (words "On") holds a
// layer below to it: only the tight value does. A record trailer an organisation sets off
// leaves a person free to turn it on, so it gives no cap.
func trailerCapBinds(t Trailer, words string) bool {
	return t.TrailerOrder(words == "On") == 1
}

// looserCommitTrailers is every trailer whose rule next sets looser than approved: a tight
// value removed, or changed to the loose one.
func looserCommitTrailers(approved, next wire.PolicyRules) []wire.PolicyRuleName {
	var out []wire.PolicyRuleName
	for _, t := range Trailers {
		old, ok := t.own(approved)
		if !ok || t.TrailerOrder(old) == 0 {
			continue // removing the loose value lifts nothing
		}
		nv, ok := t.own(next)
		if !ok || t.TrailerOrder(nv) < t.TrailerOrder(old) {
			out = append(out, t.Rule)
		}
	}
	return out
}

// validateCommitTrailers checks each known key's value. A key this machine does not know is
// kept and not checked, so a newer layer still loads.
func validateCommitTrailers(m wire.CommitTrailers) error {
	for _, t := range Trailers {
		v, ok := m[t.Key]
		if !ok {
			continue
		}
		if v != wire.CommitTrailerValueOn && v != wire.CommitTrailerValueOff {
			return fmt.Errorf("policylayers: unknown commitTrailers.%s %q", t.Key, v)
		}
	}
	for k := range m {
		if k == "" {
			return fmt.Errorf("policylayers: commitTrailers has an empty name")
		}
	}
	return nil
}
