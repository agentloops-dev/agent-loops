package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func reports(t *testing.T, l Layout) []wire.MachineSetupReport {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(l.Root, ReportsFile))
	var out []wire.MachineSetupReport
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var r wire.MachineSetupReport
		if json.Unmarshal(line, &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func lastOf(rs []wire.MachineSetupReport, step wire.MachineSetupStepID) *wire.MachineSetupReport {
	var out *wire.MachineSetupReport
	for i := range rs {
		if rs[i].Step == step {
			out = &rs[i]
		}
	}
	return out
}

// fakeHost joins as soon as it is started and installs a runtime on the second look.
func fakeHost(t *testing.T, l Layout, started *int) Deps {
	t.Helper()
	return Deps{
		Poll: time.Millisecond, JoinWait: time.Second, RuntimeWait: time.Second,
		StartHost: func(binary, root, name string) (string, error) {
			*started++
			return "in the background", os.WriteFile(filepath.Join(root, "control-host.json"), []byte("{}"), 0o600)
		},
		Running: func(root string) bool { return *started > 0 },
		Call: func(method string, params any) (json.RawMessage, error) {
			switch method {
			case wire.CallSetupProjects.Name:
				return json.Marshal(wire.MachineSetupProjects{Finished: true, Folders: []string{}})
			case wire.CallRuntimesInstall.Name:
				p := "Downloading"
				return json.Marshal(wire.RuntimeStatus{Runtime: wire.Runtime{ID: params.(wire.RuntimeRequest).RuntimeID},
					Availability: wire.RuntimeAvailability{Installing: &wire.RuntimeAvailabilityInstalling{Progress: &p}}})
			case wire.CallRuntimesList.Name:
				var all []wire.RuntimeStatus
				for _, id := range []string{"claude", "codex"} {
					v := "1.2." + id
					all = append(all, wire.RuntimeStatus{Runtime: wire.Runtime{ID: id}, Version: &v,
						Availability: wire.RuntimeAvailability{Available: &wire.RuntimeAvailabilityAvailable{Path: "/x"}}})
				}
				return json.Marshal(all)
			}
			t.Fatalf("unexpected call %s", method)
			return nil, nil
		},
	}
}

func options(t *testing.T) (Options, Layout) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "agentsd")
	must(t, os.WriteFile(exe, []byte("the host"), 0o700)) //nolint:gosec // G306: the stand-in for agentsd must run
	// A code with the right shape is not needed: the join file is written only for a parsed one,
	// so a test that has no real code joins by an existing membership.
	return Options{Join: "agents-control:x", Name: "dev-2", Home: home, Executable: exe}, LayoutOf(home)
}

func TestRunWithAnAlreadyJoinedHostSetsUpAndIsSafeToRunAgain(t *testing.T) {
	o, l := options(t)
	must(t, os.MkdirAll(l.Root, 0o700))
	must(t, os.WriteFile(filepath.Join(l.Root, "control-host.json"), []byte("{}"), 0o600))
	project := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(project, ".agents"), 0o700))
	must(t, os.WriteFile(filepath.Join(project, SetupScript), []byte("echo \"$AGENTS_SETUP $AGENTS_MACHINE $AGENTS_FROM_POOL\" > out.txt\necho warmed\n"), 0o600))
	o.Projects = []string{project}
	started := 1 // running already
	deps := fakeHost(t, l, &started)
	var out bytes.Buffer
	deps.Out = &out

	for run := 0; run < 2; run++ {
		if err := Run(context.Background(), o, deps); err != nil {
			t.Fatalf("run %d: %v\n%s", run, err, out.String())
		}
		rs := reports(t, l)
		for _, step := range []wire.MachineSetupStepID{wire.MachineSetupStepIDInstall, wire.MachineSetupStepIDCompare, wire.MachineSetupStepIDRuntimes, wire.MachineSetupStepIDProjects} {
			if r := lastOf(rs, step); r == nil || r.State != wire.MachineSetupStateDone {
				t.Fatalf("run %d: step %s = %+v", run, step, r)
			}
		}
		if got := *lastOf(rs, wire.MachineSetupStepIDRuntimes).Detail; got != "claude 1.2.claude · codex 1.2.codex" {
			t.Errorf("runtimes detail = %q", got)
		}
	}
	b, _ := os.ReadFile(filepath.Join(project, "out.txt"))
	if strings.TrimSpace(string(b)) != "1 dev-2 0" {
		t.Errorf("the script saw %q", b)
	}
	if started != 1 {
		t.Errorf("a running host was started again")
	}
}

func TestInstallRefusesWhatIsNotAHostCode(t *testing.T) {
	o, l := options(t)
	started := 0
	err := Run(context.Background(), o, fakeHost(t, l, &started))
	if err == nil || !strings.Contains(err.Error(), "not a host code") {
		t.Fatalf("err = %v", err)
	}
	r := lastOf(reports(t, l), wire.MachineSetupStepIDInstall)
	if r == nil || r.State != wire.MachineSetupStateFailed || r.Error == nil {
		t.Fatalf("install = %+v", r)
	}
	if lastOf(reports(t, l), wire.MachineSetupStepIDRuntimes) != nil {
		t.Error("runtimes started after a failed install")
	}
}

func TestAFailedScriptFailsStepSixWithItsLastLinesAndStepFiveStillRuns(t *testing.T) {
	o, l := options(t)
	must(t, os.MkdirAll(l.Root, 0o700))
	must(t, os.WriteFile(filepath.Join(l.Root, "control-host.json"), []byte("{}"), 0o600))
	bad := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(bad, ".agents"), 0o700))
	must(t, os.WriteFile(filepath.Join(bad, SetupScript), []byte("echo one\nfalse\necho never\n"), 0o600))
	o.Projects = []string{bad, filepath.Join(t.TempDir(), "missing")}
	started := 1
	err := Run(context.Background(), o, fakeHost(t, l, &started))
	if err == nil {
		t.Fatal("no error")
	}
	rs := reports(t, l)
	p := lastOf(rs, wire.MachineSetupStepIDProjects)
	if p.State != wire.MachineSetupStateFailed || !strings.Contains(*p.Error, "one") || strings.Contains(*p.Error, "never") ||
		!strings.Contains(*p.Error, "is not on this machine") {
		t.Errorf("projects = %+v", p)
	}
	if r := lastOf(rs, wire.MachineSetupStepIDRuntimes); r.State != wire.MachineSetupStateDone {
		t.Errorf("runtimes = %+v", r)
	}
}

func TestRunScriptHoldsNoCredentialsAndStopsAtTheTimeout(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret")
	project := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(project, ".agents"), 0o700))
	must(t, os.WriteFile(filepath.Join(project, SetupScript), []byte(`[ -z "$GITHUB_TOKEN" ] || exit 7
exec sleep 30
`), 0o600))
	start := time.Now()
	had, err := RunScript(context.Background(), project, "m", false, 300*time.Millisecond, nil, nil)
	if !had || err == nil || !strings.Contains(err.Error(), "ran longer than") {
		t.Fatalf("had=%v err=%v", had, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("the timeout did not stop it")
	}
	had, err = RunScript(context.Background(), t.TempDir(), "m", false, time.Second, nil, nil)
	if had || err != nil {
		t.Errorf("a project with no script: had=%v err=%v", had, err)
	}
}

func TestPlaceBinaryIsRepeatable(t *testing.T) {
	o, l := options(t)
	must(t, os.MkdirAll(l.Bin, 0o700))
	a, err := placeBinary(o.Executable, l)
	if err != nil {
		t.Fatal(err)
	}
	b, err := placeBinary(o.Executable, l)
	if err != nil || a != b {
		t.Fatalf("%v %s %s", err, a, b)
	}
	if got, _ := os.Readlink(filepath.Join(l.Bin, "current")); got != "agentsd-"+a {
		t.Errorf("current -> %s", got)
	}
}

// Step 4 carries the digits of the code this machine joined with (#5359, #5765): the control
// plane compares them with the ones it made, and fails a made machine's step with none.
func TestCompareSaysTheDigitsOfTheJoinCode(t *testing.T) {
	o, l := options(t)
	private := controllink.NewPrivateKey()
	public, _ := controllink.PublicKey(private)
	_, secret := controllink.MakeCodeSecret(private)
	code := &controllink.Code{Host: true, ControlKey: public, Secret: secret, URL: "https://cp.example", Name: "cp"}
	o.Join = code.Text()
	started := 0
	if err := Run(context.Background(), o, fakeHost(t, l, &started)); err != nil {
		t.Fatal(err)
	}
	r := lastOf(reports(t, l), wire.MachineSetupStepIDCompare)
	if r == nil || r.State != wire.MachineSetupStateDone || r.Code == nil || *r.Code != code.Digits() {
		t.Fatalf("compare = %+v, want the digits %s", r, code.Digits())
	}
	if b, _ := os.ReadFile(filepath.Join(l.Root, "control-join-code")); string(b) != o.Join {
		t.Errorf("the host joins with %q", b)
	}
}

// must fails the test at once when a set-up step fails.
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// #6890, #6896: a pool machine's installer gets the pool's projects, fails step 6 when they do
// not come, and stops after step 6; the claim's run goes on to the end, after what was said.
func TestAPoolMachineStopsAfterItsProjectsAndAClaimGoesOn(t *testing.T) {
	o, l := options(t)
	o.Smoke = true
	must(t, os.MkdirAll(l.Root, 0o700))
	must(t, os.WriteFile(filepath.Join(l.Root, "control-host.json"), []byte("{}"), 0o600))
	project := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(project, ".agents"), 0o700))
	must(t, os.WriteFile(filepath.Join(project, SetupScript), []byte("echo \"$AGENTS_FROM_POOL\" > out.txt\n"), 0o600))
	started := 1
	deps := fakeHost(t, l, &started)
	base, polls := deps.Call, 0
	problem := "app: Mac is offline"
	answer := wire.MachineSetupProjects{Pool: true, Finished: true, Problem: &problem, Folders: []string{}}
	smoke := &smokeHost{signedIn: true, state: wire.AgentStateFinished, message: "Hello.", finishCall: true}
	deps.Call = func(method string, params any) (json.RawMessage, error) {
		if method == wire.CallSetupProjects.Name {
			if polls++; polls%2 == 1 {
				detail := "Getting app"
				return json.Marshal(wire.MachineSetupProjects{Pool: true, Detail: &detail, Folders: []string{}})
			}
			return json.Marshal(answer)
		}
		if method == wire.CallAgentsOptions.Name || strings.HasPrefix(method, "agents/") {
			return smoke.call(method, params)
		}
		return base(method, params)
	}
	if err := Run(context.Background(), o, deps); err == nil {
		t.Fatal("no projects, and the set-up went on")
	}
	if r := lastOf(reports(t, l), wire.MachineSetupStepIDProjects); r.State != wire.MachineSetupStateFailed || *r.Error != problem {
		t.Fatalf("%+v", r)
	}
	answer = wire.MachineSetupProjects{Pool: true, Finished: true, Folders: []string{project}}
	if err := Run(context.Background(), o, deps); err != nil {
		t.Fatal(err)
	}
	rs := reports(t, l)
	if r := lastOf(rs, wire.MachineSetupStepIDProjects); r.State != wire.MachineSetupStateDone || !strings.Contains(*r.Detail, "ready") {
		t.Fatalf("%+v", r)
	}
	if lastOf(rs, wire.MachineSetupStepIDSignIn) != nil || lastOf(rs, wire.MachineSetupStepIDSmoke) != nil {
		t.Fatal("a pool machine went on to sign in")
	}

	o.FromPool = true
	if err := Continue(context.Background(), o, deps, l.Root, wire.MachineSetupStepIDProjects); err != nil {
		t.Fatal(err)
	}
	after := reports(t, l)
	if len(after) <= len(rs) {
		t.Fatal("the claim's run wrote over what was said")
	}
	for _, id := range []wire.MachineSetupStepID{wire.MachineSetupStepIDProjects, wire.MachineSetupStepIDSignIn, wire.MachineSetupStepIDSmoke} {
		if r := lastOf(after[len(rs):], id); r == nil || r.State != wire.MachineSetupStateDone {
			t.Fatalf("%s: %+v", id, r)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(project, "out.txt")); strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("the script saw %q", b)
	}
	if !strings.Contains(string(smoke.started.Cwd), filepath.Base(project)) {
		t.Fatalf("smoke in %s", smoke.started.Cwd)
	}
	// A host that stops says no failure: the next one runs the step again.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	said := len(reports(t, l))
	_ = Continue(ctx, o, deps, l.Root, wire.MachineSetupStepIDProjects)
	if len(reports(t, l)) != said {
		t.Fatal("a host that stopped said a step")
	}
}

// #7314: a control-only machine marks its host before it joins, so it enrols with the
// control role alone, and asks its host for no runtime, project or session.
func TestAControlOnlyMachineJoinsAndInstallsNoAgents(t *testing.T) {
	o, l := options(t)
	private := controllink.NewPrivateKey()
	public, _ := controllink.PublicKey(private)
	_, secret := controllink.MakeCodeSecret(private)
	o.Join = (&controllink.Code{Host: true, ControlKey: public, Secret: secret, URL: "https://cp.example", Name: "cp"}).Text()
	o.ControlOnly, o.Smoke = true, true
	started := 0
	deps := fakeHost(t, l, &started)
	deps.Call = func(method string, _ any) (json.RawMessage, error) {
		t.Fatalf("a control-only machine asked its host %s", method)
		return nil, nil
	}
	if err := Run(context.Background(), o, deps); err != nil {
		t.Fatal(err)
	}
	if !controllink.ControlOnly(l.Root) || started != 1 {
		t.Fatalf("control only %v, started %d", controllink.ControlOnly(l.Root), started)
	}
	rs := reports(t, l)
	for _, step := range []wire.MachineSetupStepID{wire.MachineSetupStepIDRuntimes, wire.MachineSetupStepIDProjects} {
		if r := lastOf(rs, step); r == nil || r.State != wire.MachineSetupStateDone || *r.Detail != controlOnlyStep {
			t.Fatalf("step %s = %+v", step, r)
		}
	}
	if lastOf(rs, wire.MachineSetupStepIDSmoke) != nil {
		t.Fatal("a control-only machine ran a smoke session")
	}
}

// A run of the installer marks the checkout, so a send of the same script runs nothing (#7158).
func TestRunScriptMarksTheCheckout(t *testing.T) {
	project := t.TempDir()
	if out, err := exec.Command("git", "-C", project, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	must(t, os.MkdirAll(filepath.Join(project, ".agents"), 0o700))
	must(t, os.WriteFile(filepath.Join(project, SetupScript), []byte("echo hi\n"), 0o600))
	if had, err := RunScript(context.Background(), project, "m", false, time.Minute, nil, nil); !had || err != nil {
		t.Fatal(had, err)
	}
	if had, err := RunScriptIfChanged(context.Background(), project, "m", false, time.Minute, nil, nil); had || err != nil {
		t.Fatalf("the same script ran again: %v %v", had, err)
	}
	must(t, os.WriteFile(filepath.Join(project, SetupScript), []byte("echo changed\n"), 0o600))
	if had, err := RunScriptIfChanged(context.Background(), project, "m", false, time.Minute, nil, nil); !had || err != nil {
		t.Fatalf("a changed script did not run: %v %v", had, err)
	}
}
