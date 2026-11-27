package agentsd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/internal/workspacecommands"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The person's set-up scripts on the machine that sends them, the sender (#7167, gate 1 of
// #7151, section 2): root.sh and setup.sh in ~/.agents/machine-setup/. A client of the person's
// lock approves a script's text as it is then (setup/approve), for machines as they are made or
// for one machine, with a setup.script record (machinelock/record.go). The sender keeps the text
// approved last and the approvals in force for it in <root>/machine-setup-approved.json, and
// uses a script only while the file is that text (approvedSetupScript). It stays in the data
// folder, as lock-records.json does: one synced from another machine would be a trust source.

const (
	setupScriptsFolder = "machine-setup"
	approvedSetupFile  = "machine-setup-approved.json"
	// approvedSetupMostBytes bounds the file: the two texts and the approvals of each.
	approvedSetupMostBytes = 512 << 10
)

// setupScriptFiles are the two scripts, root then setup, with the most bytes of each.
var setupScriptFiles = []struct {
	name, file string
	most       int64
}{
	{"root", "root.sh", 10 << 10},
	{"setup", "setup.sh", 64 << 10},
}

// approvedScript is a script's text as a client approved it last, and the approvals of that text
// in force, by scope.
type approvedScript struct {
	Text      string                     `json:"text"`
	Hash      string                     `json:"hash"`
	Approvals map[string]wire.LockRecord `json:"approvals"`
}

// approvedSetup is machine-setup-approved.json.
type approvedSetup struct {
	Scripts  map[string]approvedScript  `json:"scripts"`
	Counters machinelock.SenderCounters `json:"counters"`
}

type setupScriptsState struct {
	setupScriptsMu sync.Mutex
}

func (d *Daemon) registerSetupScripts() {
	On(d, wire.CallSetupScripts, func(_ context.Context, _ *Client, _ wire.Empty) (wire.SetupScripts, error) {
		return d.setupScripts()
	})
	On(d, wire.CallSetupApprove, func(_ context.Context, _ *Client, p wire.SetupApprove) (wire.SetupScript, error) {
		return d.approveSetupScript(p)
	})
}

func (d *Daemon) setupScriptsFolder() string {
	if home := d.personalHome(); home != "" {
		return filepath.Join(home, ".agents", setupScriptsFolder)
	}
	return ""
}

// scriptFile is one script as it is now: present, and its text, or why it has none. A file over
// its limit has its size too (#7548), so the page can say by how much.
type scriptFile struct {
	present bool
	text    string
	problem string
	size    int64
}

func (f scriptFile) hash() string { return scriptHash(f.text) }

func scriptHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// readSetupScript reads one script, at most its limit. A link is followed: the person's dotfiles
// may put it there.
func (d *Daemon) readSetupScript(file string, most int64) scriptFile {
	folder := d.setupScriptsFolder()
	if folder == "" {
		return scriptFile{}
	}
	f, err := os.Open(filepath.Join(folder, file))
	if errors.Is(err, fs.ErrNotExist) {
		return scriptFile{}
	}
	if err != nil {
		return scriptFile{present: true, problem: file + " cannot be read: " + err.Error()}
	}
	defer f.Close()
	info, err := f.Stat()
	switch {
	case err != nil:
		return scriptFile{present: true, problem: file + " cannot be read: " + err.Error()}
	case !info.Mode().IsRegular():
		return scriptFile{present: true, problem: file + " is not a file."}
	}
	b, err := io.ReadAll(io.LimitReader(f, most+1))
	switch {
	case err != nil:
		return scriptFile{present: true, problem: file + " cannot be read: " + err.Error()}
	case int64(len(b)) > most:
		size := max(info.Size(), most+1)
		return scriptFile{present: true, size: size,
			problem: fmt.Sprintf("%s is %d KiB, over its limit of %d KiB.", file, (size+(1<<10)-1)>>10, most>>10)}
	case len(b) == 0:
		return scriptFile{present: true, problem: file + " is empty."}
	}
	return scriptFile{present: true, text: string(b)}
}

func (d *Daemon) approvedSetupPath() string { return filepath.Join(d.Store.Loc.Root, approvedSetupFile) }

// readApprovedSetup reads machine-setup-approved.json: none when it is missing. With one that
// cannot be read, no script is approved.
func (d *Daemon) readApprovedSetup() (approvedSetup, error) {
	out := approvedSetup{Scripts: map[string]approvedScript{}, Counters: machinelock.SenderCounters{}}
	b, err := os.ReadFile(d.approvedSetupPath())
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err == nil && len(b) > approvedSetupMostBytes {
		err = fmt.Errorf("%s is over %d KiB", approvedSetupFile, approvedSetupMostBytes>>10)
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("%s does not read: %w", approvedSetupFile, err)
	}
	if out.Scripts == nil {
		out.Scripts = map[string]approvedScript{}
	}
	if out.Counters == nil {
		out.Counters = machinelock.SenderCounters{}
	}
	if !out.Counters.Valid() {
		return out, fmt.Errorf("%s does not hold counters", approvedSetupFile)
	}
	return out, nil
}

func (d *Daemon) keepApprovedSetup(a approvedSetup) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err := tooLarge(approvedSetupFile, b, approvedSetupMostBytes); err != nil {
		return err
	}
	// store-bounds: kind=key-value; count=2 scripts; size=512KiB; age=until-deleted; applied=on write (refused)
	return store.WriteAtomically(d.approvedSetupPath(), b, 0o600)
}

// inForce is the approvals of an approved text that the lock held now signed: a lock that was
// replaced takes its approvals with it.
func inForce(a approvedScript, state *machinelock.State) []wire.LockRecord {
	out := []wire.LockRecord{}
	if state == nil {
		return out
	}
	for _, r := range a.Approvals {
		if _, err := machinelock.CheckRecord(state, r); err == nil {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(x, y wire.LockRecord) int {
		switch {
		case x.Scope < y.Scope:
			return -1
		case x.Scope > y.Scope:
			return 1
		}
		return 0
	})
	return out
}

// scriptState is one script as setup/scripts says it.
func scriptState(name string, f scriptFile, approved approvedSetup, state *machinelock.State) wire.SetupScript {
	out := wire.SetupScript{Name: name, Present: f.present, Approvals: []wire.LockRecord{}, NextSeq: 1}
	if f.problem != "" {
		out.Problem = new(f.problem)
		if f.size > 0 {
			out.Size = new(f.size)
		}
	} else if f.present {
		out.Text, out.Hash, out.Size = new(f.text), new(f.hash()), new(int64(len(f.text)))
	}
	a, had := approved.Scripts[name]
	if had {
		out.ApprovedText, out.ApprovedHash = new(a.Text), new(a.Hash)
		out.Approvals = inForce(a, state)
	}
	if state != nil {
		out.NextSeq = approved.Counters.Highest(state.Root[:], machinelock.KindSetupScript, name) + 1
	}
	// A script is used only as the text approved: a new one, a changed one and a removed one
	// each ask again.
	out.Changed = f.problem != "" || f.present != had || had && a.Hash != f.hash()
	return out
}

// setupScripts is setup/scripts.
func (d *Daemon) setupScripts() (wire.SetupScripts, error) {
	d.setupScriptsMu.Lock()
	defer d.setupScriptsMu.Unlock()
	approved, err := d.readApprovedSetup()
	if err != nil {
		return wire.SetupScripts{}, &Error{Code: CodeServer, Message: "The approved set-up scripts cannot be read: " + err.Error()}
	}
	state, _ := d.heldLock()
	out := wire.SetupScripts{Scripts: []wire.SetupScript{}}
	if folder := d.setupScriptsFolder(); folder != "" {
		out.Folder = &folder
	}
	for _, s := range setupScriptFiles {
		out.Scripts = append(out.Scripts, scriptState(s.name, d.readSetupScript(s.file, s.most), approved, state))
	}
	return out, nil
}

func setupScriptFile(name string) (file string, most int64, ok bool) {
	for _, s := range setupScriptFiles {
		if s.name == name {
			return s.file, s.most, true
		}
	}
	return "", 0, false
}

// approveSetupScript is setup/approve: it keeps an approval that a client of the lock this
// machine holds signed, of the script's text as it is now.
func (d *Daemon) approveSetupScript(p wire.SetupApprove) (wire.SetupScript, error) {
	file, most, ok := setupScriptFile(p.Script)
	if !ok {
		return wire.SetupScript{}, Invalid("There is no set-up script %q: it is root or setup.", p.Script)
	}
	r := p.Approval
	if r.Kind != machinelock.KindSetupScript || r.Subject != p.Script {
		return wire.SetupScript{}, Invalid("The approval is not of %s.", file)
	}
	state, _ := d.heldLock()
	if state == nil {
		return wire.SetupScript{}, &Error{Code: wire.FailureNotInLock, Message: "This machine holds no lock: set up your lock first, then approve " + file + "."}
	}
	if _, err := machinelock.CheckRecord(state, r); err != nil {
		return wire.SetupScript{}, &Error{Code: wire.FailureNotInLock, Message: "The approval of " + file + " is not one of your devices': " + err.Error()}
	}
	d.setupScriptsMu.Lock()
	defer d.setupScriptsMu.Unlock()
	now := d.readSetupScript(file, most)
	if !machinelock.Removed(r) {
		a, err := machinelock.SetupScriptOf(r.Content)
		if err != nil {
			return wire.SetupScript{}, Invalid("The approval of %s does not read: %v", file, err)
		}
		if now.problem != "" || !now.present || a.Hash != now.hash() || a.Size != int64(len(now.text)) {
			return wire.SetupScript{}, &Error{Code: wire.FailureSetupScriptChanged, Message: file + " changed since it was read: read it again and approve what it is now."}
		}
	}
	approved, err := d.readApprovedSetup()
	if err != nil {
		return wire.SetupScript{}, &Error{Code: CodeServer, Message: "The approved set-up scripts cannot be read: " + err.Error()}
	}
	if _, err := approved.Counters.Take(r); err != nil {
		return wire.SetupScript{}, Invalid("The approval of %s cannot be taken: %v", file, err)
	}
	a := approved.Scripts[p.Script]
	switch {
	case machinelock.Removed(r):
		delete(a.Approvals, r.Scope)
	case a.Hash != now.hash():
		// A new text: the approvals of the old one are not of it.
		a = approvedScript{Text: now.text, Hash: now.hash(), Approvals: map[string]wire.LockRecord{}}
		fallthrough
	default:
		if a.Approvals == nil {
			a.Approvals = map[string]wire.LockRecord{}
		}
		a.Approvals[r.Scope] = r
	}
	// Only the approvals of the lock held now stay.
	for scope, had := range a.Approvals {
		if _, err := machinelock.CheckRecord(state, had); err != nil {
			delete(a.Approvals, scope)
		}
	}
	if len(a.Approvals) == 0 {
		delete(approved.Scripts, p.Script)
	} else {
		approved.Scripts[p.Script] = a
	}
	if err := d.keepApprovedSetup(approved); err != nil {
		return wire.SetupScript{}, &Error{Code: wire.FailureCouldNotSave, Message: "The approval of " + file + " was not kept: " + err.Error()}
	}
	return scriptState(p.Script, now, approved, state), nil
}

// approvedSetupScript is the text of a script and its approval for scope, for the sender to use
// (New Machine, make_machine, warm pools and Send Set-up), only when the file is the text a
// client approved last and an approval of that scope is in force. Else setupScriptChanged.
func (d *Daemon) approvedSetupScript(name, scope string) (string, wire.LockRecord, error) {
	file, most, ok := setupScriptFile(name)
	if !ok {
		return "", wire.LockRecord{}, Invalid("There is no set-up script %q.", name)
	}
	state, _ := d.heldLock()
	if state == nil {
		return "", wire.LockRecord{}, &Error{Code: wire.FailureNotInLock, Message: "Set-up scripts need your lock: set it up first."}
	}
	d.setupScriptsMu.Lock()
	defer d.setupScriptsMu.Unlock()
	approved, err := d.readApprovedSetup()
	if err != nil {
		return "", wire.LockRecord{}, &Error{Code: CodeServer, Message: "The approved set-up scripts cannot be read: " + err.Error()}
	}
	now := d.readSetupScript(file, most)
	a, had := approved.Scripts[name]
	r, approvedHere := a.Approvals[scope]
	if had && approvedHere {
		_, err = machinelock.CheckRecord(state, r)
	}
	if !had || !approvedHere || err != nil || now.problem != "" || !now.present || a.Hash != now.hash() {
		return "", wire.LockRecord{}, &Error{Code: wire.FailureSetupScriptChanged, Message: file + " is not the text you approved: review it and approve it again."}
	}
	return now.text, r, nil
}

// rootBootForMake is the lines that run root.sh on a machine this one makes (#7154; spec of
// #7151, section 5), with its hash: none with no root.sh. With no lock, none, and why. A root.sh
// that is not the text a client approved for made machines stops the make (setupScriptChanged).
func (d *Daemon) rootBootForMake() (string, *providers.RootBootSent, error) {
	file, most, _ := setupScriptFile("root")
	if f := d.readSetupScript(file, most); !f.present {
		return "", nil, nil
	}
	if state, _ := d.heldLock(); state == nil {
		return "", &providers.RootBootSent{Problem: "Root set-up needs your machine lock, so root.sh did not run. Set up the lock, then make a new machine."}, nil
	}
	text, _, err := d.approvedSetupScript("root", machinelock.ScopeMade)
	if err != nil {
		return "", nil, err
	}
	return workspacecommands.RootBoot([]byte(text)), &providers.RootBootSent{Hash: scriptHash(text)}, nil
}

// scriptsBeforeMake is what make_machine's card says of the person's scripts (#7157), when this
// machine carries the provider and holds the lock: nothing when each is the text approved for new
// machines. A root.sh that is not refuses before the card, as the make would stop (#855): only a
// person's client can approve it. A setup.sh that is not is said on the card: the machine is made,
// and setup.sh is not sent to it.
func (d *Daemon) scriptsBeforeMake(provider string) (string, error) {
	if !d.carried().carries(provider) {
		return "", nil
	}
	if state, _ := d.heldLock(); state == nil {
		// No script runs, and the make says so on the machine's Root set-up.
		return "", nil
	}
	for _, s := range setupScriptFiles {
		if !d.readSetupScript(s.file, s.most).present {
			continue
		}
		if _, _, err := d.approvedSetupScript(s.name, machinelock.ScopeMade); err == nil {
			continue
		}
		if s.name == "root" {
			return "", &Error{Code: wire.FailureSetupScriptChanged, Message: "Nothing was made: root.sh on this machine is new or changed since the person " +
				"approved it, so a make stops. The person reviews it with New Machine in Agent Loops: an agent can’t approve it."}
		}
		return " setup.sh is new or changed since you approved it, so it is not sent to the new machine. Approve it with New Machine or Send Set-up.", nil
	}
	return "", nil
}
