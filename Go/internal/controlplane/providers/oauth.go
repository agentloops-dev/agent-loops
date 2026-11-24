package providers

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/provideroauth"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// OAuth connections (#6072, spike #6071). The control plane only routes them: the machine
// that will carry the provider makes the PKCE verifier and keeps it, spends the code, and
// keeps the tokens (Principle 6). Here is only which machine a state is for, whose it is,
// and for ten minutes.

const (
	MethodProvidersOAuthStart  = "providers/oauthStart"
	MethodProvidersOAuthFinish = "providers/oauthFinish"
)

// oauthLife is how long a started connection waits for its finish, as its machine waits.
const oauthLife = 10 * time.Minute

type oauthStarted struct {
	machine, person, preset, provider string
	until                             time.Time
}

type oauthStarts struct {
	mu   sync.Mutex
	held map[string]oauthStarted
}

func (o *oauthStarts) put(state string, s oauthStarted) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.held == nil {
		o.held = map[string]oauthStarted{}
	}
	for k, v := range o.held {
		if time.Now().After(v.until) {
			delete(o.held, k)
		}
	}
	o.held[state] = s
}

func (o *oauthStarts) take(state string) (oauthStarted, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.held[state]
	delete(o.held, state)
	if ok && time.Now().After(s.until) {
		return oauthStarted{}, false
	}
	return s, ok
}

// oauthFlows are the presets that connect with OAuth, by preset.
var oauthFlows = map[string]*provideroauth.Flow{"digitalocean-api": provideroauth.DigitalOcean()}

func coded(code, words string) *ProviderFailure { return &ProviderFailure{Words: words, Code: code} }

// oauthStart is providers/oauthStart: the machine that will carry the provider (the one an
// existing provider has, else the one named, else the caller's first online one) starts the
// connection and gives the address to open.
func (p *Providers) oauthStart(request wire.ProviderOAuthStart, caller Caller) (wire.ProviderOAuthStarted, error) {
	preset := string(request.Preset)
	flow := oauthFlows[preset]
	if flow == nil {
		return wire.ProviderOAuthStarted{}, invalid("%s can't be connected with a sign-in: give its token instead.", preset)
	}
	var before *ProviderRecord
	provider := ""
	if request.ID != nil && *request.ID != "" {
		r, ok := p.record(string(*request.ID))
		if !ok {
			return wire.ProviderOAuthStarted{}, invalid("There is no such provider.")
		}
		if r.preset() != preset {
			return wire.ProviderOAuthStarted{}, invalid("%s isn't a %s provider.", r.Name, flow.Name)
		}
		before, provider = &r, r.ID
	} else if err := p.room(); err != nil {
		return wire.ProviderOAuthStarted{}, err
	}
	machine, err := p.carrier(ProviderAdd{Machine: request.Machine}, before, caller)
	if err != nil {
		return wire.ProviderOAuthStarted{}, err
	}
	if !p.router.HostCan(machine, wire.CapProvidersCarry3) {
		return wire.ProviderOAuthStarted{}, invalid("%s can’t keep a %s connection: update Agent Loops on it first, or give a token.",
			p.hostName(machine), flow.Name)
	}
	ask := wire.ProviderOAuthCarry{Op: "start", Preset: preset}
	if request.Origin != nil && *request.Origin != "" {
		ask.Origin = request.Origin
	}
	raw, err := Ask(p.router, machine, p.hostName(machine), wire.CallProvidersOauth.Name, ask, time.Minute)
	if err != nil {
		return wire.ProviderOAuthStarted{}, err
	}
	var started wire.ProviderOAuthCarried
	if json.Unmarshal(raw, &started) != nil || deref(started.URL) == "" || deref(started.State) == "" {
		return wire.ProviderOAuthStarted{}, providerFailure("%s answered with something this control plane can’t read.", p.hostName(machine))
	}
	p.oauth.put(*started.State, oauthStarted{machine: machine, person: caller.Person, preset: preset, provider: provider,
		until: time.Now().Add(oauthLife)})
	return wire.ProviderOAuthStarted{URL: *started.URL, State: *started.State, Machine: wire.MachineRef(machine),
		MachineName: optional(p.hostName(machine))}, nil
}

// oauthFinish is providers/oauthFinish: the machine the state is for spends the code and
// keeps the tokens; then the provider is added (or connected again) with no secret here.
func (p *Providers) oauthFinish(request wire.ProviderOAuthFinish, caller Caller) (ProviderSummary, error) {
	state := deref(request.State)
	if deref(request.Error) == "access_denied" && state == "" {
		// DigitalOcean's Deny comes back with no state: its machine's verifier just expires.
		return ProviderSummary{}, coded(provideroauth.Denied, "You chose Deny, so Agent Loops isn't connected.")
	}
	started, ok := p.oauth.take(state)
	if !ok || started.person != caller.Person {
		return ProviderSummary{}, coded(provideroauth.Expired, "That sign-in was started too long ago, or by someone else: start again.")
	}
	flow := oauthFlows[started.preset]
	request2 := ProviderAdd{Kind: KindCommands, Commands: jsonValue(map[string]any{"preset": started.preset})}
	updating := started.provider != ""
	if updating {
		before, ok := p.record(started.provider)
		if !ok {
			return ProviderSummary{}, invalid("There is no such provider.")
		}
		id := wire.ProviderID(before.ID)
		request2.ID, request2.Name, request2.Commands = &id, before.Name, jsonValue(before.Commands)
	} else {
		// Before the machine spends the code: one more would be refused after it.
		if err := p.room(); err != nil {
			return ProviderSummary{}, err
		}
		request2.Name = strings.TrimSpace(deref(request.Name))
		if request2.Name == "" {
			request2.Name = flow.Name
		}
	}
	id, _, _, err := p.saving(request2, updating)
	if err != nil {
		return ProviderSummary{}, err
	}
	pid := wire.ProviderID(id)
	raw, err := Ask(p.router, started.machine, p.hostName(started.machine), wire.CallProvidersOauth.Name,
		wire.ProviderOAuthCarry{Op: "finish", Preset: started.preset, Provider: &pid, State: &state,
			Code: request.Code, Error: request.Error}, 3*time.Minute)
	if err != nil {
		return ProviderSummary{}, err
	}
	var connected wire.ProviderOAuthCarried
	if json.Unmarshal(raw, &connected) != nil {
		return ProviderSummary{}, providerFailure("%s answered with something this control plane can’t read.", p.hostName(started.machine))
	}
	m := wire.MachineRef(started.machine)
	request2.Machine = &m
	summary, err := p.saveConnected(request2, updating, caller, &connectedSave{id: id, machine: started.machine,
		names: connected.SecretNames, oauth: &OAuthConnection{Account: deref(connected.Account), Team: deref(connected.Team)}})
	if err != nil && !updating {
		// Its machine kept tokens for a provider that was not added: revoked and forgotten.
		p.forget(ProviderRecord{ID: id, Machine: &started.machine})
	}
	return summary, err
}

// summaryOAuth is a summary's oauth: disconnected when its last list was refused.
func summaryOAuth(r ProviderRecord, l listed) *wire.ProviderOAuth {
	if r.OAuth == nil {
		return nil
	}
	state := "connected"
	if l.code == provideroauth.Disconnected {
		state = "disconnected"
	}
	return &wire.ProviderOAuth{Account: optional(r.OAuth.Account), Team: optional(r.OAuth.Team), State: state}
}

// failureCode is err's code, when a client can act on it.
func failureCode(err error) string {
	var f *ProviderFailure
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}

// pastesToken says request gives an OAuth preset's token by hand.
func pastesToken(request ProviderAdd) bool {
	for _, f := range oauthFlows {
		if request.Secrets[f.AccessSecret] != "" {
			return true
		}
	}
	return false
}

// withoutOAuthNames is names with an OAuth connection's own secrets left out (its access
// token is a pasted token's too, so it stays).
func withoutOAuthNames(names []string) []string {
	var out []string
	for _, n := range names {
		own := false
		for _, f := range oauthFlows {
			if n != f.AccessSecret && slices.Contains(f.SecretNames(), n) {
				own = true
			}
		}
		if !own {
			out = append(out, n)
		}
	}
	return out
}

func unionSorted(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
