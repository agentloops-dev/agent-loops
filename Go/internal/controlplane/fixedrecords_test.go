package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7251: an ask to join at its largest is taken and can be approved; one over it is refused,
// and the oldest ended one goes to keep the count.
func TestAJoinRequestIsWithinItsLargest(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	ctx := context.Background()
	public, _ := controllink.PublicKey(controllink.NewPrivateKey())
	ask := func(machine, from string) (*wire.MachineJoinTicket, error) {
		return p.s.joins.ask(ctx, wire.MachineJoinAsk{Name: "Box", Platform: "linux", PublicKey: public, MachineID: &machine}, from)
	}
	most := joinRecordMost - joinDecisionRoom
	first, err := ask("", "198.51.100.1")
	must(t, err)
	object, err := p.s.cfg.Store.Get(ctx, joinKey(first.Request))
	must(t, err)
	// "machineID" is left out when it is empty, so its name counts too. From is the same length.
	room := most - len(object.Data)
	var taken *wire.MachineJoinTicket
	n := room
	for ; taken == nil && n > 0; n-- {
		if ticket, err := ask(strings.Repeat("m", n), "198.51.100.2"); err == nil {
			taken = ticket
		}
	}
	if taken == nil || room-n > 64 {
		t.Fatalf("no ask near %d bytes was taken: %d", most, n)
	}
	if _, err := ask(strings.Repeat("m", n+2), "198.51.100.3"); err == nil {
		t.Fatal("an ask over its largest was taken")
	}
	if _, err := p.s.joins.decide(ctx, taken.Request, p.s.localOwner(), true); err != nil {
		t.Fatalf("an ask at its largest could not be approved: %v", err)
	}
	object, err = p.s.cfg.Store.Get(ctx, joinKey(taken.Request))
	must(t, err)
	if len(object.Data) > joinRecordMost {
		t.Fatalf("a decided one is %d bytes", len(object.Data))
	}

	for i := range joinsMost {
		r := joinRecord{ID: fmt.Sprintf("%032x", i+1), Result: "denied", Created: records.At(time.Now().Add(-time.Minute)), Expires: records.At(time.Now())}
		data, err := records.Encode(r)
		must(t, err)
		_, err = p.s.cfg.Store.Put(ctx, joinKey(r.ID), data, store.Always)
		must(t, err)
	}
	_, err = p.s.cfg.Store.Put(ctx, joinKey(strings.Repeat("f", 32)), []byte("{"), store.Always)
	must(t, err)
	if _, err := ask("", "198.51.100.4"); err != nil {
		t.Fatal(err)
	}
	keys, err := p.s.cfg.Store.List(ctx, joinsPrefix)
	must(t, err)
	if len(keys) != joinsMost {
		t.Fatalf("%d kept", len(keys))
	}
	if r, _, err := p.s.joins.read(ctx, first.Request); err != nil || r == nil {
		t.Fatalf("one that waits went: %v", err)
	}
}

// #7251: a backup's entry is cut or refused at its largest, and a backup 90 days old goes,
// but not the newest of a host that is here.
func TestBackupsHaveALargestEntryAndAnAge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, st, owner := backupPlane(t)
	b.now = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }
	commit := func(name, machine, root string) error {
		if _, err := b.said("backups/upload", "a", raw(t, wire.BackupStorePart{Name: name, Data: []byte("x")})); err != nil {
			return err
		}
		_, err := b.said("backups/commit", "a", raw(t, wire.BackupStoreCommit{Name: name, Parts: 1, Machine: machine, RootID: root, Keep: 10}))
		return err
	}
	must(t, commit("20261019T010000Z", strings.Repeat("é", 4000), "root"))
	must(t, commit("20261019T020000Z", "a", strings.Repeat("r", 3<<10)))
	if err := commit("20261019T030000Z", "a", strings.Repeat("r", backupRecord)); err == nil {
		t.Fatal("an entry over its largest was taken")
	}
	list := storedList(t, b, "a")
	if len(list) != 2 || len(list[1].Machine) > backupMachine || !strings.HasSuffix(list[1].Machine, "…") {
		t.Fatalf("%+v", list)
	}
	if err := b.upload(ctx, owner, "a", wire.BackupStorePart{Name: "20261019T040000Z", Data: make([]byte, backupPartMost+1)}); err == nil {
		t.Fatal("a part over its largest was taken")
	}

	b.now = func() time.Time { return time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC) }
	must(t, b.pruneOrphans(ctx))
	if list := storedList(t, b, "a"); len(list) != 1 || list[0].ID != "a/20261019T020000Z" {
		t.Fatalf("kept %+v", list)
	}
	if o, _ := st.Get(ctx, partKey(b.hostFolder(owner, "a"), "20261019T020000Z", 0)); o == nil {
		t.Fatal("the newest lost its part")
	}
}

// #7251: the backups' key is one small record. One that came larger is opened and made small,
// never refused: it is the only copy.
func TestABackupsKeyOverItsLargestIsMadeSmall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, st, _ := backupPlane(t)
	if _, err := putBackup(t, b, "a", "20261020T120000Z", [][]byte{[]byte("mine")}, 3); err != nil {
		t.Fatal(err)
	}
	o, err := st.Get(ctx, backupDataKey)
	must(t, err)
	var held storedDataKey
	must(t, json.Unmarshal(o.Data, &held))
	for range 100 {
		held.Sealed = append(held.Sealed, held.Sealed[0])
	}
	large := raw(t, held)
	if len(large) <= backupDataMost {
		t.Fatal("not large")
	}
	_, err = st.Put(ctx, backupDataKey, large, store.Always)
	must(t, err)
	must(t, b.keepDataKey(ctx))
	if o, err = st.Get(ctx, backupDataKey); err != nil || len(o.Data) > backupDataMost {
		t.Fatalf("%d bytes: %v", len(o.Data), err)
	}
	must(t, b.keepDataKey(ctx))
	if _, err := b.download(ctx, storedOwner(t, b), wire.BackupStorePartRequest{ID: "a/20261020T120000Z"}); err != nil {
		t.Fatalf("a part no longer opens: %v", err)
	}
}

func storedOwner(t *testing.T, b *backupStore) string {
	t.Helper()
	owner, err := b.owner("a")
	must(t, err)
	return owner
}

// #7251: a crashed copy's capacity sample goes after a day, and the oldest over the count.
func TestCapacitySamplesAreSwept(t *testing.T) {
	t.Parallel()
	ctx, st, now := context.Background(), store.NewMemory(), time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	put := func(id string, at time.Time) {
		_, err := st.Put(ctx, controlCapacityKey(id), raw(t, wire.ControlCapacityCopy{Copy: id, At: wire.DateOf(at)}), store.Always)
		must(t, err)
	}
	put("old", now.Add(-controlCapacityKept-time.Second))
	put("edge", now.Add(-controlCapacityKept))
	for i := range controlCapacityMost - 1 {
		put(fmt.Sprint(i), now)
	}
	_, err := st.Put(ctx, controlCapacityKey("bad"), []byte("{"), store.Always)
	must(t, err)
	count := func() int {
		keys, err := st.List(ctx, controlCapacityPrefix)
		must(t, err)
		return len(keys)
	}
	must(t, sweepControlCapacity(ctx, st, now))
	if o, _ := st.Get(ctx, controlCapacityKey("edge")); o == nil || count() != controlCapacityMost {
		t.Fatalf("%d left at the limit", count())
	}
	put("one-more", now)
	must(t, sweepControlCapacity(ctx, st, now))
	if o, _ := st.Get(ctx, controlCapacityKey("edge")); o != nil || count() != controlCapacityMost {
		t.Fatalf("%d left above it", count())
	}
}

// A sample with each field at its longest is within its largest record.
func TestACapacitySampleIsWithinItsLargest(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	const big = 1.7976931348623157e+308
	p.s.keepControlCapacity(controlCapacity{at: time.Now(), level: wire.CapacityLevelFull, reason: "pressure_memory",
		load1: big, load5: big, pressureMemory: big, pressureIO: big, memoryAvailable: big, swapUsed: big, heapInuse: big, goroutines: 1 << 62, openFiles: 1 << 62})
	o, err := p.s.cfg.Store.Get(context.Background(), controlCapacityKey(p.s.copyID))
	if err != nil || o == nil || len(o.Data) > controlCapacityRecord || !bytes.Contains(o.Data, []byte("e+308")) {
		t.Fatalf("%v %v", o, err)
	}
}

// #7251: a copy, a backup and a restore write each record as it is, at any size: it can be
// the only copy.
func TestACopyTakesARecordOverItsLimit(t *testing.T) {
	t.Parallel()
	ctx, from := context.Background(), store.NewMemory()
	large := bytes.Repeat([]byte("x"), joinRecordMost+1)
	for _, key := range []string{joinsPrefix + "large.json", backupDataKey} {
		_, err := from.Put(ctx, key, large, store.Always)
		must(t, err)
	}
	for name, copyAll := range map[string]func(context.Context, store.Store, store.Store) (int, int, error){"copy": CopyStore, "backup": BackupStore} {
		to := store.NewMemory()
		if n, _, err := copyAll(ctx, from, to); err != nil || n != 2 {
			t.Fatalf("%s: %d %v", name, n, err)
		}
		if o, _ := to.Get(ctx, backupDataKey); o == nil || !bytes.Equal(o.Data, large) {
			t.Fatalf("%s changed a record", name)
		}
	}
	// A receiving copy's store/put takes one too.
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	p.s.phase.set(phaseReceiving)
	if _, err := p.s.handoverAnswer("store/put", raw(t, map[string]any{"key": backupDataKey, "data": large})); err != nil {
		t.Fatal(err)
	}
	// And a backup of a home and its restore.
	dir, home := t.TempDir()+"/backup", t.TempDir()
	if _, err := Backup(ctx, from, HomeParts{}, dir); err != nil {
		t.Fatal(err)
	}
	to := store.NewFolder(home)
	if _, err := Restore(ctx, dir, HomeParts{}, to, false); err != nil {
		t.Fatal(err)
	}
	if o, _ := to.Get(ctx, backupDataKey); o == nil || !bytes.Equal(o.Data, large) {
		t.Fatal("a restore changed a record")
	}
}
