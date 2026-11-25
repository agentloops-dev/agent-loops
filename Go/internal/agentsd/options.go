package agentsd

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/acp"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
	sdk "github.com/coder/acp-go-sdk"
)

// What a new session's form offers (DaemonCore.options): agents/options asks the runtime
// for its options and commands, and keeps them in <cache>/option-cache.json as the Swift
// host does, so the next form shows them at once and is told of any change after.
//
// The Swift host keeps the session it opened, and the start takes it. This host opens one
// only to ask, and lets it go: the start opens its own (#1136).

// optionBook is the drafts a form holds open, under optionMu, and the remembered offers.
type optionBook struct {
	optionCache *store.JSONFile[optionEntry]
	optionMu    sync.Mutex
	openDrafts  map[wire.UUID]bool
	// offersOf asks a runtime what it offers in a folder; nil is whatItOffers (a test's seam).
	offersOf func(rt *runtimeDef, path string) (optionEntry, error)
}

// optionEntry is OptionCache.Entry.
type optionEntry struct {
	Options  []wire.ConfigOption `json:"options"`
	Commands []wire.SlashCommand `json:"commands"`
	SavedAt  wire.Date           `json:"savedAt"`
}

func (e optionEntry) worthKeeping() bool { return len(e.Options) > 0 || len(e.Commands) > 0 }

// commandsWait is how long a new session waits for its commands, which come as an update a
// moment after the session itself (whatItOffers).
var commandsWait = 500 * time.Millisecond

func (d *Daemon) optionCachePath() string {
	return filepath.Join(d.Store.Loc.Cache, "option-cache.json")
}

// optionKey is OptionCache.key: the runtime, the folder and the servers' names.
func optionKey(runtimeID string, cwd wire.URL, servers []wire.MCPServer) string {
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, mcpServerName(s))
	}
	sort.Strings(names)
	return runtimeID + "\t" + filepath.Clean(store.FolderPath(cwd)) + "\t" + strings.Join(names, ",")
}

func mcpServerName(s wire.MCPServer) string {
	b, _ := json.Marshal(s)
	var v struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(b, &v) // b is our own Marshal of s
	return v.Name
}

// openOptionCache opens the remembered offers. A runtime says them again, so the oldest go.
func (d *Daemon) openOptionCache() *store.JSONFile[optionEntry] {
	return smallFile(d, store.Spec{Name: "option-cache.json", Kind: "cache", Scope: "machine",
		Bounds: store.Bounds{MaxRecordBytes: 256 << 10, MaxCount: 50, MaxAge: store.MaxAge, Action: store.EvictOldest}},
		d.optionCachePath, func(e *optionEntry) *wire.Date { return &e.SavedAt })
}

func (d *Daemon) remembered(key string) (optionEntry, bool) {
	cache, err := d.optionCache.Read()
	if err != nil {
		return optionEntry{}, false
	}
	e, ok := cache[key]
	return e, ok && e.worthKeeping()
}

// runtimeOffers is the options a runtime last offered in any folder, newest first, or nil:
// what a start's model is matched with before its session says its own (#2274).
func (d *Daemon) runtimeOffers(runtimeID string) []wire.ConfigOption {
	cache, err := d.optionCache.Read()
	if err != nil {
		return nil
	}
	var newest optionEntry
	for k, e := range cache {
		if strings.HasPrefix(k, runtimeID+"\t") && len(e.Options) > 0 && e.SavedAt > newest.SavedAt {
			newest = e
		}
	}
	return newest.Options
}

// refreshOffers asks the runtime what it offers now in the folder and keeps it (#6891): Auto
// does this before it passes a runtime by for a model its cached offer lacks.
func (d *Daemon) refreshOffers(rt *runtimeDef, cwd wire.URL) ([]wire.ConfigOption, error) {
	d.optionMu.Lock()
	ask := d.offersOf
	d.optionMu.Unlock()
	if ask == nil {
		ask = d.whatItOffers
	}
	e, err := ask(rt, store.FolderPath(cwd))
	if err != nil {
		return nil, err
	}
	if len(e.Options) == 0 {
		return nil, &Error{Code: CodeRuntimeWillNotStart, Message: rt.Name + " said no options"}
	}
	d.remember(optionKey(rt.ID, cwd, nil), e)
	return e.Options, nil
}

// remember keeps what a runtime offered. Of one that is too large nothing is kept, so a form
// does not show an earlier offer for it.
func (d *Daemon) remember(key string, e optionEntry) {
	if !e.worthKeeping() {
		return
	}
	e.SavedAt = now()
	err := os.MkdirAll(d.Store.Loc.Cache, 0o755) //nolint:gosec // G301: the mode this folder always had
	if err == nil {
		err = d.optionCache.Update(func(m map[string]optionEntry) error { m[key] = e; return nil })
	}
	if errors.Is(err, store.ErrLimit) {
		err = errors.Join(err, d.optionCache.Update(func(m map[string]optionEntry) error { delete(m, key); return nil }))
	}
	if err != nil {
		log.Printf("option-cache.json: could not save: %v", err)
	}
}

// options is agents/options.
func (d *Daemon) options(r wire.OptionsRequest) (wire.OptionsResponse, error) {
	rt := runtimeByID(r.RuntimeID)
	if rt == nil {
		return wire.OptionsResponse{}, &Error{Code: CodeRuntimeNotFound, Message: "no runtime " + r.RuntimeID}
	}
	path := store.FolderPath(r.Cwd)
	if !isDir(path) {
		return wire.OptionsResponse{}, &Error{Code: CodeFolderGone, Message: path + " is not there any more."}
	}
	if err := d.needsSignIn(r.RuntimeID); err != nil {
		return wire.OptionsResponse{}, err
	}
	draft := store.NewUUID()
	d.optionMu.Lock()
	if d.openDrafts == nil {
		d.openDrafts = map[wire.UUID]bool{}
	}
	d.openDrafts[draft] = true
	d.optionMu.Unlock()
	key := optionKey(r.RuntimeID, r.Cwd, r.McpServers)
	if shown, ok := d.remembered(key); ok {
		// Shown at once; the runtime is asked after, and a change is said (settleDraft).
		d.goJob("draft options", func() { d.settleDraft(draft, key, rt, path, shown) })
		return wire.OptionsResponse{DraftID: draft, Options: d.markOptions(r.RuntimeID, shown.Options), Commands: shown.Commands}, nil
	}
	e, err := d.whatItOffers(rt, path)
	if err != nil {
		d.closeDraft(draft)
		return wire.OptionsResponse{}, err
	}
	d.remember(key, e)
	return wire.OptionsResponse{DraftID: draft, Options: d.markOptions(r.RuntimeID, e.Options), Commands: e.Commands}, nil
}

func (d *Daemon) settleDraft(draft wire.UUID, key string, rt *runtimeDef, path string, shown optionEntry) {
	e, err := d.whatItOffers(rt, path)
	if err != nil {
		if d.closeDraft(draft) {
			msg := err.Error()
			if e, ok := err.(*Error); ok {
				msg = e.Message
			}
			Broadcast(d, wire.NotifyAgentsDraftOptions, wire.DraftOptionsNotification{DraftID: draft,
				Options: []wire.ConfigOption{}, Commands: []wire.SlashCommand{}, Failure: &msg})
		}
		return
	}
	d.remember(key, e)
	d.optionMu.Lock()
	open := d.openDrafts[draft]
	d.optionMu.Unlock()
	if !open || (sameJSON(e.Options, shown.Options) && sameJSON(e.Commands, shown.Commands)) {
		return
	}
	Broadcast(d, wire.NotifyAgentsDraftOptions, wire.DraftOptionsNotification{DraftID: draft, Options: d.markOptions(rt.ID, e.Options), Commands: e.Commands})
}

// closeDraft lets a draft go (agents/discardDraft, or the start that used it), and says
// whether it was open.
func (d *Daemon) closeDraft(draft wire.UUID) bool {
	d.optionMu.Lock()
	defer d.optionMu.Unlock()
	open := d.openDrafts[draft]
	delete(d.openDrafts, draft)
	return open
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// whatItOffers opens a session in the folder, with the folder's servers and the
// person's, and reads what it offers: its options with the session, its commands a moment
// after.
func (d *Daemon) whatItOffers(rt *runtimeDef, path string) (optionEntry, error) {
	launch, err := d.launchFor(rt, path)
	if err != nil {
		return optionEntry{}, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 90*time.Second)
	defer cancel()
	p := &probe{commands: make(chan []wire.SlashCommand, 1)}
	meta := sessionMeta(rt.ID)
	s, err := acp.Start(ctx, launch, p, d.sessionMCP(path), meta, meta, "", "")
	if err != nil {
		return optionEntry{}, &Error{Code: CodeRuntimeWillNotStart, Message: rt.Name + " did not start: " + err.Error()}
	}
	defer s.Stop()
	e := optionEntry{Options: []wire.ConfigOption{}, Commands: []wire.SlashCommand{}}
	if opts := configOptions(s.NewResult.ConfigOptions); opts != nil {
		e.Options = opts
	}
	select {
	case cmds := <-p.commands:
		e.Commands = cmds
	case <-time.After(commandsWait):
	}
	if opts := p.lastOptions(); opts != nil {
		e.Options = opts
	}
	// The app's own options beside the runtime's, off for a new chat (#1837).
	e.Options = withAppOptions(rt.ID, nil, e.Options)
	return e, nil
}

// probe is the session whatItOffers opens: it hears the offers, and refuses anything else.
type probe struct {
	mu       sync.Mutex
	options  []wire.ConfigOption
	commands chan []wire.SlashCommand
}

func (p *probe) Update(n sdk.SessionNotification) {
	u := n.Update
	switch {
	case u.ConfigOptionUpdate != nil:
		if opts := configOptions(u.ConfigOptionUpdate.ConfigOptions); opts != nil {
			p.mu.Lock()
			p.options = opts
			p.mu.Unlock()
		}
	case u.AvailableCommandsUpdate != nil:
		b, _ := json.Marshal(u.AvailableCommandsUpdate.AvailableCommands)
		var cmds []wire.SlashCommand
		if json.Unmarshal(b, &cmds) == nil && cmds != nil {
			select {
			case p.commands <- cmds:
			default:
			}
		}
	}
}

func (p *probe) lastOptions() []wire.ConfigOption {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.options
}

func (p *probe) Permission(context.Context, sdk.RequestPermissionRequest) string { return "" }
func (p *probe) Served(string, string)                                           {}
func (p *probe) Elicitation(context.Context, json.RawMessage) (string, map[string]any) {
	return "cancel", nil
}
func (p *probe) ElicitationComplete(string) {}

func (d *Daemon) registerOptions() {
	On(d, wire.CallAgentsOptions, func(ctx context.Context, c *Client, r wire.OptionsRequest) (wire.OptionsResponse, error) {
		return d.options(r)
	})
	On(d, wire.CallAgentsDiscardDraft, func(ctx context.Context, c *Client, r wire.DiscardDraftRequest) (wire.Empty, error) {
		d.closeDraft(r.DraftID)
		return wire.Empty{}, nil
	})
}
