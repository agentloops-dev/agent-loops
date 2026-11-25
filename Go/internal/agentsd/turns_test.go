package agentsd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"go.uber.org/goleak"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/acp"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/browser"
	"github.com/agentloops-dev/agent-loops/Go/internal/flaky"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The test binary is also a fake ACP runtime: with AGENTS_FAKE_ACP=1 it serves one on its
// stdin and stdout. A prompt with "ask" in it asks for permission before it answers; one
// with "wait for me" waits to be cancelled, and one with "keep going" waits for a SIGTERM
// (or, in the test's process, its end: fakeRuntimes).
func TestMain(m *testing.M) {
	if os.Getenv("AGENTS_FAKE_MCP") == "1" {
		fakeMCPServer()
		return
	}
	if os.Getenv("AGENTS_FAKE_ACP") == "1" {
		f := &fakeRuntime{ready: make(chan struct{}), env: os.Environ()}
		f.conn = sdk.NewAgentSideConnection(f, os.Stdout, os.Stdin)
		close(f.ready)
		<-f.conn.Done()
		return
	}
	// go test always gives the binary its -test. flags. With none, a daemon started it as a
	// stand-in without its switch: it fails at once, not runs every test while the daemon
	// waits for an answer (#5988).
	if !slices.ContainsFunc(os.Args[1:], func(a string) bool { return strings.HasPrefix(a, "-test.") }) {
		fmt.Fprintln(os.Stderr, "agentsd.test: started with no -test. flag and no AGENTS_FAKE_MCP or AGENTS_FAKE_ACP switch")
		os.Exit(2)
	}
	// A test's writes are not synced to the disk: on a Mac each sync costs about 4 ms (#4709).
	store.SyncWrites = false
	// The browser and the screen do not ask in a test unless it says so (askDrive, #4264).
	askDriveByDefault = false
	// The sealed-secret tests are the Mac's path on every system; a test of the Linux release
	// layer turns it on for its daemon (releaseOn).
	releaseLayerHere = false
	// The machine's memory pressure does not move a test's warm pool (#4514).
	setPressureNow(func() string { return "" })
	// The browser is Chrome for Testing, which a test fakes: never the machine's own Google
	// Chrome, which GitHub's Linux runners have, with Xvfb (#5227).
	browser.FindChrome = func() string { return "" }
	// Nor does the load of the machine a test runs on raise machine.busy (#4578).
	setCapacitySampler(func(context.Context) capacitySample { return capacitySample{} })
	// No test asks the person's gh about merged pull requests unless it says so.
	findGH = func() string { return "" }
	// Nor asks the forge for an issue's parent (#3582): a test sets issueLookup.
	parentLookup = func(context.Context, string, forgeRepo, int) (issueOnForge, error) {
		return issueOnForge{}, errNoForgeInTests
	}
	// Nor for a merged PR's files (#4552): a test sets prFactsLookup.
	prFactsAsk = func(context.Context, string, forgeRepo, int) (prFacts, error) { return prFacts{}, errNoForgeInTests }
	// Nor erases a deleted agent's runtime copy (#1001): that starts the runtime.
	erasing = false
	// Nor asks a runtime its version (#3674): that starts the runtime too.
	runtimeVersion = func(string) string { return "" }
	// Nor fetches the ready-made workflows from GitHub (#5091): every daemon has the fixture's.
	useReadyMadeFixture()
	// Every daemon of the process has one Mac toolsets folder, not its root's own, so tests that
	// run side by side do not change where the others look (#4733).
	shared, err := os.MkdirTemp("", "agentsd-tools-")
	if err != nil {
		panic(err)
	}
	sharedToolsHome = filepath.Join(shared, "tools")
	serverToolsHome = filepath.Dir(toolsetFolder("claude"))
	resetToolsHomes()
	// Nor starts a runtime installed on the machine (#5974): a CI runner's claude ran a turn
	// for a test that did not call fakeRuntimes. A test sees the runtimes it made, as a Linux
	// runner with none installed sees them, and fakeRuntimes gives it the fake.
	locateFor = locateMadeByATest
	before := goleak.IgnoreCurrent()
	code := m.Run()
	if code == 0 {
		// No goroutine outlives the run (#6130): startWith's cleanup names a daemon's own
		// (d.goJob) by test; this finds any other, by where it was created. A kept-alive
		// connection in an HTTP client's pool is not one: the default client's are let go
		// first, and another client's (a test's own, a library's) waits for its server.
		http.DefaultClient.CloseIdleConnections()
		if err := goleak.Find(before,
			goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
			goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "goroutines still run after every test ended (#6130):\n%v\n", err)
			code = 1
		}
	}
	if err := os.RemoveAll(shared); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "could not remove the shared toolsets folder: %v\n", err)
	}
	removeRunTempDirs()
	os.Exit(code)
}

// runTempDirs are the folders that live for the whole run, not one test (the helpers that
// prepareWatch and prepareDrive build once): TestMain removes them when the run ends (#5726).
var (
	runTempDirsMu sync.Mutex
	runTempDirs   []string
)

// runTempDir makes a folder for the whole run, which TestMain removes after m.Run.
func runTempDir(pattern string) (string, error) {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", err
	}
	runTempDirsMu.Lock()
	runTempDirs = append(runTempDirs, dir)
	runTempDirsMu.Unlock()
	return dir, nil
}

func removeRunTempDirs() {
	runTempDirsMu.Lock()
	defer runTempDirsMu.Unlock()
	for _, dir := range runTempDirs {
		if err := os.RemoveAll(dir); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "could not remove %s: %v\n", dir, err)
		}
	}
	runTempDirs = nil
}

// resetToolsHomes puts the tools folders back as a daemon of this process starts with them:
// after a test whose HOME or folders were its own, so that the tests that run side by side
// next find them as they are and do not each write them (#4733).
func resetToolsHomes() {
	switch goruntime.GOOS {
	case "darwin":
		setToolsHomes(sharedToolsHome, sharedToolsHome, false)
	case "linux":
		setToolsHomes("", serverToolsHome, true)
	}
}

// serverToolsHome is ~/.agents-server/tools of the test process's HOME (resetToolsHomes).
var serverToolsHome string

// locateMadeByATest is locate for a test: a runtime found outside the temporary folder and the
// toolsets folders that the tests share (#4733), where every test makes its own, is installed
// on the machine, so it is not found.
func locateMadeByATest(r *runtimeDef) (string, []string, []string) {
	path, args, looked := r.locate()
	if path == "" || madeByATest(path, os.TempDir(), sharedToolsHome, serverToolsHome) {
		return path, args, looked
	}
	return "", nil, append(append([]string{}, looked...), filepath.Dir(path)+" (installed on this machine, which a test never starts)")
}

// madeByATest is whether path is in one of dirs, as written or with its links followed.
func madeByATest(path string, dirs ...string) bool {
	in := func(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) }
	abs, _ := filepath.Abs(path)
	realPath, _ := filepath.EvalSymlinks(abs)
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		dir = filepath.Clean(dir)
		realDir, _ := filepath.EvalSymlinks(dir)
		for _, d := range []string{dir, realDir} {
			if d != "" && (in(abs, d) || realPath != "" && in(realPath, d)) {
				return true
			}
		}
	}
	return false
}

type fakeRuntime struct {
	sdk.Agent // what it does not answer
	conn      *sdk.AgentSideConnection
	// ready is closed once conn is set: the SDK serves before it returns the connection.
	ready chan struct{}
	// sandboxOff is whether session/new's _meta turned Claude's sandbox off.
	sandboxOff bool
	// rateLimited is whether "rate limit me" was refused once already.
	rateLimited bool
	// systemPrompt is the runtime's system-prompt field from session/new or resume.
	systemPrompt string
	// env is the environment the runtime was launched with: the process's own for a fake
	// that is a process, the launch's for one in the test's process (#6143).
	env []string
}

// getenv is a variable of the fake's environment, "" when it has none.
func (f *fakeRuntime) getenv(key string) string {
	v := ""
	for _, kv := range f.env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// systemPromptOf is the brief a runtime was given in _meta, as Claude or Grok take it.
func systemPromptOf(meta any) string {
	m, _ := meta.(map[string]any)
	if sp, ok := m["systemPrompt"].(map[string]any); ok {
		s, _ := sp["append"].(string)
		return s
	}
	if jetbrains, ok := m["jetbrains"].(map[string]any); ok {
		if air, ok := jetbrains["air"].(map[string]any); ok {
			s, _ := air["customInstructions"].(string)
			if s != "" {
				return s
			}
		}
	}
	s, _ := m["rules"].(string)
	return s
}

// send is a session/update to the daemon. A failed one is said on stderr and the fake
// goes on: the daemon has ended the connection, and the test that reads the transcript
// says what did not come (#6318).
func (f *fakeRuntime) send(ctx context.Context, n sdk.SessionNotification) {
	if err := f.conn.SessionUpdate(ctx, n); err != nil {
		fmt.Fprintf(os.Stderr, "fake runtime: session/update: %v\n", err)
	}
}

// instructionsOf is the files named in OPENCODE_CONFIG_CONTENT's instructions, read as
// OpenCode reads them on each prompt (#2612).
func (f *fakeRuntime) instructionsOf() string {
	var config struct{ Instructions []string }
	if c := f.getenv("OPENCODE_CONFIG_CONTENT"); c != "" {
		if err := json.Unmarshal([]byte(c), &config); err != nil {
			return "OPENCODE_CONFIG_CONTENT: " + err.Error()
		}
	}
	var out []string
	for _, file := range config.Instructions {
		b, _ := os.ReadFile(file)
		out = append(out, strings.TrimSuffix(string(b), "\n"))
	}
	return strings.Join(out, "\n\n")
}

func (f *fakeRuntime) Initialize(context.Context, sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	// AGENTS_FAKE_STARTS names a file the fake adds a line to each time it starts.
	if file := f.getenv("AGENTS_FAKE_STARTS"); file != "" {
		if out, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			_, _ = out.WriteString("started\n")
			out.Close()
		}
	}
	// AGENTS_FAKE_NO_DELETE: an adapter that cannot delete a session.
	var del *sdk.SessionDeleteCapabilities
	if f.getenv("AGENTS_FAKE_NO_DELETE") == "" {
		del = &sdk.SessionDeleteCapabilities{}
	}
	// AGENTS_FAKE_CONTINUE: "load", an adapter that continues a conversation only with
	// session/load; "none", one that can neither load nor resume (#4513).
	load, resume := true, &sdk.SessionResumeCapabilities{}
	switch f.getenv("AGENTS_FAKE_CONTINUE") {
	case "load":
		resume = nil
	case "none":
		load, resume = false, nil
	}
	return sdk.InitializeResponse{ProtocolVersion: sdk.ProtocolVersionNumber, AuthMethods: fakeAuthMethods(),
		AgentCapabilities: sdk.AgentCapabilities{Auth: sdk.AgentAuthCapabilities{Logout: &sdk.LogoutCapabilities{}},
			Providers: &sdk.ProvidersCapabilities{}, LoadSession: load, SessionCapabilities: sdk.SessionCapabilities{Resume: resume,
				Fork: &sdk.SessionForkCapabilities{}, List: &sdk.SessionListCapabilities{}, Delete: del}}}, nil
}

// The fake holds one conversation the app did not start, "theirs", in any folder.
func (f *fakeRuntime) ListSessions(_ context.Context, r sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	title, at := "Their talk", "2026-10-16T09:00:00Z"
	return sdk.ListSessionsResponse{Sessions: []sdk.SessionInfo{{SessionId: "theirs", Cwd: *r.Cwd, Title: &title, UpdatedAt: &at}}}, nil
}

// LoadSession replays "theirs": the person's words in two chunks, then the reply. Its own,
// "fake-session", it loads with no replay.
func (f *fakeRuntime) LoadSession(ctx context.Context, r sdk.LoadSessionRequest) (sdk.LoadSessionResponse, error) {
	if r.SessionId == "fake-session" {
		return sdk.LoadSessionResponse{}, nil
	}
	if r.SessionId != "theirs" {
		return sdk.LoadSessionResponse{}, fmt.Errorf("no session %s", r.SessionId)
	}
	<-f.ready
	for _, u := range []sdk.SessionUpdate{sdk.UpdateUserMessageText("What is "), sdk.UpdateUserMessageText("two and two?"), sdk.UpdateAgentMessageText("Four.")} {
		f.send(ctx, sdk.SessionNotification{SessionId: r.SessionId, Update: u})
	}
	return sdk.LoadSessionResponse{}, nil
}

func (f *fakeRuntime) UnstableDeleteSession(_ context.Context, r sdk.UnstableDeleteSessionRequest) (sdk.UnstableDeleteSessionResponse, error) {
	// AGENTS_FAKE_DELETED names a file the fake adds each deleted session's ID to, a line each.
	if file := f.getenv("AGENTS_FAKE_DELETED"); file != "" {
		if out, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			_, _ = out.WriteString(string(r.SessionId) + "\n")
			out.Close()
		}
	}
	return sdk.UnstableDeleteSessionResponse{}, nil
}

func (f *fakeRuntime) UnstableForkSession(_ context.Context, r sdk.UnstableForkSessionRequest) (sdk.UnstableForkSessionResponse, error) {
	return sdk.UnstableForkSessionResponse{SessionId: "forked-" + r.SessionId}, nil
}

// ResumeSession has its own conversation, and no other. In a folder called reconnecting
// it hands back a mode of its own ("default"), as a runtime's fresh process does before
// the daemon re-pushes the agent's saved mode (#3082).
func (f *fakeRuntime) ResumeSession(_ context.Context, r sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	if r.SessionId != "fake-session" {
		return sdk.ResumeSessionResponse{}, fmt.Errorf("no session %s", r.SessionId)
	}
	b, _ := json.Marshal(r.Meta)
	f.sandboxOff = strings.Contains(string(b), `"sandbox":{"enabled":false}`)
	f.systemPrompt = systemPromptOf(r.Meta) + f.instructionsOf()
	var out sdk.ResumeSessionResponse
	if filepath.Base(r.Cwd) == "reconnecting" {
		if err := json.Unmarshal([]byte(`{"configOptions":[{"type":"select","id":"mode","name":"Mode","currentValue":"default",
			"options":[{"value":"default","name":"Default"},{"value":"plan","name":"Plan"}]}]}`), &out); err != nil {
			return out, err
		}
	}
	return out, nil
}

// NewSession in a folder called offers offers a model, and a command a moment after; in
// one called signedout it wants a sign-in, as Cursor and Antigravity do (#1430).
func (f *fakeRuntime) NewSession(_ context.Context, r sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	var out sdk.NewSessionResponse
	if filepath.Base(r.Cwd) == "signedout" {
		return out, &sdk.RequestError{Code: authRequired, Message: "Authentication required"}
	}
	b, _ := json.Marshal(r.Meta)
	f.sandboxOff = strings.Contains(string(b), `"sandbox":{"enabled":false}`)
	f.systemPrompt = systemPromptOf(r.Meta) + f.instructionsOf()
	if filepath.Base(r.Cwd) != "offers" {
		return sdk.NewSessionResponse{SessionId: "fake-session"}, nil
	}
	if err := json.Unmarshal([]byte(`{"sessionId":"fake-session","configOptions":[{"type":"select","id":"model","name":"Model",
		"currentValue":"opus","options":[{"value":"opus","name":"Opus"},{"value":"sonnet","name":"Sonnet"}]}]}`), &out); err != nil {
		return out, err
	}
	go func() {
		<-f.ready
		time.Sleep(50 * time.Millisecond)
		var u sdk.SessionUpdate
		if err := json.Unmarshal([]byte(`{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"review","description":"Review the change"}]}`), &u); err != nil {
			fmt.Fprintf(os.Stderr, "fake runtime: %v\n", err)
			return
		}
		f.send(context.Background(), sdk.SessionNotification{SessionId: "fake-session", Update: u})
	}()
	return out, nil
}
func (f *fakeRuntime) Cancel(context.Context, sdk.CancelNotification) error { return nil }

// SetSessionConfigOption, for a value ID, hands back what it was just set to: a runtime
// saying its options are different from what the daemon asked for (#3082).
func (f *fakeRuntime) SetSessionConfigOption(_ context.Context, r sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	if r.ValueId == nil {
		return sdk.SetSessionConfigOptionResponse{}, nil
	}
	var opt sdk.SessionConfigOption
	raw, _ := json.Marshal(map[string]any{"type": "select", "id": r.ValueId.ConfigId, "name": "Mode",
		"currentValue": r.ValueId.Value, "options": []map[string]any{{"value": r.ValueId.Value, "name": r.ValueId.Value}}})
	if err := json.Unmarshal(raw, &opt); err != nil {
		return sdk.SetSessionConfigOptionResponse{}, err
	}
	return sdk.SetSessionConfigOptionResponse{ConfigOptions: []sdk.SessionConfigOption{opt}}, nil
}

func (f *fakeRuntime) Prompt(ctx context.Context, p sdk.PromptRequest) (sdk.PromptResponse, error) {
	<-f.ready
	// The person's words are the last text: ours (a preface, the brief) go before them.
	text, before := fakePromptWords(p.Prompt)
	say := func(s string) {
		f.send(ctx, sdk.SessionNotification{SessionId: p.SessionId, Update: sdk.UpdateAgentMessageText(s)})
	}
	// What it was told besides the words: its system prompt and what came before them.
	if strings.Contains(text, "say your brief") {
		say("System: " + f.systemPrompt + "\nBefore: " + strings.Join(before, "\n"))
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	// An agent on an org account (#999): it calls the model with what its environment
	// holds (the relay's address, a stand-in and the relay's CA), then prints that
	// environment, as an agent that runs env would.
	if strings.Contains(text, "call the model") {
		say("The model said " + callTheModel(f.getenv) + ".\nMy environment:\n" + strings.Join(f.env, "\n"))
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	// A token split over two chunks, and a last word held back to the turn's end (#1001).
	if strings.Contains(text, "split a token") {
		token := "ghp_" + strings.Repeat("a1B2", 9)
		say("Your token is " + token[:10])
		say(token[10:] + " so keep it. Done")
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	// Deaf to session/cancel, as Claude was waiting on CI (#1611): only a SIGTERM ends it,
	// or, in the test's process, the end of its connection.
	if strings.Contains(text, "keep going") {
		<-f.conn.Done()
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	}
	if strings.Contains(text, "wait for me") {
		<-ctx.Done()
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	}
	// A pause before ending the turn, so a test can open an event wait on this agent
	// first, as joining a lease's line does mid-turn (#2911).
	if strings.Contains(text, "line for a lease") {
		time.Sleep(300 * time.Millisecond)
		say("Waiting for the build lease.")
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	// Refused once for too many requests, then answered.
	if strings.Contains(text, "rate limit me") {
		if !f.rateLimited {
			f.rateLimited = true
			return sdk.PromptResponse{}, &sdk.RequestError{Code: 429, Message: "Too many requests"}
		}
		say("Answered at last.")
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	// Claude's plan window says spent, and the refusal comes typed beside end_turn (052).
	if strings.Contains(text, "typed limit") || strings.Contains(text, "pay extra") {
		return f.promptTypedLimit(ctx, p.SessionId, text, say)
	}
	if strings.Contains(text, "spend it all") {
		return sdk.PromptResponse{}, &sdk.RequestError{Code: -32603, Message: "Internal error", Data: map[string]any{"message": "Your credit balance is too low to access the API."}}
	}
	// Another agent's news, answered in a sentence (#3173).
	if strings.Contains(text, "fix merged") {
		say("Noted.")
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	if strings.Contains(text, "sandbox fails") && !f.sandboxOff {
		// A command whose sandbox could not be set up, as Claude's Bash says it.
		f.send(ctx, sdk.SessionNotification{SessionId: p.SessionId, Update: sdk.StartToolCall("call_s", "Run make",
			sdk.WithStartKind(sdk.ToolKindExecute), sdk.WithStartStatus(sdk.ToolCallStatusCompleted),
			sdk.WithStartContent([]sdk.ToolCallContent{sdk.ToolContent(sdk.TextBlock("sandbox_apply: Operation not permitted"))}))})
		say("The command could not run.")
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	// Claude with a background command (#4323): the prompt is answered when the model stops,
	// and the command's end starts a cycle of the runtime's own, with no prompt in flight.
	// The cycle says what it found, waits until the file the words name is there, then
	// ends; "quietly" leaves out the result's usage_update, as a runtime that names no
	// cycle's origin does.
	if _, path, ok := strings.Cut(text, "build in the background, then wait for "); ok {
		return f.promptBackgroundBuild(p.SessionId, text, path, say)
	}
	if strings.Contains(text, "a form") {
		return f.form(ctx, p.SessionId, strings.Contains(text, "withdraw"), say)
	}
	// Copilot on a refused card (#3722): the call fails and the turn ends, with no word.
	if strings.Contains(text, "deny quietly") {
		return f.promptDenyQuietly(ctx, p.SessionId, say)
	}
	if strings.Contains(text, "Carry on without it") {
		say("Carrying on without it.")
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	// Writes "agent's\n" to the file the words name, and reports the edit (#3437).
	if _, path, ok := strings.Cut(text, "write the file "); ok {
		return f.promptWriteFile(ctx, p.SessionId, path, say)
	}
	f.send(ctx, sdk.SessionNotification{SessionId: p.SessionId, Update: sdk.StartToolCall("call_1", "Write hi.txt",
		sdk.WithStartKind(sdk.ToolKindEdit), sdk.WithStartStatus(sdk.ToolCallStatusPending))})
	// The restart's words (wordsAboutTheRestart) say the question went: with
	// AGENTS_FAKE_ASK_AGAIN=1 it asks again, as a runtime that still needs it does (#6188).
	askAgain := f.getenv("AGENTS_FAKE_ASK_AGAIN") == "1" && strings.Contains(text, "the question you had asked went with it")
	if strings.Contains(text, "Please ask") || askAgain {
		if r, ended := f.askToWrite(ctx, p.SessionId, text, say); ended {
			return r, nil
		}
	}
	f.send(ctx, sdk.SessionNotification{SessionId: p.SessionId, Update: sdk.UpdateToolCall("call_1",
		sdk.WithUpdateStatus(sdk.ToolCallStatusCompleted))})
	say("I wrote hi.txt. ")
	say("Anything else?")
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

// askToWrite asks for permission to write hi.txt. It is true, with the turn's answer,
// when the turn ends there: stopped, or refused.
func (f *fakeRuntime) askToWrite(ctx context.Context, session sdk.SessionId, text string, say func(string)) (sdk.PromptResponse, bool) {
	req := sdk.RequestPermissionRequest{SessionId: session,
		ToolCall: sdk.ToolCallUpdate{ToolCallId: "call_1", Title: sdk.Ptr("Write hi.txt")},
		Options: []sdk.PermissionOption{{Kind: sdk.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
			{Kind: sdk.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"}}}
	// "and end at once on a stop": session/cancel ends the turn with the request still
	// open and no $/cancel_request, as a runtime that drops its own question does (#6367).
	if strings.Contains(text, "end at once on a stop") {
		go func() { _, _ = f.conn.RequestPermission(context.WithoutCancel(ctx), req) }()
		<-ctx.Done()
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, true
	}
	r, _ := f.conn.RequestPermission(ctx, req)
	// A stop answers the card cancelled, and the turn ends cancelled, as ACP says (#6188).
	if r.Outcome.Cancelled != nil || ctx.Err() != nil {
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, true
	}
	if r.Outcome.Selected == nil || r.Outcome.Selected.OptionId != "allow" {
		say("You said no.")
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, true
	}
	return sdk.PromptResponse{}, false
}

// fakePromptWords gives the last text of a prompt, and the texts before it.
func fakePromptWords(blocks []sdk.ContentBlock) (text string, before []string) {
	for _, b := range blocks {
		if b.Text != nil {
			if text != "" {
				before = append(before, text)
			}
			text = b.Text.Text
		}
	}
	return text, before
}

// promptTypedLimit answers as Claude does when its plan window says spent: the refusal
// comes typed beside end_turn (052). With "pay extra" it is on extra usage, and answers.
func (f *fakeRuntime) promptTypedLimit(ctx context.Context, session sdk.SessionId, text string, say func(string)) (sdk.PromptResponse, error) {
	var u sdk.SessionUpdate
	w := fmt.Sprintf(`{"status":"rejected","resetsAt":%d,"rateLimitType":"five_hour","utilization":1}`, time.Now().Add(3*time.Hour).Unix())
	if strings.Contains(text, "pay extra") {
		w = `{"status":"allowed","rateLimitType":"five_hour","utilization":0.4,"isUsingOverage":true}`
	}
	if err := json.Unmarshal([]byte(`{"sessionUpdate":"usage_update","used":10,"size":100,"_meta":{"_claude/rateLimit":`+w+`}}`), &u); err != nil {
		return sdk.PromptResponse{}, err
	}
	f.send(ctx, sdk.SessionNotification{SessionId: session, Update: u})
	if strings.Contains(text, "pay extra") {
		say("Done, on extra usage.")
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn, Meta: map[string]any{"jetbrains": map[string]any{"air": map[string]any{
		"sessionFailure": map[string]any{"id": "f1", "category": "limit", "title": "You've hit your session limit", "actions": []any{}}}}}}, nil
}

// promptBackgroundBuild answers as Claude with a background command (#4323). The goroutine
// it starts is the runtime's own cycle, with no prompt in flight.
func (f *fakeRuntime) promptBackgroundBuild(session sdk.SessionId, text, path string, say func(string)) (sdk.PromptResponse, error) {
	path = strings.Fields(path)[0]
	quietly := strings.Contains(text, "quietly")
	say("Started the build in the background.")
	go func() {
		bg := context.Background()
		time.Sleep(200 * time.Millisecond)
		f.send(bg, sdk.SessionNotification{SessionId: session, Update: sdk.UpdateAgentMessageText("The build finished. ")})
		// The poll of a file the test makes, not a wait for a time: it ends with the
		// connection too, so no goroutine outlives the run in the test's process.
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			if _, err := os.Stat(path); err == nil {
				break
			}
			select {
			case <-f.conn.Done():
				return
			case <-tick.C:
			}
		}
		f.send(bg, sdk.SessionNotification{SessionId: session, Update: sdk.UpdateAgentMessageText("All done.")})
		if !quietly {
			var u sdk.SessionUpdate
			if err := json.Unmarshal([]byte(`{"sessionUpdate":"usage_update","used":10,"size":100,"_meta":{"_claude/origin":{"kind":"task-notification"}}}`), &u); err != nil {
				fmt.Fprintf(os.Stderr, "fake runtime: %v\n", err)
				return
			}
			f.send(bg, sdk.SessionNotification{SessionId: session, Update: u})
		}
	}()
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

// promptDenyQuietly answers as Copilot on a refused card (#3722): the call fails and the
// turn ends, with no word.
func (f *fakeRuntime) promptDenyQuietly(ctx context.Context, session sdk.SessionId, say func(string)) (sdk.PromptResponse, error) {
	f.send(ctx, sdk.SessionNotification{SessionId: session, Update: sdk.StartToolCall("call_d", "Write /tmp/out.txt",
		sdk.WithStartKind(sdk.ToolKindEdit), sdk.WithStartStatus(sdk.ToolCallStatusPending))})
	r, _ := f.conn.RequestPermission(ctx, sdk.RequestPermissionRequest{SessionId: session,
		ToolCall: sdk.ToolCallUpdate{ToolCallId: "call_d", Title: sdk.Ptr("Write /tmp/out.txt")},
		Options: []sdk.PermissionOption{{Kind: sdk.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
			{Kind: sdk.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"}}})
	f.send(ctx, sdk.SessionNotification{SessionId: session, Update: sdk.UpdateToolCall("call_d",
		sdk.WithUpdateStatus(sdk.ToolCallStatusFailed))})
	if r.Outcome.Selected != nil && r.Outcome.Selected.OptionId == "allow" {
		say("Written.")
	}
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

// promptWriteFile writes "agent's\n" to the file the words name, and reports the edit
// (#3437).
func (f *fakeRuntime) promptWriteFile(ctx context.Context, session sdk.SessionId, path string, say func(string)) (sdk.PromptResponse, error) {
	path = strings.Fields(path)[0]
	var old []string
	if b, err := os.ReadFile(path); err == nil {
		old = []string{string(b)}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: the mode this folder always had; only its error is new
		return sdk.PromptResponse{}, err
	}
	if err := os.WriteFile(path, []byte("agent's\n"), 0o644); err != nil { //nolint:gosec // G306: the mode this file always had; only its error is new
		return sdk.PromptResponse{}, err
	}
	f.send(ctx, sdk.SessionNotification{SessionId: session, Update: sdk.StartToolCall("call_w", "Write "+filepath.Base(path),
		sdk.WithStartKind(sdk.ToolKindEdit), sdk.WithStartStatus(sdk.ToolCallStatusCompleted),
		sdk.WithStartContent([]sdk.ToolCallContent{sdk.ToolDiffContent(path, "agent's\n", old...)}))})
	say("Written.")
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

// form asks which colour, and says what it heard; with withdraw, it asks for a page by
// its own id and takes the question down itself.
func (f *fakeRuntime) form(ctx context.Context, session sdk.SessionId, withdraw bool, say func(string)) (sdk.PromptResponse, error) {
	req := sdk.UnstableCreateElicitationRequest{Form: &sdk.UnstableCreateElicitationForm{Mode: "form", Message: "Which colour?",
		RequestedSchema: sdk.UnstableElicitationSchema{Type: "object", Required: []string{"colour"},
			Properties: map[string]any{"colour": map[string]any{"type": "string", "enum": []string{"red", "blue"}}}}}}
	if withdraw {
		req = sdk.UnstableCreateElicitationRequest{Url: &sdk.UnstableCreateElicitationUrl{Mode: "url", Message: "Sign in",
			ElicitationId: "e1", Url: "https://example.com/sign-in"}}
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := f.conn.UnstableCompleteElicitation(ctx, sdk.UnstableCompleteElicitationNotification{ElicitationId: "e1"}); err != nil {
				fmt.Fprintf(os.Stderr, "fake runtime: elicitation/complete: %v\n", err)
			}
		}()
	}
	r, err := f.conn.UnstableCreateElicitation(ctx, req)
	switch {
	case ctx.Err() != nil: // session/cancel came first
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	case err != nil:
		say("No form: " + err.Error())
	case r.Accept != nil:
		say(fmt.Sprintf("You chose %v.", r.Accept.Content["colour"]))
	case r.Decline != nil:
		say("You would not say.")
	default:
		// Cancelled by a stop: session/cancel follows the answer, and the turn waits for it,
		// as a runtime that keeps to ACP does. 300 ms was too short under CI's load, and the
		// turn ended finished before the stop came (#6551). A withdrawn link has no stop to
		// wait for.
		wait := 10 * time.Second
		if withdraw {
			wait = 300 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
		case <-time.After(wait):
		}
		say("The form was closed.")
	}
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

// fakeRuntimes makes every runtime a daemon of this test starts the fake: in the test's own
// process, over a pipe (#6143), not a new copy of the test binary, which waits about a second
// to start. A test of the runtime's process (its pid, its group, a signal, what it leaves
// running) takes fakeRuntimeProcesses.
func fakeRuntimes(t testing.TB) {
	t.Helper()
	fakeRuntimeProcesses(t)
	testHooksOf(t).change(func(h *testHooks) { h.opts.attach = attachFakeRuntime })
}

// fakeRuntimeProcesses is fakeRuntimes with each runtime a process: the test binary, made
// one by AGENTS_FAKE_ACP=1 in the runtime's own environment. The process's stays as it was,
// so other tests running in parallel are not changed (#5926).
func fakeRuntimeProcesses(t testing.TB) {
	t.Helper()
	self, _ := os.Executable()
	testHooksOf(t).change(func(h *testHooks) {
		h.opts.locate = func(*runtimeDef) (string, []string, []string) { return self, nil, nil }
		h.opts.runtimeEnv = []string{"AGENTS_FAKE_ACP=1"}
		h.opts.attach = nil
	})
}

// attachFakeRuntime is a fake runtime in the test's process, with the launch's environment.
// Its end closes both pipes, as a process's exit does.
func attachFakeRuntime(l acp.Launch) (acp.Attached, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	f := &fakeRuntime{ready: make(chan struct{}), env: l.Env}
	f.conn = sdk.NewAgentSideConnection(f, outW, inR)
	close(f.ready)
	done := make(chan struct{})
	end := func() {
		_ = inW.Close()
		_ = outW.Close()
	}
	go func() {
		<-f.conn.Done()
		end()
		close(done)
	}()
	return acp.Attached{Stdin: inW, Stdout: outR, End: end, Done: done}, nil
}

// until waits for a notification that ok accepts: first of those that the last call read
// past, as a call's effect can come before its answer (#7227), then of those still to come.
func (c *conn) until(t testing.TB, what string, ok func(method string, params json.RawMessage) bool) {
	t.Helper()
	for m, some := c.nextHeard(); some; m, some = c.nextHeard() {
		if ok(m.Method, m.Params) {
			return
		}
	}
	deadline := afterLimit(t)
	for {
		select {
		case line := <-c.out:
			var m message
			mustUnmarshal(t, []byte(line), &m)
			if m.Method != "" && ok(m.Method, m.Params) {
				return
			}
		case <-deadline:
			waitFailed(t, "never heard %s", what)
		}
	}
}

func ended(id wire.UUID, state wire.AgentState) func(string, json.RawMessage) bool {
	return func(method string, p json.RawMessage) bool {
		if method != "agent/changed" {
			return false
		}
		var a wire.Agent
		return json.Unmarshal(p, &a) == nil && a.ID == id && a.State == state
	}
}

// A whole turn: the agent starts, the runtime asks, the card is answered, the turn ends
// finished with a report from its last words, and the records on disk are today's.
func TestATurnWithACard(t *testing.T) {
	flaky.UnderLoad(t, "#7227")
	t.Parallel()

	fakeRuntimes(t)
	root := t.TempDir()
	work := t.TempDir()
	d, c := start(t, root)
	defer d.Shutdown()
	var id wire.UUID
	var permission wire.PermissionRequest
	mustUnmarshal(t, c.callHearing(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + work,
		"prompt": "Please ask, then write hi.txt", "startOptions": map[string]any{"values": map[string]any{}, "extraArguments": []string{}}},
		"a card", aCard(&permission)), &id)
	if got := d.pendingPermissions(); len(got) != 1 || got[0].ID != permission.ID {
		t.Fatalf("pending %v", got)
	}
	if a := d.Store.Agent(id); a.State != wire.AgentStateWaitingOnUser {
		t.Fatalf("state %s while asking", a.State)
	}
	c.call(t, 2, "permissions/answer", map[string]any{"permissionID": permission.ID, "optionID": "allow"})
	c.until(t, "the end", ended(id, wire.AgentStateFinished))
	a := d.Store.Agent(id)
	if a.Report == nil || a.Report.Outcome != wire.WorkOutcomeNeedsAnswer || a.Report.Message != "I wrote hi.txt." {
		t.Fatalf("report %+v", a.Report)
	}
	if a.RuntimeSessionID == nil || *a.RuntimeSessionID != "fake-session" || a.EndedReason == nil || *a.EndedReason != wire.EndedReasonEndTurn {
		t.Fatalf("%+v", a)
	}
	// The record's last line follows the agent/changed that says it ended.
	waitFor(t, "the ending recorded", func() bool { k := kinds(d, id); return len(k) > 0 && k[len(k)-1] == "stateChanged" })
	entries, _, _ := d.Store.Transcript(id, 0)
	var kinds []string
	for _, e := range entries {
		b, _ := json.Marshal(e.Kind)
		var k map[string]any
		mustUnmarshal(t, b, &k)
		for name := range k {
			kinds = append(kinds, name)
		}
	}
	want := "runtimeNote userMessage stateChanged toolCall permissionAsked stateChanged permissionAnswered stateChanged toolCallUpdate agentMessage agentMessage stateChanged"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("transcript\n got %s\nwant %s", got, want)
	}
	// The record on disk is the Swift store's: ISO dates.
	b, _ := os.ReadFile(filepath.Join(root, "agents", string(id), "agent.json"))
	var back wire.Agent
	if err := diskjson.Unmarshal(b, &back); err != nil || !strings.Contains(string(b), `"createdAt":"20`) {
		t.Fatalf("agent.json %s: %v", b, err)
	}
}

// A card the person denies, after which the runtime ends the turn with no word, as Copilot
// does (#3722): the app starts it again, to carry on without that call.
func TestADeniedCardThatEndsTheTurnCarriesOn(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(),
		"prompt": "deny quietly"}), &id)
	var permission wire.PermissionRequest
	c.until(t, "a card", func(method string, p json.RawMessage) bool {
		var n wire.PermissionNotification
		if method == "agent/permission" && json.Unmarshal(p, &n) == nil && n.Request != nil {
			permission = *n.Request
			return true
		}
		return false
	})
	c.call(t, 2, "permissions/answer", map[string]any{"permissionID": permission.ID, "optionID": "reject"})
	waitFor(t, "the carry-on turn's end", func() bool {
		a := d.Store.Agent(id)
		return a.State == wire.AgentStateFinished && a.Report != nil && a.Report.Message == "Carrying on without it."
	})
	entries, _, _ := d.Store.Transcript(id, 0)
	var app []string
	for _, e := range entries {
		if u := e.Kind.UserMessage; u != nil && u.From != nil && *u.From == wire.PromptOriginApp {
			app = append(app, u.Value)
		}
	}
	if want := carryOnAfterDenyWords("Write /tmp/out.txt"); len(app) != 1 || app[0] != want {
		t.Fatalf("app prompts %q, want one %q", app, want)
	}
}

// An allowed card, or a denied one the agent answers in words, sends nothing more.
func TestACardAnsweredInWordsDoesNotCarryOn(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	var id wire.UUID
	var permission wire.PermissionRequest
	mustUnmarshal(t, c.callHearing(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(),
		"prompt": "Please ask, then write hi.txt"}, "a card", aCard(&permission)), &id)
	c.call(t, 2, "permissions/answer", map[string]any{"permissionID": permission.ID, "optionID": "reject"})
	waitFor(t, "the end", func() bool { a := d.Store.Agent(id); return a.State == wire.AgentStateFinished })
	if a := d.Store.Agent(id); len(a.QueuedPrompts) != 0 || a.Report == nil || a.Report.Message != "You said no." {
		t.Fatalf("queued %v, report %+v", a.QueuedPrompts, a.Report)
	}
}

func TestStopEndsTheTurnCancelled(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(),
		"prompt": "wait for me"}), &id)
	c.until(t, "running", ended(id, wire.AgentStateRunning))
	time.Sleep(200 * time.Millisecond)
	c.call(t, 2, "agents/stop", map[string]any{"agentID": id})
	// The record, not the notification: call drops a notification that comes before
	// its answer, and under load the stop's agent/changed can (#3623).
	waitFor(t, "stopped", func() bool { return d.Store.Agent(id).State == wire.AgentStateStopped })
	if a := d.Store.Agent(id); *a.EndedReason != wire.EndedReasonCancelled {
		t.Fatalf("ended %s", *a.EndedReason)
	}
	c.call(t, 3, "agents/archive", map[string]any{"agentID": id})
	if a := d.Store.Agent(id); a.State != wire.AgentStateArchived || a.ArchivedAt == nil {
		t.Fatalf("archived %+v", a.State)
	}
}

// A stop after kick and before the turn begins is not lost (#3621): under load the run
// loop's goroutine starts late, and turn cleared the stop and worked on.
func TestAStopBeforeTheTurnBeginsHolds(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) {
		a.QueuedPrompts = []wire.QueuedPrompt{{ID: store.NewUUID(), Text: "wait for me", From: wire.PromptOriginPerson}}
	})
	l := d.live(a.ID)
	l.mu.Lock()
	l.running = true // kicked; its goroutine has not begun
	l.mu.Unlock()
	c.call(t, 1, "agents/stop", map[string]any{"agentID": a.ID})
	d.goAfterTurn("agent "+string(a.ID), l.run)
	waitFor(t, "stopped", func() bool { return d.Store.Agent(a.ID).State == wire.AgentStateStopped })
	if got := d.Store.Agent(a.ID); *got.EndedReason != wire.EndedReasonCancelled {
		t.Fatalf("ended %s", *got.EndedReason)
	}
}

// An admin's stop from the Console (#2435) reads as theirs, not "Stopped by you"; the
// owner's next stop is the owner's again.
func TestAnAdminsStopSaysSo(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(),
		"prompt": "wait for me"}), &id)
	c.until(t, "running", ended(id, wire.AgentStateRunning))
	time.Sleep(200 * time.Millisecond)
	c.call(t, 2, "agents/stop", map[string]any{"agentID": id, "byAdmin": true})
	// The record, not the notification: call drops a notification that comes before
	// its answer, and under load the stop's agent/changed can (#3623).
	waitFor(t, "stopped", func() bool { return d.Store.Agent(id).State == wire.AgentStateStopped })
	if a := d.Store.Agent(id); *a.EndedReason != wire.EndedReasonStoppedByAdmin {
		t.Fatalf("ended %s", *a.EndedReason)
	}
	if endedSummary[wire.EndedReasonStoppedByAdmin] != "Stopped by an admin in the Console" {
		t.Fatal(endedSummary[wire.EndedReasonStoppedByAdmin])
	}
	d.noteAdminStop(id, true)
	d.noteAdminStop(id, false)
	if d.takeAdminStop(id) {
		t.Fatal("the owner's own stop read as an admin's")
	}
}

func TestAStartInAFolderThatIsNotThere(t *testing.T) {
	t.Parallel()

	_, c := start(t, t.TempDir())
	c.in <- `{"jsonrpc":"2.0","id":1,"method":"agents/start","params":{"runtimeID":"claude","cwd":"file:///no/such","prompt":"x"}}`
	for line := range c.out {
		if strings.Contains(line, `"id":1`) {
			if !strings.Contains(line, "-32004") {
				t.Fatal(line)
			}
			return
		}
	}
}

// A runtime with no sign-in ends the turn as not signed in, not as a crash, and its
// account says so (#1430).
func TestAStartWithNoSignIn(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	folder := filepath.Join(t.TempDir(), "signedout")
	must(t, os.Mkdir(folder, 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "cursor", "cwd": "file://" + folder, "prompt": "hi"}), &id)
	c.until(t, "stopped", ended(id, wire.AgentStateStopped))
	if a := d.Store.Agent(id); a.EndedReason == nil || *a.EndedReason != wire.EndedReasonSignInRefused {
		t.Fatalf("%+v", a.EndedReason)
	}
	entries, _, _ := d.Store.Transcript(id, 0)
	said := false
	for _, e := range entries {
		said = said || e.Kind.Notice != nil && e.Kind.Notice.Value.Title == "Cursor is not signed in"
	}
	if !said {
		t.Fatal(kinds(d, id))
	}
	for _, a := range d.runtimeAccounts() {
		if a.RuntimeID == "cursor" && a.State != wire.RuntimeAccountStateNeedsSignIn {
			t.Fatal(a.State)
		}
	}
}

// Antigravity gets its own home in the root and the switches Swift gave it (049 D8).
func TestAntigravityLaunchesInItsOwnHome(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	root := t.TempDir()
	d, _ := start(t, root)
	defer d.Shutdown()
	l, err := d.launchFor(runtimeByID("antigravity"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(l.Env, "\n")
	for _, want := range []string{"GEMINI_HOME=" + filepath.Join(root, "runtimes", "antigravity", "home"),
		"AGY_ACP_DISABLE_WORKSPACE_TRUST=1", "AGY_ACP_FORCE_FILE_STORAGE=1"} {
		if !strings.Contains(env, want) {
			t.Fatalf("no %s in\n%s", want, env)
		}
	}
}

// OpenCode's own copy gets a data directory in the root, and no XDG_DATA_HOME from the
// login shell. It then does not open ~/.local/share/opencode, which a newer OpenCode
// migrates until this copy exits with "no such column: project_id".
func TestOpenCodeLaunchesInItsOwnDataDirectory(t *testing.T) {
	fakeRuntimes(t)
	root := t.TempDir()
	d, _ := start(t, root)
	defer d.Shutdown()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "person"))
	l, err := d.launchFor(runtimeByID("opencode"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "runtimes", "opencode", "data")
	n := 0
	for _, kv := range l.Env {
		if !strings.HasPrefix(kv, "XDG_DATA_HOME=") {
			continue
		}
		n++
		if kv != "XDG_DATA_HOME="+want {
			t.Fatalf("%s", kv)
		}
	}
	if n != 1 {
		t.Fatalf("XDG_DATA_HOME count %d in\n%s", n, strings.Join(l.Env, "\n"))
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

// Antigravity 1.4.5 writes skills/system/run-workflow into the session's folder (#3731):
// info/exclude keeps it out of git status, also in a subfolder, and once only.
func TestAntigravityLaunchKeepsItsSkillsOutOfGit(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	sub := filepath.Join(dir, "app")
	must(t, os.MkdirAll(filepath.Join(dir, "skills", "system", "run-workflow"), 0o755))                           //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(dir, "skills", "system", "run-workflow", "SKILL.md"), []byte("x"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	must(t, os.MkdirAll(filepath.Join(sub, "skills", "system", "run-workflow"), 0o755))                           //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(sub, "skills", "system", "run-workflow", "SKILL.md"), []byte("x"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	for _, at := range []string{dir, sub, dir} {
		if _, err := d.launchFor(runtimeByID("antigravity"), at); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if strings.Count(string(b), "\n/skills/system/\n") != 1 || strings.Count(string(b), "\n/app/skills/system/\n") != 1 {
		t.Fatalf("%s", b)
	}
	if out, err := git(dir, "status", "--porcelain", "--untracked-files=all"); err != nil || out != "" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := d.launchFor(runtimeByID("antigravity"), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimesList(t *testing.T) {
	t.Parallel()

	_, c := start(t, t.TempDir())
	var rs []wire.RuntimeStatus
	mustUnmarshal(t, c.call(t, 1, "runtimes/list", nil), &rs)
	if len(rs) != 7 || rs[0].Runtime.ID != "antigravity" {
		t.Fatalf("%d runtimes, first %v", len(rs), rs)
	}
	for _, r := range rs {
		if r.Availability.Available == nil && r.Availability.Missing == nil {
			t.Fatalf("%s has no availability", r.Runtime.ID)
		}
	}
}

func TestAToolCallIsRecordedAsSwiftRecordsIt(t *testing.T) {
	t.Parallel()

	tc := toolCall(sdk.SessionToolCallUpdate{ToolCallId: "c1", Status: sdk.Ptr(sdk.ToolCallStatusCompleted),
		RawOutput: map[string]any{"ok": true}, Meta: map[string]any{"x": 1}})
	if tc.Title != "Tool call" || *tc.ToolCallID != "c1" || *tc.Status != "completed" || string(tc.RawOutput) != `{"ok":true}` {
		t.Fatalf("%+v", tc)
	}
	if strings.Contains(string(tc.Raw), "rawOutput") || strings.Contains(string(tc.Raw), "_meta") || !strings.Contains(string(tc.Raw), "toolCallId") {
		t.Fatalf("raw %s", tc.Raw)
	}
	kind := toolCall(sdk.SessionToolCallUpdate{ToolCallId: "c2", Kind: sdk.Ptr(sdk.ToolKindEdit)})
	if kind.Title != "edit" {
		t.Fatalf("title %q", kind.Title)
	}
}

func TestAToolCallLeavesBigImagesOutOfTheTranscript(t *testing.T) {
	t.Parallel()

	shot := strings.Repeat("A", 200<<10)
	small := strings.Repeat("B", 100)
	tc := toolCall(sdk.SessionToolCallUpdate{ToolCallId: "c1", Status: sdk.Ptr(sdk.ToolCallStatusCompleted),
		Content: []sdk.ToolCallContent{
			sdk.ToolContent(sdk.ImageBlock(shot, "image/jpeg")),
			sdk.ToolContent(sdk.ImageBlock(small, "image/png")),
			sdk.ToolContent(sdk.TextBlock("done")),
		},
		RawOutput: []any{
			map[string]any{"type": "image", "data": shot, "mimeType": "image/jpeg"},
			map[string]any{"type": "text", "text": "done"},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///x", "blob": shot}},
		}})
	if c := tc.Content[0].Content; c.Type != "text" || *c.Text != "image, 200 KB" {
		t.Fatalf("big image kept: %+v", c)
	}
	if c := tc.Content[1].Content; c.Type != "image" || *c.Data != small {
		t.Fatalf("small image lost: %+v", c)
	}
	if *tc.Content[2].Content.Text != "done" {
		t.Fatalf("text lost: %+v", tc.Content[2])
	}
	raw := string(tc.RawOutput)
	if len(raw) > 1024 || !strings.Contains(raw, `"image, 200 KB"`) || !strings.Contains(raw, `"blob":"blob, 200 KB"`) || !strings.Contains(raw, `"done"`) {
		t.Fatalf("raw output %d bytes: %.300s", len(raw), raw)
	}
}

// callTheModel is the fake runtime's model request: to ANTHROPIC_BASE_URL with
// ANTHROPIC_AUTH_TOKEN, trusting NODE_EXTRA_CA_CERTS, as Claude Code does.
func callTheModel(getenv func(string) string) string {
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(getenv("NODE_EXTRA_CA_CERTS")); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", getenv("ANTHROPIC_BASE_URL")+"/v1/messages", strings.NewReader(`{"model":"claude","messages":[]}`))
	if err != nil {
		return err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+getenv("ANTHROPIC_AUTH_TOKEN"))
	resp, err := client.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return err.Error()
	}
	return strconv.Itoa(resp.StatusCode)
}
