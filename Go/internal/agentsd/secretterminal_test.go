package agentsd

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #4179: agents-secret NAME with no session's grant (a terminal) asks a person over
// daemon.sock, which a stranger may call. The request has no session, names the command that
// ran it, and takes once, hour or deny; hour is kept in memory, so a call in the hour does not
// ask. A flagged secret and a name that is not set get words and no request.
func TestATerminalsAgentsSecretAsksAPerson(t *testing.T) {
	answered, left := formHolds(t)
	wasHour, wasParent := terminalHour, parentCommand
	parentCommand = func() string { return "/bin/zsh" }
	defer func() { terminalHour, parentCommand = wasHour, wasParent }()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, sock := serveWith(t, fixedPolicy{role: RoleStranger, ok: true})
	const plain, flagged = "plain-4179-not-real", "flagged-4179-not-real"
	if err := d.saveSecret("PLAIN_KEY", plain, false); err != nil {
		t.Fatal(err)
	}
	if err := d.saveSecret("FLAGGED_KEY", flagged, true); err != nil {
		t.Fatal(err)
	}
	noGrant := func(string) string { return "" }
	type result struct {
		code     int
		out, err string
	}
	// noticed is closed when a CLI's stderr says it waits for a person: its first call's hold
	// ran out (#5938).
	var noticed chan struct{}
	run := func(name string) chan result {
		done := make(chan result, 1)
		noticed = make(chan struct{})
		errOut := &noticeWriter{notice: "Needs you", seen: noticed}
		go func() {
			var out bytes.Buffer
			code := SecretCommand([]string{name}, noGrant, sock, &out, errOut)
			done <- result{code, out.String(), errOut.String()}
		}()
		return done
	}
	got := func(done chan result) result {
		t.Helper()
		select {
		case r := <-done:
			return r
		case <-time.After(10 * time.Second):
			t.Fatal("agents-secret never ended")
		}
		return result{}
	}

	// No allow yet: a request with no session, held past the hold, then answered once.
	left()
	done := run("PLAIN_KEY")
	r := openSecretUse(t, d)
	if u := r.Detail.SecretUse; r.AskedBy != nil || u.Use != wire.SecretUseTerminal || u.Name != "PLAIN_KEY" || u.Place != "zsh" {
		t.Fatalf("the request: %+v %+v", r, u)
	}
	// Wait until the CLI's first call has gone past its hold, not for a fixed time: under load
	// the call may not get to its timer in twice the hold (#5938). The CLI then calls again, on
	// the same request.
	select {
	case <-noticed:
	case <-time.After(10 * time.Second):
		t.Fatal("the CLI never said it waits for a person")
	}
	if _, err := d.answerSecretUse(r, &wire.RequestSecretUseAnswer{Action: secretUseSession}, nil); err == nil {
		t.Fatal("a terminal's request took Approve for This Session")
	}
	answerUse(t, d, r, secretUseOnce)
	if res := got(done); res.code != 0 || res.out != plain || !strings.Contains(res.err, "Needs you") {
		t.Fatalf("allowed once: %+v", res)
	}

	// Once was one call: the next asks again, and Approve for 1 Hour lets the rest through. The
	// calls from here on are answered while they are held.
	answered()
	done = run("PLAIN_KEY")
	answerUse(t, d, openSecretUse(t, d), secretUseHour)
	if res := got(done); res.code != 0 || res.out != plain {
		t.Fatalf("allowed for the hour: %+v", res)
	}
	if res := got(run("PLAIN_KEY")); res.code != 0 || res.out != plain {
		t.Fatalf("in the hour: %+v", res)
	}
	for _, q := range d.listRequests(wire.RequestsListStateOpen, "") {
		t.Fatalf("a call in the hour asked: %+v", q)
	}

	// After the hour it asks again, and Deny refuses.
	d.secretUseMu.Lock()
	d.terminalUntil["PLAIN_KEY"] = time.Now().Add(-time.Second)
	d.secretUseMu.Unlock()
	done = run("PLAIN_KEY")
	answerUse(t, d, openSecretUse(t, d), secretUseDeny)
	if res := got(done); res.code != 1 || res.out != "" || !strings.Contains(res.err, "denied") {
		t.Fatalf("denied: %+v", res)
	}

	if res := got(run("FLAGGED_KEY")); res.code != 1 || res.out != "" || strings.Contains(res.err, flagged) || !strings.Contains(res.err, "Ask before use") {
		t.Fatalf("FLAGGED_KEY: %+v", res)
	}
	if res := got(run("MISSING_KEY")); res.code != 1 || !strings.Contains(res.err, "not set") {
		t.Fatalf("MISSING_KEY: %+v", res)
	}
	for _, q := range d.listRequests(wire.RequestsListStateOpen, "") {
		t.Fatalf("a refused name asked: %+v", q)
	}

	var reads []string
	for _, e := range secretEvents(d, "secret.read") {
		reads = append(reads, e.Details["name"]+" "+e.Details["place"]+" "+e.Details["agent"])
	}
	// Each read is recorded with no session and the place terminal (the event log folds the same read raised again at once).
	if len(reads) == 0 {
		t.Fatal("no read was recorded")
	}
	for _, r := range reads {
		if r != "PLAIN_KEY "+terminalPlace+" " {
			t.Fatalf("the reads: %q", reads)
		}
	}
	if denied := secretEvents(d, "secret.denied"); len(denied) != 1 || denied[0].Details["place"] != terminalPlace {
		t.Fatalf("the denial: %+v", denied)
	}
}

// noticeWriter is a CLI's stderr that the test reads while the CLI writes: it closes seen
// once what it has holds notice.
type noticeWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	notice string
	seen   chan struct{}
	closed bool
}

func (w *noticeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if !w.closed && strings.Contains(w.buf.String(), w.notice) {
		w.closed = true
		close(w.seen)
	}
	return n, err
}

func (w *noticeWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// The banner of a terminal's request names the terminal and the known gap, with its three
// answers.
func TestATerminalsRequestHasItsBanner(t *testing.T) {
	t.Parallel()

	u := wire.RequestSecretUse{Name: "PLAIN_KEY", Use: wire.SecretUseTerminal, Place: "zsh"}
	var ids []string
	for _, a := range secretUseAnswers(u) {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, " ") != "once hour deny" {
		t.Fatalf("the answers: %v", ids)
	}
	if w := secretUseWanted(u); !strings.Contains(w, "PLAIN_KEY") || !strings.Contains(w, terminalCaveat) {
		t.Fatalf("the banner: %q", w)
	}
	if terminalCommandName("") != "a terminal" || terminalCommandName("/usr/bin/python3\n") != "python3" {
		t.Fatal("the command's name")
	}
}
