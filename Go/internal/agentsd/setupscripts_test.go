package agentsd

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/internal/workspacecommands"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// approvalOf is a setup.script record of text, signed by k.
func approvalOf(t *testing.T, k *ecdsa.PrivateKey, s *machinelock.State, script, scope string, seq int64, text string) wire.LockRecord {
	t.Helper()
	content, err := json.Marshal(wire.SetupScriptApproval{Hash: scriptHash(text), Size: int64(len(text))})
	must(t, err)
	return signedRecord(t, k, s, script, scope, seq, string(content))
}

// senderWith is a locked machine (h-1) whose home has setup.sh with text.
func senderWith(t *testing.T, text string) (d *Daemon, phone, peer *ecdsa.PrivateKey, state *machinelock.State, write func(string)) {
	t.Helper()
	d, phone, peer, state = lockedMachine(t)
	t.Cleanup(d.Shutdown)
	d.PersonalHome = t.TempDir()
	folder := filepath.Join(d.PersonalHome, ".agents", "machine-setup")
	must(t, os.MkdirAll(folder, 0o700))
	write = func(text string) {
		t.Helper()
		must(t, os.WriteFile(filepath.Join(folder, "setup.sh"), []byte(text), 0o600)) //nolint:gosec // G703: the test's own temp folder
	}
	write(text)
	return d, phone, peer, state, write
}

func codeOf(err error) int {
	var re *Error
	if errors.As(err, &re) {
		return re.Code
	}
	return 0
}

// #7167: the sender keeps an approval that a client of the lock signed of the text that is
// there now, and refuses one from an agent's, a machine's or an unknown key, of another text, at
// a lower number, or of another script.
func TestASenderKeepsOnlyAClientsApprovalOfTheTextThere(t *testing.T) {
	t.Parallel()
	const text = "#!/bin/bash\nbrew install jq\n"
	d, phone, peer, state, write := senderWith(t, text)
	scripts, err := d.setupScripts()
	must(t, err)
	if len(scripts.Scripts) != 2 || scripts.Scripts[0].Present || !scripts.Scripts[1].Present || !scripts.Scripts[1].Changed ||
		scripts.Scripts[1].NextSeq != 1 || *scripts.Scripts[1].Hash != scriptHash(text) {
		t.Fatalf("%+v", scripts)
	}
	// #7512: the answer names the folder, so Settings ▸ Machines opens the one read here.
	if scripts.Folder == nil || *scripts.Folder != filepath.Join(d.PersonalHome, ".agents", "machine-setup") {
		t.Fatalf("folder: %v", scripts.Folder)
	}
	if _, _, err := d.approvedSetupScript("setup", machinelock.ScopeMade); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a script no client approved is used: %v", err)
	}
	approve := func(r wire.LockRecord) (wire.SetupScript, error) {
		return d.approveSetupScript(wire.SetupApprove{Script: "setup", Approval: r})
	}
	refused := func(name string, r wire.LockRecord, code int) {
		t.Helper()
		if _, err := approve(r); codeOf(err) != code {
			t.Errorf("%s: %v", name, err)
		}
	}
	agent := newLockKey(t)
	refused("an agent's key", approvalOf(t, agent, state, "setup", "made", 1, text), wire.FailureNotInLock)
	refused("a machine's key", approvalOf(t, peer, state, "setup", "made", 1, text), wire.FailureNotInLock)
	refused("another text", approvalOf(t, phone, state, "setup", "made", 1, "echo other\n"), wire.FailureSetupScriptChanged)
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "root", Approval: approvalOf(t, phone, state, "setup", "made", 1, text)}); codeOf(err) != CodeInvalidParams {
		t.Errorf("an approval of another script: %v", err)
	}

	got, err := approve(approvalOf(t, phone, state, "setup", "made", 5, text))
	if err != nil || got.Changed || len(got.Approvals) != 1 || got.NextSeq != 6 || *got.ApprovedText != text {
		t.Fatalf("%+v %v", got, err)
	}
	used, r, err := d.approvedSetupScript("setup", machinelock.ScopeMade)
	if err != nil || used != text || r.Seq != 5 {
		t.Fatalf("%q %v", used, err)
	}
	if _, _, err := d.approvedSetupScript("setup", machinelock.ScopeOf("h-9")); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("an approval for made machines went to a named one: %v", err)
	}
	refused("a lower number", approvalOf(t, phone, state, "setup", "made", 4, text), CodeInvalidParams)
	info, err := os.Stat(filepath.Join(d.Store.Loc.Root, approvedSetupFile))
	must(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}

	// The file changes: it is not used until a client approves it again, and the new text's
	// approval replaces the old one's.
	const next = "#!/bin/bash\nbrew install jq gh\n"
	write(next)
	if _, _, err := d.approvedSetupScript("setup", machinelock.ScopeMade); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a changed file is used: %v", err)
	}
	if s, _ := d.setupScripts(); !s.Scripts[1].Changed {
		t.Fatal("not changed")
	}
	got, err = approve(approvalOf(t, phone, state, "setup", "m:h-9", 6, next))
	if err != nil || got.Changed || len(got.Approvals) != 1 || got.Approvals[0].Scope != "m:h-9" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, _, err := d.approvedSetupScript("setup", machinelock.ScopeMade); err == nil {
		t.Fatal("the old text's approval stayed for the new text")
	}
	// An empty approval takes it away.
	got, err = approve(signedRecord(t, phone, state, "setup", "m:h-9", 7, ""))
	if err != nil || len(got.Approvals) != 0 || !got.Changed {
		t.Fatalf("%+v %v", got, err)
	}

	// With no lock, nothing is approved.
	d.ResetLock()
	refused("no lock", approvalOf(t, phone, state, "setup", "made", 8, next), wire.FailureNotInLock)
}

// #7167: a script over its limit is not one to approve.
// #7547: each try of a Send Set-up that waits gives the same approval again. It is taken, and
// machine-setup-approved.json is not written again; a new approval is written.
func TestTheSameApprovalAgainDoesNotWriteTheApprovals(t *testing.T) {
	t.Parallel()
	const text = "#!/bin/bash\nbrew install jq\n"
	d, phone, _, state, _ := senderWith(t, text)
	scope := machinelock.ScopeOf("h-2")
	r := approvalOf(t, phone, state, "setup", scope, 1, text)
	approve := func(r wire.LockRecord) {
		t.Helper()
		if _, err := d.approveSetupScript(wire.SetupApprove{Script: "setup", Approval: r}); err != nil {
			t.Fatal(err)
		}
	}
	approve(r)
	path := d.approvedSetupPath()
	old := time.Unix(1_000_000, 0)
	must(t, os.Chtimes(path, old, old))
	written := func() bool {
		t.Helper()
		info, err := os.Stat(path)
		must(t, err)
		return !info.ModTime().Equal(old)
	}
	approve(r)
	if written() {
		t.Fatal("the same approval wrote the approvals again")
	}
	if _, _, err := d.approvedSetupScript("setup", scope); err != nil {
		t.Fatalf("the approval is not in force: %v", err)
	}
	approve(approvalOf(t, phone, state, "setup", scope, 2, text))
	if !written() {
		t.Fatal("a new approval was not written")
	}
}

func TestASetUpScriptOverItsLimitIsNotUsed(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("#", 64<<10+1)
	d, phone, _, state, _ := senderWith(t, text)
	s, err := d.setupScripts()
	must(t, err)
	if s.Scripts[1].Problem == nil || s.Scripts[1].Text != nil {
		t.Fatalf("%+v", s.Scripts[1])
	}
	// #7548: it says its size and the limit, rounded up, and carries the size.
	if got := *s.Scripts[1].Problem; got != "setup.sh is 65 KiB, over its limit of 64 KiB." {
		t.Errorf("problem %q", got)
	}
	if s.Scripts[1].Size == nil || *s.Scripts[1].Size != 64<<10+1 {
		t.Errorf("size %v", s.Scripts[1].Size)
	}
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "setup", Approval: approvalOf(t, phone, state, "setup", "made", 1, text)}); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("%v", err)
	}
}

// #7167: the target takes a delivered script only with a client's approval of that text for it,
// at a number not lower than the last, brought by a machine of the lock for it.
func TestATargetChecksADeliveredScript(t *testing.T) {
	t.Parallel()
	d, phone, peer, state := lockedMachine(t)
	defer d.Shutdown()
	const text = "#!/bin/bash\nbrew install jq\n"
	fromPeer := withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, peer), to: "h-1"})
	refused := func(name string, ctx context.Context, r wire.LockRecord, made bool) {
		t.Helper()
		if _, err := d.checkSetupDelivery(ctx, "setup", text, r, made); codeOf(err) != wire.FailureNotInLock {
			t.Errorf("%s: %v", name, err)
		}
	}
	good := approvalOf(t, phone, state, "setup", "m:h-1", 3, text)
	// Rule 4: who brings it.
	refused("no signer", context.Background(), good, false)
	refused("a client brings it", withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, phone), to: "h-1"}), good, false)
	refused("for no machine", withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, peer)}), good, false)
	// Rule 1: who approved it, and of what.
	refused("an agent's key", fromPeer, approvalOf(t, newLockKey(t), state, "setup", "m:h-1", 3, text), false)
	refused("a machine's key", fromPeer, approvalOf(t, peer, state, "setup", "m:h-1", 3, text), false)
	refused("a wrong hash", fromPeer, approvalOf(t, phone, state, "setup", "m:h-1", 3, "echo other\n"), false)
	refused("another script", fromPeer, approvalOf(t, phone, state, "root", "m:h-1", 3, text), false)
	// Rule 2: where it may go.
	refused("another machine", fromPeer, approvalOf(t, phone, state, "setup", "m:h-2", 3, text), false)
	refused("made, to a machine in use", fromPeer, approvalOf(t, phone, state, "setup", "made", 3, text), false)

	client, err := d.takeSetupDelivery(fromPeer, "setup", text, good, false)
	if err != nil || client.ID != phoneClient {
		t.Fatalf("%q %v", client.ID, err)
	}
	if _, err := d.checkSetupDelivery(fromPeer, "setup", text, good, false); err != nil {
		t.Fatalf("the same one again: %v", err)
	}
	// Rule 3: a lower number.
	refused("a lower number", fromPeer, approvalOf(t, phone, state, "setup", "made", 2, text), true)
	if _, err := d.checkSetupDelivery(fromPeer, "setup", text, approvalOf(t, phone, state, "setup", "made", 4, text), true); err != nil {
		t.Fatalf("a made machine's higher one: %v", err)
	}
}

func lockMemberOf(t *testing.T, s *machinelock.State, k *ecdsa.PrivateKey) machinelock.Member {
	t.Helper()
	m, ok := s.Member(machinelock.PublicKey(k))
	if !ok {
		t.Fatal("not a member")
	}
	return m
}

// #7154: a make fills in rootBoot only from the root.sh that a client approved for made machines:
// none with no file, a stopped make while it is not the approved text, and none, with why, with no lock.
func TestAMakeRunsOnlyTheApprovedRootScript(t *testing.T) {
	t.Parallel()
	d, phone, _, state, _ := senderWith(t, "echo setup\n")
	if lines, sent, err := d.rootBootForMake(); lines != "" || sent != nil || err != nil {
		t.Fatalf("with no root.sh: %q %+v %v", lines, sent, err)
	}
	const text = "#!/bin/bash\napt-get install -y gh\n"
	root := filepath.Join(d.PersonalHome, ".agents", "machine-setup", "root.sh")
	must(t, os.WriteFile(root, []byte(text), 0o600))
	if _, _, err := d.rootBootForMake(); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a root.sh no client approved was sent: %v", err)
	}
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "root", Approval: approvalOf(t, phone, state, "root", "made", 1, text)}); err != nil {
		t.Fatal(err)
	}
	lines, sent, err := d.rootBootForMake()
	if err != nil || sent == nil || sent.Hash != scriptHash(text) || lines != workspacecommands.RootBoot([]byte(text)) {
		t.Fatalf("%q %+v %v", lines, sent, err)
	}
	must(t, os.WriteFile(root, []byte(text+"apt-get install -y jq\n"), 0o600))
	if _, _, err := d.rootBootForMake(); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a changed root.sh was sent: %v", err)
	}
}

func TestAMakeWithNoLockSendsNoRootScriptAndSaysWhy(t *testing.T) {
	t.Parallel()
	d, _, _ := joinedMachine(t)
	t.Cleanup(d.Shutdown)
	d.PersonalHome = t.TempDir()
	folder := filepath.Join(d.PersonalHome, ".agents", "machine-setup")
	must(t, os.MkdirAll(folder, 0o700))
	must(t, os.WriteFile(filepath.Join(folder, "root.sh"), []byte("apt-get install -y gh\n"), 0o600))
	lines, sent, err := d.rootBootForMake()
	if err != nil || lines != "" || sent == nil || !strings.Contains(sent.Problem, "needs your machine lock") {
		t.Fatalf("%q %+v %v", lines, sent, err)
	}
}

// #7157: a make that root.sh stopped carries the code setup_script_changed, so a warm pool waits
// for a review; make_machine says a changed setup.sh on its card, and refuses before the card
// for a changed root.sh, only on the machine that carries the provider.
func TestAChangedScriptShowsBeforeAMake(t *testing.T) {
	t.Parallel()
	const text = "echo setup\n"
	d, phone, _, state, write := senderWith(t, text)
	if got := rootBootRefused(&Error{Code: wire.FailureSetupScriptChanged, Message: "root.sh changed"}); got.Code == nil || *got.Code != "setup_script_changed" ||
		*got.Error != "root.sh changed" {
		t.Fatalf("%+v", got)
	}
	if got := rootBootRefused(errors.New("no")); got.Code != nil {
		t.Fatalf("%+v", got)
	}
	// Not the provider's carrier: nothing is said.
	if note, err := d.scriptsBeforeMake("do"); note != "" || err != nil {
		t.Fatalf("%q %v", note, err)
	}
	c := d.carried()
	c.mu.Lock()
	c.held["do"] = &carriedConnection{}
	c.mu.Unlock()
	note, err := d.scriptsBeforeMake("do")
	if err != nil || !strings.Contains(note, "setup.sh is new or changed") {
		t.Fatalf("a setup.sh no client approved: %q %v", note, err)
	}
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "setup", Approval: approvalOf(t, phone, state, "setup", "made", 1, text)}); err != nil {
		t.Fatal(err)
	}
	if note, err := d.scriptsBeforeMake("do"); note != "" || err != nil {
		t.Fatalf("nothing changed shows nothing: %q %v", note, err)
	}
	write("echo other\n")
	if note, _ := d.scriptsBeforeMake("do"); !strings.Contains(note, "setup.sh") {
		t.Fatalf("a changed setup.sh: %q", note)
	}
	write(text)
	root := filepath.Join(d.PersonalHome, ".agents", "machine-setup", "root.sh")
	must(t, os.WriteFile(root, []byte("apt-get install -y gh\n"), 0o600))
	if _, err := d.scriptsBeforeMake("do"); codeOf(err) != wire.FailureSetupScriptChanged || !strings.Contains(err.Error(), "root.sh") {
		t.Fatalf("a changed root.sh: %v", err)
	}
}

// #7461: setup/scripts gives each script's example from the pinned dotagents copy, what New
// root.sh and New setup.sh write: gh with apt, and a setup.sh that declares GH_TOKEN and gives
// it to gh only on stdin. A copy of another commit gives none.
func TestSetUpScriptsGiveThePinnedExamples(t *testing.T) {
	t.Parallel()
	d, _, _, _, _ := senderWith(t, "#!/bin/bash\necho hi\n")
	scripts, err := d.setupScripts()
	must(t, err)
	for _, s := range scripts.Scripts {
		if s.Example != nil {
			t.Fatalf("%s: an example with no ready-made copy: %q", s.Name, *s.Example)
		}
	}
	fixture := readyMadeFixture()
	must(t, saveReadyMade(d.PersonalHome, fixture))
	scripts, err = d.setupScripts()
	must(t, err)
	if len(scripts.Scripts) != 2 || scripts.Scripts[0].Example == nil || scripts.Scripts[1].Example == nil {
		t.Fatalf("%+v", scripts)
	}
	root, setup := *scripts.Scripts[0].Example, *scripts.Scripts[1].Example
	if root != fixture.Scripts["root"] || !strings.Contains(root, "apt-get -o DPkg::Lock::Timeout=300 install -y gh\n") {
		t.Errorf("root.sh: %q", root)
	}
	if got, err := declaredSecrets(setup); err != nil || len(got) != 1 || got[0] != "GH_TOKEN" {
		t.Errorf("setup.sh declares %v: %v", got, err)
	}
	if !strings.Contains(setup, "agents-secret GH_TOKEN | gh auth login --with-token\n") {
		t.Errorf("setup.sh: %q", setup)
	}

	other := *fixture
	other.Commit = strings.Repeat("b", 40) // another size too, so the cached read is not used
	must(t, saveReadyMade(d.PersonalHome, &other))
	scripts, err = d.setupScripts()
	must(t, err)
	if scripts.Scripts[0].Example != nil {
		t.Errorf("an unpinned copy gave an example")
	}
}
