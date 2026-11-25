package agentsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/mcpcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/envfile"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The user scope on this machine (#1760, spec 736 US4): machine/userSettings, which the
// control plane sends on channel 0 when this machine joins and when its person's settings
// or secrets change. Each part goes into the file this machine already reads:
//
//	mcpServers    ~/.agents/mcp.json's mcpServers
//	agentConfig   ~/.agents/AGENTS.md, ~/.agents/personas/<name>.md, ~/.agents/skills/<name>/SKILL.md
//	runtimes      <settings>/sandbox.json and <settings>/client-permissions.json
//	costLimits    <settings>/limits.json
//	notifications <settings>/display.json's noticeBurst and noticeCooldownMinutes (#2859)
//	resources     <root>/resources.json
//	secrets       ~/.agents-secrets/sealed.json, sealed (#4177)
//	environment   ~/.agents/env, unless it was changed by hand here (#5259, environment.go)
//	person        <settings>/person.json, unless this machine already says the same (#5573)
//	auto          <settings>/auto.json, as auto/set keeps it (#5573)
//	retention     <settings>/retention.json, as a confirmed retention/set (#5573)
//
// <root>/user-settings-sync.json stores sync metadata: secret names and the agentConfig
// files written. A machine does not send its own agent configuration files to the person's
// other machines, nor write the ones a control plane sends (agentConfigFiles), so one
// machine cannot change what runs on the others.

const (
	userSettingsSyncFile = "user-settings-sync.json"
	// userMarkMostBytes bounds that file.
	userMarkMostBytes = 1 << 20
	// The files that the agentConfig part writes: one, and all of them.
	agentConfigFileMost  = 1 << 20
	agentConfigFilesMost = 1000
)

// userSettingsSyncState never holds a secret's value.
type userSettingsSyncState struct {
	User    string   `json:"user,omitempty"`
	Secrets []string `json:"secrets"`
	// Versions is the version of each secret opened here (#3397): an older one, which a
	// control plane could send back after the person changed it, is refused.
	Versions map[string]int64 `json:"versions,omitempty"`
	// About is what list_secrets says of each secret in Secrets (#4167): its kind, description
	// and Ask before use, as the control plane sent them. Waiting is the person's secrets that
	// are not sealed to this machine yet. Never a value.
	About   []wire.UserSecretAbout `json:"about,omitempty"`
	Waiting []wire.UserSecretAbout `json:"waiting,omitempty"`
	// The agentConfig files written, relative to ~/.agents, so a persona or skill the
	// control plane no longer sends is taken away again.
	Files []string `json:"files,omitempty"`
	// TimeZone is the person's timeZone part (#4818): the zone of a workflow's hours: with
	// no time-zone:. "" is none, and this host's own zone.
	TimeZone string `json:"timeZone,omitempty"`
	// EnvDigest is the SHA-256 of what the environment part last wrote to ~/.agents/env
	// (#5259), so an edit by hand is told from it; EnvText is that text, for Discard. A value
	// that looks like a secret is refused by the control plane, so neither holds one.
	EnvDigest string `json:"envDigest,omitempty"`
	EnvText   string `json:"envText,omitempty"`
}

type userScopeState struct {
	// agentOffersMu makes one offer of an agent's secret at a time (#5308).
	agentOffersMu sync.Mutex
	// offersRunMu guards offersRunning and offersAgain: one offerWaitingAgentSecrets runs at a
	// time, and a delivery while it runs asks for one more run after it (#6626).
	offersRunMu   sync.Mutex
	offersRunning bool
	offersAgain   bool
	// personMu is person.json's (person/set and the part person, #5573).
	personMu    sync.Mutex
	userScopeMu sync.Mutex
	userMark    *userSettingsSyncState
	personTZ    atomic.Pointer[string]
}

// agentConfigName is a persona's or a skill's name, as one folder or file name.
var agentConfigName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (d *Daemon) userSettingsSyncPath() string {
	return filepath.Join(d.Store.Loc.Root, userSettingsSyncFile)
}

// userMarkLocked is the mark, read once.
func (d *Daemon) userMarkLocked() userSettingsSyncState {
	if d.userMark == nil {
		var m userSettingsSyncState
		b, err := os.ReadFile(d.userSettingsSyncPath())
		if err != nil {
			// Read the old metadata once, so its secret names and files survive the upgrade.
			b, _ = os.ReadFile(filepath.Join(d.Store.Loc.Root, "from-control-plane.json"))
		}
		if len(b) > 0 {
			if err := json.Unmarshal(b, &m); err != nil {
				log.Printf("user settings: could not read the sync state: %v", err)
			}
		}
		d.userMark = &m
	}
	return *d.userMark
}

// toldUserSettings is machine/userSettings.
func (d *Daemon) toldUserSettings(params json.RawMessage) {
	var u wire.MachineUserSettings
	if err := json.Unmarshal(params, &u); err != nil {
		log.Printf("user settings: %v", err)
		return
	}
	if err := d.ApplyUserSettings(u); err != nil {
		log.Printf("user settings: %v", err)
	}
}

// ApplyUserSettings writes what the control plane sent. Its errors never hold a secret.
func (d *Daemon) ApplyUserSettings(u wire.MachineUserSettings) error {
	// A person's Set, Replace… and Remove… in Settings ▸ Secrets (#4156), raised once the lock
	// is let go. The control plane's audit has them already, from the person's own call.
	var changes []secretChange
	envChanged := false
	defer func() {
		if envChanged {
			d.tellEnvChanged("", true)
		}
		for _, c := range changes {
			d.secretChanged("", c.name, c.what, false)
		}
		// What this machine holds now, with each hint and last operation (#4158).
		d.goJob("tellSecretsHeld", func() { d.tellSecretsHeld() })
		// An agent's secret here that is not offered yet goes to the person's apps (#5308).
		d.goJob("offerWaitingAgentSecrets", func() { d.offerWaitingAgentSecrets() })
	}()
	d.userScopeMu.Lock()
	defer d.userScopeMu.Unlock()
	before := d.userMarkLocked()
	mark := userSettingsSyncState{User: u.User, Secrets: []string{}}
	var problems []string
	note := func(part wire.UserSettingsPartName, err error) {
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", part, err))
		}
	}
	home := d.personalHome()
	s := u.Settings
	envChanged = d.applyUserSettingsPartsLocked(home, s, before, &mark, note)
	// timeZone (#4818): the zone of a workflow's hours: with no time-zone:.
	mark.TimeZone = zoneOfPart(s.TimeZone)
	opened, flagged, versions, kept, hints := d.openUserSecrets(u, before)
	coming := map[string]bool{}
	for _, w := range u.Waiting {
		coming[string(w.Name)] = true
	}
	names, err := d.syncUserSecrets(home, opened, flagged, before.Secrets, kept, u.OffersWaiting, coming)
	for i := range opened {
		opened[i].Value = ""
	}
	for i := range flagged {
		flagged[i].Value = ""
	}
	if err != nil {
		// This machine's key that does not read is said once for each change (#6632), not
		// at each delivery.
		if !isMachineKeyError(err) {
			problems = append(problems, "secrets: "+err.Error())
		}
		names, versions = before.Secrets, before.Versions
	}
	mark.Secrets, mark.Versions = names, versions
	if err == nil {
		keepSecretHintsHere(home, hints, names)
	}
	if err == nil && before.User == u.User {
		changes = userSecretChanges(before, mark)
		// A name an agent's secret keeps here while its offer waits (#5308) was not removed.
		held := sealedSecretsHere(home)
		changes = slices.DeleteFunc(changes, func(c secretChange) bool {
			_, ok := held[c.name]
			return c.what == wire.SecretOperationWhatRemoved && ok
		})
	}
	mark.About, mark.Waiting = secretsAbout(u.Secrets, names), u.Waiting
	if err != nil {
		mark.About = before.About
	}
	if err := d.writeUserMark(mark); err != nil {
		problems = append(problems, err.Error())
	} else {
		_ = os.Remove(filepath.Join(d.Store.Loc.Root, "from-control-plane.json"))
	}
	d.userMark = &mark
	zone := mark.TimeZone
	d.personTZ.Store(&zone)
	log.Printf("user settings synced from the control plane: %d secrets", len(mark.Secrets))
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// saveUserMarkLocked writes the mark and keeps it. Called with userScopeMu held.
func (d *Daemon) saveUserMarkLocked(mark userSettingsSyncState) error {
	if err := d.writeUserMark(mark); err != nil {
		return err
	}
	d.userMark = &mark
	return nil
}

func (d *Daemon) writeUserMark(mark userSettingsSyncState) error {
	b, err := diskjson.Marshal(mark)
	if err != nil {
		return err
	}
	if err := settingFits(d.userSettingsSyncPath(), b, userMarkMostBytes); err != nil {
		return err
	}
	// store-bounds: kind=key-value; count=1; size=1MiB; age=until-deleted; applied=on write (refused)
	return store.WriteAtomically(d.userSettingsSyncPath(), b, 0o644)
}

func partSent(v wire.JSONValue) bool { return len(v) > 0 && string(v) != "null" }

// writeMCPServers is mcpServers as ~/.agents/mcp.json, with the file's other keys kept. A new or
// changed server waits for the person's approval here (syncedconnectors.go).
func (d *Daemon) writeMCPServers(home string, servers wire.JSONValue) error {
	if home == "" {
		return errors.New("this machine has no personal home")
	}
	var check map[string]json.RawMessage
	if json.Unmarshal(servers, &check) != nil {
		return errors.New("not an object")
	}
	path, err := safeConfigPath(filepath.Join(home, ".agents"), "mcp.json", true)
	if err != nil {
		return err
	}
	file := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil {
		// Its other keys are kept, so a file that cannot be read is not written over.
		if err := json.Unmarshal(b, &file); err != nil {
			return fmt.Errorf("%s could not be read: %w", path, err)
		}
		if file == nil {
			file = map[string]json.RawMessage{}
		}
	}
	held := file["mcpServers"]
	file["mcpServers"] = json.RawMessage(servers)
	b, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := settingFits(path, b, mcpcatalog.MostBytes); err != nil {
		return err
	}
	on, readErr := os.ReadFile(path)
	if err := d.writeSyncedServers(held, servers, func() error { return writeIfChanged(path, b) }); err != nil {
		return err
	}
	if readErr == nil && bytes.Equal(on, b) {
		return nil
	}
	d.goJob("personal connectors", d.tellPersonalConnectors)
	return nil
}

// writeIfChanged writes a file only when its bytes on disk differ, so a delivery that
// changes one part does not write and fsync every file of the others again (#2729).
func writeIfChanged(path string, b []byte) error {
	if on, err := os.ReadFile(path); err == nil && bytes.Equal(on, b) {
		return nil
	}
	// store-bounds: kind=key-value; count=1000; size=1MiB a file; age=until-deleted; applied=on write (refused)
	return store.WriteAtomically(path, b, 0o644)
}

// writeAgentConfig writes the instructions, personas and skills, and takes away the files
// it wrote before that are no longer sent. It gives the files it wrote.
func writeAgentConfig(home string, raw wire.JSONValue, wroteBefore []string) ([]string, error) {
	if home == "" {
		return wroteBefore, errors.New("this machine has no personal home")
	}
	var config wire.UserAgentConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return wroteBefore, errors.New("not an agent configuration")
	}
	files := map[string]string{}
	if config.Instructions != nil {
		files["AGENTS.md"] = *config.Instructions
	}
	for name, text := range config.Personas {
		if !agentConfigName.MatchString(name) || strings.Contains(name, "..") {
			return wroteBefore, fmt.Errorf("%q is not a persona's name", name)
		}
		files[filepath.Join("personas", name+".md")] = text
	}
	for name, text := range config.Skills {
		if !agentConfigName.MatchString(name) || strings.Contains(name, "..") {
			return wroteBefore, fmt.Errorf("%q is not a skill's name", name)
		}
		files[filepath.Join("skills", name, "SKILL.md")] = text
	}
	base := filepath.Join(home, ".agents")
	// Refused whole, before any file is written.
	if len(files) > agentConfigFilesMost {
		return wroteBefore, fmt.Errorf("%d files, at most %d: %w", len(files), agentConfigFilesMost, store.ErrLimit)
	}
	for rel, text := range files {
		if err := settingFits(filepath.Join(base, rel), []byte(text), agentConfigFileMost); err != nil {
			return wroteBefore, fmt.Errorf("%s: %w", rel, err)
		}
	}
	var wrote []string
	for rel, text := range files {
		path := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return wroteBefore, err
		}
		if err := writeIfChanged(path, []byte(text)); err != nil {
			return wroteBefore, err
		}
		wrote = append(wrote, rel)
	}
	for _, rel := range wroteBefore {
		if _, still := files[rel]; still || !safeRelative(rel) {
			continue
		}
		os.Remove(filepath.Join(base, rel))
		if strings.HasPrefix(rel, "skills"+string(filepath.Separator)) {
			os.Remove(filepath.Dir(filepath.Join(base, rel))) // only when it is empty
		}
	}
	sort.Strings(wrote)
	return wrote, nil
}

// agentConfigWrote is what the agentConfig part can have written of the files a mark names.
// A mark from before agentConfigFiles were refused also names files other machines sent
// (mcp.json, plugins and any file of a skill); those stay, as this machine's own.
func agentConfigWrote(files []string) []string {
	var out []string
	for _, rel := range files {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if rel == "AGENTS.md" || len(parts) == 2 && parts[0] == "personas" && strings.HasSuffix(parts[1], ".md") ||
			len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md" {
			out = append(out, rel)
		}
	}
	return out
}

// safeRelative is a path the mark may name under ~/.agents.
func safeRelative(rel string) bool {
	clean := filepath.Clean(rel)
	if clean != rel || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return false
	}
	return rel == "AGENTS.md" || rel == "mcp.json" || strings.HasPrefix(rel, "personas"+string(filepath.Separator)) ||
		strings.HasPrefix(rel, "skills"+string(filepath.Separator)) || strings.HasPrefix(rel, "plugins"+string(filepath.Separator))
}

func safeAgentConfigPath(rel string) bool {
	if !safeRelative(rel) {
		return false
	}
	if rel == "mcp.json" {
		return true
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 1 {
		return parts[0] == "AGENTS.md"
	}
	for _, part := range parts {
		switch strings.ToLower(part) {
		case "node_modules", ".git", ".cache", "cache", "caches", "dist", "build", "target", "vendor", "__pycache__":
			return false
		}
	}
	if len(parts) < 2 || !agentConfigName.MatchString(parts[1]) || strings.Contains(parts[1], "..") {
		return false
	}
	if parts[0] == "personas" {
		return len(parts) == 2 && strings.HasSuffix(parts[1], ".md")
	}
	return len(parts) >= 3
}

func safeConfigPath(base, rel string, createParents bool) (string, error) {
	if !safeAgentConfigPath(rel) {
		return "", errors.New("invalid agent configuration path")
	}
	if createParents {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", err
		}
	}
	if info, err := os.Lstat(base); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("~/.agents is not a regular folder")
		}
	} else if !errors.Is(err, os.ErrNotExist) || createParents {
		return "", err
	}
	parts := strings.Split(filepath.FromSlash(rel), string(filepath.Separator))
	current := base
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		if createParents {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
		}
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && !createParents {
				return filepath.Join(base, filepath.FromSlash(rel)), nil
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("agent configuration path crosses a link or file")
		}
	}
	target := filepath.Join(current, parts[len(parts)-1])
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("agent configuration file is a link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return target, nil
}

// userRuntimes is the runtimes part: each runtime's choices that follow the person.
// Sign-ins stay on each machine.
type userRuntimes struct {
	Sandbox           *wire.SandboxSettings          `json:"sandbox,omitempty"`
	ClientPermissions *wire.ClientPermissionSettings `json:"clientPermissions,omitempty"`
}

func (d *Daemon) writeRuntimeChoices(raw wire.JSONValue) error {
	var r userRuntimes
	if err := json.Unmarshal(raw, &r); err != nil {
		return errors.New("not runtime choices")
	}
	if err := os.MkdirAll(d.SettingsDir, 0o755); err != nil {
		return err
	}
	if r.Sandbox != nil {
		if err := d.writeSetting("sandbox.json", r.Sandbox); err != nil {
			return err
		}
	}
	if r.ClientPermissions != nil {
		if err := d.writeSetting("client-permissions.json", r.ClientPermissions); err != nil {
			return err
		}
	}
	return nil
}

// applyPersonPart is the part person (#5573): what agents call the person, with the name
// their Mac resolved. A machine whose own settings already give that name and those
// pronouns keeps them as they are, so the Mac's own "the account's first name" stays.
func (d *Daemon) applyPersonPart(raw wire.JSONValue) error {
	var p wire.PersonSettings
	if err := json.Unmarshal(raw, &p); err != nil {
		return errors.New("not a person's settings")
	}
	d.personMu.Lock()
	here := readPerson(d.personFile())
	d.personMu.Unlock()
	if personSays(here) == personSays(p) {
		return nil
	}
	_, err := d.setPerson(p)
	return err
}

// personSays is what an agent is told of the person: the effective name and the pronouns.
func personSays(p wire.PersonSettings) [2]string {
	pronouns := ""
	if p.Pronouns != nil {
		pronouns = strings.TrimSpace(*p.Pronouns)
	}
	return [2]string{personName(p.Name), pronouns}
}

// applyAutoPart is the part auto (#5573): Auto's settings, kept as auto/set keeps them.
func (d *Daemon) applyAutoPart(raw wire.JSONValue) error {
	var a wire.AutoSettings
	if err := json.Unmarshal(raw, &a); err != nil {
		return errors.New("not Auto's settings")
	}
	if reflect.DeepEqual(normalAuto(a), d.autoSettings()) {
		return nil
	}
	_, err := d.setAutoSettings(a)
	return err
}

// applyRetentionPart is the part retention (#5573): how long archived agents are kept, as
// a confirmed retention/set, since the person confirmed it on their Mac.
func (d *Daemon) applyRetentionPart(raw wire.JSONValue) error {
	var r wire.RetentionSettings
	if err := json.Unmarshal(raw, &r); err != nil {
		return errors.New("not retention settings")
	}
	if d.retentionState().Settings.KeepFor == r.KeepFor {
		return nil
	}
	_, err := d.setRetention(wire.RetentionSetRequest{Settings: r, Confirmed: true})
	return err
}

// applyYoursFromControlPlane is the part policy from the control plane (#2957, research
// R11): a copy of your layer, applied as a non-keeper machine does (applySignedYours),
// pinning the person key on first use when this machine has none pinned yet.
func (d *Daemon) applyYoursFromControlPlane(raw wire.JSONValue) error {
	var signed wire.SignedYourPolicy
	if err := json.Unmarshal(raw, &signed); err != nil {
		return errors.New("not a signed policy")
	}
	_, err := d.applySignedYours(signed, controllink.PinFromControlPlane)
	if err != nil {
		log.Printf("policy: the control plane's copy of your layer: %v", err)
	}
	return nil // never fails the whole delivery: a bad copy is logged and ignored (FR-011)
}

// openUserSecrets opens each secret the person's client sealed to this machine (#3397):
// the values, those with Ask before use apart (#4014), the version of each, and the names to
// keep as they are in the file, those whose version here is newer than the one sent. One
// that does not open is left out: a flag the control plane changed does not open.
func (d *Daemon) openUserSecrets(u wire.MachineUserSettings, before userSettingsSyncState) (plain, flagged []wire.UserSecret, versions map[string]int64, kept []string, hints map[string]secretHintRecord) {
	d.orgRouteMu.Lock()
	id := d.identityLocked()
	d.orgRouteMu.Unlock()
	versions = map[string]int64{}
	hints = map[string]secretHintRecord{}
	for _, s := range u.Secrets {
		name := string(s.Name)
		if had := before.Versions[name]; s.Version < had && slices.Contains(before.Secrets, name) {
			log.Printf("user settings: %s version %d is older than the %d here; kept the one here", name, s.Version, had)
			versions[name] = had
			kept = append(kept, name)
			continue
		}
		if id == nil {
			log.Printf("user settings: %s: this machine has not joined a control plane", name)
			continue
		}
		ask := s.NeedsApproval != nil && *s.NeedsApproval
		ad := controllink.UserSecretADAsk(u.User, controllink.MachineRecipient(id.host), name, s.Version, ask)
		value, err := controllink.Open(id.private, controllink.UserSecretPurpose, ad, s.Sealed)
		if err != nil {
			log.Printf("user settings: %s version %d does not open here: %v", name, s.Version, err)
			continue
		}
		opened := wire.UserSecret{Name: s.Name, Value: string(value)}
		hints[name] = secretHintRecord{Version: s.Version, Hint: secretHint(opened.Value, s.Kind)}
		clear(value)
		if ask {
			flagged = append(flagged, opened)
		} else {
			plain = append(plain, opened)
		}
		versions[name] = s.Version
	}
	return plain, flagged, versions, kept, hints
}

// secretsAbout is what list_secrets says of each sent secret that this machine keeps (#4167).
func secretsAbout(sent []wire.SealedUserSecret, kept []string) []wire.UserSecretAbout {
	var out []wire.UserSecretAbout
	for _, s := range sent {
		if !slices.Contains(kept, string(s.Name)) {
			continue
		}
		out = append(out, wire.UserSecretAbout{Name: s.Name, Kind: s.Kind, Description: s.Description, NeedsApproval: s.NeedsApproval})
	}
	return out
}

var secretNameHere = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// registerUserScope is settings/userPart: a part as this machine has it, for Use This
// Mac's under You.
func (d *Daemon) registerUserScope() {
	On(d, wire.CallSettingsUserPart, func(ctx context.Context, c *Client, r wire.UserPartRequest) (wire.UserPartHere, error) {
		return d.userPartHere(r.Part)
	})
}

func (d *Daemon) userPartHere(part wire.UserSettingsPartName) (wire.UserPartHere, error) {
	out := wire.UserPartHere{Part: part}
	home := d.personalHome()
	switch part {
	case wire.UserSettingsPartNameMcpServers:
		servers := json.RawMessage(`{}`)
		if home != "" {
			var file struct {
				MCPServers json.RawMessage `json:"mcpServers"`
			}
			if b, err := os.ReadFile(filepath.Join(home, ".agents", "mcp.json")); err == nil && json.Unmarshal(b, &file) == nil && len(file.MCPServers) > 0 {
				servers = file.MCPServers
			}
		}
		if err := tokensInClear(servers); err != nil {
			return out, err
		}
		out.Value = servers
	case wire.UserSettingsPartNameAgentConfig:
		config, left := agentConfigHere(home)
		out.Value, out.LeftOut = asJSON(config), left
	case wire.UserSettingsPartNameRuntimes:
		out.Value = asJSON(userRuntimes{Sandbox: new(d.sandboxSettings()), ClientPermissions: new(d.clientPermissions())})
	case wire.UserSettingsPartNameCostLimits:
		out.Value = asJSON(d.costState().Limits)
	default:
		return out, Invalid("There is no part called %q.", part)
	}
	return out, nil
}

// secretLooking is a header or variable whose value is a secret by its name.
var secretLooking = envfile.SecretLookingName

// tokensInClear refuses servers with a secret written in clear in an env or a header: in
// the user settings it would be kept unsealed. Its words name the server and the key,
// never the value.
func tokensInClear(servers json.RawMessage) error {
	var list map[string]struct {
		Env     map[string]string `json:"env"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(servers, &list); err != nil {
		return Invalid("mcp.json's mcpServers cannot be read.")
	}
	names := make([]string, 0, len(list))
	for name := range list {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := list[name]
		for _, kv := range []map[string]string{s.Env, s.Headers} {
			for k, v := range kv {
				if v == "" || secretRef.MatchString(v) {
					continue
				}
				if secretLooking.MatchString(k) || strings.HasPrefix(strings.ToLower(v), "bearer ") {
					return Invalid("The server %s has %s written in clear. Put it in a secret under You, write ${NAME} in its place, and try again.", name, k)
				}
			}
		}
	}
	return nil
}

// agentConfigHere is ~/.agents' instructions, personas and one-file skills. A skill with
// other files beside its SKILL.md is left out, by name: the user settings keep text only.
func agentConfigHere(home string) (wire.UserAgentConfig, []string) {
	var config wire.UserAgentConfig
	var left []string
	if home == "" {
		return config, nil
	}
	base := filepath.Join(home, ".agents")
	if b, err := os.ReadFile(filepath.Join(base, "AGENTS.md")); err == nil {
		text := string(b)
		config.Instructions = &text
	}
	config.Personas = agentConfigPersonasHere(base)
	config.Skills, left = agentConfigSkillsHere(base)
	sort.Strings(left)
	return config, left
}

func asJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// applyUserSettingsPartsLocked writes each part of the settings that the control plane
// sent, and gives note each part's error. It says whether the environment changed.
// d.userScopeMu is held.
func (d *Daemon) applyUserSettingsPartsLocked(home string, s wire.UserSettings, before userSettingsSyncState, mark *userSettingsSyncState, note func(wire.UserSettingsPartName, error)) (envChanged bool) {
	if partSent(s.McpServers) {
		note(wire.UserSettingsPartNameMcpServers, d.writeMCPServers(home, s.McpServers))
	}
	if partSent(s.AgentConfig) {
		files, err := writeAgentConfig(home, s.AgentConfig, agentConfigWrote(before.Files))
		mark.Files = files
		note(wire.UserSettingsPartNameAgentConfig, err)
	} else {
		// Not the control plane's any more: what it wrote stays, as this machine's own.
		mark.Files = nil
	}
	if s.AgentConfigFiles != nil && len(s.AgentConfigFiles.Files) > 0 {
		log.Printf("user settings: %d agent configuration files from other machines were not written", len(s.AgentConfigFiles.Files))
	}
	if partSent(s.Runtimes) {
		note(wire.UserSettingsPartNameRuntimes, d.writeRuntimeChoices(s.Runtimes))
	}
	// costLimits and notifications go (#2957, research R11): their rules now come from
	// your layer, sent in the part policy instead and applied by applyYoursFromControlPlane.
	if partSent(s.Policy) {
		note(wire.UserSettingsPartNamePolicy, d.applyYoursFromControlPlane(s.Policy))
	}
	if partSent(s.Environment) {
		changed, err := d.writeYoursEnvLocked(home, s.Environment, before, mark)
		envChanged = changed
		note(wire.UserSettingsPartNameEnvironment, err)
	}
	// person, auto and retention (#5573): the Mac sets each once, here, in place of a call to
	// each of the person's machines.
	if partSent(s.Person) {
		note(wire.UserSettingsPartNamePerson, d.applyPersonPart(s.Person))
	}
	if partSent(s.Auto) {
		note(wire.UserSettingsPartNameAuto, d.applyAutoPart(s.Auto))
	}
	if partSent(s.Retention) {
		note(wire.UserSettingsPartNameRetention, d.applyRetentionPart(s.Retention))
	}
	return envChanged
}

// keepSecretHintsHere writes the hints of the secrets names. A name kept as it is here
// keeps its hint (#4158).
func keepSecretHintsHere(home string, hints map[string]secretHintRecord, names []string) {
	for n, h := range readSecretHints(home) {
		if _, opened := hints[n]; !opened {
			hints[n] = h
		}
	}
	writeSecretHints(home, hints, names)
}

// agentConfigPersonasHere reads the personas under base, by name, or nil when there are none.
func agentConfigPersonasHere(base string) map[string]string {
	var personas map[string]string
	entries, err := os.ReadDir(filepath.Join(base, "personas"))
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || !e.Type().IsRegular() || !agentConfigName.MatchString(name) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(base, "personas", e.Name())); err == nil {
			if personas == nil {
				personas = map[string]string{}
			}
			personas[name] = string(b)
		}
	}
	return personas
}

// agentConfigSkillsHere reads the skills under base, by name, or nil when there are none.
// It also gives the skills it left out, because they hold more than a SKILL.md.
func agentConfigSkillsHere(base string) (skills map[string]string, left []string) {
	entries, err := os.ReadDir(filepath.Join(base, "skills"))
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if !e.IsDir() || !agentConfigName.MatchString(e.Name()) {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(base, "skills", e.Name()))
		if len(files) != 1 || files[0].Name() != "SKILL.md" || !files[0].Type().IsRegular() {
			left = append(left, e.Name())
			continue
		}
		if b, err := os.ReadFile(filepath.Join(base, "skills", e.Name(), "SKILL.md")); err == nil {
			if skills == nil {
				skills = map[string]string{}
			}
			skills[e.Name()] = string(b)
		}
	}
	return skills, left
}
