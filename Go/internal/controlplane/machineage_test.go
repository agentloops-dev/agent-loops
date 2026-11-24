package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6968: a set-up goes 90 days after it ended, or when its machine is gone. One that runs
// stays, also before its machine has joined, and one that waits for its claim.
func TestMachineSetupsAgeOut(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	now := time.Now()
	// A machine's id has capitals, its set-up's key has none. "Elsewhere" joined at another
	// copy: these records have not read it.
	for _, id := range []string{"Ready", "Recent", "Failed", "Waiting"} {
		must(t, p.s.records.AddHost(t.Context(), records.MachineRecord{ID: id, Name: id}))
	}
	keepJSON(t, st, records.HostKey("Elsewhere"), records.MachineRecord{ID: "Elsewhere"})
	put := func(id string, age time.Duration, change func(*storedSetup, *time.Time)) {
		at := now.Add(-age)
		setup := newStoredSetup(at)
		change(setup, &at)
		keepJSON(t, st, machineSetupKey(id), setup)
	}
	running := func(s *storedSetup, at *time.Time) {
		s.Steps[0].State, s.Steps[0].Started = wire.MachineSetupStateRunning, at
	}
	ready := func(s *storedSetup, at *time.Time) { s.Ready = at }
	put("Ready", 91*ageDay, ready)
	put("Recent", 89*ageDay, ready)
	put("Failed", 91*ageDay, func(s *storedSetup, at *time.Time) {
		s.Steps[0].State, s.Steps[0].Ended = wire.MachineSetupStateFailed, at
	})
	put("Waiting", 200*ageDay, running)
	put("Elsewhere", 200*ageDay, running)
	put("making", time.Hour, running)
	put("gone", 8*ageDay, running)
	// It began long ago, and a step of it ran now.
	put("slow", 8*ageDay, func(s *storedSetup, _ *time.Time) { running(s, &now) })
	_, err := st.Put(t.Context(), machineSetupKey("broken"), []byte("{"), store.Always)
	must(t, err)

	_, err = sweep(t.Context(), p.s.records, now, mostPerSweep, nil)
	must(t, err)
	want := map[string]bool{}
	for id, kept := range map[string]bool{"Ready": false, "Recent": true, "Failed": false, "Waiting": true,
		"Elsewhere": true, "making": true, "gone": false, "slow": true, "broken": false} {
		want[machineSetupKey(id)] = kept
	}
	keptKeys(t, st, want)

	// A step's words are cut, so the record stays in its size.
	long := strings.Repeat("é", setupWordsMost)
	must(t, p.s.setupStep(t.Context(), "Waiting", wire.MachineSetupReport{Step: wire.MachineSetupStepIDCreate, State: wire.MachineSetupStateRunning, Detail: &long}))
	stored, _, err := p.s.readMachineSetup(t.Context(), "Waiting")
	if err != nil || len(stored.Steps[0].Detail) > setupWordsMost || !strings.HasSuffix(stored.Steps[0].Detail, "é…") {
		t.Fatalf("%v: %d bytes", err, len(stored.Steps[0].Detail))
	}
	_, err = p.s.changeMachineSetup(t.Context(), "Waiting", false, func(s *storedSetup) error {
		s.Projects = []string{strings.Repeat("x", machineSetupMost)}
		return nil
	})
	if err == nil {
		t.Fatal("a set-up over its size was kept")
	}
}

// #6969: a deploy goes after 90 days; the version now stays, and the object goes with its
// machine.
func TestMachineVersionsAgeOut(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	ctx, now := t.Context(), time.Now()
	must(t, p.s.records.AddHost(ctx, records.MachineRecord{ID: "BOX", Name: "BOX"}))
	old := func(age time.Duration) wire.Deploy {
		return observedDeploy("BOX", fmt.Sprintf("0.%d.0", int(age/ageDay)), time.Time{}, now.Add(-age))
	}
	keepJSON(t, st, machineVersionsKey("BOX"), storedMachineVersions{Version: "9.0.0", FirstSeen: now.Add(-89 * ageDay),
		Seen: now, Deploys: []wire.Deploy{old(89 * ageDay), old(91 * ageDay)}})
	keepJSON(t, st, machineVersionsKey("gone"), storedMachineVersions{Version: "1.0.0", Seen: now.Add(-25 * time.Hour)})
	keepJSON(t, st, machineVersionsKey("new"), storedMachineVersions{Version: "1.0.0", Seen: now})
	_, err := sweep(ctx, p.s.records, now, mostPerSweep, nil)
	must(t, err)
	keptKeys(t, st, map[string]bool{machineVersionsKey("BOX"): true, machineVersionsKey("gone"): false, machineVersionsKey("new"): true})
	v, _, err := p.s.readMachineVersions(ctx, "BOX")
	if err != nil || v.Version != "9.0.0" || v.Previous != "0.91.0" || len(v.Deploys) != 1 {
		t.Fatalf("%v %+v", err, v)
	}
	_, err = sweep(ctx, p.s.records, now.Add(2*ageDay), mostPerSweep, nil)
	must(t, err)
	if v, _, err = p.s.readMachineVersions(ctx, "BOX"); err != nil || v.Version != "9.0.0" || len(v.Deploys) != 0 {
		t.Fatalf("%v %+v", err, v)
	}
	if err := p.s.noteVersion(ctx, "BOX", strings.Repeat("1", mostVersionText+1), now); err == nil {
		t.Fatal("a version too long was kept")
	}
	// At the longest versions, the record stays in its size.
	for i := range mostMachineVersions + 1 {
		must(t, p.s.noteVersion(ctx, "BOX", fmt.Sprintf("%0*d", mostVersionText, i), now.Add(time.Duration(i)*time.Second)))
	}
	object, err := st.Get(ctx, machineVersionsKey("BOX"))
	if err != nil || len(object.Data) > machineVersionsMost {
		t.Fatalf("%v: %d bytes", err, len(object.Data))
	}
}

// #6971: a failed make goes after 90 days, one with no time counts from the first sweep, and
// a removed provider's go.
func TestFailedMakesAgeOut(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	ctx, now := t.Context(), time.Now()
	key := func(id string) string { return providers.FailedMakesPrefix + id + ".json" }
	keepJSON(t, st, providers.ProviderKey("do-1"), providers.ProviderRecord{ID: "do-1"})
	keepJSON(t, st, key("do-1"), []map[string]any{{"name": "a", "reason": "r", "at": now.Add(-91 * ageDay)},
		{"name": "b", "reason": "r"}, {"name": "c", "reason": "r", "at": now.Add(-89 * ageDay)}})
	keepJSON(t, st, key("gone"), []map[string]any{{"name": "a", "reason": "r"}})
	left := func(at time.Time) string {
		t.Helper()
		_, err := sweep(ctx, p.s.records, at, mostPerSweep, nil)
		must(t, err)
		object, err := st.Get(ctx, key("do-1"))
		must(t, err)
		if object == nil {
			return ""
		}
		var rows []struct{ Name string }
		must(t, json.Unmarshal(object.Data, &rows))
		return fmt.Sprint(rows)
	}
	for _, step := range []struct {
		days int
		want string
	}{{0, "[{b} {c}]"}, {2, "[{b}]"}, {89, "[{b}]"}, {90, ""}} {
		if got := left(now.Add(time.Duration(step.days) * ageDay)); got != step.want {
			t.Errorf("after %d days: %s, want %s", step.days, got, step.want)
		}
	}
	keptKeys(t, st, map[string]bool{key("gone"): false})
}
