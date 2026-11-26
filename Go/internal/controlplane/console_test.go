package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Console ▸ Machines (#1804, M1): an admin sees every machine with its owner; a member is
// refused the Console.
func TestConsoleFleetIsEveryMachineForAnAdmin(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	owner := p.host("OWNERS-MAC")
	_, admin := p.client("Owner's window")
	bea := p.invite(admin, "Bea", wire.UserRoleMember)
	beas := p.machine(bea, "BEAS-MAC")

	var board wire.FleetBoard
	must(t, json.Unmarshal(call(t, admin, "", "console/fleet", map[string]any{}), &board))
	if len(board.Machines) != 2 {
		t.Fatalf("%+v", board)
	}
	owners := map[wire.MachineRef]string{}
	for _, m := range board.Machines {
		owners[m.Host.ID] = m.OwnerName
		if m.Bot || m.Host.State == "" {
			t.Fatalf("%+v", m)
		}
	}
	if owners[wire.MachineRef(owner.member.Host)] != "Local owner" || owners[wire.MachineRef(beas.member.Host)] != "Bea" {
		t.Fatalf("%v", owners)
	}

	if e := callError(t, bea.window, "", "console/fleet", map[string]any{}); !strings.Contains(e, "admin") {
		t.Fatal(e)
	}
}

func TestControlCapacityListsLastLookOfLiveCopies(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, admin := p.client("Owner's window")
	copy := copyRecord{ID: p.s.copyID, PeerURL: p.url, Started: records.Now(), Heartbeat: records.Now()}
	copyData, err := json.Marshal(copy)
	must(t, err)
	_, err = st.Put(context.Background(), copyRecordKey(copy.ID), copyData, store.Always)
	must(t, err)
	want := wire.ControlCapacityCopy{Copy: copy.ID, Level: wire.CapacityLevelNear, Reason: "the control plane is near capacity: its disk is busy", LoadPerCore: 2.3, At: wire.DateOf(time.Now())}
	capacityData, err := json.Marshal(want)
	must(t, err)
	_, err = st.Put(context.Background(), controlCapacityKey(copy.ID), capacityData, store.Always)
	must(t, err)

	var got wire.ControlCapacityBoard
	must(t, json.Unmarshal(call(t, admin, "", "control/capacity", map[string]any{}), &got))
	if len(got.Copies) != 1 || got.Copies[0].Copy != want.Copy || got.Copies[0].Level != want.Level || got.Copies[0].LoadPerCore != want.LoadPerCore {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// readsOf is a store that records the keys read in a context that carries readsKey.
type readsOf struct {
	store.Store
	mu   sync.Mutex
	read []string
}

type readsKey struct{}

func (r *readsOf) Get(ctx context.Context, key string) (*store.Object, error) {
	if ctx.Value(readsKey{}) != nil {
		r.mu.Lock()
		r.read = append(r.read, key)
		r.mu.Unlock()
	}
	return r.Store.Get(ctx, key)
}

// #7360: control/capacity finds the copies from their samples, so it never reads a host's
// kept list in v1/copies/, however large, and leaves out the sample of a copy that is gone.
func TestControlCapacityReadsNoHostsKeptList(t *testing.T) {
	t.Parallel()
	st := &readsOf{Store: store.NewMemory()}
	p := startPlane(t, st, controllink.NewPrivateKey())
	ctx := context.Background()
	live := copyRecord{ID: p.s.copyID, PeerURL: p.url, Started: records.Now(), Heartbeat: records.Now()}
	data, err := json.Marshal(live)
	must(t, err)
	_, err = st.Put(ctx, copyRecordKey(live.ID), data, store.Always)
	must(t, err)
	for _, id := range []string{live.ID, "deadc0de"} {
		data, err := json.Marshal(wire.ControlCapacityCopy{Copy: id, Level: wire.CapacityLevelOk, At: wire.DateOf(time.Now())})
		must(t, err)
		_, err = st.Put(ctx, controlCapacityKey(id), data, store.Always)
		must(t, err)
	}
	agents := make([]map[string]any, 500)
	for i := range agents {
		agents[i] = map[string]any{"id": fmt.Sprintf("A%d", i), "state": "running", "title": strings.Repeat("x", 200)}
	}
	list, err := json.Marshal(map[string]any{"agents": agents})
	must(t, err)
	for i := range 20 {
		_, err = st.Put(ctx, copyKey(fmt.Sprintf("HOST-%d", i)), list, store.Always)
		must(t, err)
	}

	got, err := p.s.methods.controlCapacity(context.WithValue(ctx, readsKey{}, true))
	must(t, err)
	if len(got.Copies) != 1 || got.Copies[0].Copy != live.ID {
		t.Fatalf("only the live copy's sample: %+v", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, key := range st.read {
		if strings.HasPrefix(key, copiesPrefix) && key != copyRecordKey(live.ID) && key != copyRecordKey("deadc0de") {
			t.Errorf("control/capacity read %s", key)
		}
	}
	// This copy's own sample needs no copy record (#6591).
	if len(st.read) != 3 {
		t.Errorf("two samples and the other copy's record, not %v", st.read)
	}
}

// A copy's look names the machine record that runs it (#6591), so Machines can give that row
// its level; a sample kept before the copy knew its machine takes the copy record's.
func TestControlCapacityNamesTheCopysMachine(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, admin := p.client("Owner's window")
	copy := copyRecord{ID: "copy-b", PeerURL: p.url, Started: records.Now(), Heartbeat: records.Now(), Machine: "control"}
	copyData, err := json.Marshal(copy)
	must(t, err)
	_, err = st.Put(context.Background(), copyRecordKey(copy.ID), copyData, store.Always)
	must(t, err)
	capacityData, err := json.Marshal(wire.ControlCapacityCopy{Copy: copy.ID, Level: wire.CapacityLevelOk, At: wire.DateOf(time.Now())})
	must(t, err)
	_, err = st.Put(context.Background(), controlCapacityKey(copy.ID), capacityData, store.Always)
	must(t, err)

	var got wire.ControlCapacityBoard
	must(t, json.Unmarshal(call(t, admin, "", "control/capacity", map[string]any{}), &got))
	for _, c := range got.Copies {
		if c.Copy == copy.ID {
			if c.Machine == nil || *c.Machine != "control" {
				t.Fatalf("got %+v, want the machine control", c)
			}
			return
		}
	}
	t.Fatalf("no look of %s in %+v", copy.ID, got)
}

// A single copy has no mesh and so no copy record: its own look is on the board all the same.
func TestControlCapacityHasTheOneCopysOwnLook(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, admin := p.client("Owner's window")
	p.s.keepControlCapacity(controlCapacity{level: wire.CapacityLevelOk, at: time.Now()})

	var got wire.ControlCapacityBoard
	must(t, json.Unmarshal(call(t, admin, "", "control/capacity", map[string]any{}), &got))
	if len(got.Copies) != 1 || got.Copies[0].Copy != p.s.copyID {
		t.Fatalf("got %+v, want the look of %s", got, p.s.copyID)
	}
}

func TestKeptControlCapacityNamesItsMachine(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	id := "control"
	p.s.controlMachine.Store(&id)
	got := p.s.keepControlCapacity(controlCapacity{level: wire.CapacityLevelNear, reason: "pressure_io", at: time.Now()})
	if got.Machine == nil || *got.Machine != "control" {
		t.Fatalf("got %+v, want the machine control", got)
	}
}

// Every Console method is an admin's and names no machine (as TestEveryClientMethodChecksTheOwner
// has it for the others), and none is in the other lists.
func TestConsoleMethodsAreAdminsOnly(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	_, admin := p.client("Owner's window")
	bea := p.invite(admin, "Bea", wire.UserRoleMember)
	for method := range consoleMethods {
		if own[method] || workspaceMethods[method] || hostNamingMethods[method] || !p.s.methods.Handles(method) {
			t.Errorf("%s: a Console method is the Console's alone", method)
		}
		if e := callError(t, bea.window, "", method, map[string]any{}); !strings.Contains(e, "Only an admin") {
			t.Errorf("%s: %s", method, e)
		}
	}
}

// Console ▸ Team Workflows (#1804, M6): each bot with its group, its machines, the
// workflows it ran this week and the agents running on its machines, by id only.
func TestConsoleTeamWorkflowsIsEachBotsRuns(t *testing.T) {
	t.Parallel()
	p := startPlaneWith(t, store.NewMemory(), controllink.NewPrivateKey(), func(c *Config) { c.ProviderHome = t.TempDir() })
	_, admin := p.client("Alex's Mac")
	sam := p.invite(admin, "Sam", wire.UserRoleMember)
	var platform wire.Group
	must(t, json.Unmarshal(call(t, admin, "", "groups/set", wire.GroupSet{Name: "Platform", Members: []string{sam.id}}), &platform))
	var made wire.BotMade
	must(t, json.Unmarshal(call(t, admin, "", "bots/make", wire.BotMake{Name: "review-bot", Group: platform.ID}), &made))
	box := p.botMachine(admin, made.User, "MACHINE-B1")
	host := box.member.Host

	now := time.Now()
	for _, trigger := range []string{"pr.opened 1", "pr.opened 2"} {
		if _, err := p.s.claimRun(context.Background(), host, wire.WorkflowClaim{Workflow: "review", Trigger: trigger}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.s.claimRun(context.Background(), host, wire.WorkflowClaim{Workflow: "deps", Trigger: "schedule"}, now); err != nil {
		t.Fatal(err)
	}
	p.s.copies.take(host, "agents/list", `{"result":[{"id":"A1","state":"running","title":"secret words","startedByWorkflow":"review","createdAt":800000000},{"id":"A2","state":"finished"}]}`)

	var board wire.TeamWorkflows
	raw := call(t, admin, "", "console/teamWorkflows", map[string]any{})
	must(t, json.Unmarshal(raw, &board))
	if len(board.Bots) != 1 {
		t.Fatalf("%s", raw)
	}
	bot := board.Bots[0]
	if bot.Name != "review-bot" || deref(bot.GroupName) != "Platform" || bot.Machines != 1 || bot.Capped {
		t.Fatalf("%+v", bot)
	}
	if len(bot.Workflows) != 2 || bot.Workflows[0].Workflow != "review" || bot.Workflows[0].Runs != 2 || bot.Workflows[1].Runs != 1 {
		t.Fatalf("%+v", bot.Workflows)
	}
	if len(bot.Live) != 1 || bot.Live[0].Agent != "A1" || deref(bot.Live[0].Workflow) != "review" || !strings.HasSuffix(bot.Live[0].HostName, "MACHINE-B1") {
		t.Fatalf("%+v", bot.Live)
	}
	if strings.Contains(string(raw), "secret words") {
		t.Fatal("a title left the machine's list")
	}
}

// Console ▸ Live Now (#2019, M9): every agent running now on every machine, a person's and a
// bot's, with its owner, runtime, state and cost so far, the most costly first; no title.
// console/stopAgent reaches any machine for an admin, and no member.
func TestConsoleLiveIsEveryRunningAgent(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	owner := p.host("OWNERS-MAC")
	_, admin := p.client("Owner's window")
	bea := p.invite(admin, "Bea", wire.UserRoleMember)
	beas := p.machine(bea, "BEAS-MAC")
	p.s.copies.take(owner.member.Host, "agents/list", `{"result":[{"id":"A1","runtimeID":"claude","state":"running","title":"secret words","costToDate":{"USD":0.5},"createdAt":800000000},{"id":"A2","state":"finished","costToDate":{"USD":99}}]}`)
	p.s.copies.take(beas.member.Host, "agents/list", `{"result":[{"id":"B1","runtimeID":"codex","state":"waitingOnUser","costToDate":{"USD":7}},{"id":"B2","runtimeID":"codex","state":"running","eventWait":{"events":["pr.merged"]},"startedByWorkflow":"review"}]}`)

	var board wire.LiveBoard
	raw := call(t, admin, "", "console/live", map[string]any{})
	must(t, json.Unmarshal(raw, &board))
	ids := []string{}
	for _, a := range board.Agents {
		ids = append(ids, string(a.Agent))
	}
	if strings.Join(ids, " ") != "B1 A1 B2" {
		t.Fatalf("%s", raw)
	}
	b1, a1, b2 := board.Agents[0], board.Agents[1], board.Agents[2]
	if b1.OwnerName != "Bea" || b1.State != "waitingOnUser" || b1.Runtime != "codex" || b1.HeardAt == nil || b1.Bot {
		t.Fatalf("%+v", b1)
	}
	if a1.OwnerName != "Local owner" || a1.CostToDate["USD"] != 0.5 || a1.Since == nil {
		t.Fatalf("%+v", a1)
	}
	if !deref(b2.WaitsOnOthers) || deref(b2.Workflow) != "review" || deref(a1.WaitsOnOthers) {
		t.Fatalf("%+v %+v", b2, a1)
	}
	if strings.Contains(string(raw), "secret words") {
		t.Fatal("a title left the machine's list")
	}

	stop := wire.ConsoleStopAgent{Host: wire.MachineRef(beas.member.Host), Agent: "B1"}
	if e := callError(t, bea.window, "", "console/stopAgent", stop); !strings.Contains(e, "Only an admin") {
		t.Fatal(e)
	}
	// The fake machine does not know agents/stop: its refusal is what came back, so it got there.
	if e := callError(t, admin, "", "console/stopAgent", stop); !strings.Contains(e, "agents/stop") {
		t.Fatal(e)
	}
	if e := callError(t, admin, "", "console/stopAgent", wire.ConsoleStopAgent{Host: "NOWHERE", Agent: "B1"}); !strings.Contains(e, "No machine") {
		t.Fatal(e)
	}
}
