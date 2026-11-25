package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The organisation's policy (#998, spec 998, slice 1): one document, kept at
// v1/policy.json and signed by the control key in v1/policy.sig, so every copy can send it
// with no key. An admin changes it with policy/set, policy/stopAll and policy/raise; each
// change is one version higher, is audited, is kept in v1/policy/versions/ as it was, and
// goes to every machine that says host.policy/1 on its channel 0, on every copy. A machine
// enforces it (slice 2a); here it is only kept, signed and sent.

// CapHostPolicy is what a machine says when it keeps and follows the policy. A machine
// that does not say it is sent nothing.
const CapHostPolicy = wire.CapHostPolicy1

const (
	// store-bounds: kind=key-value; count=1; size=64KiB a record, 96KiB with the kill switch; age=until-deleted; applied=on write
	policyKey = "v1/policy.json"
	// store-bounds: kind=key-value; count=1; size=1KiB a record (a signature); age=until-deleted; applied=on write
	policySigKey = "v1/policy.sig"
	// store-bounds: kind=document; count=100; size=96KiB a record; age=90d; applied=on write (not kept) and hourly sweep, from when a version was replaced
	policyVersionsKey = "v1/policy/versions/"
	// mostPolicyBytes, mostPolicyItems and the rest bound the document (data-model.md),
	// so a slip of the keyboard is refused.
	mostPolicyBytes = 64 << 10
	// mostPolicyVersion is the largest version kept: mostPolicyBytes, and room for the kill
	// switch, whose note and two actors are short. A policy from before the limit can be over it.
	mostPolicyVersion  = 96 << 10
	mostPolicyItems    = 200
	mostPolicyName     = 200
	mostPolicyNote     = 500
	mostPolicyDailyUSD = 1_000_000
	mostPolicyTokens   = 1_000_000_000_000
	mostPolicyHelpers  = 1000
	policyVersionsKept = 100
	// policyVersionAge is how long a version is kept after the next one replaced it (#6765).
	policyVersionAge    = 90 * 24 * time.Hour
	policyRetries       = 5
	policyAppliedField  = "policyVersion"
	policyDeliveryPause = 200 * time.Millisecond
)

// policyGated are the calls the control plane refuses on a machine that does not follow
// the policy, when it says requireFollowing: those that start work.
var policyGated = methodSet(
	"agents/branchFrom",
	"agents/continueInFolder",
	"agents/fork",
	"agents/prompt",
	"agents/sendNow",
	"agents/start",
)

// permissionModeNames are permissionMode's values (#2957), Auto's four levels.
var permissionModeNames = []string{"plan", "ask", "approveForMe", "bypassPermissions"}

// permissionRequestsNames are permissionRequests' values (#2957).
var permissionRequestsNames = []string{"default", "alwaysApprove"}

// sandboxNames are sandbox's values (#2957): required means on, runtime refuses only off.
var sandboxNames = []string{"required", "runtime"}

// orgModeCeilingMap is research R5's map: each of the old modeCeiling's five values onto
// Auto's four permission levels, the same or a tighter one, so the one-time migration never
// loosens a stored policy by this reading.
var orgModeCeilingMap = map[string]wire.PermissionLevel{
	"ask":               wire.PermissionLevelPlan,
	"default":           wire.PermissionLevelAsk,
	"acceptEdits":       wire.PermissionLevelApproveForMe,
	"auto":              wire.PermissionLevelApproveForMe,
	"bypassPermissions": wire.PermissionLevelBypassPermissions,
}

// orgModeCeilingToPermissionLevel reads research R5's map once, for the migration of a
// stored policy that still has the old modeCeiling field (migrateModeCeiling).
func orgModeCeilingToPermissionLevel(ceiling string) wire.PermissionLevel {
	if l, ok := orgModeCeilingMap[ceiling]; ok {
		return l
	}
	return wire.PermissionLevelBypassPermissions // an unknown ceiling counts as the loosest (R6)
}

// policyBook is what this copy knows of the policy: its version and requireFollowing, for
// the router's gate, and what each machine here was last sent, by a hash. sending is a
// lock for each machine with a send in progress, so the sends to one machine are in
// order (#5069).
type policyBook struct {
	mu               sync.Mutex
	version          int64
	requireFollowing bool
	told             map[string][sha256.Size]byte
	sending          keyLocks
}

// lockSend waits for host's earlier sends to finish. Call the function it returns to let
// the next one go.
func (b *policyBook) lockSend(host string) func() { return b.sending.lock(host) }

// keptPolicy is the policy as the store has it: the document's bytes, exactly as signed,
// the document, its tag and its signature.
type keptPolicy struct {
	raw       []byte
	policy    *wire.OrgPolicy
	rev       string
	signature *wire.PolicySignature
}

// signedPolicyLine is machine/policy with the document's own bytes, so a machine checks the
// signature over what was signed, a field this build does not know included.
type signedPolicyLine struct {
	Policy    json.RawMessage      `json:"policy"`
	Signature wire.PolicySignature `json:"signature"`
}

func (m *Methods) policyMethod(ctx context.Context, method string, params json.RawMessage, from Caller) (any, error) {
	user := m.callerPerson(from)
	if method == "policy/mine" {
		kept, err := m.s.readPolicy(ctx)
		if err != nil {
			return nil, err
		}
		return policyViewFor(kept.policy, user), nil
	}
	if !m.s.isAdmin(user) {
		return nil, fail(notPermitted, "Only an admin can see or change the organisation's policy.")
	}
	actor := m.s.actorFor(from.Client)
	switch method {
	case "policy/get":
		kept, err := m.s.readPolicy(ctx)
		if err != nil {
			return nil, err
		}
		return m.s.policyKept(kept), nil
	case "policy/set":
		return m.policySet(ctx, params, actor)
	case "policy/stopAll":
		return m.policyStopAll(ctx, params, actor)
	case "policy/raise":
		return m.policyRaise(ctx, params, actor)
	}
	return nil, notFound(method)
}

// policySet is policy/set: the admin's document, kept with the kill switch and the
// raises as they are.
func (m *Methods) policySet(ctx context.Context, params json.RawMessage, actor wire.AuditActor) (any, error) {
	request, err := require[wire.PolicySet](params, "PolicySet")
	if err != nil {
		return nil, err
	}
	if err := checkPolicyRawEnums(params); err != nil {
		return nil, err
	}
	if err := checkPolicy(request.Policy); err != nil {
		return nil, err
	}
	rev := deref(request.Rev)
	return m.s.changePolicy(ctx, actor, &rev, func(now time.Time, current *wire.OrgPolicy) (wire.OrgPolicy, error) {
		next := request.Policy
		// The kill switch and the raises change only by their own methods.
		next.StopAll = nil
		var raises []wire.BudgetRaise
		if current != nil {
			next.StopAll = current.StopAll
			if current.Budgets != nil {
				raises = current.Budgets.Raises
			}
		}
		if next.Budgets != nil {
			next.Budgets.Raises = raises
		}
		return next, nil
	})
}

// policyStopAll is policy/stopAll: the kill switch on or off, with the admin's note.
func (m *Methods) policyStopAll(ctx context.Context, params json.RawMessage, actor wire.AuditActor) (any, error) {
	request, err := require[wire.PolicyStopAllRequest](params, "PolicyStopAllRequest")
	if err != nil {
		return nil, err
	}
	note := strings.TrimSpace(deref(request.Note))
	if utf8.RuneCountInString(note) > mostPolicyNote {
		return nil, fail(invalidParams, "Keep the note to %d characters.", mostPolicyNote)
	}
	return m.s.changePolicy(ctx, actor, nil, func(now time.Time, current *wire.OrgPolicy) (wire.OrgPolicy, error) {
		next := wire.OrgPolicy{}
		if current != nil {
			next = *current
		}
		next.StopAll = &wire.PolicyStopAll{On: request.On, By: actor, At: wire.DateOf(now)}
		if note != "" {
			next.StopAll.Note = &note
		}
		return next, nil
	})
}

// policyRaise is policy/raise: a person's budget raised for today, which answers their Ask
// for More.
func (m *Methods) policyRaise(ctx context.Context, params json.RawMessage, actor wire.AuditActor) (any, error) {
	request, err := require[wire.PolicyRaiseRequest](params, "PolicyRaiseRequest")
	if err != nil {
		return nil, err
	}
	if p := m.records.Person(request.User); p == nil || p.IsRemoved() {
		return nil, fail(invalidParams, "There is no such person.")
	}
	if request.Daily == nil && request.DailyTokens == nil {
		return nil, fail(invalidParams, "Say how much to raise the budget to: dollars, tokens or both.")
	}
	if err := checkBudget(request.Daily, request.DailyTokens); err != nil {
		return nil, err
	}
	var raised wire.BudgetRaise
	kept, err := m.s.changePolicy(ctx, actor, nil, func(now time.Time, current *wire.OrgPolicy) (wire.OrgPolicy, error) {
		if current == nil || current.Budgets == nil {
			return wire.OrgPolicy{}, fail(invalidParams, "The policy sets no budget to raise.")
		}
		next := *current
		budgets := *current.Budgets
		day := now.UTC().Format(time.DateOnly)
		raise := wire.BudgetRaise{User: request.User, Day: day, Daily: request.Daily, DailyTokens: request.DailyTokens,
			By: actor, Request: request.Request}
		// One raise a person a day: the newest.
		budgets.Raises = append(slices.DeleteFunc(slices.Clone(budgets.Raises), func(r wire.BudgetRaise) bool {
			return r.User == request.User && r.Day == day
		}), raise)
		next.Budgets = &budgets
		raised = raise
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	// The person's Ask for More of today, if they asked (#3262), is answered by it.
	m.s.raisedFor(ctx, actor, raised, time.Now())
	return kept, nil
}

// policyViewFor is the policy as a person sees it: every rule, and only their own raises.
func policyViewFor(policy *wire.OrgPolicy, user string) wire.PolicyView {
	if policy == nil {
		return wire.PolicyView{}
	}
	view := *policy
	if policy.Budgets != nil {
		budgets := *policy.Budgets
		budgets.Raises = []wire.BudgetRaise{}
		for _, r := range policy.Budgets.Raises {
			if r.User == user {
				budgets.Raises = append(budgets.Raises, r)
			}
		}
		view.Budgets = &budgets
	}
	return wire.PolicyView{Policy: &view}
}

// policyKept is policy/get's answer: the document, its tag and each machine's version.
func (s *Service) policyKept(kept keptPolicy) wire.PolicyKept {
	out := wire.PolicyKept{Policy: kept.policy, Machines: map[string]int64{}}
	if kept.rev != "" {
		out.Rev = &kept.rev
	}
	for _, h := range s.records.Hosts() {
		if v, ok := appliedPolicy(h); ok {
			out.Machines[h.ID] = v
		}
	}
	return out
}

// MARK: The bounds

var (
	passThroughName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\*?$`)
	policyID        = regexp.MustCompile(`^[^\s*]+\*?$`)
)

// checkPolicyRawEnums refuses an unrecognised permissionMode or permissionRequests before
// require's decode ever reaches them: PermissionLevel and ClientPermissionMode have an
// x-fallback (#2658, as policylayers.Validate's own comment explains), so their generated
// UnmarshalJSON would otherwise silently replace an unknown string with the fallback before
// checkPolicy ever saw it. params is policy/set's own raw request.
func checkPolicyRawEnums(params json.RawMessage) error {
	var wrapper struct {
		Policy struct {
			PermissionMode     *string `json:"permissionMode"`
			PermissionRequests *string `json:"permissionRequests"`
		} `json:"policy"`
	}
	if json.Unmarshal(params, &wrapper) != nil {
		return nil // a badly shaped request fails require's own decode instead
	}
	if wrapper.Policy.PermissionMode != nil && !slices.Contains(permissionModeNames, *wrapper.Policy.PermissionMode) {
		return fail(invalidParams, "The permission mode is one of %s.", strings.Join(permissionModeNames, ", "))
	}
	if wrapper.Policy.PermissionRequests != nil && !slices.Contains(permissionRequestsNames, *wrapper.Policy.PermissionRequests) {
		return fail(invalidParams, "Permission requests are one of %s.", strings.Join(permissionRequestsNames, ", "))
	}
	return nil
}

// checkPolicy refuses a document out of bounds, in words for the admin.
func checkPolicy(p wire.OrgPolicy) error {
	if err := checkPolicyLayers(p); err != nil {
		return err
	}
	if err := checkPolicyRuntimes(p); err != nil {
		return err
	}
	if n := p.Notices; n != nil && n.AntigravityTerms != nil && !slices.Contains(antigravityTerms, *n.AntigravityTerms) {
		return fail(invalidParams, "Antigravity's terms are %s.", strings.Join(antigravityTerms, ", "))
	}
	if p.Marketplaces != nil {
		if err := checkNames("marketplaces", p.Marketplaces.Allowed, false); err != nil {
			return err
		}
		if c := p.Marketplaces.CustomServers; c != "allow" && c != "refuse" {
			return fail(invalidParams, "Servers added by hand are allow or refuse.")
		}
	}
	if b := p.Budgets; b != nil {
		if err := checkPolicyBudgets(*b); err != nil {
			return err
		}
	}
	if h := p.Helpers; h != nil {
		for _, n := range []*int64{h.MostRunning, h.MostNotArchived, h.MostQueued} {
			if n != nil && (*n < 0 || *n > mostPolicyHelpers) {
				return fail(invalidParams, "A helper limit is from 0 to %d.", mostPolicyHelpers)
			}
		}
	}
	if e := p.Environment; e != nil {
		return checkPassThrough(e.PassThrough)
	}
	return nil
}

// checkPolicyLayers bounds the rules of #2957: the permission mode and requests, the
// sandbox, the most workflows on and the notice limits.
func checkPolicyLayers(p wire.OrgPolicy) error {
	if p.PermissionMode != nil && !slices.Contains(permissionModeNames, string(*p.PermissionMode)) {
		return fail(invalidParams, "The permission mode is one of %s.", strings.Join(permissionModeNames, ", "))
	}
	if p.PermissionRequests != nil && !slices.Contains(permissionRequestsNames, string(*p.PermissionRequests)) {
		return fail(invalidParams, "Permission requests are one of %s.", strings.Join(permissionRequestsNames, ", "))
	}
	if p.Sandbox != nil && !slices.Contains(sandboxNames, *p.Sandbox) {
		return fail(invalidParams, "The sandbox rule is one of %s, or not set.", strings.Join(sandboxNames, ", "))
	}
	if w := p.Workflows; w != nil && w.MostOn != nil && (*w.MostOn < 1 || *w.MostOn > 100) {
		return fail(invalidParams, "The most workflows on at once is 1 to 100.")
	}
	if n := p.NoticeLimits; n != nil {
		if n.Burst != nil && (*n.Burst < 1 || *n.Burst > 20) {
			return fail(invalidParams, "The notice burst is 1 to 20.")
		}
		if n.WaitMinutes != nil && (*n.WaitMinutes < 1 || *n.WaitMinutes > 1440) {
			return fail(invalidParams, "The notice wait is 1 to 1440 minutes.")
		}
	}
	return nil
}

// checkPolicyRuntimes bounds the runtimes' rules: how many, their ids, their models and
// their ways to the model.
func checkPolicyRuntimes(p wire.OrgPolicy) error {
	if len(p.Runtimes) > mostPolicyItems {
		return fail(invalidParams, "Allow at most %d runtimes.", mostPolicyItems)
	}
	for id, rule := range p.Runtimes {
		if !goodName(id, false) {
			return fail(invalidParams, "%q is not a runtime id.", id)
		}
		if err := checkNames("models of "+id, rule.Models, true); err != nil {
			return err
		}
		if err := checkReach(id, rule.Reach); err != nil {
			return err
		}
	}
	return nil
}

// checkPolicyBudgets bounds the budgets: a day's and an agent's, and the unmetered
// runtimes.
func checkPolicyBudgets(b wire.PolicyBudgets) error {
	if err := checkBudget(b.Daily, b.DailyTokens); err != nil {
		return err
	}
	if err := checkBudget(b.PerAgent, b.PerAgentTokens); err != nil {
		return err
	}
	return checkNames("unmetered runtimes", b.AllowUnmetered, false)
}

// checkPassThrough bounds the environment variables a machine passes through to agents.
func checkPassThrough(names []string) error {
	if len(names) > mostPolicyItems {
		return fail(invalidParams, "Name at most %d variables.", mostPolicyItems)
	}
	for _, name := range names {
		if len(name) > mostPolicyName || !passThroughName.MatchString(name) {
			return fail(invalidParams, "%q is not a variable name (letters, digits and _, with * at the end allowed).", name)
		}
	}
	return nil
}

// antigravityTerms are notices.antigravityTerms' values (#999, spec D10).
var antigravityTerms = []string{"show", "accepted", "refuse"}

// checkReach bounds runtimes.<id>.reach (#999, FR-008): each way once, at least one.
func checkReach(id string, reach []wire.PolicyReach) error {
	if reach == nil {
		return nil
	}
	if len(reach) == 0 {
		return fail(invalidParams, "Allow %s at least one way to the model, or leave its ways out.", id)
	}
	seen := map[wire.PolicyReach]bool{}
	for _, way := range reach {
		switch way {
		case wire.PolicyReachSignIn, wire.PolicyReachOrgKey, wire.PolicyReachGateway:
		default:
			return fail(invalidParams, "%q is not a way to the model: signIn, orgKey or gateway.", way)
		}
		if seen[way] {
			return fail(invalidParams, "%s names %s twice.", id, way)
		}
		seen[way] = true
	}
	return nil
}

func checkBudget(usd *float64, tokens *int64) error {
	if usd != nil && (math.IsNaN(*usd) || *usd < 0 || *usd > mostPolicyDailyUSD) {
		return fail(invalidParams, "A budget is from $0 to $%d a day.", mostPolicyDailyUSD)
	}
	if tokens != nil && (*tokens < 0 || *tokens > mostPolicyTokens) {
		return fail(invalidParams, "A token budget is from 0 to %d a day.", int64(mostPolicyTokens))
	}
	return nil
}

func checkNames(what string, names []string, star bool) error {
	if len(names) > mostPolicyItems {
		return fail(invalidParams, "Name at most %d %s.", mostPolicyItems, what)
	}
	for _, n := range names {
		if !goodName(n, star) {
			return fail(invalidParams, "%q is not one of the %s.", n, what)
		}
	}
	return nil
}

// goodName is an id: no spaces, and with star a * at the end only.
func goodName(s string, star bool) bool {
	if s == "" || len(s) > mostPolicyName || !policyID.MatchString(s) {
		return false
	}
	return star || !strings.HasSuffix(s, "*")
}

// MARK: The store

// readPolicy is the policy as the store has it, or nothing kept when none was ever set.
func (s *Service) readPolicy(ctx context.Context) (keptPolicy, error) {
	object, err := s.cfg.Store.Get(ctx, policyKey)
	if err != nil {
		return keptPolicy{}, fail(storeUnavailable, "The policy could not be read: %v", err)
	}
	if object == nil {
		return keptPolicy{}, nil
	}
	var policy wire.OrgPolicy
	if err := json.Unmarshal(object.Data, &policy); err != nil {
		return keptPolicy{}, fmt.Errorf("%s: %w", policyKey, err)
	}
	kept := keptPolicy{raw: object.Data, policy: &policy, rev: object.ETag}
	if sig, err := s.cfg.Store.Get(ctx, policySigKey); err == nil && sig != nil {
		var signature wire.PolicySignature
		if json.Unmarshal(sig.Data, &signature) == nil {
			kept.signature = &signature
		}
	}
	return s.migrateModeCeiling(ctx, kept)
}

// migrateModeCeiling reads a stored policy's old modeCeiling field, when there is one and
// permissionMode is not already set, into permissionMode (research R5), signs a new version,
// and keeps it (#2957, T035, data-model.md "modeCeiling goes"). A limit that only vanished
// would make the organisation's rules looser with no word said (FR-012's reasoning, applied
// to this layer too), so the migration runs once, here, before anything else reads the
// policy: encodePolicy never writes modeCeiling again, so a later read finds nothing to
// migrate and returns at once.
func (s *Service) migrateModeCeiling(ctx context.Context, kept keptPolicy) (keptPolicy, error) {
	if kept.policy == nil || kept.policy.PermissionMode != nil {
		return kept, nil
	}
	var legacy struct {
		ModeCeiling *string `json:"modeCeiling"`
	}
	if json.Unmarshal(kept.raw, &legacy) != nil || legacy.ModeCeiling == nil {
		return kept, nil
	}
	level := orgModeCeilingToPermissionLevel(*legacy.ModeCeiling)
	next := *kept.policy
	next.PermissionMode = &level
	next.Version++
	next.Issued = wire.DateOf(time.Now().UTC())
	raw, err := encodePolicy(next)
	if err != nil {
		return kept, err
	}
	tag, err := s.cfg.Store.Put(ctx, policyKey, raw, store.Matching(kept.rev))
	if err != nil {
		// Another copy is migrating it, or already has: not fatal, this read just keeps
		// the old shape once more; the next one finds the migrated document.
		return kept, nil
	}
	if err := s.signPolicy(ctx, next.Version, raw); err != nil {
		return kept, err
	}
	s.keepPolicyVersion(ctx, next.Version, raw)
	s.log(fmt.Sprintf("the policy's modeCeiling was read once into permissionMode; now version %d", next.Version))
	return keptPolicy{raw: raw, policy: &next, rev: tag}, nil
}

// changePolicy keeps the next policy, one version higher and signed, then has every copy
// send it. rev is the tag the admin read (policy/set), "" for none; nil changes whatever
// is there now (the kill switch, a raise), again if another admin saved first.
func (s *Service) changePolicy(ctx context.Context, actor wire.AuditActor, rev *string,
	change func(now time.Time, current *wire.OrgPolicy) (wire.OrgPolicy, error)) (wire.PolicyKept, error) {
	for try := 0; ; try++ {
		kept, err := s.readPolicy(ctx)
		if err != nil {
			return wire.PolicyKept{}, err
		}
		if rev != nil && *rev != kept.rev {
			return wire.PolicyKept{}, fail(changedElsewhere, "Another admin changed the policy. Load it again, then save.")
		}
		now := time.Now().UTC()
		next, raw, err := nextPolicy(kept, now, actor, change)
		if err != nil {
			return wire.PolicyKept{}, err
		}
		when := store.Absent
		if kept.rev != "" {
			when = store.Matching(kept.rev)
		}
		tag, err := s.cfg.Store.Put(ctx, policyKey, raw, when)
		if store.IsConflict(err) {
			if rev != nil || try >= policyRetries {
				return wire.PolicyKept{}, fail(changedElsewhere, "Another admin changed the policy. Load it again, then save.")
			}
			continue
		}
		if err != nil {
			return wire.PolicyKept{}, fail(storeUnavailable, "The policy could not be kept: %v", err)
		}
		return s.policyChangeKept(ctx, actor, kept, next, raw, tag)
	}
}

// nextPolicy is the change made to the kept policy, one version higher and stamped, and
// its canonical JSON, which must fit the bound. One that is over it already can be changed
// to one no larger (#6772). The kill switch is never refused for its size: it is short, and
// nothing else changes with it.
func nextPolicy(kept keptPolicy, now time.Time, actor wire.AuditActor,
	change func(now time.Time, current *wire.OrgPolicy) (wire.OrgPolicy, error)) (wire.OrgPolicy, []byte, error) {
	next, err := change(now, kept.policy)
	if err != nil {
		return wire.OrgPolicy{}, nil, err
	}
	next.Version = 1
	var stopped *wire.PolicyStopAll
	if kept.policy != nil {
		stopped = kept.policy.StopAll
	}
	// policy/set keeps the kill switch it read, so only policy/stopAll gives another.
	switched := next.StopAll != stopped
	if kept.policy != nil {
		next.Version = kept.policy.Version + 1
	}
	next.Issued, next.ChangedBy = wire.DateOf(now), actor
	next.Requires = policyRequires(next)
	if next.Budgets != nil {
		next.Budgets.Raises = currentRaises(next.Budgets.Raises, now)
		if next.Budgets.AllowUnmetered == nil {
			next.Budgets.AllowUnmetered = []string{}
		}
	}
	next.Groups = nil
	raw, err := encodePolicy(next)
	if err != nil {
		return wire.OrgPolicy{}, nil, err
	}
	if len(raw) > mostPolicyBytes && len(raw) > len(kept.raw) && !switched {
		return wire.OrgPolicy{}, nil, fail(invalidParams, "The policy is larger than %d KiB.", mostPolicyBytes>>10)
	}
	return next, raw, nil
}

// policyChangeKept signs the policy the store took, keeps its version, and tells every
// copy, saying whether the kill switch changed.
func (s *Service) policyChangeKept(ctx context.Context, actor wire.AuditActor, kept keptPolicy, next wire.OrgPolicy,
	raw []byte, tag string) (wire.PolicyKept, error) {
	if err := s.signPolicy(ctx, next.Version, raw); err != nil {
		return wire.PolicyKept{}, err
	}
	s.keepPolicyVersion(ctx, next.Version, raw)
	s.log(fmt.Sprintf("the policy is now version %d", next.Version))
	stopped := kept.policy != nil && kept.policy.StopAll != nil && kept.policy.StopAll.On
	s.policyChanged(fmt.Sprint(next.Version), actor.User, stopped != (next.StopAll != nil && next.StopAll.On))
	return s.policyKept(keptPolicy{raw: raw, policy: &next, rev: tag}), nil
}

// encodePolicy is the document as kept and signed: its canonical JSON.
func encodePolicy(p wire.OrgPolicy) ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return controllink.CanonicalJSON(b)
}

// policyRequires is what a machine needs to follow p: host.policy/1 for every rule,
// host.org-routes/1 for a reach rule or Antigravity's terms (FR-014, FR-030; #999 FR-008),
// and host.policy-layers/1 when any of the new rules of #2957 is set, so an older machine
// refuses work rather than silently ignore them (data-model.md "OrgPolicy", "requires").
func policyRequires(p wire.OrgPolicy) []string {
	out := []string{CapHostPolicy}
	routes := p.Notices != nil && p.Notices.AntigravityTerms != nil
	for _, rule := range p.Runtimes {
		routes = routes || rule.Reach != nil
	}
	if routes {
		out = append(out, CapHostOrgRoutes)
	}
	if p.PermissionMode != nil || p.PermissionRequests != nil || p.Workflows != nil ||
		p.NoticeLimits != nil || p.ArchiveMyChats != nil || (p.Sandbox != nil && *p.Sandbox == "runtime") {
		out = append(out, wire.CapHostPolicyLayers1)
	}
	return out
}

// currentRaises drops the raises of days before today (UTC): each is for one day.
func currentRaises(raises []wire.BudgetRaise, now time.Time) []wire.BudgetRaise {
	today := now.UTC().Format(time.DateOnly)
	out := []wire.BudgetRaise{}
	for _, r := range raises {
		if r.Day >= today {
			out = append(out, r)
		}
	}
	return out
}

// signPolicy keeps the control key's signature of the document's bytes beside it.
func (s *Service) signPolicy(ctx context.Context, version int64, raw []byte) error {
	keys := s.currentKeys()
	sig, err := controllink.SignPolicy(keys.private, raw)
	if err != nil {
		return err
	}
	data, err := json.Marshal(wire.PolicySignature{Version: version, KeyID: controllink.KeyID(keys.public), Alg: controllink.PolicyAlg, Sig: sig})
	if err != nil {
		return err
	}
	if _, err := s.cfg.Store.Put(ctx, policySigKey, data, store.Always); err != nil {
		return fail(storeUnavailable, "The policy was kept, but its signature could not be: %v. Save it again.", err)
	}
	return nil
}

// keepPolicyVersion keeps each version as it was, so an admin sees what changed (FR-005):
// the newest policyVersionsKept, and an old one for policyVersionAge.
func (s *Service) keepPolicyVersion(ctx context.Context, version int64, raw []byte) {
	if len(raw) > mostPolicyVersion {
		s.log(fmt.Sprintf("policy version %d could not be kept: %d bytes, and the most is %d; the policy itself is kept", version, len(raw), mostPolicyVersion))
		return
	}
	key := fmt.Sprintf("%s%010d.json", policyVersionsKey, version)
	if _, err := s.cfg.Store.Put(ctx, key, raw, store.Absent); err != nil && !store.IsConflict(err) {
		s.log("policy version " + fmt.Sprint(version) + " could not be kept: " + err.Error())
		return
	}
	if err := s.sweepPolicyVersions(ctx, time.Now()); err != nil {
		s.log("the old policy versions could not be removed: " + err.Error())
	}
}

// sweepPolicyVersions deletes the versions past the newest policyVersionsKept, and those
// replaced policyVersionAge ago (#6765). A version's age runs from when the next was issued,
// so the one in force has none, and stays.
func (s *Service) sweepPolicyVersions(ctx context.Context, now time.Time) error {
	listed, err := s.cfg.Store.List(ctx, policyVersionsKey)
	if err != nil {
		return err
	}
	before := wire.DateOf(now.Add(-policyVersionAge))
	for i, k := range listed[:max(len(listed)-1, 0)] {
		if len(listed)-i <= policyVersionsKept {
			object, err := s.cfg.Store.Get(ctx, listed[i+1].Key)
			if err != nil {
				return err
			}
			var next wire.OrgPolicy
			if object == nil || json.Unmarshal(object.Data, &next) != nil {
				continue
			}
			// In order: the rest were replaced later.
			if next.Issued >= before {
				return nil
			}
		}
		if err := s.cfg.Store.Delete(ctx, k.Key); err != nil {
			return err
		}
	}
	return nil
}

// resignPolicy signs the policy again with the control key, after a rotation (T4): the
// version stays, and each machine is sent the new signature.
func (s *Service) resignPolicy(ctx context.Context) {
	kept, err := s.readPolicy(ctx)
	if err != nil || kept.policy == nil {
		return
	}
	if err := s.signPolicy(ctx, kept.policy.Version, kept.raw); err != nil {
		s.log("the policy could not be signed with the new key: " + err.Error())
		return
	}
	s.log("the policy was signed with the new control key")
	s.policyChangedHere(false)
}

// MARK: Sending it

// policyChanged is the policy changed here: the other copies are told, then this copy's
// machines and every person's clients.
func (s *Service) policyChanged(version, by string, stopAllFlipped bool) {
	kind := "policyChanged"
	if stopAllFlipped {
		kind = "policyStopAll"
	}
	s.announce(kind, version, by)
	s.policyChangedHere(stopAllFlipped)
}

// policyChangedHere is a change's effect on this copy: what the gate knows, each machine
// whose uplink ends here, and each person's clients here.
func (s *Service) policyChangedHere(stopAllFlipped bool) {
	ctx, cancel := context.WithTimeout(context.Background(), userDeliveryTimeout)
	defer cancel()
	kept, err := s.readPolicy(ctx)
	if err != nil {
		s.log("the policy could not be read: " + err.Error())
		return
	}
	s.notePolicy(kept)
	for _, host := range s.router.HostsHeldHere() {
		go s.sendPolicy(host, false)
	}
	s.router.BroadcastControlEach(wire.NotifyControlPolicyChanged.Name, func(person string) any {
		return policyViewFor(kept.policy, person)
	})
	if stopAllFlipped {
		s.router.BroadcastControlEach(wire.NotifyControlStopAll.Name, func(person string) any {
			return policyViewFor(kept.policy, person)
		})
	}
}

// notePolicy is what the router's gate knows of the policy.
func (s *Service) notePolicy(kept keptPolicy) {
	s.policies.mu.Lock()
	defer s.policies.mu.Unlock()
	s.policies.version, s.policies.requireFollowing = 0, false
	if kept.policy != nil {
		s.policies.version = kept.policy.Version
		s.policies.requireFollowing = kept.policy.RequireFollowing != nil && *kept.policy.RequireFollowing
	}
}

// takesPolicy says a machine keeps and follows the policy.
func (s *Service) takesPolicy(host string) bool {
	h := s.records.Host(host)
	return h != nil && slices.Contains(hostCapabilities(*h), CapHostPolicy)
}

// sendPolicy sends a machine the signed policy, or none, on its channel 0 (FR-006).
// always sends even what it was sent last (a hello). A copy that reads a signature of
// another version is in the middle of a change: it waits for it to finish.
//
// The sends to one machine go one at a time, each reading the store after the one before
// it sent: two changes close together start two sends, and without the lock the older
// version could reach the machine last, so it kept the kill switch on after it was
// turned off (#5069).
func (s *Service) sendPolicy(host string, always bool) {
	if !s.takesPolicy(host) {
		return
	}
	defer s.policies.lockSend(host)()
	ctx, cancel := context.WithTimeout(context.Background(), userDeliveryTimeout)
	defer cancel()
	var line any
	for try := 0; ; try++ {
		kept, err := s.readPolicy(ctx)
		if err != nil {
			s.log("the policy for " + host + " could not be read: " + err.Error())
			return
		}
		if kept.policy == nil {
			line = wire.MachinePolicy{None: optional(true)}
			break
		}
		if kept.signature != nil && kept.signature.Version == kept.policy.Version {
			line = signedPolicyLine{Policy: kept.raw, Signature: *kept.signature}
			break
		}
		if try >= policyRetries*5 {
			s.log("the policy's signature is not yet its version's; " + host + " is sent it at the next change")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(policyDeliveryPause):
		}
	}
	b, _ := json.Marshal(line)
	sum := sha256.Sum256(b)
	s.policies.mu.Lock()
	if s.policies.told == nil {
		s.policies.told = map[string][sha256.Size]byte{}
	}
	same := s.policies.told[host] == sum
	s.policies.mu.Unlock()
	if same && !always {
		return
	}
	if !s.router.Tell(host, wire.NotifyMachinePolicy.Name, json.RawMessage(b)) {
		s.policies.mu.Lock()
		delete(s.policies.told, host)
		s.policies.mu.Unlock()
		return
	}
	s.policies.mu.Lock()
	s.policies.told[host] = sum
	s.policies.mu.Unlock()
}

// policyApplied is machine/policyApplied: the machine kept this version, noted on its record
// for policy/get, the Console and the gate at every copy.
func (s *Service) policyApplied(ctx context.Context, host string, applied wire.PolicyApplied) error {
	if applied.Version < 1 {
		return fail(invalidParams, "Say the version kept.")
	}
	value, _ := json.Marshal(applied.Version)
	changed := false
	if err := s.records.ChangeHost(ctx, host, func(r *records.MachineRecord) bool {
		if bytes.Equal(r.Extra[policyAppliedField], value) {
			return false
		}
		// A new map: the record's own is shared with whoever read it.
		extra := make(records.Extra, len(r.Extra)+1)
		maps.Copy(extra, r.Extra)
		extra[policyAppliedField] = value
		r.Extra = extra
		changed = true
		return true
	}); err != nil {
		return err
	}
	if changed {
		s.announce("policyApplied", host, host)
	}
	return nil
}

// appliedPolicy is the version a machine said it kept.
func appliedPolicy(h records.MachineRecord) (int64, bool) {
	var v int64
	if json.Unmarshal(h.Extra[policyAppliedField], &v) != nil || v < 1 {
		return 0, false
	}
	return v, true
}

// policyGate is the router's check of a call to a machine: with requireFollowing, a
// machine that has not applied the current version starts no work (spec 998, edge case
// "a machine of an older version"). The router holds its lock: this reads only memory.
func (s *Service) policyGate(host, method string) error {
	if !policyGated[method] {
		return nil
	}
	s.policies.mu.Lock()
	version, require := s.policies.version, s.policies.requireFollowing
	s.policies.mu.Unlock()
	if !require || version == 0 {
		return nil
	}
	h := s.records.Host(host)
	if h == nil {
		return nil
	}
	if applied, ok := appliedPolicy(*h); ok && applied >= version {
		return nil
	}
	words := fmt.Sprintf("%s does not follow your organisation's policy yet. Update Agent Loops on it, and connect it to the network.", h.Name)
	data, _ := json.Marshal(wire.PolicyRefusal{Reason: "update", Rule: optional("requireFollowing"), Words: words})
	return &RPCError{Code: wire.FailurePolicyRefused, Message: words, Data: data}
}
