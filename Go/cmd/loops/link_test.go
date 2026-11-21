package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// relinkFake is print mode's fake host with a link that drops: drop says which transcript
// reads fail with a lost link, and dials < 0 says no dial comes back.
type relinkFake struct {
	*fake
	mu      sync.Mutex
	drop    map[int]bool // the agents/transcript reads, from 1, that fail with a lost link
	n       int
	dials   int
	relinks int
	ch      chan controllink.Note
}

func (r *relinkFake) Call(ctx context.Context, host, method string, params any) (json.RawMessage, error) {
	r.mu.Lock()
	if method == "agents/transcript" {
		r.n++
	}
	drop := method == "agents/transcript" && r.drop[r.n]
	r.mu.Unlock()
	if drop {
		return nil, controllink.ErrLinkLost
	}
	return r.fake.Call(ctx, host, method, params)
}

func (r *relinkFake) relink(_ context.Context, _ time.Duration, say func()) error {
	say()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.relinks++
	if r.dials < 0 {
		return errors.New("connection refused")
	}
	r.ch = make(chan controllink.Note)
	return nil
}

func (r *relinkFake) notes() <-chan controllink.Note {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ch
}

func fastRecheck(t *testing.T) {
	was := recheck
	recheck = time.Millisecond
	t.Cleanup(func() { recheck = was })
}

// A link that drops in the middle of a turn: print mode dials again, reads on from the
// last index it read, and prints the whole reply once, with exit 0 (#6478).
func TestPrintModeDialsAgainWhenTheLinkDropsAndLosesNoEntry(t *testing.T) {
	fastRecheck(t)
	f, _ := hostFake(t, turnSteps)
	// The 2nd read drops, and the 3rd, the first after the relink.
	r := &relinkFake{fake: f, drop: map[int]bool{2: true, 3: true}, ch: make(chan controllink.Note)}
	var b bytes.Buffer
	err := startAndFollow(context.Background(), &b, r, r.notes(), here, options{print: true, json: true}, "go")
	if err != nil {
		t.Fatal(err)
	}
	if r.relinks != 2 {
		t.Fatalf("two drops, two relinks: %d", r.relinks)
	}
	out := b.String()
	for _, want := range []string{`"text":"Reading the CI runs since 2026-11-19…"`, `"text":"gh run list"`, `"text":"3 failures, all TestStopWaits under load (#6385)."`, `{"exit":0,"state":"done","type":"end"}`} {
		if strings.Count(out, want) != 1 {
			t.Fatalf("%q once in\n%s", want, out)
		}
	}
	reports := 0
	for _, c := range f.calls {
		if c == "h1 presence/report" {
			reports++
		}
	}
	if reports != 3 {
		t.Fatalf("the session is shown again after each relink, so its entries come: %v", f.calls)
	}
}

// The control plane back before the host has joined again says "That machine is
// offline": print mode waits for the host, as for the link, and goes on (#6478).
func TestPrintModeWaitsForAHostThatIsOfflineForAWhile(t *testing.T) {
	fastRecheck(t)
	fastBackoff(t)
	f, _ := hostFake(t, turnSteps)
	offline := errors.New(`{"code":-32090,"message":"That machine is offline."}`)
	reads, reports := 0, 0
	read := f.answer["agents/transcript"]
	f.answer["agents/transcript"] = func(h string, p any) (any, error) {
		if reads++; reads == 2 {
			return nil, offline
		}
		return read(h, p)
	}
	f.answer["presence/report"] = func(string, any) (any, error) {
		if reports++; reports == 2 || reports == 3 {
			return nil, offline
		}
		return wire.Empty{}, nil
	}
	var b bytes.Buffer
	err := startAndFollow(context.Background(), &b, f, make(chan controllink.Note), here, options{print: true}, "go")
	if err != nil || b.String() != "3 failures, all TestStopWaits under load (#6385).\n" || reports != 4 {
		t.Fatalf("got %q %v, %d reports", b.String(), err, reports)
	}
	// A host that stays offline for the wait is exit 6.
	was := relinkFor
	relinkFor = 30 * time.Millisecond
	t.Cleanup(func() { relinkFor = was })
	f, _ = hostFake(t, turnSteps)
	f.answer["agents/transcript"] = func(string, any) (any, error) { return nil, offline }
	f.answer["presence/report"] = func(string, any) (any, error) {
		if reports++; reports > 5 {
			return nil, offline
		}
		return wire.Empty{}, nil
	}
	err = startAndFollow(context.Background(), &b, f, make(chan controllink.Note), here, options{print: true}, "go")
	if codeOf(err) != exitLinkLost || !strings.Contains(err.Error(), "That machine is offline") {
		t.Fatalf("got %d %v", codeOf(err), err)
	}
}

func fastBackoff(t *testing.T) {
	wasFirst, wasMost := relinkFirst, relinkMost
	relinkFirst, relinkMost = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { relinkFirst, relinkMost = wasFirst, wasMost })
}

// A link that stays lost is exit 6, not 1, so a script tells it from a failed turn.
func TestALinkLostForGoodIsExitSix(t *testing.T) {
	fastRecheck(t)
	f, _ := hostFake(t, turnSteps)
	r := &relinkFake{fake: f, drop: map[int]bool{2: true}, dials: -1}
	var b bytes.Buffer
	err := startAndFollow(context.Background(), &b, r, make(chan controllink.Note), here, options{print: true, json: true}, "go")
	if codeOf(err) != exitLinkLost || !strings.Contains(err.Error(), "loops -r ab12cd") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("got %d %v", codeOf(err), err)
	}
	if !strings.HasSuffix(b.String(), `{"exit":6,"state":"lost","type":"end"}`+"\n") {
		t.Fatalf("the end line says it:\n%s", b.String())
	}
	// With no way to dial again, the notes that end are a lost link at once.
	f, _ = hostFake(t, []step{{state: wire.AgentStateRunning}})
	notes := make(chan controllink.Note)
	close(notes)
	recheck = time.Hour
	err = startAndFollow(context.Background(), &b, f, notes, here, options{print: true}, "go")
	if codeOf(err) != exitLinkLost {
		t.Fatalf("got %d %v", codeOf(err), err)
	}
	// A host that refuses a read is still an error of its own (exit 1).
	f, _ = hostFake(t, turnSteps)
	f.answer["agents/transcript"] = func(string, any) (any, error) { return nil, errors.New(`{"code":-32001,"message":"host offline"}`) }
	recheck = time.Millisecond
	if err = startAndFollow(context.Background(), &b, f, make(chan controllink.Note), here, options{print: true}, "go"); codeOf(err) != exitError {
		t.Fatalf("got %d %v", codeOf(err), err)
	}
}

// link dials again with backoff until one comes back, or until the wait ends.
func TestTheLinkDialsAgainWithBackoffForTheWait(t *testing.T) {
	fastBackoff(t)
	tries, refuse := 0, false
	l := &link{redial: func(context.Context, *options) (*controllink.Session, error) {
		tries++
		if tries < 4 || refuse {
			return nil, errors.New("connection refused")
		}
		return nil, nil
	}}
	said := 0
	if err := l.relink(context.Background(), time.Second, func() { said++ }); err != nil || tries != 4 || said != 1 {
		t.Fatalf("back on the 4th dial, said once: %v %d %d", err, tries, said)
	}
	refuse = true
	start := time.Now()
	err := l.relink(context.Background(), 30*time.Millisecond, func() {})
	if err == nil || err.Error() != "connection refused" || time.Since(start) > time.Second {
		t.Fatalf("gives up after the wait with the last error: %v %s", err, time.Since(start))
	}
}

// chatRelink is the chat's fake with a link that drops and comes back.
type chatRelink struct {
	*chatFake
	relinks int
	back    chan controllink.Note
}

func (c *chatRelink) relink(_ context.Context, _ time.Duration, say func()) error {
	say()
	c.mu.Lock()
	c.relinks++
	c.ch = c.back
	c.mu.Unlock()
	return nil
}

func (c *chatRelink) notes() <-chan controllink.Note {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ch
}

// The chat says "reconnecting…", dials again, reads what came while it was away from the
// last index it read, each entry once, and ends with the turn that ended meanwhile.
func TestTheChatReconnectsAndShowsWhatCameWhileAway(t *testing.T) {
	cf := newChatFake()
	c := &chatRelink{chatFake: cf, back: make(chan controllink.Note, 64)}
	id := wire.UUID("C40D1100-0000-0000-0000-000000000003")
	finished := false
	cf.answer["agents/list"] = func(any) any {
		c.mu.Lock()
		defer c.mu.Unlock()
		state := wire.AgentStateRunning
		if finished {
			state = wire.AgentStateFinished
		}
		return []wire.Agent{{ID: id, Title: ptr("Fix the stop test"), RuntimeID: "claude", State: state}}
	}
	entry := func(eid, text string) wire.TranscriptEntry {
		return wire.TranscriptEntry{ID: wire.UUID(eid), Kind: wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: text}}}
	}
	all := []wire.TranscriptEntry{entry("1", "first"), entry("2", "second"), entry("3", "while away")}
	cf.answer["agents/transcript"] = func(p any) any {
		req := p.(wire.TranscriptRequest)
		if req.From == nil { // attach: the last entries, then one
			return daemonPage(all[:1], req)
		}
		return daemonPage(all, req)
	}
	cf.answer["permissions/pending"] = func(any) any { return []wire.PermissionRequest{} }
	cf.answer["elicitations/pending"] = func(any) any { return []wire.ElicitationRequest{} }
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), c, chatHere, options{}, chatArgs{cont: true}, terminal{out: out, st: plain})
	}()
	waitFor(t, "the attach", func() bool { return strings.Contains(out.String(), "first") })
	cf.note("agent/entry", wire.EntryNotification{AgentID: id, Entry: all[1], Index: ptr(int64(1))})
	waitFor(t, "the live entry", func() bool { return strings.Contains(out.String(), "second") })
	c.mu.Lock()
	finished = true
	c.mu.Unlock()
	close(cf.ch)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the chat did not end with the turn that ended while away:\n%s", out.String())
	}
	got := out.String()
	for _, want := range []string{"reconnecting…", "✓ reconnected", "while away", "● done"} {
		if strings.Count(got, want) != 1 {
			t.Fatalf("%q once in\n%s", want, got)
		}
	}
	if strings.Count(got, "first") != 1 || strings.Count(got, "second") != 1 {
		t.Fatalf("no entry twice:\n%s", got)
	}
	reads := cf.called("agents/transcript")
	if from := reads[len(reads)-1].(wire.TranscriptRequest).From; from == nil || *from != 2 {
		t.Fatalf("it reads from the index after the last entry: %v", from)
	}
}

// A chat whose link does not come back ends with exit 6.
func TestAChatWithNoRelinkEndsWithExitSix(t *testing.T) {
	cf := newChatFake()
	cf.answer["agents/start"] = func(any) any { return "B21E9000-0000-0000-0000-000000000000" }
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runChat(context.Background(), cf, chatHere, options{}, chatArgs{text: "go"}, terminal{out: out, st: plain})
	}()
	waitFor(t, "the start", func() bool { return len(cf.called("presence/report")) == 1 })
	close(cf.ch)
	select {
	case err := <-done:
		if codeOf(err) != exitLinkLost || !strings.Contains(err.Error(), "loops -r b21e90") {
			t.Fatalf("got %d %v", codeOf(err), err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the chat did not end")
	}
}
