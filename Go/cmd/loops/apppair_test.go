package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// No test asks the real agentsd of the Mac it runs on: that would open a request in a
// person's Needs you (#6875).
func init() { appSocket = func() string { return "" } }

// fakeApp is agentsd's socket, answering clients/pairTerminal with each reply in turn.
type fakeApp struct {
	mu      sync.Mutex
	asked   []wire.TerminalPairingRequest
	replies []string // each a JSON-RPC reply line's result or error part
}

func serveFakeApp(t *testing.T, replies ...string) (*fakeApp, string) {
	t.Helper()
	// A socket's path must be short: /tmp, not the test's own folder.
	dir, err := os.MkdirTemp("/tmp", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	f := &fakeApp{replies: replies}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, err := bufio.NewReader(c).ReadBytes('\n')
			if err != nil {
				_ = c.Close()
				continue
			}
			var call struct {
				Method string                      `json:"method"`
				Params wire.TerminalPairingRequest `json:"params"`
			}
			_ = json.Unmarshal(line, &call)
			f.mu.Lock()
			f.asked = append(f.asked, call.Params)
			reply := `"error":{"code":-32601,"message":"no such method"}`
			if call.Method == wire.CallClientsPairTerminal.Name && len(f.replies) > 0 {
				reply, f.replies = f.replies[0], f.replies[1:]
			}
			f.mu.Unlock()
			// A notice first: the reply is the line with the call's id.
			_, _ = c.Write([]byte(`{"jsonrpc":"2.0","method":"daemon/notice","params":{}}` + "\n"))
			_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,` + reply + "}\n"))
			_ = c.Close()
		}
	}()
	return f, socket
}

func useApp(t *testing.T, socket string) {
	t.Helper()
	was := appSocket
	appSocket = func() string { return socket }
	t.Cleanup(func() { appSocket = was })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LOOPS_KEY_FILES", "1")
}

// #6875: loops asks the app, says once that it waits, and asks again on the same client id
// and key until a person answered.
func TestPairFromAppAsksUntilAPersonAnswers(t *testing.T) {
	f, socket := serveFakeApp(t,
		`"result":{"request":"6875a","words":"Approve it in Needs you."}`,
		`"result":{"request":"6875a","words":"Approve it in Needs you."}`,
		`"result":{"code":"not a code"}`)
	useApp(t, socket)
	was := commandName
	commandName = func() string { return "zsh" }
	defer func() { commandName = was }()
	var say bytes.Buffer
	err := pairFromApp(t.Context(), &say)
	if err == nil || !strings.Contains(err.Error(), "not a pairing code") {
		t.Fatalf("the code it got: %v", err)
	}
	if got := strings.Count(say.String(), "Approve it in Needs you."); got != 1 {
		t.Fatalf("said %d times that it waits: %q", got, say.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.asked) != 3 {
		t.Fatalf("asked %d times", len(f.asked))
	}
	first := f.asked[0]
	if !strings.HasPrefix(first.Name, "loops") || first.Client == "" || len(first.Key) != 65 || deref(first.Command) != "zsh" {
		t.Fatalf("the request: %+v", first)
	}
	for _, a := range f.asked[1:] {
		if a.Client != first.Client || !bytes.Equal(a.Key, first.Key) {
			t.Fatalf("asked again as another terminal: %+v", a)
		}
	}
}

// #6875: with no app, or one from before the method, loops is not paired as before; a person
// who said no gets words and the way to pair by hand.
func TestPairFromAppWithNoAppOrANo(t *testing.T) {
	useApp(t, "")
	if err := pairFromApp(t.Context(), &bytes.Buffer{}); !errors.Is(err, errNoApp) {
		t.Fatalf("no socket: %v", err)
	}
	if _, err := dialWith(t.Context(), &options{}, joinAsk); codeOf(err) != exitNotPair {
		t.Fatalf("no app, no pairing: %v", err)
	}

	_, old := serveFakeApp(t) // every method is not found
	useApp(t, old)
	if err := pairFromApp(t.Context(), &bytes.Buffer{}); !errors.Is(err, errNoApp) {
		t.Fatalf("an app from before #6875: %v", err)
	}

	refused := `"error":{"code":-32602,"message":"A person did not pair this terminal."}`
	_, no := serveFakeApp(t, refused, refused)
	useApp(t, no)
	err := pairFromApp(t.Context(), &bytes.Buffer{})
	if codeOf(err) != exitNotPair || !strings.Contains(err.Error(), "did not pair") || !strings.Contains(err.Error(), "loops auth login '<code>'") {
		t.Fatalf("a no: %v", err)
	}
	if err := authLoginFromApp(t.Context(), &bytes.Buffer{}); codeOf(err) != exitNotPair {
		t.Fatalf("loops auth login with no code: %v", err)
	}
	useApp(t, "")
	if err := authLoginFromApp(t.Context(), &bytes.Buffer{}); codeOf(err) != exitError || !strings.Contains(err.Error(), "not running on this Mac") {
		t.Fatalf("loops auth login with no app: %v", err)
	}
}

// #6875: ^C while it waits ends it.
func TestPairFromAppStopsWithTheContext(t *testing.T) {
	_, socket := serveFakeApp(t, `"result":{"request":"6875a","words":"Waiting."}`)
	useApp(t, socket)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := pairFromApp(ctx, &bytes.Buffer{}); err == nil || errors.Is(err, errNoApp) {
		t.Fatalf("a stopped wait: %v", err)
	}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
