package providers

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
)

// KindCommands is the one provider kind, as ProviderRecord.Kind says it: the command
// provider Agent Loops ships (workspace-commands). The MCP server kinds, "stdio" and
// "http", were removed (#6623).
const KindCommands = "commands"

func knownKind(kind string) bool { return kind == KindCommands }

// ProvidersPrefix is WorkspaceProviders.prefix: where provider records are kept.
const ProvidersPrefix = "v1/providers/"

// ProviderKey is WorkspaceProviders.key.
func ProviderKey(id string) string { return ProvidersPrefix + id + ".json" }

// ProviderRecord is ProviderRecord (v1/providers/<id>.json): never its secrets' values,
// which stay on the machine that carries it (#3398).
type ProviderRecord struct {
	ID   string
	Name string
	Kind string
	// The command provider's configuration.
	Commands    any
	SecretNames []string
	// Which optional contract tools it has: options, create, delete.
	Capabilities []string
	Contract     int
	Rev          int
	Forgotten    *bool
	// When it was removed, so the sweep deletes its tombstone a week on (#3748).
	ForgottenAt *records.Stamp
	// The group whose users may see it and start machines from it (#1761, FR-028); nil
	// for the admins' alone, as every provider made before groups is.
	Group *string
	// The machine that carries it (#3398): runs its server and keeps its secrets. Nil for
	// one made before, which is added again on a machine.
	Machine *string
	// When it was added, so the first one added is the default for New Machine (#3972).
	// Nil for one added before it was kept.
	AddedAt *records.Stamp
	// Whose account its OAuth connection is for (#6072); nil for a pasted token. The
	// tokens stay on its machine.
	OAuth *OAuthConnection
	// What a later build wrote that this one does not know, written back as it was.
	Extra records.Extra
}

type providerJSON struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Commands     any            `json:"commands,omitempty"`
	SecretNames  []string       `json:"secretNames"`
	Capabilities []string       `json:"capabilities"`
	Contract     int            `json:"contract"`
	Rev          int            `json:"rev"`
	Forgotten    *bool          `json:"forgotten,omitempty"`
	ForgottenAt  *records.Stamp `json:"forgottenAt,omitempty"`
	Group        *string        `json:"group,omitempty"`
	Machine      *string        `json:"machine,omitempty"`
	AddedAt      *records.Stamp `json:"addedAt,omitempty"`
	OAuth        *OAuthConnection `json:"oauth,omitempty"`
}

// OAuthConnection is a provider connected with OAuth (#6072): the account and the team
// the person chose on the provider's consent page. Never a token.
type OAuthConnection struct {
	Account string `json:"account,omitempty"`
	Team    string `json:"team,omitempty"`
}

func (p ProviderRecord) MarshalJSON() ([]byte, error) {
	j := providerJSON{p.ID, p.Name, p.Kind, p.Commands, p.SecretNames, p.Capabilities, p.Contract, p.Rev, p.Forgotten,
		p.ForgottenAt, p.Group, p.Machine, p.AddedAt, p.OAuth}
	if j.SecretNames == nil {
		j.SecretNames = []string{}
	}
	if j.Capabilities == nil {
		j.Capabilities = []string{}
	}
	return encodeKeeping(j, p.Extra)
}

func (p *ProviderRecord) UnmarshalJSON(data []byte) error {
	var j providerJSON
	extra, err := decodeKeeping(data, &j, "id", "name", "kind", "secretNames", "capabilities", "contract", "rev")
	if err != nil {
		return err
	}
	*p = ProviderRecord{j.ID, j.Name, j.Kind, j.Commands, j.SecretNames, j.Capabilities, j.Contract, j.Rev, j.Forgotten,
		j.ForgottenAt, j.Group, j.Machine, j.AddedAt, j.OAuth, extra}
	return nil
}

// Problem is ProviderRecord.problem: why it cannot be used as it is, in words for the
// sheet; "" when it can.
func (p *ProviderRecord) Problem() string {
	trimmed := trimSpaces(p.Name)
	if trimmed == "" {
		return "Give it a name."
	}
	if utf8.RuneCountInString(trimmed) > 60 {
		return "The name is over 60 characters."
	}
	if _, ok := obj(p.Commands); p.Kind == KindCommands && !ok {
		return "Choose a preset or give its commands."
	}
	return ""
}

// preset is the command provider's preset, when it uses one.
func (p *ProviderRecord) preset() string { return strOr(get(p.Commands, "preset")) }

// DefaultIdleMinutes is WorkspaceLink.defaultIdleMinutes.
const DefaultIdleMinutes = 120

// WorkspaceLink is WorkspaceLink: a host and the workspace it runs on, kept in the host's
// own record (MachineRecord.Workspace).
type WorkspaceLink struct {
	Provider string
	// The workspace's id, as its provider lists it.
	Workspace string
	// A prompt or agent start for it while stopped starts it (US3). Nil reads as on.
	StartWhenUsed *bool
	// Stopped once idle this long (US4); nil is off.
	StopWhenIdleMinutes *int
	// Started a little before the host's next scheduled workflow (#1004). Nil reads as on.
	StartForSchedules *bool
	// The host's next scheduled workflow as it last said it, kept while it is stopped.
	Schedule *Schedule
	// How long its last start took, from asking the provider to the host connecting.
	MeasuredStartSeconds *int
	// The due time it was last started for, so each starts it once.
	WokeFor *time.Time
	// The ssh host key trusted at the link, in known_hosts form, for a host with no
	// install to have trusted one (#3379); "" for none.
	KnownHosts string
	Extra      records.Extra
}

// Schedule is DaemonAPI.MachineSchedule as a link keeps it.
type Schedule struct {
	At       *time.Time
	Workflow string
}

func (s *Schedule) equal(o *Schedule) bool {
	if s == nil || o == nil {
		return s == nil && o == nil
	}
	return s.Workflow == o.Workflow && sameTime(s.At, o.At)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Truncate(time.Millisecond).Equal(b.Truncate(time.Millisecond))
}

type scheduleJSON struct {
	At       *records.Stamp `json:"at,omitempty"`
	Workflow string         `json:"workflow,omitempty"`
}

type linkJSON struct {
	Provider             string         `json:"provider"`
	Workspace            string         `json:"workspace"`
	StartWhenUsed        *bool          `json:"startWhenUsed,omitempty"`
	StopWhenIdleMinutes  *int           `json:"stopWhenIdleMinutes,omitempty"`
	StartForSchedules    *bool          `json:"startForSchedules,omitempty"`
	Schedule             *scheduleJSON  `json:"schedule,omitempty"`
	MeasuredStartSeconds *int           `json:"measuredStartSeconds,omitempty"`
	WokeFor              *records.Stamp `json:"wokeFor,omitempty"`
	KnownHosts           string         `json:"knownHosts,omitempty"`
}

func stamp(t *time.Time) *records.Stamp {
	if t == nil {
		return nil
	}
	s := records.At(*t)
	return &s
}

func unstamp(s *records.Stamp) *time.Time {
	if s == nil {
		return nil
	}
	t := s.Time
	return &t
}

func (l WorkspaceLink) MarshalJSON() ([]byte, error) {
	j := linkJSON{l.Provider, l.Workspace, l.StartWhenUsed, l.StopWhenIdleMinutes, l.StartForSchedules, nil,
		l.MeasuredStartSeconds, stamp(l.WokeFor), l.KnownHosts}
	if l.Schedule != nil {
		j.Schedule = &scheduleJSON{stamp(l.Schedule.At), l.Schedule.Workflow}
	}
	return encodeKeeping(j, l.Extra)
}

func (l *WorkspaceLink) UnmarshalJSON(data []byte) error {
	var j linkJSON
	extra, err := decodeKeeping(data, &j, "provider", "workspace")
	if err != nil {
		return err
	}
	*l = WorkspaceLink{j.Provider, j.Workspace, j.StartWhenUsed, j.StopWhenIdleMinutes, j.StartForSchedules, nil,
		j.MeasuredStartSeconds, unstamp(j.WokeFor), j.KnownHosts, extra}
	if j.Schedule != nil {
		l.Schedule = &Schedule{unstamp(j.Schedule.At), j.Schedule.Workflow}
	}
	return nil
}

func (l *WorkspaceLink) equal(o *WorkspaceLink) bool {
	if l == nil || o == nil {
		return l == nil && o == nil
	}
	a, _ := json.Marshal(l)
	b, _ := json.Marshal(o)
	return string(a) == string(b)
}

// LinkOf is a host's workspace link, or nil.
func LinkOf(h *records.MachineRecord) *WorkspaceLink {
	if h == nil || len(h.Workspace) == 0 || string(h.Workspace) == "null" {
		return nil
	}
	var l WorkspaceLink
	if json.Unmarshal(h.Workspace, &l) != nil {
		return nil
	}
	return &l
}

// setLink sets a host's workspace link; nil unlinks it.
func setLink(h *records.MachineRecord, l *WorkspaceLink) {
	if l == nil {
		h.Workspace = nil
		return
	}
	data, err := json.Marshal(l)
	if err != nil {
		return
	}
	h.Workspace = data
}

// decodeKeeping decodes data into v (a struct with no methods of its own), checks that
// each of required is there, and returns what it did not know.
func decodeKeeping(data []byte, v any, required ...string) (records.Extra, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	for _, name := range required {
		if raw, ok := all[name]; !ok || string(raw) == "null" {
			return nil, fmt.Errorf("no %s", name)
		}
	}
	if err := decodeJSON(data, v); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	t := reflect.TypeOf(v).Elem()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		known[name] = true
	}
	var extra records.Extra
	for name, raw := range all {
		if !known[name] {
			if extra == nil {
				extra = records.Extra{}
			}
			extra[name] = raw
		}
	}
	return extra, nil
}

// encodeKeeping encodes v with extra beside it.
func encodeKeeping(v any, extra records.Extra) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return raw, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	for name, value := range extra {
		if _, ok := all[name]; !ok {
			all[name] = value
		}
	}
	return json.Marshal(all)
}
