package agentsd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/execfile"
	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7156: the names come from the # secrets: lines of the comment lines at the top, each once.
func TestSetUpScriptDeclaresItsSecretsAtItsTop(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ text, want, problem string }{
		{"#!/bin/bash\n# My set-up\n# secrets: GH_TOKEN NPM_TOKEN\n\n# secrets: GH_TOKEN AWS_KEY_2\necho hi\n# secrets: LATE\n", "GH_TOKEN NPM_TOKEN AWS_KEY_2", ""},
		{"echo hi\n# secrets: LATE\n", "", ""},
		{"#secrets:ONE\n", "ONE", ""},
		{"# secrets: gh_token\n", "", "not a secret's name"},
		{"# secrets: " + strings.Repeat("A ", 1) + "B C D E F G H I J K L M N O P Q R S T U\n", "", "21 secrets"},
	} {
		names, err := declaredSecrets(c.text)
		if c.problem != "" {
			if err == nil || !strings.Contains(err.Error(), c.problem) {
				t.Fatalf("%q: %v", c.text, err)
			}
			continue
		}
		must(t, err)
		if got := strings.Join(names, " "); got != c.want {
			t.Fatalf("%q: %q", c.text, got)
		}
	}
}

// setUpScriptMachine is a machine of the lock with a personal home, and a function that gives
// it text as setup.sh from another machine of the lock.
func setUpScriptMachine(t *testing.T) (*Daemon, func(text string)) {
	t.Helper()
	d, phone, peer, state := lockedMachine(t)
	t.Cleanup(d.Shutdown)
	d.PersonalHome = t.TempDir()
	fromPeer := withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, peer), to: "h-1"})
	var seq int64
	return d, func(text string) {
		t.Helper()
		seq++
		must(t, d.takeUserSetup(fromPeer, wire.SetupDeliver{Script: "setup", Text: text, Approval: approvalOf(t, phone, state, "setup", "m:h-1", seq, text)}))
	}
}

// synced says the person's settings came to d, with waiting as the secrets on their way.
func synced(d *Daemon, waiting ...wire.UserSecretAbout) {
	d.userScopeMu.Lock()
	defer d.userScopeMu.Unlock()
	d.userMark = &userSettingsSyncState{User: "sam", Waiting: waiting}
}

// #7156: a run of setup.sh gets agents-secret on its PATH and a grant of its own. The grant
// gives the names the script declared and no other, records each read with the place setup.sh
// and the script's hash, and asks nothing once the run has ended.
func TestSetUpScriptReadsOnlyTheSecretsItDeclares(t *testing.T) {
	t.Parallel()
	d, send := setUpScriptMachine(t)
	synced(d)
	const plain, other = "plain-7156-not-real", "other-7156-not-real"
	must(t, d.saveSecret("PLAIN_KEY", plain, false))
	must(t, d.saveSecret("OTHER_KEY", other, false))
	stub := filepath.Join(t.TempDir(), "agentsd")
	must(t, execfile.Write(stub, []byte("#!/bin/sh\necho \"agentsd $*\"\n"), 0o700))
	d.credentialSelf = stub

	const text = "#!/bin/bash\n# secrets: PLAIN_KEY\n" +
		"env > env.txt\nagents-secret PLAIN_KEY > shim.txt\n" +
		"while [ ! -f go ]; do sleep 0.05; done\n"
	send(text)
	home := d.PersonalHome
	var env []string
	waitFor(t, "setup.sh's environment", func() bool {
		b, err := os.ReadFile(filepath.Join(home, "shim.txt"))
		if err != nil || len(b) == 0 {
			return false
		}
		e, _ := os.ReadFile(filepath.Join(home, "env.txt"))
		env = strings.Split(strings.TrimSpace(string(e)), "\n")
		return true
	})
	if strings.Contains(strings.Join(env, "\n"), plain) || lastEnv(env, secretGrantEnv) == "" {
		t.Fatalf("the environment:\n%s", strings.Join(env, "\n"))
	}
	if shim, _ := os.ReadFile(filepath.Join(home, "shim.txt")); strings.TrimSpace(string(shim)) != "agentsd secret PLAIN_KEY" {
		t.Fatalf("agents-secret on PATH ran %q", shim)
	}
	ask := func(name string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := SecretCommand([]string{name}, func(k string) string { return lastEnv(env, k) }, "", &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, out, said := ask("PLAIN_KEY"); code != 0 || out != plain {
		t.Fatalf("PLAIN_KEY: %d %q %q", code, out, said)
	}
	if code, out, said := ask("OTHER_KEY"); code != 1 || out != "" || !strings.Contains(said, "does not declare OTHER_KEY") {
		t.Fatalf("OTHER_KEY: %d %q %q", code, out, said)
	}
	var reads []string
	for _, e := range secretEvents(d, "secret.read") {
		reads = append(reads, e.Details["name"]+" "+e.Details["place"]+" "+e.Details["agent"])
	}
	if strings.Join(reads, ", ") != "PLAIN_KEY setup.sh "+scriptHash(text)+" " {
		t.Fatalf("the reads: %v", reads)
	}

	// The run ends: its grant asks nothing.
	must(t, os.WriteFile(filepath.Join(home, "go"), nil, 0o600))
	if u := userSetupEnded(t, d); u.Error != "" {
		t.Fatalf("%+v", u)
	}
	if code, out, said := ask("PLAIN_KEY"); code != 1 || out != "" || !strings.Contains(said, "setup.sh it was for has ended") {
		t.Fatalf("after the run: %d %q %q", code, out, said)
	}
}

// #7156: a declared secret that is not the person's, or that has Ask before use, fails the step
// before the script runs, and the step names it. A script that declares none gets no grant.
func TestSetUpScriptDoesNotStartWithASecretItCannotHave(t *testing.T) {
	t.Parallel()
	d, send := setUpScriptMachine(t)
	synced(d, wire.UserSecretAbout{Name: "FAR_ASK", NeedsApproval: new(true)})
	must(t, d.saveSecret("ASK_KEY", "ask-7156-not-real", true))
	for _, c := range []struct{ names, problem string }{
		{"MISSING_KEY", "MISSING_KEY, which is not one of your secrets"},
		{"ASK_KEY", "ASK_KEY, which has Ask before use"},
		{"FAR_ASK ASK_KEY", "FAR_ASK and ASK_KEY, which have Ask before use"},
	} {
		send("# secrets: " + c.names + "\ntouch ran.txt\n")
		u := userSetupEnded(t, d)
		if !strings.Contains(u.Error, c.problem) {
			t.Fatalf("%s: %+v", c.names, u)
		}
		if _, err := os.Stat(filepath.Join(d.PersonalHome, "ran.txt")); err == nil {
			t.Fatalf("%s: setup.sh ran", c.names)
		}
		said := userReports(t, d)
		if last := said[len(said)-1]; last.State != wire.MachineSetupStateFailed || last.Error == nil || !strings.Contains(*last.Error, c.problem) {
			t.Fatalf("%s: %+v", c.names, last)
		}
	}

	send("env > env.txt\n")
	if u := userSetupEnded(t, d); u.Error != "" {
		t.Fatalf("%+v", u)
	}
	e, _ := os.ReadFile(filepath.Join(d.PersonalHome, "env.txt"))
	if strings.Contains(string(e), secretGrantEnv) {
		t.Fatal("a script that declares no secret got a grant")
	}
}

// #7156: a declared secret of the person's that has no copy here yet makes the step wait, in
// needs-you, with the words that say what to do, for longer than the run's time limit; the run
// starts once a client seals the copy here.
func TestSetUpScriptWaitsForAClientToSealItsSecrets(t *testing.T) {
	setFor(t, &setupSecretsPoll, 20*time.Millisecond)
	setFor(t, &userSetupTimeout, time.Second)
	d, send := setUpScriptMachine(t)
	synced(d, wire.UserSecretAbout{Name: "GH_TOKEN"})
	d.credentialSelf = filepath.Join(t.TempDir(), "agentsd")

	send("# secrets: GH_TOKEN\ntouch ran.txt\n")
	waitFor(t, "the step waits", func() bool {
		if _, err := os.Stat(filepath.Join(d.Store.Loc.Root, hostsetup.ReportsFile)); err != nil {
			return false
		}
		said := userReports(t, d)
		return len(said) > 0 && said[len(said)-1].State == wire.MachineSetupStateNeedsYou
	})
	said := userReports(t, d)
	if last := said[len(said)-1]; last.Detail == nil || *last.Detail != "Open Agent Loops to send GH_TOKEN to this machine." {
		t.Fatalf("%+v", last)
	}
	waited := time.Now()
	waitFor(t, "longer than the run's time limit", func() bool {
		if _, err := os.Stat(filepath.Join(d.PersonalHome, "ran.txt")); err == nil {
			t.Fatal("setup.sh ran before its secret was here")
		}
		return time.Since(waited) > userSetupTimeout+500*time.Millisecond
	})
	if u, _ := d.readUserSetup(); !u.Running {
		t.Fatalf("the wait ended the run: %+v", u)
	}
	must(t, d.saveSecret("GH_TOKEN", "gh-7156-not-real", false))
	if u := userSetupEnded(t, d); u.Error != "" {
		t.Fatalf("%+v", u)
	}
	if _, err := os.Stat(filepath.Join(d.PersonalHome, "ran.txt")); err != nil {
		t.Fatal("setup.sh did not run once its secret was here")
	}
	said = userReports(t, d)
	if last := said[len(said)-1]; last.State != wire.MachineSetupStateDone {
		t.Fatalf("%+v", last)
	}
}
