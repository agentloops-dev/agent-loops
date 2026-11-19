package acp

// The conformance suite for the driver (#6186, epic #6183): a scripted runtime made to
// do each way an adapter fails, and what the driver must do for each. The same checks run
// against each installed adapter with `-tags live` (conformance_live_test.go).
//
// The faults, by name:
//   crash      the runtime exits during a turn
//   hang       the runtime never answers a turn
//   malformed  a line that is not JSON, a method or an update that the driver does not know
//   drift      an update changes what the runtime offers (no resume, no load)
//   resume     session/resume or session/load fails, or answers with another session
//   late card  a permission request comes after session/cancel
//   big        a very large message, a very large stderr
//
// For each, the driver ends the turn with an error that says what happened (never a hang),
// hands on what the runtime said before it, and leaves another session of its own alone.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

// line is one JSON-RPC message from the driver, as the scripted runtime reads it.
type line struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// script is the runtime: it reads each line the driver writes and says what it does.
type script struct {
	// caps is agentCapabilities in the initialize answer.
	caps string
	// sessionID is what session/new answers.
	sessionID string
	// on handles a request or notification beyond the lifecycle ones, and says whether it did.
	on func(r *run, l line) bool
	// resume, when set, answers session/resume and session/load in place of an empty result.
	resume func(r *run, l line)
}

// run is one scripted runtime in the test's process, over two pipes.
type run struct {
	t      *testing.T
	s      script
	mu     sync.Mutex
	out    io.WriteCloser
	in     io.Reader
	closed chan struct{}
	// answers is each answer the driver gave to a request of the runtime's.
	answers chan string
	// ended closes the pipes: the runtime's exit.
	end func()
}

func (r *run) send(v any) {
	b, _ := json.Marshal(v)
	r.raw(string(b))
}

func (r *run) raw(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = io.WriteString(r.out, s+"\n")
}

func (r *run) reply(l line, result any) {
	r.send(map[string]any{"jsonrpc": "2.0", "id": l.ID, "result": result})
}

func (r *run) fail(l line, code int, message string) {
	r.send(map[string]any{"jsonrpc": "2.0", "id": l.ID, "error": map[string]any{"code": code, "message": message}})
}

func (r *run) update(session string, u map[string]any) {
	r.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": u}})
}

func (r *run) say(session, text string) {
	r.update(session, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
}

func (r *run) serve() {
	sc := bufio.NewReaderSize(r.in, 1<<20)
	for {
		b, err := sc.ReadBytes('\n')
		if len(b) == 0 && err != nil {
			return
		}
		var l line
		if json.Unmarshal(b, &l) != nil || l.Method == "" {
			// An answer to the runtime's own request.
			select {
			case r.answers <- string(b):
			default:
			}
			continue
		}
		switch l.Method {
		case "initialize":
			caps := r.s.caps
			if caps == "" {
				caps = `{}`
			}
			r.raw(`{"jsonrpc":"2.0","id":` + string(l.ID) + `,"result":{"protocolVersion":1,"agentCapabilities":` + caps + `}}`)
		case "session/new":
			id := r.s.sessionID
			if id == "" {
				id = "s1"
			}
			r.reply(l, map[string]any{"sessionId": id})
		case "session/resume", "session/load":
			if r.s.resume != nil {
				r.s.resume(r, l)
			} else {
				r.reply(l, map[string]any{})
			}
		default:
			if r.s.on != nil {
				r.s.on(r, l)
			}
		}
	}
}

// attached starts the script and is the Launch that reaches it.
func attached(t *testing.T, s script) Launch {
	t.Helper()
	return Launch{Command: "scripted", Name: "Scripted", Dir: t.TempDir(), Attach: func(Launch) (Attached, error) {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		r := &run{t: t, s: s, out: outW, in: inR, closed: make(chan struct{}), answers: make(chan string, 16)}
		var once sync.Once
		r.end = func() { once.Do(func() { _ = inW.Close(); _ = outW.Close(); close(r.closed) }) }
		go r.serve()
		current.Store(r)
		return Attached{Stdin: inW, Stdout: outR, End: r.end, Done: r.closed}, nil
	}}
}

// current is the last runtime started, for a test that has to end it from outside.
var current runHolder

type runHolder struct {
	mu sync.Mutex
	r  *run
}

func (h *runHolder) Store(r *run) { h.mu.Lock(); h.r = r; h.mu.Unlock() }

// recorder is a Sink that keeps what the driver hands on.
type recorder struct {
	mu       sync.Mutex
	said     []string
	asked    int
	answer   string
	asking   chan struct{}
	released chan struct{}
}

func (r *recorder) Update(n sdk.SessionNotification) {
	if m := n.Update.AgentMessageChunk; m != nil && m.Content.Text != nil {
		r.mu.Lock()
		r.said = append(r.said, m.Content.Text.Text)
		r.mu.Unlock()
	}
}

func (r *recorder) Permission(ctx context.Context, _ sdk.RequestPermissionRequest) string {
	r.mu.Lock()
	r.asked++
	r.mu.Unlock()
	if r.asking != nil {
		close(r.asking)
	}
	if r.released != nil {
		select {
		case <-r.released:
		case <-ctx.Done():
		}
	}
	return r.answer
}
func (r *recorder) Served(string, string) {}
func (r *recorder) Elicitation(context.Context, json.RawMessage) (string, map[string]any) {
	return "cancel", nil
}
func (r *recorder) ElicitationComplete(string) {}

func (r *recorder) heard() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.said, "")
}

func start(t *testing.T, s script, sink Sink) *Session {
	t.Helper()
	sess, err := Start(context.Background(), attached(t, s), sink, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Stop)
	return sess
}

func text(s string) []sdk.ContentBlock { return []sdk.ContentBlock{sdk.TextBlock(s)} }

// finishesIn fails the test when f does not return in d: a hang is the fault, not a slow test.
func finishesIn(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return in %v", what, d)
	}
}

// answersTurn is a prompt handler that says "hello" and ends the turn.
func answersTurn(r *run, l line) bool {
	if l.Method != "session/prompt" {
		return false
	}
	r.say("s1", "hello")
	r.reply(l, map[string]any{"stopReason": "end_turn"})
	return true
}

// A runtime that answers is the base the faults are measured from.
func TestConformanceBaseline(t *testing.T) {
	sink := &recorder{}
	s := start(t, script{on: answersTurn}, sink)
	r, err := s.Prompt(context.Background(), text("hi"))
	if err != nil || r.StopReason != sdk.StopReasonEndTurn {
		t.Fatalf("%v %v", r, err)
	}
	if sink.heard() != "hello" {
		t.Fatalf("heard %q", sink.heard())
	}
}

// crash: the runtime exits during a turn, after it spoke. The turn ends with an error that
// says the runtime exited; what it said before is kept; another session is not touched.
func TestConformanceCrashDuringATurn(t *testing.T) {
	other := &recorder{}
	otherSession := start(t, script{on: answersTurn}, other)

	sink := &recorder{}
	s := start(t, script{on: func(r *run, l line) bool {
		if l.Method != "session/prompt" {
			return false
		}
		r.say("s1", "partial ")
		time.Sleep(50 * time.Millisecond)
		r.end() // exit with the turn open
		return true
	}}, sink)
	finishesIn(t, 10*time.Second, "a turn of a runtime that exited", func() {
		_, err := s.Prompt(context.Background(), text("go"))
		if err == nil || !strings.Contains(err.Error(), "the runtime exited") {
			t.Errorf("error %v", err)
		}
	})
	if got := sink.heard(); got != "partial " {
		t.Fatalf("heard %q before the crash", got)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done is not closed after the runtime exited")
	}
	finishesIn(t, 10*time.Second, "Stop after a crash", s.Stop)
	if _, err := otherSession.Prompt(context.Background(), text("hi")); err != nil || other.heard() != "hello" {
		t.Fatalf("the other session: %v %q", err, other.heard())
	}
}

// crash: a runtime that exits before it answers initialize is an error that names the
// runtime, not a hang.
func TestConformanceCrashBeforeInitialize(t *testing.T) {
	l := Launch{Command: "scripted", Name: "Scripted", Attach: func(Launch) (Attached, error) {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, inR); _ = outW.Close(); close(done) }()
		_ = outW.Close()
		return Attached{Stdin: inW, Stdout: outR, End: func() { _ = inW.Close() }, Done: done}, nil
	}}
	finishesIn(t, 10*time.Second, "a start of a runtime that exited", func() {
		if _, err := Start(context.Background(), l, &recorder{}, nil, nil, nil, "", ""); err == nil {
			t.Error("no error")
		}
	})
}

// crash: a process that is killed mid-turn: the same, with a real process and its stderr in
// the error.
func TestConformanceProcessKilledDuringATurn(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
while read line; do
  id=$(printf '%s' "$line" | sed 's/.*"id":\([0-9]*\).*/\1/')
  case "$line" in
  *'"initialize"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id";;
  *'"session/new"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"p1"}}\n' "$id";;
  *'"session/prompt"'*) echo "adapter: out of memory" >&2; kill -9 $$;;
  esac
done
`
	exe := filepath.Join(dir, "runtime")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Start(context.Background(), Launch{Command: exe, Dir: dir, Name: "Scripted"}, &recorder{}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	finishesIn(t, 10*time.Second, "a turn of a killed runtime", func() {
		_, err := s.Prompt(context.Background(), text("go"))
		if err == nil || !strings.Contains(err.Error(), "the runtime exited") || !strings.Contains(err.Error(), "out of memory") {
			t.Errorf("error %v", err)
		}
	})
}

// hang: the runtime never answers the turn. The caller's own limit ends the turn with the
// context's error; Cancel does not block; Stop ends the runtime.
func TestConformanceHangingTurn(t *testing.T) {
	sink := &recorder{}
	s := start(t, script{on: func(r *run, l line) bool { return l.Method == "session/prompt" }}, sink)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	finishesIn(t, 10*time.Second, "a hanging turn with a limit", func() {
		_, err := s.Prompt(ctx, text("go"))
		if err == nil {
			t.Error("no error")
		}
	})
	finishesIn(t, 5*time.Second, "Cancel", func() { _ = s.Cancel(context.Background()) })
	finishesIn(t, 10*time.Second, "Stop of a hung runtime", s.Stop)
	select {
	case <-s.Done():
	default:
		t.Fatal("not Done after Stop")
	}
}

// hang: a runtime that does not answer initialize ends the start with ErrNoAnswer (the
// handshake tests cover the retry; here, that nothing is left running).
func TestConformanceHangingInitialize(t *testing.T) {
	old := handshakeWait
	handshakeWait = 300 * time.Millisecond
	defer func() { handshakeWait = old }()
	var launched, stopped atomic.Int32
	l := Launch{Command: "scripted", Name: "Scripted", Attach: func(Launch) (Attached, error) {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan struct{})
		var once sync.Once
		launched.Add(1)
		end := func() { once.Do(func() { _ = inW.Close(); _ = outW.Close(); close(done); stopped.Add(1) }) }
		go func() { _, _ = io.Copy(io.Discard, inR) }()
		return Attached{Stdin: inW, Stdout: outR, End: end, Done: done}, nil
	}}
	finishesIn(t, 10*time.Second, "a start of a silent runtime", func() {
		_, err := Handshake(context.Background(), l, &recorder{})
		if !errors.Is(err, ErrNoAnswer) {
			t.Errorf("error %v", err)
		}
	})
	if launched.Load() != stopped.Load() {
		t.Fatalf("%d silent runtimes started, %d stopped", launched.Load(), stopped.Load())
	}
}

// malformed: a line that is not JSON, a notification of a method the driver does not know,
// an update of a type it does not know, and a request of an unknown method (which must be
// answered, or the runtime waits for ever). The turn still ends well.
func TestConformanceMalformedMessages(t *testing.T) {
	sink := &recorder{}
	s := start(t, script{on: func(r *run, l line) bool {
		if l.Method != "session/prompt" {
			return false
		}
		r.raw(`this is not json`)
		r.raw(`{"jsonrpc":"2.0","method":"vendor/unknown","params":{"a":1}}`)
		r.raw(`{"jsonrpc":"2.0","id":900,"method":"vendor/ask","params":{}}`)
		r.update("s1", map[string]any{"sessionUpdate": "a_kind_from_next_year", "x": 1})
		r.raw(`{"jsonrpc":"2.0","method":"session/update","params":"nope"}`)
		r.say("s1", "still here")
		r.reply(l, map[string]any{"stopReason": "end_turn"})
		return true
	}}, sink)
	finishesIn(t, 10*time.Second, "a turn with malformed messages", func() {
		r, err := s.Prompt(context.Background(), text("go"))
		if err != nil || r.StopReason != sdk.StopReasonEndTurn {
			t.Errorf("%v %v", r, err)
		}
	})
	if got := sink.heard(); got != "still here" {
		t.Fatalf("heard %q", got)
	}
}

// malformed: an answer to the turn that is not a response the driver can read ends the turn
// with an error, not a hang and not an empty success.
func TestConformanceUnreadableTurnAnswer(t *testing.T) {
	s := start(t, script{on: func(r *run, l line) bool {
		if l.Method != "session/prompt" {
			return false
		}
		r.raw(`{"jsonrpc":"2.0","id":` + string(l.ID) + `,"result":{"stopReason":42}}`)
		return true
	}}, &recorder{})
	finishesIn(t, 10*time.Second, "an unreadable answer", func() {
		_, err := s.Prompt(context.Background(), text("go"))
		if err == nil {
			t.Error("no error")
		}
	})
}

// A request the runtime made that the driver cannot serve gets an error answer: the runtime
// is told, and does not wait.
func TestConformanceUnknownRequestIsAnswered(t *testing.T) {
	s := start(t, script{on: func(r *run, l line) bool {
		if l.Method != "session/prompt" {
			return false
		}
		r.raw(`{"jsonrpc":"2.0","id":"q1","method":"vendor/ask","params":{}}`)
		time.Sleep(200 * time.Millisecond)
		r.reply(l, map[string]any{"stopReason": "end_turn"})
		return true
	}}, &recorder{})
	current.mu.Lock()
	r := current.r
	current.mu.Unlock()
	finishesIn(t, 10*time.Second, "a turn", func() { _, _ = s.Prompt(context.Background(), text("go")) })
	select {
	case a := <-r.answers:
		if !strings.Contains(a, `"q1"`) || !strings.Contains(a, `"error"`) {
			t.Fatalf("answer %s", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the request of an unknown method was never answered")
	}
}

// drift: an adapter update that drops resume and load. A conversation that cannot be
// continued begins again, and the session says so (Lost).
func TestConformanceAdapterWithoutResumeBeginsAgain(t *testing.T) {
	sink := &recorder{}
	sess, err := Start(context.Background(), attached(t, script{caps: `{}`, sessionID: "fresh"}), sink, nil, nil, nil, "old-session", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()
	if sess.Continued || !sess.Lost || sess.ID != "fresh" {
		t.Fatalf("continued %v lost %v id %q", sess.Continued, sess.Lost, sess.ID)
	}
}

// drift: the capabilities the adapter gives are what the session reports, as read from
// initialize, and no model, mode or option is assumed.
func TestConformanceCapabilitiesAreWhatInitializeSays(t *testing.T) {
	s := start(t, script{caps: `{"loadSession":true,"sessionCapabilities":{"list":{}},"mcpCapabilities":{"http":true}}`}, &recorder{})
	if !s.CanLoad() || !s.CanList() || s.CanDelete() || s.SupportsSteering() || s.SupportsSystemPrompt() {
		t.Fatalf("load %v list %v delete %v steering %v prompt %v", s.CanLoad(), s.CanList(), s.CanDelete(), s.SupportsSteering(), s.SupportsSystemPrompt())
	}
	if len(s.NewResult.ConfigOptions) != 0 {
		t.Fatalf("options %v", s.NewResult.ConfigOptions)
	}
}

// resume: session/resume answers with an error, as when the adapter lost the conversation:
// the session begins again and is Lost.
func TestConformanceFailedResumeBeginsAgain(t *testing.T) {
	for _, caps := range []string{`{"sessionCapabilities":{"resume":{}}}`, `{"loadSession":true}`} {
		s := script{caps: caps, sessionID: "fresh", resume: func(r *run, l line) { r.fail(l, -32002, "Resource not found") }}
		sess, err := Start(context.Background(), attached(t, s), &recorder{}, nil, nil, nil, "old-session", "")
		if err != nil {
			t.Fatalf("%s: %v", caps, err)
		}
		if sess.Continued || !sess.Lost || sess.ID != "fresh" {
			t.Errorf("%s: continued %v lost %v id %q", caps, sess.Continued, sess.Lost, sess.ID)
		}
		sess.Stop()
	}
}

// resume: a resume that works keeps the conversation's own id, and the conversation the
// runtime replays on load is not handed on as new words.
func TestConformanceResumeKeepsTheConversation(t *testing.T) {
	sink := &recorder{}
	s := script{caps: `{"loadSession":true}`, resume: func(r *run, l line) {
		r.say("old-session", "an old answer")
		r.reply(l, map[string]any{})
	}}
	sess, err := Start(context.Background(), attached(t, s), sink, nil, nil, nil, "old-session", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()
	if !sess.Continued || sess.Lost || sess.ID != "old-session" {
		t.Fatalf("continued %v lost %v id %q", sess.Continued, sess.Lost, sess.ID)
	}
}

// resume: a runtime that does not answer the resume in time is an error that says so, and
// the process is stopped: it is not taken for a lost conversation, which would begin a
// second one beside the first.
func TestConformanceResumeThatHangsIsNotALostConversation(t *testing.T) {
	s := script{caps: `{"sessionCapabilities":{"resume":{}}}`, resume: func(r *run, l line) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	finishesIn(t, 10*time.Second, "a hanging resume", func() {
		sess, err := Start(ctx, attached(t, s), &recorder{}, nil, nil, nil, "old-session", "")
		if err == nil {
			sess.Stop()
			t.Error("a hanging resume gave a session")
		}
	})
}

// late card: the runtime asks permission after session/cancel. The sink is asked (the
// driver does not drop a request the runtime waits on) and the answer, cancelled, goes back;
// the turn ends cancelled.
func TestConformanceCardAfterCancel(t *testing.T) {
	sink := &recorder{}
	cancelled := make(chan struct{})
	s := start(t, script{on: func(r *run, l line) bool {
		switch l.Method {
		case "session/prompt":
			go func() {
				<-cancelled
				r.send(map[string]any{"jsonrpc": "2.0", "id": "perm1", "method": "session/request_permission", "params": map[string]any{
					"sessionId": "s1", "toolCall": map[string]any{"toolCallId": "t1", "title": "Run it"},
					"options": []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}}}})
				time.Sleep(200 * time.Millisecond)
				r.reply(l, map[string]any{"stopReason": "cancelled"})
			}()
			return true
		case "session/cancel":
			close(cancelled)
			return true
		}
		return false
	}}, sink)
	var stop sdk.StopReason
	finishesIn(t, 10*time.Second, "a turn cancelled with a card after", func() {
		go func() { time.Sleep(100 * time.Millisecond); _ = s.Cancel(context.Background()) }()
		r, err := s.Prompt(context.Background(), text("go"))
		if err != nil {
			t.Errorf("%v", err)
		}
		stop = r.StopReason
	})
	if stop != sdk.StopReasonCancelled {
		t.Fatalf("stop reason %q", stop)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.asked != 1 {
		t.Fatalf("the card was asked %d times", sink.asked)
	}
}

// late card: a card that waits for the person when the runtime is stopped does not keep
// Stop waiting.
func TestConformanceStopWithACardOpen(t *testing.T) {
	sink := &recorder{asking: make(chan struct{}), released: make(chan struct{})}
	s := start(t, script{on: func(r *run, l line) bool {
		if l.Method != "session/prompt" {
			return false
		}
		r.send(map[string]any{"jsonrpc": "2.0", "id": "perm1", "method": "session/request_permission", "params": map[string]any{
			"sessionId": "s1", "toolCall": map[string]any{"toolCallId": "t1", "title": "Run it"},
			"options": []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}}}})
		return true
	}}, sink)
	go func() { _, _ = s.Prompt(context.Background(), text("go")) }()
	select {
	case <-sink.asking:
	case <-time.After(5 * time.Second):
		t.Fatal("the card never came")
	}
	finishesIn(t, 15*time.Second, "Stop with a card open", s.Stop)
	close(sink.released)
}

// big: a message of 8 MB (a tool result) is read whole. One of 20 MB, over what the SDK reads,
// is dropped, not the session: the words after it arrive and the turn ends well.
func TestConformanceVeryLargeMessage(t *testing.T) {
	for _, c := range []struct {
		size int
		want string
	}{{8 << 20, strings.Repeat("x", 8<<20) + "after"}, {20 << 20, "after"}} {
		sink := &recorder{}
		big := strings.Repeat("x", c.size)
		s := start(t, script{on: func(r *run, l line) bool {
			if l.Method != "session/prompt" {
				return false
			}
			r.say("s1", big)
			r.say("s1", "after")
			r.reply(l, map[string]any{"stopReason": "end_turn"})
			return true
		}}, sink)
		finishesIn(t, 30*time.Second, "a turn with a large message", func() {
			if _, err := s.Prompt(context.Background(), text("go")); err != nil {
				t.Errorf("%d bytes: %v", c.size, err)
			}
		})
		if got := sink.heard(); got != c.want {
			t.Errorf("%d bytes: heard %d bytes, want %d", c.size, len(got), len(c.want))
		}
	}
}

// big: a runtime that writes 64 MB to stderr does not fill memory: the driver keeps the
// last 8 KiB, and the error of a later crash has the end of it.
func TestConformanceVeryLargeStderr(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
while read line; do
  id=$(printf '%s' "$line" | sed 's/.*"id":\([0-9]*\).*/\1/')
  case "$line" in
  *'"initialize"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id";;
  *'"session/new"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"p1"}}\n' "$id";;
  *'"session/prompt"'*)
    i=0; while [ $i -lt 64 ]; do head -c 1048576 /dev/zero | tr '\0' 'e' >&2; echo >&2; i=$((i+1)); done
    echo "the end of stderr" >&2; kill -9 $$;;
  esac
done
`
	exe := filepath.Join(dir, "runtime")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Start(context.Background(), Launch{Command: exe, Dir: dir, Name: "Scripted"}, &recorder{}, nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	finishesIn(t, 60*time.Second, "a turn of a runtime with a large stderr", func() {
		_, err := s.Prompt(context.Background(), text("go"))
		if err == nil || !strings.Contains(err.Error(), "the end of stderr") {
			t.Errorf("error %v", err)
		}
		if err != nil && len(err.Error()) > 2000 {
			t.Errorf("the error is %d bytes", len(err.Error()))
		}
	})
}
