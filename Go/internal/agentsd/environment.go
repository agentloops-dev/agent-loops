package agentsd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/envfile"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The person's and the repo's variables (#5259, epic #5254; decided in #5256).
//
//	~/.agents/env            yours: the environment part of the person's settings, written to
//	                         every machine of theirs by machine/userSettings (usersettings.go)
//	<folder>/.agents/env     the repo's: committed with it, so a branch can change it
//
// A runtime's process starts with the login environment, then the repo's file, then yours,
// then Agents' own variables (launchOn). Its tools, the MCP servers it starts and its ACP
// terminals inherit them, and the person's own terminal in that folder gets them (shells.go).
// Nothing else agentsd runs does: git, gh and the daemon's own MCP servers do not.

// envRel is the file's place under ~ or a folder.
var envRel = filepath.Join(".agents", "env")

func (d *Daemon) yoursEnvPath() string {
	home := d.personalHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, envRel)
}

func repoEnvPath(folder string) string { return filepath.Join(folder, envRel) }

// envFile is one file as env/list says it: each variable with its state.
func (d *Daemon) envFile(layer wire.EnvLayer, path, folder string) wire.EnvFile {
	out := wire.EnvFile{Layer: layer, Path: path, Variables: []wire.EnvVariable{}}
	if folder != "" {
		out.Folder = &folder
	}
	if path == "" {
		return out
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	out.Exists = true
	p := envfile.Parse(b)
	if p.Problem != "" {
		out.Problem = &p.Problem
	}
	policy := d.orgPolicy()
	for _, v := range p.Variables {
		state, reason := envState(v.Name, v.Value, layer == wire.EnvLayerRepo, policy)
		ev := wire.EnvVariable{Name: v.Name, Value: v.Value, Line: int64(v.Line), State: state}
		if v.Description != "" {
			ev.Description = &v.Description
		}
		if reason != "" {
			ev.Reason = &reason
		}
		out.Variables = append(out.Variables, ev)
	}
	return out
}

// envState is what a session gets of one variable, and why.
func envState(name, value string, repo bool, policy *wire.OrgPolicy) (wire.EnvVariableState, string) {
	if why := envRefusal(name, repo); why != "" {
		return wire.EnvVariableStateRefused, why
	}
	if len(value) > envfile.ValueMost {
		return wire.EnvVariableStateRefused, "The value of " + name + " is over 4 KB."
	}
	if !passThroughAllowed(policy, name) {
		return wire.EnvVariableStatePolicyBlocked, passThroughWords
	}
	if why := envfile.SecretWords(name, value); why != "" {
		return wire.EnvVariableStateSecretLooking, why
	}
	return wire.EnvVariableStateGiven, ""
}

// envGives is whether a variable in this state is set on a session.
func envGives(s wire.EnvVariableState) bool {
	return s == wire.EnvVariableStateGiven || s == wire.EnvVariableStateSecretLooking
}

// envLayers is the two files for a folder, the lower first.
func (d *Daemon) envLayers(folder string) []wire.EnvFile {
	var out []wire.EnvFile
	if folder != "" {
		out = append(out, d.envFile(wire.EnvLayerRepo, repoEnvPath(folder), folder))
	}
	return append(out, d.envFile(wire.EnvLayerYours, d.yoursEnvPath(), ""))
}

// withPersonEnv is env with the repo's variables and then yours set over it: what a session in
// folder starts with, before Agents' own.
func (d *Daemon) withPersonEnv(env []string, folder string) []string {
	for _, f := range d.envLayers(folder) {
		for _, v := range f.Variables {
			if envGives(v.State) {
				env = append(without(env, v.Name), v.Name+"="+v.Value)
			}
		}
	}
	return env
}

// envResolve is env/resolve for a folder: each name of the two files, as a session sees it.
func (d *Daemon) envResolve(folder string) wire.EnvResolved {
	out := wire.EnvResolved{Folder: folder, Variables: []wire.EnvResolvedVariable{}}
	login := map[string]bool{}
	for _, kv := range loginEnvironment() {
		if n, _, ok := strings.Cut(kv, "="); ok {
			login[n] = true
		}
	}
	byName := map[string]*wire.EnvResolvedVariable{}
	var names []string
	for _, f := range d.envLayers(folder) {
		for _, v := range f.Variables {
			r := byName[v.Name]
			if r == nil {
				r = &wire.EnvResolvedVariable{Name: v.Name, Over: []wire.EnvLayer{}}
				if login[v.Name] {
					r.Over = append(r.Over, wire.EnvLayerLogin)
				}
				byName[v.Name] = r
				names = append(names, v.Name)
			}
			if !envGives(v.State) {
				if r.Layer == nil {
					r.State, r.Reason = v.State, v.Reason
				}
				continue
			}
			if r.Layer != nil {
				r.Over = append(r.Over, *r.Layer)
			}
			layer, value := f.Layer, v.Value
			r.Layer, r.Value, r.State, r.Reason = &layer, &value, v.State, v.Reason
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out.Variables = append(out.Variables, *byName[n])
	}
	return out
}

// envList is env/list: yours, then each known folder's file (one folder only, when named).
func (d *Daemon) envList(folder string) wire.EnvList {
	out := wire.EnvList{Files: []wire.EnvFile{d.yoursEnvFile()}}
	if folder != "" {
		out.Files = append(out.Files, d.repoEnvFile(folder))
		return out
	}
	seen := map[string]bool{}
	var folders []string
	for _, p := range d.Store.Folders() {
		if f := store.FolderPath(p.Folder); !seen[f] {
			seen[f] = true
			folders = append(folders, f)
		}
	}
	sort.Strings(folders)
	for _, f := range folders {
		if _, err := os.Stat(repoEnvPath(f)); err == nil {
			out.Files = append(out.Files, d.repoEnvFile(f))
		}
	}
	return out
}

func (d *Daemon) yoursEnvFile() wire.EnvFile {
	f := d.envFile(wire.EnvLayerYours, d.yoursEnvPath(), "")
	if d.yoursEnvChangedByHand() {
		t := true
		f.ChangedByHand = &t
	}
	return f
}

func (d *Daemon) repoEnvFile(folder string) wire.EnvFile {
	f := d.envFile(wire.EnvLayerRepo, repoEnvPath(folder), folder)
	if f.Exists && envUncommitted(folder) {
		t := true
		f.Uncommitted = &t
	}
	return f
}

// envUncommitted is whether git has changes to the folder's .agents/env that are not committed:
// a new file not yet added counts. A folder that is not a git work tree has none.
func envUncommitted(folder string) bool {
	cmd := exec.Command("git", "-C", folder, "status", "--porcelain", "--", envRel) //nolint:gosec // G702: git itself, with no shell: the folder is one argument and cannot start another command
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// envDigest is how the person's settings' last write to ~/.agents/env is told from an edit by hand.
func envDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// yoursEnvChangedByHand is whether ~/.agents/env differs from what the person's settings last
// wrote to it. With no write from the settings yet, the file is the person's own and not "changed".
func (d *Daemon) yoursEnvChangedByHand() bool {
	d.userScopeMu.Lock()
	mark := d.userMarkLocked()
	d.userScopeMu.Unlock()
	if mark.EnvDigest == "" {
		return false
	}
	b, err := os.ReadFile(d.yoursEnvPath())
	if err != nil {
		return true
	}
	return envDigest(b) != mark.EnvDigest
}

// writeYoursEnv is the environment part of the person's settings (#5259), written to
// ~/.agents/env. A file changed by hand since the last write is kept, so this machine goes on
// using it, until the person keeps it for all machines or discards it. It says whether the file
// changed. Called with userScopeMu held.
func (d *Daemon) writeYoursEnvLocked(home string, part wire.JSONValue, before userSettingsSyncState, mark *userSettingsSyncState) (bool, error) {
	mark.EnvDigest, mark.EnvText = before.EnvDigest, before.EnvText
	if home == "" {
		return false, nil
	}
	var u wire.UserEnvironment
	if err := json.Unmarshal(part, &u); err != nil {
		return false, errors.New("the environment part is not a UserEnvironment")
	}
	if len(u.Text) > envfile.FileMost {
		return false, errors.New("the environment part is over 64 KB")
	}
	path := filepath.Join(home, envRel)
	now, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	mark.EnvText = u.Text
	if exists && string(now) == u.Text {
		mark.EnvDigest = envDigest(now)
		return false, nil
	}
	// Changed by hand since the last write, or the person's own file from before their
	// settings had the part: kept, and env/list says so.
	if before.EnvDigest == "" && exists || before.EnvDigest != "" && (!exists || envDigest(now) != before.EnvDigest) {
		mark.EnvDigest = envDigest([]byte(u.Text))
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	// store-bounds: kind=key-value; count=1; size=64KiB; age=until-deleted; applied=on write (refused)
	if err := store.WriteAtomically(path, []byte(u.Text), 0o600); err != nil {
		return false, err
	}
	mark.EnvDigest = envDigest([]byte(u.Text))
	return true, nil
}

// discardYoursEnv is env/discard: ~/.agents/env as the person's settings last sent it.
func (d *Daemon) discardYoursEnv() error {
	d.userScopeMu.Lock()
	mark := d.userMarkLocked()
	if mark.EnvDigest == "" {
		d.userScopeMu.Unlock()
		return Invalid("~/.agents/env has not come from your settings on this machine, so there is nothing to go back to.")
	}
	path := d.yoursEnvPath()
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		// store-bounds: kind=key-value; count=1; size=64KiB; age=until-deleted; applied=on write (the text the settings sent, refused above this size)
		err = store.WriteAtomically(path, []byte(mark.EnvText), 0o600)
	}
	if err == nil {
		mark.EnvDigest = envDigest([]byte(mark.EnvText))
		err = d.saveUserMarkLocked(mark)
	}
	d.userScopeMu.Unlock()
	if err != nil {
		return err
	}
	d.tellEnvChanged("", true)
	return nil
}

// envWrite is env/write: one variable of a folder's .agents/env, the other lines kept. The file
// is left not committed.
func (d *Daemon) envWrite(r wire.EnvWriteRequest) error {
	folder := store.FolderPath(wire.URL(r.Folder))
	if !d.knownFolder(folder) {
		return &Error{Code: -32005, Message: "There is no such folder here."}
	}
	name := strings.TrimSpace(r.Name)
	if !envfile.Name.MatchString(name) {
		return Invalid("%q is not a variable's name: letters, digits and _, not starting with a digit.", r.Name)
	}
	if r.Value != nil {
		if why := envRefusal(name, true); why != "" {
			return Invalid("%s", why)
		}
		if len(*r.Value) > envfile.ValueMost {
			return Invalid("The value of %s is over 4 KB.", name)
		}
		if strings.ContainsAny(*r.Value, "\r\n\x00") {
			return Invalid("The value of %s is one line.", name)
		}
		if why := envfile.SecretWords(name, *r.Value); why != "" {
			return Invalid("%s A repo's file is plain text, and many repos are public: keep the value in a secret instead.", why)
		}
		if !passThroughAllowed(d.orgPolicy(), name) {
			return Invalid("%s", passThroughWords)
		}
	}
	path := repoEnvPath(folder)
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if p := envfile.Parse(b); p.Problem != "" {
		return Invalid("%s", p.Problem)
	} else if r.Value != nil && len(p.Variables) >= envfile.VariablesMost && !hasEnvName(p.Variables, name) {
		return Invalid("The file has %d variables, the most it can have.", envfile.VariablesMost)
	}
	next := envfile.Edit(b, name, r.Value, r.Description)
	if len(next) > envfile.FileMost {
		return Invalid("The file would be over %d KB.", envfile.FileMost>>10)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	// store-bounds: not a store: a file in the person's project
	if err := store.WriteAtomically(path, next, 0o644); err != nil {
		return err
	}
	d.tellEnvChanged(folder, false)
	return nil
}

func hasEnvName(vs []envfile.Variable, name string) bool {
	for _, v := range vs {
		if v.Name == name {
			return true
		}
	}
	return false
}

// knownFolder is a folder this machine has, or one an agent works in (a worktree).
func (d *Daemon) knownFolder(folder string) bool {
	for _, p := range d.Store.Folders() {
		if store.FolderPath(p.Folder) == folder {
			return true
		}
	}
	for _, f := range d.Store.AgentFolders() {
		if store.FolderPath(wire.URL(f)) == folder {
			return true
		}
	}
	return false
}

// tellEnvChanged is env/changed to the person's own clients.
func (d *Daemon) tellEnvChanged(folder string, yours bool) {
	n := wire.EnvChanged{}
	if folder != "" {
		n.Folder = &folder
	}
	if yours {
		n.Yours = &yours
	}
	Broadcast(d, wire.NotifyEnvChanged, n)
}

func (d *Daemon) registerEnvironment() {
	On(d, wire.CallEnvList, func(_ context.Context, c *Client, r wire.EnvListRequest) (wire.EnvList, error) {
		if err := envClient(c); err != nil {
			return wire.EnvList{}, err
		}
		folder := ""
		if r.Folder != nil && *r.Folder != "" {
			folder = store.FolderPath(wire.URL(*r.Folder))
		}
		return d.envList(folder), nil
	})
	On(d, wire.CallEnvResolve, func(_ context.Context, c *Client, r wire.EnvResolveRequest) (wire.EnvResolved, error) {
		if err := envClient(c); err != nil {
			return wire.EnvResolved{}, err
		}
		folder := ""
		switch {
		case r.AgentID != nil && *r.AgentID != "":
			a := d.Store.Agent(wire.UUID(strings.ToUpper(string(*r.AgentID))))
			if a == nil {
				return wire.EnvResolved{}, &Error{Code: -32005, Message: "There is no such agent."}
			}
			folder = store.FolderPath(a.Cwd)
		case r.Folder != nil && *r.Folder != "":
			folder = store.FolderPath(wire.URL(*r.Folder))
		default:
			return wire.EnvResolved{}, Invalid("Say a folder or an agent.")
		}
		return d.envResolve(folder), nil
	})
	On(d, wire.CallEnvWrite, func(_ context.Context, c *Client, r wire.EnvWriteRequest) (wire.Empty, error) {
		if err := envClient(c); err != nil {
			return wire.Empty{}, err
		}
		return wire.Empty{}, d.envWrite(r)
	})
	On(d, wire.CallEnvDiscard, func(_ context.Context, c *Client, _ wire.Empty) (wire.Empty, error) {
		if err := envClient(c); err != nil {
			return wire.Empty{}, err
		}
		return wire.Empty{}, d.discardYoursEnv()
	})
}

// envClient is the gate on env/*: the person's own clients, never an agent.
func envClient(c *Client) error {
	if c == nil || (c.Role != RoleControl && c.Role != RoleDevice) {
		return &Error{Code: CodeNotPermitted, Message: "Only your own clients can read or change the environment's variables."}
	}
	return nil
}
