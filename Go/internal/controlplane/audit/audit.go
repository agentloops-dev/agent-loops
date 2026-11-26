// Package audit is who did what (#996, spec 736 US8, T3): the control plane writes an
// entry for each action a client takes through it, with the actor it knows the client by,
// never one the client names. Entries are the control plane's events, under
// <prefix>/v1/events/<day>/by/<person>/, kept for auditDays (90 by default), at
// most 1,000,000 of them, and swept by the hour (#6755). The person in the key lets a member's list read only their own entries (#2051).
//
// Until people (#994) are built, a control plane has one person, the local owner, and
// every client is theirs: every actor is the local owner and the client it used.
package audit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

const (
	// DefaultDays is how long entries are kept when control.json says nothing (FR-040).
	DefaultDays = 90
	// MostDays bounds what an admin may set (#6677). A longer copy is a sink's.
	MostDays = 90
	// MostEntries is the most entries kept, whatever their age (Alex, #6755).
	MostEntries = 1_000_000
	// MostEntryBytes is the largest entry; entry cuts its text to fit (#6755).
	MostEntryBytes = 16 << 10
	// Grace is how long the sweep still keeps the days before a shorter time (#2067).
	Grace = 24 * time.Hour
	// Prefix is where entries live: the control plane's events.
	// store-bounds: kind=log; count=1_000_000; size=16KiB a record; age=90d; applied=hourly sweep
	Prefix    = "v1/events/"
	dayLayout = "2006-01-02"
	// byPerson is the folder under a day that holds each person's entries.
	byPerson = "by/"
	// Kind is an entry's kind, beside the control plane's own events (clientPaired, …).
	Kind = "audit"

	defaultLimit = 200
	mostLimit    = 1000
	// mostExported bounds an export, so that one answer stays a size a client can hold.
	mostExported = 50_000
	mostTarget   = 120
	// What an entry's text is cut to (#6755): at most six bytes a byte as JSON, the 13
	// texts and the target are under MostEntryBytes.
	mostText       = 100
	mostTargetText = 1024
	// LocalOwnerName is the one person's name until people have names of their own (#994).
	LocalOwnerName = "Local owner"
)

// hostActions are the calls passed to a host that the audit keeps (spec 736 US8 #1): start,
// stop or delete an agent; answer a card; change a setting; approve a workflow, a plugin
// or an MCP server. Reads, prompts and presence are not kept.
var hostActions = map[string]string{
	"agents/start":                "Started an agent",
	"firstBuild/start":            "Started the first build",
	"agents/stop":                 "Stopped an agent",
	"agents/stopBackground":       "Stopped an agent's background work",
	"agents/delete":               "Deleted an agent",
	"agents/archive":              "Archived an agent",
	"agents/unarchive":            "Unarchived an agent",
	"agents/fork":                 "Forked an agent",
	"agents/branchFrom":           "Branched an agent",
	"agents/continueInFolder":     "Continued an agent in a folder",
	"agents/continueInNewSession": "Continued an agent in a new session",
	"sessions/delete":             "Deleted a session",
	"approvals/answer":            "Answered an approval",
	"permissions/answer":          "Answered a permission card",
	"elicitations/answer":         "Answered a question card",
	"agents/answerSandbox":        "Answered a sandbox card",
	"agents/setSandbox":           "Changed an agent's sandbox",
	"agents/setCeiling":           "Changed an agent's cost ceiling",
	"preferences/set":             "Changed a setting",
	"cost/setLimits":              "Changed the cost limits",
	"wake/set":                    "Changed the wake settings",
	"autopilot/set":               "Chose what Autopilot starts in a folder",
	"folders/setDiskSpace":        "Changed a folder's disk space",
	"folders/setHelperLimits":     "Changed a folder's helper limits",
	"folders/setPRCostComment":    "Changed what a folder's merged pull requests say of their cost",
	"folders/setWorkingHours":     "Changed a project's working hours",
	"runtimes/setProvider":        "Changed a runtime's provider",
	"runtimes/disableProvider":    "Turned off a runtime's provider",
	"runtimes/install":            "Installed a runtime",
	"runtimes/logout":             "Signed a runtime out",
	"workflows/approve":           "Approved a workflow",
	"workflows/deny":              "Denied a workflow",
	"workflows/enable":            "Turned a workflow on or off",
	"workflows/run":               "Ran a workflow",
	"workflows/archive":           "Archived a workflow",
	"workflows/delete":            "Deleted a workflow",
	"plugins/approve":             "Approved a plugin",
	"env/write":                   "Changed an environment variable",
	"mcp/approve":                 "Approved an MCP server",
	"mcp/add":                     "Added an MCP server",
	"mcp/add-by-hand":             "Added an MCP server",
	"mcp/remove":                  "Removed an MCP server",
	"mcp/set-secret":              "Set an MCP server's secret",
	"skills/add":                  "Added a skill",
	"skills/remove":               "Removed a skill",
	"personas/add":                "Added a persona",
	"personas/remove":             "Removed a persona",
	"marketplaces/add":            "Added a marketplace",
	"marketplaces/remove":         "Removed a marketplace",
	"marketplaces/set-enabled":    "Turned a marketplace on or off",
	"folders/add":                 "Added a folder",
	"folders/delete":              "Deleted a folder",
	"folders/archive":             "Archived a folder",
	"worktrees/remove":            "Removed a worktree",
	"resources/declare":           "Declared a resource",
	"resources/remove":            "Removed a resource",
	"connections/disconnect":      "Disconnected an account",
	"credentials/lend":            "Lent a sign-in",
	"daemon/quit":                 "Quit a host",
}

// controlActions are the control plane's own methods that the audit keeps.
var controlActions = map[string]string{
	"clients/forget":             "Forgot a client",
	"clients/forgetSelf":         "Forgot a client",
	"devices/forget":             "Forgot a client",
	"machines/remove":            "Removed a machine",
	"clients/startPairing":       "Made a pairing code",
	"devices/startPairing":       "Made a pairing code",
	"machines/startEnroll":       "Made a machine code",
	"machines/install":           "Installed a machine over ssh",
	"machines/update":            "Updated a machine",
	"machines/lendSignIn":        "Lent a sign-in to a machine",
	"machines/detect":            "Added the machines it found",
	"control/setFolderDetection": "Changed folder detection",
	"control/setAuditDays":       "Changed how long the audit is kept",
	"audit/setSinks":             "Changed where the audit is sent",
	"control/rotate":             "Rotated the control key",
	"policy/set":                 "Changed the organisation's policy",
	"policy/stopAll":             "Turned the kill switch on or off",
	"policy/raise":               "Raised a person's budget for today",
	"orgRoutes/set":              "Added or changed an org route",
	"orgRoutes/replaceKey":       "Replaced an org route's key",
	"orgRoutes/check":            "Checked an org route",
	"orgRoutes/remove":           "Removed an org route",
	"providers/add":              "Added a machine provider",
	"providers/create":           "Made a machine",
	"providers/deleteWorkspace":  "Deleted a machine that never joined",
	"providers/update":           "Changed a machine provider",
	"providers/remove":           "Removed a machine provider",
	"machines/start":             "Started a workspace",
	"machines/stop":              "Stopped a workspace",
	"machines/deleteMachine":     "Deleted a workspace",
	"machines/link":              "Linked a machine to a workspace",
	"machines/unlink":            "Unlinked a machine from its workspace",

	// Org sign-in (#1002, FR-025): each writes its own entry, with what changed and no secret.
	"signIn/orgProviders/set":    "Added or changed an org sign-in provider",
	"signIn/orgProviders/check":  "Checked an org sign-in provider",
	"signIn/orgProviders/remove": "Removed an org sign-in provider",
	"signIn/domains/claim":       "Claimed a domain",
	"signIn/domains/check":       "Checked a domain",
	"signIn/domains/set":         "Changed a domain's sign-in",
	"signIn/domains/remove":      "Removed a domain's claim",
	"signIn/domains/lost":        "Lost a domain's proof",
	"signIn/ticket":              "Signed in with an org provider",
	"people/refusedFull":         "Refused a new person: the people are at their limit",
	"signIn/refusedByRequire":    "Refused a sign-in the organisation does not allow",

	// SCIM and suspension (#1002, slice 2a, FR-025): each writes its own entry. A SCIM
	// write's actor is the provider and its token's id, never its value.
	"scim/set":           "Turned SCIM on or off",
	"scim/tokens/make":   "Made a SCIM token",
	"scim/tokens/revoke": "Revoked a SCIM token",
	"groups/setRole":     "Changed the role a group gives",
	"scim/user/create":   "Added a person by SCIM",
	"scim/user/change":   "Changed a person by SCIM",
	"scim/user/delete":   "Deleted a person by SCIM",
	"scim/group/create":  "Added a group by SCIM",
	"scim/group/change":  "Changed a group by SCIM",
	"scim/group/delete":  "Deleted a group by SCIM",
	"person/suspended":   "Suspended a person",
	"person/unsuspended": "Ended a person's suspension",

	// Catalogue governance (#2019, M5): each writes its own entry, naming the item.
	"governance/asked":    "Asked for an MCP server or skill",
	"governance/approved": "Approved an MCP server or skill",
	"governance/refused":  "Refused an MCP server or skill",
	"governance/revoked":  "Took back an approval of an MCP server or skill",
	// Ask for More (#3262): the ask and a refusal write their own entries; a raise is policy/raise's.
	"policy/askedForMore": "Asked for more of today's budget",
	"policy/refusedMore":  "Refused more of today's budget",
	// The read API (#2019): each writes its own entry, naming the token, never its value.
	"readApi/made":    "Made a read API token",
	"readApi/revoked": "Revoked a read API token",
	// A person's secrets (#4156): the target is the secret's name, never its value.
	"user/setSecret":    "Set a secret",
	"user/removeSecret": "Removed a secret",
}

// Action is what a call is in words, and whether the audit keeps it at all. toHost is a
// call passed to a host; otherwise it is the control plane's own.
func Action(method string, toHost bool) (string, bool) {
	if toHost {
		a, ok := hostActions[method]
		return a, ok
	}
	a, ok := controlActions[method]
	return a, ok
}

// HostMethods are the calls passed to a host that the audit keeps, sorted.
func HostMethods() []string {
	out := make([]string, 0, len(hostActions))
	for m := range hostActions {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// targetKeys are where a call names what it acts on, most telling first.
var targetKeys = []string{"agentID", "id", "workflowID", "key", "server", "name", "host", "provider",
	"permissionID", "requestID", "cwd", "folder", "path"}

// Target is what a call acts on, as its params name it: an agent, a workflow, a server,
// a folder. Never a prompt or a secret's value.
func Target(params json.RawMessage) *string {
	var o map[string]json.RawMessage
	if json.Unmarshal(params, &o) != nil {
		return nil
	}
	for _, k := range targetKeys {
		var s string
		if json.Unmarshal(o[k], &s) == nil && strings.TrimSpace(s) != "" {
			s = strings.TrimPrefix(strings.TrimSpace(s), "file://")
			if len(s) > mostTarget {
				s = s[:mostTarget] + "…"
			}
			return &s
		}
	}
	return nil
}

// Days is how long entries are kept: what control.json says, or the default.
func Days(settings *records.Settings) int {
	if settings != nil && settings.AuditDays != nil && *settings.AuditDays > 0 {
		return min(*settings.AuditDays, MostDays)
	}
	return DefaultDays
}

// SweepDays is how many days the sweep keeps: Days, or for a day after it was made
// shorter, the days before (#2067).
func SweepDays(settings *records.Settings, now time.Time) int {
	days := Days(settings)
	if settings != nil && settings.AuditDaysBefore != nil && settings.AuditShortened != nil &&
		now.Before(settings.AuditShortened.Add(Grace)) && *settings.AuditDaysBefore > days {
		return min(*settings.AuditDaysBefore, MostDays)
	}
	return days
}

// SetDays is control/setAuditDays on the settings at now: a shorter time keeps what the
// sweep keeps now for Grace, and a time as long as that ends the grace (#2067).
func SetDays(s *records.Settings, days int, now time.Time) {
	keep := SweepDays(s, now)
	s.AuditDays = &days
	if days < keep {
		stamp := records.At(now)
		s.AuditDaysBefore, s.AuditShortened = &keep, &stamp
	} else {
		s.AuditDaysBefore, s.AuditShortened = nil, nil
	}
}

// stored is an entry as the store keeps it, beside the control plane's own events.
type stored struct {
	Kind   string           `json:"kind"`
	Action string           `json:"action,omitempty"`
	Method string           `json:"method,omitempty"`
	Host   *string          `json:"host,omitempty"`
	Target *string          `json:"target,omitempty"`
	At     records.Stamp    `json:"at"`
	By     string           `json:"by,omitempty"`
	Actor  *wire.AuditActor `json:"actor,omitempty"`
	// The chain this entry is in (#1001): the copy that wrote it, its place in that copy's
	// entries, and the SHA-256 of the stored bytes of the one before (none for the first).
	Chain string `json:"chain,omitempty"`
	Seq   int64  `json:"seq,omitempty"`
	Prev  string `json:"prev,omitempty"`
}

// Log writes and reads the entries.
type Log struct {
	Store store.Store
	// Entries is the store's log of entries; nil opens it.
	Entries *store.DayLog
	Now     func() time.Time
	// Chain links what this copy writes (#1001); nil writes entries in no chain.
	Chain *Chain
	// Out hears each entry kept, for the sinks (#1001); nil sends nowhere.
	Out func(key string, data []byte)
}

func (l *Log) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Open is the log that keeps the entries in s, with their bounds (#6755). sweep, when there
// is one, is asked for a sweep before the hour.
func Open(s store.Store, sweep func(context.Context)) (*store.DayLog, error) {
	return store.OpenDayLog(store.Spec{Name: "audit", Kind: "log", Scope: "organisation", Bounds: store.Bounds{
		MaxRecordBytes: MostEntryBytes, MaxCount: MostEntries, MaxAge: MostDays * 24 * time.Hour, SweepRequested: sweep,
	}}, Prefix, s)
}

func (l *Log) entries() (*store.DayLog, error) {
	if l.Entries != nil {
		return l.Entries, nil
	}
	return Open(l.Store, nil)
}

// keep puts an entry at key, which Key made from at.
func (l *Log) keep(ctx context.Context, key string, at time.Time, data []byte) error {
	entries, err := l.entries()
	if err != nil {
		return err
	}
	return entries.Append(ctx, strings.TrimPrefix(key, DayFolder(KeyDay(key))), at, data)
}

// Key is where an entry made at t by user goes: by day, then by person, then by the
// millisecond, so a person's keys for a day sort by time.
func Key(t time.Time, user string) string {
	t = t.UTC()
	return fmt.Sprintf("%s%013d-%s.json", dayPrefix(t.Format(dayLayout), user), t.UnixMilli(), strings.ToLower(records.NewUUID()[:8]))
}

// dayPrefix is the folder of a day's entries, or of one person's entries that day. The
// name is base64, so that no name makes a path of its own ("..", "/").
func dayPrefix(day, user string) string {
	return Prefix + day + "/" + partition(user)
}

// partition is the folder of one person's entries in each day's, or "" for everyone's.
func partition(user string) string {
	if user == "" {
		return ""
	}
	return byPerson + base64.RawURLEncoding.EncodeToString([]byte(user)) + "/"
}

// Write keeps one action. The store's create-only write means two copies never write
// over each other.
func (l *Log) Write(ctx context.Context, actor wire.AuditActor, method, action string, host string, target *string) error {
	at := l.now()
	e, key := entry(actor, method, action, host, target, at)
	var data []byte
	var err error
	if l.Chain != nil {
		data, err = l.Chain.write(ctx, l, &e, key, at)
	} else if data, err = records.Encode(e); err == nil {
		err = l.keep(ctx, key, at, data)
	}
	if err == nil && l.Out != nil {
		l.Out(key, data)
	}
	return err
}

// clip cuts text to most bytes, at a character.
func clip(text string, most int) string {
	if len(text) <= most {
		return text
	}
	return strings.ToValidUTF8(text[:most-len("…")], "") + "…"
}

func clipped(text *string) *string {
	if text == nil {
		return nil
	}
	return new(clip(*text, mostText))
}

// clipActor is the actor with its texts cut, so that an entry keeps its size (#6755).
func clipActor(a wire.AuditActor) wire.AuditActor {
	a.User, a.UserName, a.ClientName = clip(a.User, mostText), clipped(a.UserName), clipped(a.ClientName)
	if a.Client != nil {
		a.Client = new(wire.UUID(clip(string(*a.Client), mostText)))
	}
	if a.Event != nil {
		a.Event = &wire.AuditCause{Kind: clip(a.Event.Kind, mostText), ID: clip(a.Event.ID, mostText)}
	}
	if a.Scim != nil {
		a.Scim = &wire.AuditSCIM{Provider: clip(a.Scim.Provider, mostText), Token: clip(a.Scim.Token, mostText)}
	}
	if a.Refresh != nil {
		a.Refresh = &wire.AuditRefresh{Provider: clip(a.Refresh.Provider, mostText)}
	}
	return a
}

// entry is what Write and a Writer keep, and where. Its texts are cut (#6755).
func entry(actor wire.AuditActor, method, action string, host string, target *string, at time.Time) (stored, string) {
	actor, method, action, host = clipActor(actor), clip(method, mostText), clip(action, mostText), clip(host, mostText)
	if target != nil {
		target = new(clip(*target, mostTargetText))
	}
	e := stored{Kind: Kind, Action: action, Method: method, Target: target, At: records.At(at), Actor: &actor}
	if actor.Client != nil {
		e.By = string(*actor.Client)
	}
	if host != "" {
		e.Host = &host
	}
	return e, Key(at, actor.User)
}

// decode reads one stored event, by its id in the log, as an entry. The control plane's own events between
// copies (clientPaired, hostRemoved, …) are not entries: the call that made each is.
func decode(id string, data []byte, owner func(by string) wire.AuditActor) (wire.AuditEntry, bool) {
	var s stored
	if json.Unmarshal(data, &s) != nil || s.At.IsZero() {
		return wire.AuditEntry{}, false
	}
	e := wire.AuditEntry{ID: strings.TrimSuffix(id, ".json"), At: wire.DateOf(s.At.Time),
		Action: s.Action, Method: s.Method, Target: s.Target}
	if s.Host != nil {
		h := wire.MachineRef(*s.Host)
		e.Host = &h
	}
	if s.Kind != Kind {
		return wire.AuditEntry{}, false
	}
	if s.Actor != nil {
		e.Actor = *s.Actor
	} else {
		e.Actor = owner(s.By)
	}
	return e, true
}

// Query is a list or an export asked for: the range, and whose entries. Only is set for
// a caller who sees only their own.
type Query struct {
	Since, Until time.Time
	User         string
	Limit        int
	Oldest       bool
}

// NewQuery is a wire query bounded: at most days back, at most mostLimit entries.
func NewQuery(q wire.AuditQuery, days int, now time.Time, export bool) Query {
	out := Query{Since: now.Add(-time.Duration(days) * 24 * time.Hour), Until: now, Limit: defaultLimit}
	if q.Since != nil && q.Since.Time().After(out.Since) {
		out.Since = q.Since.Time()
	}
	if q.Until != nil && q.Until.Time().Before(out.Until) {
		out.Until = q.Until.Time()
	}
	if q.User != nil {
		out.User = *q.User
	}
	if q.Limit != nil && *q.Limit > 0 {
		out.Limit = int(*q.Limit)
	}
	out.Limit = min(out.Limit, mostLimit)
	if export {
		out.Limit, out.Oldest = mostExported, true
	}
	return out
}

// List is the entries in the query's range, newest first (or oldest first for an
// export), and whether more were left out. It reads a day's keys at a time, from the
// newest day, and stops once it has enough. A query for one person lists and reads only
// that person's folder of each day, never the team's entries (#2051).
func (l *Log) List(ctx context.Context, q Query, owner func(by string) wire.AuditActor) ([]wire.AuditEntry, bool, error) {
	log, err := l.entries()
	if err != nil {
		return nil, false, err
	}
	out := []wire.AuditEntry{}
	for r, err := range log.Range(ctx, q.Since, q.Until, partition(q.User), !q.Oldest) {
		if err != nil {
			return nil, false, err
		}
		e, ok := decode(r.ID, r.Value, owner)
		if !ok {
			continue
		}
		at := e.At.Time()
		if at.Before(q.Since) || at.After(q.Until) || q.User != "" && e.Actor.User != q.User {
			continue
		}
		if len(out) == q.Limit {
			return out, true, nil
		}
		out = append(out, e)
	}
	return out, false, nil
}

// Lines is entries as JSON lines (FR-041's export).
func Lines(entries []wire.AuditEntry) string {
	var b bytes.Buffer
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// Expired says whether an event's key is older than days before now: what the sweep
// deletes.
func Expired(key string, days int, now time.Time) bool {
	return KeyDay(key) < Cutoff(days, now)
}

// KeyDay is the day in an event's key.
func KeyDay(key string) string {
	day, _, _ := strings.Cut(strings.TrimPrefix(key, Prefix), "/")
	return day
}

// Cutoff is the first day kept: every day before it has expired.
func Cutoff(days int, now time.Time) string {
	return now.Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(dayLayout)
}

// DayFolder is the folder of a day's entries, every person's.
func DayFolder(day string) string { return dayPrefix(day, "") }

// NextDay is the day after day, or "" for one that is not a day.
func NextDay(day string) string {
	t, err := time.Parse(dayLayout, day)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 0, 1).Format(dayLayout)
}
