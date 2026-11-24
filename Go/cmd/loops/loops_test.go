package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// fake is a control plane that answers each method with a function, and keeps the calls.
type fake struct {
	answer map[string]func(host string, params any) (any, error)
	calls  []string
}

func (f *fake) Call(_ context.Context, host, method string, params any) (json.RawMessage, error) {
	f.calls = append(f.calls, host+" "+method)
	a, ok := f.answer[method]
	if !ok {
		return nil, fmt.Errorf("no method %s", method)
	}
	v, err := a(host, params)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(v)
	return b, nil
}

func ptr[T any](v T) *T { return &v }

func TestHostsKeepsOnePairingPerControlPlaneInAFileOnlyItsOwnerReads(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h, err := readHosts()
	if err != nil || len(h.Hosts) != 0 {
		t.Fatalf("no file is no pairing: %+v %v", h, err)
	}
	h.put(controllink.ClientMembership{Client: "A", URL: "wss://one.example"})
	h.put(controllink.ClientMembership{Client: "B", URL: "wss://two.example"})
	h.put(controllink.ClientMembership{Client: "C", URL: "wss://one.example"})
	if err := h.write(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(hostsFile())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("hosts.json is %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(hostsFile()))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("its folder is %v, want 0700", di.Mode().Perm())
	}
	got, err := readHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != 2 || got.Default != "wss://one.example" || got.pick("").Client != "C" {
		t.Fatalf("a pairing again replaces the old one and is the default: %+v", got)
	}
	if got.pick("two.example").Client != "B" || got.pick("wss://two.example").Client != "B" || got.pick("three.example") != nil {
		t.Fatalf("--control picks by URL or host name")
	}
	got.remove("wss://one.example")
	if got.Default != "wss://two.example" || len(got.Hosts) != 1 {
		t.Fatalf("the default moves to the one left: %+v", got)
	}
	got.remove("wss://two.example")
	if got.pick("") != nil || got.Default != "" {
		t.Fatalf("none left: %+v", got)
	}
}

func TestCandidatesAreTheDirectoryAndItsParentsNearestFirst(t *testing.T) {
	got := candidates("/home/a/src/agent-loops/Go/internal/")
	want := []string{"/home/a/src/agent-loops/Go/internal", "/home/a/src/agent-loops/Go", "/home/a/src/agent-loops", "/home/a/src", "/home/a", "/home", "/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestPickMachine(t *testing.T) {
	list := []wire.ControlMachine{
		{ID: "h1", Name: "Alex's MacBook Pro", MachineID: ptr("UUID-ONE")},
		{ID: "h2", Name: "devbox", MachineID: ptr("abc123")},
	}
	for _, c := range []struct{ want, here, hostname, id string }{
		{"", "uuid-one", "", "h1"},         // this computer, by its machine id
		{"", "nope", "devbox.local", "h2"}, // else by its host name
		{"devbox", "uuid-one", "", "h2"},   // --machine by name
		{"H1", "", "", "h1"},               // by the control plane's id
		{"abc123", "", "", "h2"},           // by its machine id
	} {
		m, err := pickMachine(list, c.want, c.here, c.hostname)
		if err != nil || string(m.ID) != c.id {
			t.Errorf("%+v: got %s %v", c, m.ID, err)
		}
	}
	if _, err := pickMachine(list, "", "other", "other"); codeOf(err) != exitError || !strings.Contains(err.Error(), "--machine") {
		t.Errorf("not a machine: %v", err)
	}
	if _, err := pickMachine(list, "gone", "", ""); codeOf(err) != exitError {
		t.Errorf("no such machine: %v", err)
	}
}

func machinesFake(f *fake) {
	f.answer["machines/list"] = func(string, any) (any, error) {
		return wire.MachineList{Machines: []wire.ControlMachine{{ID: "h1", Name: "Alex's MacBook Pro", MachineID: ptr("UUID-ONE")}}}, nil
	}
}

func TestLocateFindsTheNearestFolderInOneCall(t *testing.T) {
	t.Setenv("AGENTS_MACHINE_ID", "uuid-one")
	f := &fake{answer: map[string]func(string, any) (any, error){}}
	machinesFake(f)
	var asked []string
	f.answer["folders/find"] = func(host string, params any) (any, error) {
		asked = params.(wire.FoldersFindRequest).Folders
		out := []wire.FoundFolder{}
		for _, p := range asked {
			out = append(out, wire.FoundFolder{Folder: wire.URL("file://" + p), IsFolder: p == "/src/agent-loops" || p == "/src"})
		}
		return out, nil
	}
	p, err := locate(context.Background(), f, options{}, "/src/agent-loops/Go/internal")
	if err != nil {
		t.Fatal(err)
	}
	if p.folder != "file:///src/agent-loops" || p.folderName() != "agent-loops" || p.machineName() != "Alex's MacBook Pro" {
		t.Fatalf("got %+v", p)
	}
	if len(asked) != 5 || strings.Join(f.calls, ",") != " machines/list,h1 folders/find" {
		t.Fatalf("one folders/find with the directory and its parents: %v %v", asked, f.calls)
	}

	f.answer["folders/find"] = func(string, any) (any, error) {
		return []wire.FoundFolder{{Folder: "file:///tmp", IsFolder: false}}, nil
	}
	p, err = locate(context.Background(), f, options{}, "/tmp")
	if err != nil || p.folder != "" {
		t.Fatalf("not in a folder: %+v %v", p, err)
	}
	err = notInFolder(p)
	want := "/tmp is not in a folder on Alex's MacBook Pro.\nAdd it: loops folders add"
	if codeOf(err) != exitNoFolder || err.Error() != want {
		t.Fatalf("state 6: got %q, want %q", err.Error(), want)
	}
}

func TestLocateByFolderName(t *testing.T) {
	t.Setenv("AGENTS_MACHINE_ID", "uuid-one")
	f := &fake{answer: map[string]func(string, any) (any, error){}}
	machinesFake(f)
	f.answer["folders/list"] = func(string, any) (any, error) {
		return []wire.FolderSummary{
			{Name: "agent-loops", Folder: wire.Folder{Folder: "file:///src/agent-loops"}},
			{Name: "site", Folder: wire.Folder{Folder: "file:///src/site"}},
			{Name: "site", Folder: wire.Folder{Folder: "file:///old/site"}},
		}, nil
	}
	p, err := locate(context.Background(), f, options{folder: "agent-loops"}, "/anywhere")
	if err != nil || p.folder != "file:///src/agent-loops" {
		t.Fatalf("by name: %+v %v", p, err)
	}
	if _, err := locate(context.Background(), f, options{folder: "site"}, "/anywhere"); codeOf(err) != exitError {
		t.Fatalf("two of that name: %v", err)
	}
	if _, err := locate(context.Background(), f, options{folder: "nope"}, "/anywhere"); codeOf(err) != exitNoFolder {
		t.Fatalf("none of that name: %v", err)
	}
}

func agents(n int, from time.Time) []wire.Agent {
	out := make([]wire.Agent, n)
	for i := range out {
		out[i] = wire.Agent{ID: wire.UUID(fmt.Sprintf("%08X-0000-0000-0000-000000000000", i)), State: wire.AgentStateFinished,
			LastActivityAt: wire.DateOf(from.Add(-time.Duration(i) * time.Minute))}
	}
	return out
}

func TestListSessionsPagesOnlyWithAll(t *testing.T) {
	now := time.Now()
	all := agents(45, now)
	f := &fake{answer: map[string]func(string, any) (any, error){}}
	var reqs []wire.ListRequest
	f.answer["agents/list"] = func(_ string, params any) (any, error) {
		r := params.(wire.ListRequest)
		reqs = append(reqs, r)
		start := 0
		if r.After != nil {
			for i, a := range all {
				if a.ID == r.After.ID {
					start = i + 1
				}
			}
		}
		return all[start:min(start+int(*r.Limit), len(all))], nil
	}
	p := place{machine: wire.ControlMachine{ID: "h1"}, folder: "file:///src/x"}
	list, more, err := listSessions(context.Background(), f, p, false)
	if err != nil || len(list) != 20 || !more || len(reqs) != 1 || *reqs[0].Folder != p.folder || reqs[0].IncludeArchived {
		t.Fatalf("one page of 20: %d %v %v %+v", len(list), more, err, reqs)
	}
	reqs = nil
	list, more, err = listSessions(context.Background(), f, p, true)
	if err != nil || len(list) != 45 || more || len(reqs) != 3 || !reqs[0].IncludeArchived || reqs[2].After.ID != all[39].ID {
		t.Fatalf("--all reads every page: %d %v %v", len(list), more, err)
	}
}

func TestRecentSessionsReadsOneBoundedPage(t *testing.T) {
	now := time.Now()
	all := agents(recentLimit+3, now)
	f := &fake{answer: map[string]func(string, any) (any, error){}}
	var reqs []wire.ListRequest
	f.answer["agents/list"] = func(_ string, params any) (any, error) {
		reqs = append(reqs, params.(wire.ListRequest))
		return all, nil
	}
	p := place{machine: wire.ControlMachine{ID: "h1"}, folder: "file:///src/agent-loops"}
	list, err := recentSessions(context.Background(), f, p)
	if err != nil || len(list) != recentLimit || len(reqs) != 1 || strings.Join(f.calls, ",") != "h1 agents/list" {
		t.Fatalf("one bounded page: %d %v %v %v", len(list), len(reqs), err, f.calls)
	}
	r := reqs[0]
	if r.Limit == nil || *r.Limit != recentLimit || r.Folder == nil || *r.Folder != p.folder || !r.Lean || r.IncludeArchived || r.After != nil || r.AgentID != nil {
		t.Fatalf("the recent request: %+v", r)
	}
	for i := range list {
		if list[i].ID != all[i].ID {
			t.Fatalf("kept the newest %d, not %s", recentLimit, list[i].ID)
		}
	}
}

func TestPrintRecentIsANumberedTableWithoutAFolderColumn(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) wire.Date { return wire.DateOf(now.Add(-d)) }
	list := []wire.Agent{
		{ID: "7F3A2C10-0000-0000-0000-000000000000", Title: ptr("Update the parity table"), State: wire.AgentStateWaitingOnUser, RuntimeID: "opencode", LastActivityAt: at(2 * time.Minute)},
		{ID: "91BE0400-0000-0000-0000-000000000000", Title: ptr("Fix the flaky stop test"), State: wire.AgentStateRunning, RuntimeID: "claude", LastActivityAt: at(0)},
	}
	p := place{machine: wire.ControlMachine{ID: "h1"}, folder: "file:///src/agent-loops"}
	var out bytes.Buffer
	printRecent(&out, plain, p, list, now)
	want := `Recent sessions in agent-loops
  1  Update the parity table  waiting  opencode  2 min ago
  2  Fix the flaky stop test  working  claude    now
`
	if out.String() != want {
		t.Fatalf("recent:\n%s\nwant:\n%s", out.String(), want)
	}
	if strings.Contains(out.String(), "TITLE") || strings.Contains(out.String(), "FOLDER") {
		t.Fatalf("the picker has no column headings: %s", out.String())
	}
	if stateRole(wire.AgentStateWaitingOnUser) != roleAttention {
		t.Fatal("waiting uses the attention role")
	}
}

func TestPrintList(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) wire.Date { return wire.DateOf(now.Add(-d)) }
	list := []wire.Agent{
		{ID: "7F3A2C10-0000-0000-0000-000000000000", Title: ptr("Update the parity table for the CLI"), State: wire.AgentStateWaitingOnUser, RuntimeID: "opencode", LastActivityAt: at(2 * time.Minute)},
		{ID: "91BE0400-0000-0000-0000-000000000000", Title: ptr("Split wiregen functions"), State: wire.AgentStateRunning, RuntimeID: "claude", LastActivityAt: at(0)},
		{ID: "C40D1100-0000-0000-0000-000000000000", Title: ptr("Fix the flaky stop test"), State: wire.AgentStateFinished, RuntimeID: "opencode", LastActivityAt: at(12 * time.Minute)},
		{ID: "0A9E7700-0000-0000-0000-000000000000", Title: ptr("Trim the Periphery cache"), State: wire.AgentStateStopped, RuntimeID: "codex", LastActivityAt: at(3 * time.Hour)},
	}
	p := place{machine: wire.ControlMachine{ID: "h1", Name: "Alex's MacBook Pro"}, folder: "file:///src/agent-loops"}
	var out, errOut bytes.Buffer
	printList(&out, &errOut, plain, p, list, false, false, now)
	want := `ID      TITLE                                STATE    RUNTIME   FOLDER       UPDATED
7f3a2c  Update the parity table for the CLI  waiting  opencode  agent-loops  2 min ago
91be04  Split wiregen functions              working  claude    agent-loops  now
c40d11  Fix the flaky stop test              idle     opencode  agent-loops  12 min ago
0a9e77  Trim the Periphery cache             done     codex     agent-loops  3 h ago
`
	if out.String() != want {
		t.Fatalf("state 4:\n%s\nwant:\n%s", out.String(), want)
	}
	if got := errOut.String(); got != "4 sessions · loops sessions list --all\n" {
		t.Fatalf("state 4 hint: %q", got)
	}
	out.Reset()
	errOut.Reset()
	printList(&out, &errOut, plain, place{machine: wire.ControlMachine{ID: "h1", Name: "Alex's MacBook Pro"}, folder: "file:///src/new-thing"}, nil, false, false, now)
	if out.String() != "" || errOut.String() != "No sessions in new-thing.\nStart one: loops \"what this folder needs\"\n" {
		t.Fatalf("state 5: %q %q", out.String(), errOut.String())
	}
}

func TestSessionsListPageCutHintGoesToStderr(t *testing.T) {
	now := time.Now()
	var out, errOut bytes.Buffer
	printList(&out, &errOut, plain, place{machine: wire.ControlMachine{ID: "h1"}, folder: "file:///src/x"}, agents(pageSize+2, now), true, false, now)
	if got := errOut.String(); got != "22 of many · loops sessions list --all\n" {
		t.Fatalf("page cut: %q", got)
	}
	if !strings.Contains(out.String(), "TITLE") || !strings.Contains(out.String(), "00000015") {
		t.Fatalf("page cut table: %q", out.String())
	}
}

func TestSessionsListKeepsTodaysJSONAsPrintOnly(t *testing.T) {
	err := run(context.Background(), []string{"sessions", "list", "--json"}, &bytes.Buffer{})
	if codeOf(err) != exitError || !strings.Contains(err.Error(), "--json goes with -p or --detach") {
		t.Fatalf("json on list: %v", err)
	}
}

func TestShortIDsGrowUntilUnique(t *testing.T) {
	got := shortIDs([]wire.Agent{{ID: "ABCDEF12-0000"}, {ID: "ABCDEF34-0000"}, {ID: "123456-0000"}})
	if !reflect.DeepEqual(got, []string{"abcdef1", "abcdef3", "1234560"}) {
		t.Fatalf("got %v", got)
	}
	if got := shortIDs([]wire.Agent{{ID: "same"}, {ID: "same"}}); got[0] != "same" {
		t.Fatalf("a duplicate does not loop for ever: %v", got)
	}
}

func TestNotPairedExitsFive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"sessions", "list"}, {"sessions", "create"}, {"auth", "status"}, {"auth", "logout"}, {"folders", "add"}, {"fix the test"}, {"--all", "sessions", "list"}} {
		err := run(context.Background(), args, &bytes.Buffer{})
		if codeOf(err) != exitNotPair || !strings.Contains(err.Error(), "loops auth login '<code>'") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestFlagsGoBeforeOrAfterTheCommand(t *testing.T) {
	o, rest, err := parse([]string{"--folder", "x", "sessions", "list", "--all", "--machine", "devbox"})
	if err != nil || o.folder != "x" || !o.all || o.machine != "devbox" || !reflect.DeepEqual(rest, []string{"sessions", "list"}) {
		t.Fatalf("%+v %v %v", o, rest, err)
	}
	if _, _, err := parse([]string{"--nope"}); codeOf(err) != exitError {
		t.Fatalf("an unknown flag: %v", err)
	}
	var b bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &b); err != nil || !strings.Contains(b.String(), "COMMANDS") || !strings.Contains(b.String(), "GLOBAL FLAGS") {
		t.Fatalf("help: %v\n%s", err, b.String())
	}
}

func TestUnreachableSaysNothingChanged(t *testing.T) {
	err := unreachable("control.agentloops.dev", fmt.Errorf("dial: %w", context.DeadlineExceeded))
	if codeOf(err) != exitError || err.Error() != "Cannot reach control.agentloops.dev (timed out).\nNothing changed. Try again, or run: loops auth status" {
		t.Fatalf("state 9: %q", err)
	}
	if codeOf(errors.New("x")) != exitError || codeOf(nil) != exitOK {
		t.Fatal("codeOf")
	}
}

// -h and --help are the help on stdout with exit 0, also beside print mode's flags.
func TestHelpFlagsPrintTheUsageAndExitZero(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"-p", "-h"}} {
		var b bytes.Buffer
		if err := run(context.Background(), args, &b); err != nil || !strings.Contains(b.String(), "USAGE") || !strings.Contains(b.String(), "GLOBAL FLAGS") {
			t.Fatalf("%v: %v\n%s", args, err, b.String())
		}
	}
	err := run(context.Background(), []string{"--bogus"}, &bytes.Buffer{})
	if codeOf(err) != exitError || !strings.Contains(err.Error(), "See: loops --help") || strings.Contains(err.Error(), "USAGE") {
		t.Fatalf("an unknown flag says so in two lines: %v", err)
	}
}

// A dial's error is a few words, as wireframe 9's; --debug adds its raw words (#6479).
func TestUnreachableSaysShortReasons(t *testing.T) {
	refused := httptest.NewServer(http.NotFoundHandler())
	refusedURL := refused.URL
	refused.Close()
	notPinned := httptest.NewTLSServer(http.NotFoundHandler())
	defer notPinned.Close()
	pin := controllink.B64URL(make([]byte, 32))
	cases := []struct {
		name, base, pin, want string
	}{
		{"refused", refusedURL, "", "it refused the connection"},
		{"no such host", "https://nowhere.invalid", "", "no such host"},
		{"not pinned", notPinned.URL, pin, "its certificate is not the paired one"},
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := controllink.Dial(ctx, c.base, c.pin)
		cancel()
		if err == nil {
			t.Fatalf("%s: dialled", c.name)
		}
		got := unreachable("control.example", err).Error()
		if want := "Cannot reach control.example (" + c.want + ")."; !strings.HasPrefix(got, want) || strings.Contains(got, "Debug:") {
			t.Fatalf("%s: %q, want %q (from %v)", c.name, got, want, err)
		}
	}
	noAnswer := &controllink.NoAnswerError{Host: "control.example", Wait: 10 * time.Second}
	if got := reason(fmt.Errorf("join: %w", noAnswer)); got != "timed out after 10 s" {
		t.Fatalf("no answer: %q", got)
	}
	if got := reason(errors.New("the key is wrong")); got != "the key is wrong" {
		t.Fatalf("an error with no short words keeps its own: %q", got)
	}
	debug = true
	t.Cleanup(func() { debug = false })
	raw := fmt.Errorf("dial tcp 127.0.0.1:1: %w", syscall.ECONNREFUSED)
	if got := unreachable("control.example", raw).Error(); !strings.HasSuffix(got, "\nDebug: "+raw.Error()) || !strings.HasPrefix(got, "Cannot reach control.example (it refused the connection).") {
		t.Fatalf("--debug: %q", got)
	}
}
