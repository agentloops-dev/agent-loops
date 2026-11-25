package agentsd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6749, #6750: an answer has no age while its connector or plugin is there, in the folder or in
// a worktree of it. 90 days after it went, each answer about it goes.
func TestAnApprovalGoesNinetyDaysAfterItsItemWent(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	write := func(path, text string) {
		must(t, os.MkdirAll(filepath.Dir(path), 0o750))
		must(t, os.WriteFile(path, []byte(text), 0o600))
	}
	server := func(name string) string { return `{"mcpServers":{"` + name + `":{"command":"x"}}}` }
	write(filepath.Join(dir, ".agents", "mcp.json"), server("here"))
	write(filepath.Join(dir, ".agents", "worktrees", "w", ".agents", "mcp.json"), server("a/branch"))
	must(t, os.MkdirAll(filepath.Join(dir, ".agents", "plugins", "here"), 0o750))
	key := func(name string) string { return `"` + dir + `|.agents/mcp.json|` + name + `":"x"` }
	write(d.mcpApprovalsPath(), `{"approvalsBegan":1,"approved":{`+key("here")+`,`+key("a/branch")+`,`+key("away")+`},`+
		`"denied":{`+key("away")+`},"from":{`+key("away")+`},"views":{"`+dir+`|away|ui://v":"show:h","`+dir+`|here|ui://v":"show:h"}}`)
	plugin := func(name string) string { return `"` + dir + `/.agents/plugins/` + name + `":"x"` }
	write(d.pluginApprovalsPath(), `{"approvalsBegan":1,"approved":{`+plugin("here")+`,`+plugin("away")+`},"denied":{`+plugin("away")+`}}`)
	d.mcpMu.Lock()
	d.mcpLoaded = false
	d.mcpMu.Unlock()
	texts := func() (string, string) {
		m, err := os.ReadFile(d.mcpApprovalsPath())
		must(t, err)
		p, err := os.ReadFile(d.pluginApprovalsPath())
		must(t, err)
		return string(m), string(p)
	}

	at := time.Now()
	d.sweepSmallStores(at)
	d.sweepSmallStores(at.Add(approvalKeptGone - time.Hour))
	if m, p := texts(); strings.Count(m, "away") != 6 || strings.Count(p, "away") != 3 {
		t.Fatalf("before 90 days: %s\n%s", m, p)
	}
	d.sweepSmallStores(at.Add(approvalKeptGone + time.Second))
	m, p := texts()
	if strings.Contains(m, "away") || strings.Contains(p, "away") {
		t.Fatalf("after 90 days: %s\n%s", m, p)
	}
	if strings.Count(m, "here") != 2 || !strings.Contains(m, "a/branch") || !strings.Contains(p, "here") {
		t.Fatalf("one that is there went: %s\n%s", m, p)
	}
}

type itemsBack struct{}

func (itemsBack) folderGone(string) bool { return false }
func (itemsBack) itemThere(string) bool  { return true }

// An item that is back within the 90 days counts from the start.
func TestAnItemThatIsBackHasNoMark(t *testing.T) {
	t.Parallel()

	at := time.Now()
	m, was := map[string]string{"k": "x"}, map[string]wire.Date{"k": wire.DateOf(at.Add(-2 * approvalKeptGone))}
	marks := map[string]wire.Date{}
	ageApprovals(m, was, marks, "", itemsBack{}, at)
	if len(m) != 1 || len(marks) != 0 {
		t.Fatalf("%v %v", m, marks)
	}
}

func TestApprovalsAreRefusedPastTheirLimits(t *testing.T) {
	t.Parallel()

	m := map[string]string{}
	for i := range approvalsMost {
		must(t, keepApproval(m, nil, "", fmt.Sprint(i), "x"))
	}
	if err := keepApproval(m, nil, "", "more", "x"); err == nil || len(m) != approvalsMost {
		t.Fatalf("%v, %d answers", err, len(m))
	}
	must(t, keepApproval(m, nil, "", "7", "y"))
	must(t, keepApproval(m, map[string]wire.Date{"view 3": 2, "view 4": 1}, "view ", "more", "x"))
	if _, kept := m["4"]; kept || m["3"] == "" || m["more"] == "" || len(m) != approvalsMost {
		t.Fatal("the answer whose item went the longest ago did not make room")
	}
	if err := keepApproval(m, nil, "", "8", strings.Repeat("x", approvalMostBytes)); err == nil {
		t.Fatal("one too large was kept")
	}
}

// #6747: readings age out; a credential out until a time still to come stays.
func TestAllowancesAgeOut(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	at := time.Now()
	date := func(ago time.Duration) wire.Date { return wire.DateOf(at.Add(-ago)) }
	day := 24 * time.Hour
	state := func(key string, changed time.Duration) wire.AllowanceState {
		s := availableState(key)
		s.ChangedAt = date(changed)
		return s
	}
	until, weekly := date(-day), "weekly"
	out := state("out:sign-in", 100*day)
	out.Status = wire.AllowanceStateStatus{Out: &wire.AllowanceStateStatusOut{Until: &until}}
	read := state("read:sign-in", 100*day)
	read.Weeks = []wire.AllowanceReading{{Window: &weekly, At: date(91 * day)}, {Window: &weekly, At: date(day)}}
	ahead := state("ahead:sign-in", -1000*day)
	d.allowMu.Lock()
	for _, s := range []wire.AllowanceState{state("old:sign-in", 90*day), out, read, ahead} {
		d.allowancesLocked()[s.CredentialKey] = s
	}
	d.allowMu.Unlock()

	d.sweepAllowances(at)

	d.allowMu.Lock()
	defer d.allowMu.Unlock()
	if _, kept := d.allowances["old:sign-in"]; kept || d.allowances["out:sign-in"].Status.Out == nil {
		t.Fatalf("%+v", d.allowances)
	}
	if w := d.allowances["read:sign-in"].Weeks; len(w) != 1 || w[0].At != date(day) {
		t.Fatalf("%+v", w)
	}
	if got := d.allowances["ahead:sign-in"].ChangedAt; got != wire.DateOf(at) {
		t.Fatalf("%v", got.Time())
	}
	if b, _ := os.ReadFile(d.allowancesPath()); strings.Contains(string(b), "old:") || !strings.Contains(string(b), "out:") {
		t.Fatalf("%s", b)
	}

	for i := range allowancesMost + 5 {
		s := state(fmt.Sprintf("%d:sign-in", i), time.Duration(i+1)*time.Minute)
		if i == 0 {
			s.EntryID = wire.UUID(strings.Repeat("x", allowanceMostBytes))
		}
		d.allowances[s.CredentialKey] = s
	}
	d.saveAllowancesLocked()
	if _, kept := d.allowances["0:sign-in"]; kept || len(d.allowances) != allowancesMost {
		t.Fatalf("%d records", len(d.allowances))
	}
	if _, kept := d.allowances["1:sign-in"]; !kept {
		t.Fatal("the newest went")
	}
}

// Another machine's allowance that aged out, or from a clock far ahead, is not taken back.
func TestAnAgedAllowanceOfAnotherMachineIsNotTaken(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	for _, ago := range []time.Duration{allowanceKept, -48 * time.Hour} {
		s := availableState("claude:sign-in")
		s.ChangedAt = wire.DateOf(time.Now().Add(-ago))
		if d.applyAllowances([]wire.AllowanceState{s}) {
			t.Fatalf("taken: %v ago", ago)
		}
	}
}

// #6748: a hundred accounts at most; one more is refused, and no account goes.
func TestAddedAccountsHaveALimit(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.accountMu.Lock()
	d.profilesLocked()
	for i := range accountsMost {
		d.profiles = append(d.profiles, wire.AccountProfile{ID: fmt.Sprint(i), RuntimeID: "claude", Name: fmt.Sprint("n", i)})
	}
	d.accountMu.Unlock()
	if _, err := d.addAccount(wire.AddAccountRequest{RuntimeID: "claude", Name: "one more"}); err == nil || !strings.Contains(err.Error(), "Remove one first") {
		t.Fatalf("%v", err)
	}
	if len(d.profiles) != accountsMost {
		t.Fatalf("%d accounts", len(d.profiles))
	}
}
