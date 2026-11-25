package agentsd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// partsLink is the control plane between two machines, on a link with delay (#7120): each
// machines/sendFolder goes to there's own methods.
type partsLink struct {
	there *Daemon
	// atOnce is what it says; 0 is an older control plane, or an older machine there.
	atOnce int64
	// before is how long the part at an offset takes on its way, or why it is lost.
	before func(ctx context.Context, offset int64) error

	mu           sync.Mutex
	flying, most int
	ended        []int64
	alone        map[int64]bool // whether the part at an offset had the link to itself
}

func (l *partsLink) ask(ctx context.Context, _ string, params any) (json.RawMessage, error) {
	call, ok := params.(wire.MachineWorkspaceCall)
	if !ok || call.Method != sendFolderMethod {
		return json.RawMessage(`{}`), nil
	}
	var step wire.MachineFolderSend
	if err := json.Unmarshal(call.Params, &step); err != nil {
		return nil, err
	}
	var offset int64
	if step.Upload != nil {
		offset = step.Upload.Offset
	} else {
		offset = step.Read.Offset
	}
	l.mu.Lock()
	l.flying++
	l.most = max(l.most, l.flying)
	if l.alone == nil {
		l.alone = map[int64]bool{}
	}
	l.alone[offset] = l.flying == 1
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.flying--
		l.ended = append(l.ended, offset)
	}()
	if l.before != nil {
		if err := l.before(ctx, offset); err != nil {
			return nil, err
		}
	}
	var out wire.MachineFolderSent
	if l.atOnce > 0 {
		out.PartsAtOnce = &l.atOnce
	}
	if step.Upload != nil {
		if err := l.there.receiveUpload(*step.Upload); err != nil {
			return nil, err
		}
	} else {
		r, err := l.there.sendRead(*step.Read)
		if err != nil {
			return nil, err
		}
		out.Read = &r
	}
	return json.Marshal(out)
}

// wait is a part's time on the link, cut by ctx as the uplink's Ask is.
func wait(ctx context.Context, t time.Duration) error {
	timer := time.NewTimer(t)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// overtaken has the third part of a file wait until the fourth, sent after it, is there, and
// the fourth until the third is on its way.
func (l *partsLink) overtaken(ctx context.Context, offset int64) error {
	for l.atOnce > 0 && (offset == 2*sendThereParts || offset == 3*sendThereParts) {
		l.mu.Lock()
		passed := l.flying > 1
		if offset == 2*sendThereParts {
			passed = slices.Contains(l.ended, 3*sendThereParts)
		}
		l.mu.Unlock()
		if passed {
			return nil
		}
		if err := wait(ctx, time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func partsMachines(t *testing.T) (here, there *Daemon) {
	t.Helper()
	here, _ = start(t, t.TempDir())
	t.Cleanup(here.Shutdown)
	there, _ = start(t, t.TempDir())
	t.Cleanup(there.Shutdown)
	here.partsQuick = time.Hour // a busy machine is not a full link
	return here, there
}

// outgoing puts data in d's Outgoing.
func outgoing(t *testing.T, d *Daemon, data []byte) wire.SendFile {
	t.Helper()
	id := store.NewUUID()
	file, err := d.outgoingFile(id)
	must(t, err)
	must(t, os.MkdirAll(d.outgoingFolder(), 0o700))
	must(t, os.WriteFile(file, data, 0o600))
	return wire.SendFile{ID: id, Size: int64(len(data))}
}

// partsData is eight parts of sendThereParts, and one that is not whole.
func partsData() []byte {
	data := make([]byte, 8*sendThereParts+12345)
	_, _ = rand.Read(data)
	return data
}

func uploaded(t *testing.T, d *Daemon, id wire.UUID) []byte {
	t.Helper()
	file, err := d.incomingFile(id)
	must(t, err)
	must(t, d.wholeUploads(file))
	b, err := os.ReadFile(file)
	must(t, err)
	return b
}

func TestAnUploadHasSeveralPartsInFlight(t *testing.T) {
	t.Parallel()
	here, there := partsMachines(t)
	data := partsData()
	link := &partsLink{there: there, atOnce: sendPartsAtOnce}
	link.before = link.overtaken
	here.SetControlAsk(link.ask)
	var sizes []int64
	id, err := here.uploadThere(personSending, "devbox", outgoing(t, here, data).ID, func(n int64) { sizes = append(sizes, n) })
	must(t, err)
	if !bytes.Equal(uploaded(t, there, id), data) {
		t.Fatal("what came is not what went")
	}
	if link.most < 2 || link.most > sendPartsAtOnce {
		t.Fatalf("%d parts in flight", link.most)
	}
	if slices.IsSorted(link.ended) {
		t.Fatalf("the parts came in order: %v", link.ended)
	}
	if link.ended[0] != 0 {
		t.Fatalf("a part went before the first ended: %v", link.ended)
	}
	var total int64
	for _, n := range sizes {
		total += n
	}
	if len(sizes) != 9 || total != int64(len(data)) {
		t.Fatalf("heard %v", sizes)
	}
}

func TestAReadHasSeveralPartsInFlight(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{"a last part": partsData(), "whole parts": partsData()[:8*sendPartSize],
		"two": partsData()[:2*sendPartSize], "one": partsData()[:sendPartSize], "less": partsData()[:9], "none": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			here, there := partsMachines(t)
			link := &partsLink{there: there, atOnce: sendPartsAtOnce}
			if len(data) > 4*sendPartSize {
				link.before = link.overtaken
			}
			here.SetControlAsk(link.ask)
			file, id := outgoing(t, there, data), store.NewUUID()
			var total int64
			must(t, here.readThere(personSending, "devbox", file, id, func(n int64) { total += n }))
			if !bytes.Equal(uploaded(t, here, id), data) || total != file.Size {
				t.Fatalf("what came is not what went: %d of %d", total, file.Size)
			}
			if left, _ := os.ReadDir(there.outgoingFolder()); len(left) != 0 {
				t.Fatalf("left there: %v", left)
			}
			if link.most > sendPartsAtOnce || len(data) > 4*sendPartSize && link.most < 2 {
				t.Fatalf("%d parts in flight", link.most)
			}
			// The file goes with its last part, so no other part may be in flight then.
			if last := link.ended[len(link.ended)-1]; !link.alone[last] || last+sendPartSize < file.Size {
				t.Fatalf("the last part was not alone: %v", link.ended)
			}
		})
	}
}

// An older machine, or an older control plane, gets one part at a time, in order.
func TestAnOlderMachineGetsOnePartAtATime(t *testing.T) {
	t.Parallel()
	here, there := partsMachines(t)
	data := partsData()
	link := &partsLink{there: there}
	here.SetControlAsk(link.ask)
	id, err := here.uploadThere(personSending, "devbox", outgoing(t, here, data).ID, nil)
	must(t, err)
	read := store.NewUUID()
	must(t, here.readThere(personSending, "devbox", outgoing(t, there, data), read, nil))
	if !bytes.Equal(uploaded(t, there, id), data) || !bytes.Equal(uploaded(t, here, read), data) {
		t.Fatal("what came is not what went")
	}
	if link.most != 1 || !slices.IsSorted(link.ended[:9]) || !slices.IsSorted(link.ended[9:]) {
		t.Fatalf("%d in flight, %v", link.most, link.ended)
	}
}

// A lost part ends the send: no more parts go, and what came is refused when it is used.
func TestALostPartEndsTheSend(t *testing.T) {
	t.Parallel()
	here, there := partsMachines(t)
	data := partsData()
	lose := func(ctx context.Context, offset int64) error {
		if offset == 3*sendThereParts {
			return errors.New("lost")
		}
		return wait(ctx, 30*time.Millisecond)
	}
	link := &partsLink{there: there, atOnce: sendPartsAtOnce, before: lose}
	here.SetControlAsk(link.ask)
	if _, err := here.uploadThere(personSending, "devbox", outgoing(t, here, data).ID, nil); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Fatal(err)
	}
	if link.flying != 0 || slices.Contains(link.ended, 8*sendThereParts) {
		t.Fatalf("%d in flight, after %v", link.flying, link.ended)
	}
	id := store.NewUUID()
	if err := here.readThere(personSending, "devbox", outgoing(t, there, data), id, nil); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Fatal(err)
	}
	here.dropUpload(id)
	if left, _ := os.ReadDir(here.incomingFolder()); len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
}

func TestAHostThatStopsWaitsForNoPart(t *testing.T) {
	t.Parallel()
	here, there := partsMachines(t)
	held := make(chan struct{}, sendPartsAtOnce)
	link := &partsLink{there: there, atOnce: sendPartsAtOnce, before: func(ctx context.Context, offset int64) error {
		if offset < 4*sendThereParts {
			return nil
		}
		held <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}
	here.SetControlAsk(link.ask)
	file := outgoing(t, here, partsData())
	ended := make(chan error, 1)
	here.goJob("a send", func() {
		_, err := here.uploadThere(personSending, "devbox", file.ID, nil)
		ended <- err
	})
	<-held
	<-held
	began := time.Now()
	here.Shutdown()
	if err := <-ended; err == nil || !strings.Contains(err.Error(), "stopped before the send was done") {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 5*time.Second || link.flying != 0 {
		t.Fatalf("it took %s, and %d parts are in flight", took, link.flying)
	}
}

func TestAnUploadsPartsComeInAnyOrder(t *testing.T) {
	t.Parallel()
	_, d := partsMachines(t)
	id := store.NewUUID()
	file, err := d.incomingFile(id)
	must(t, err)
	part := func(offset int64, data string) error {
		return d.receiveUpload(wire.ReceiveUploadRequest{Upload: id, Offset: offset, Data: []byte(data)})
	}
	missing := func(err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatal(err)
		}
	}
	missing(part(3, "def"))
	must(t, part(0, "abc"))
	must(t, part(9, "jkl"))
	missing(d.wholeUploads(file))
	_, err = d.receive(wire.ReceiveRequest{Folder: t.TempDir(), Bundle: &id, Checkout: "main",
		Branches: []wire.SentBranch{{Name: "main", Commit: strings.Repeat("a", 40)}}})
	missing(err)
	missing(part(3, "def")) // a receive takes its uploads, whatever it says

	must(t, part(0, "abc"))
	must(t, part(6, "ghi"))
	missing(part(6, "ghi"))
	missing(part(5, "fgh"))
	missing(part(2, "cde"))
	missing(d.wholeUploads(file))
	must(t, part(3, "def"))
	must(t, d.wholeUploads(file))
	if got := string(uploaded(t, d, id)); got != "abcdefghi" {
		t.Fatal(got)
	}
	must(t, part(0, "new")) // the part at 0 starts it again
	if got := string(uploaded(t, d, id)); got != "new" {
		t.Fatal(got)
	}
	if err := part(receiveLimit-1, "xy"); err == nil || !strings.Contains(err.Error(), "8 GB") {
		t.Fatal(err)
	}
	if err := part(-1, "x"); err == nil {
		t.Fatal("a part before the file")
	}
}

// A part that takes long means the link is full: more in flight would only wait in line.
func TestASlowLinkGetsFewerParts(t *testing.T) {
	t.Parallel()
	w := sendWindow{now: 1, most: 4, quick: quickPart}
	for _, want := range []int{2, 3, 4, 4} {
		if w.took(quickPart); w.now != want {
			t.Fatal(w.now, want)
		}
	}
	for _, want := range []int{2, 1, 1} {
		if w.took(quickPart + 1); w.now != want {
			t.Fatal(w.now, want)
		}
	}
}
