package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The app's MCP client for providers: MCPToolClient, MCPClient and MCPStdioProcess, cut
// down to what a provider needs. A connection, its tools, and calls to them, over stdio.
// Logged: the server's name and what happened in a word, never its command, environment
// or any body.

const mcpProtocolVersion = "2025-06-18"

// mcpServer is MCPServer: a stdio server the control plane runs.
type mcpServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// mcpFailure is MCPClient.Failure, worded as MCPToolClient.failure words it.
type mcpFailure struct {
	kind   string // auth, status, expired, unreachable, stopped, timeout, refused, notMCP
	words  string
	status int
	code   int
}

func (f *mcpFailure) Error() string {
	switch f.kind {
	case "auth":
		return "It wants a sign-in first."
	case "status":
		return fmt.Sprintf("It answered with HTTP %d.", f.status)
	case "expired":
		return "It forgot this session."
	case "stopped":
		if f.words == "" {
			return "The server stopped."
		}
		return f.words
	case "timeout":
		return "It didn’t answer in time."
	case "notMCP":
		return "What it answered isn’t MCP."
	}
	return f.words
}

// logWord is one word for the log: never the server's own text.
func (f *mcpFailure) logWord() string {
	switch f.kind {
	case "auth":
		return "auth required"
	case "status":
		return fmt.Sprintf("http %d", f.status)
	case "expired":
		return "session expired"
	case "timeout":
		return "timed out"
	case "refused":
		return fmt.Sprintf("refused %d", f.code)
	case "notMCP":
		return "not MCP"
	}
	return f.kind
}

// retriable: the connection went, so a new one may do what this one could not. Never
// after the server heard the call and said no, or ran out of time.
func (f *mcpFailure) retriable() bool {
	return f.kind == "stopped" || f.kind == "expired" || f.kind == "unreachable"
}

// mcpClient is MCPToolClient over MCPClient.
type mcpClient struct {
	server  mcpServer
	cwd     string
	timeout time.Duration
	log     func(string)

	mu         sync.Mutex
	nextID     int
	stdio      *stdioProcess
	negotiated string
}

func newMCPClient(server mcpServer, cwd string, timeout time.Duration, log func(string)) *mcpClient {
	return &mcpClient{server: server, cwd: cwd, timeout: timeout, log: log, nextID: 1}
}

// connect starts the server or opens the session, and initializes.
func (c *mcpClient) connect() error {
	err := func() error {
		p := startStdio(c.server, c.cwd, c.log)
		c.mu.Lock()
		c.stdio = p
		c.mu.Unlock()
		if why, stopped := p.stoppedBecause(); stopped {
			return &mcpFailure{kind: "stopped", words: why}
		}
		params := map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{"extensions": map[string]any{
				"io.modelcontextprotocol/ui": map[string]any{"mimeTypes": []string{"text/html;profile=mcp-app"}}}},
			"clientInfo": map[string]any{"name": "Agent Loops", "version": "1"},
		}
		result, err := c.request("initialize", params, false, 0)
		if err != nil {
			return err
		}
		if result == nil {
			return &mcpFailure{kind: "notMCP"}
		}
		negotiated, _ := str(get(result, "protocolVersion"))
		c.mu.Lock()
		c.negotiated = negotiated
		c.mu.Unlock()
		return c.notify("notifications/initialized")
	}()
	var f *mcpFailure
	if errors.As(err, &f) {
		c.log("mcp client: " + c.server.Name + " did not answer (" + f.logWord() + ")")
		return f
	}
	if err != nil {
		return err
	}
	c.log("mcp client: " + c.server.Name + " answered")
	return nil
}

// end stops the stdio server. Safe to call more than once.
func (c *mcpClient) end() {
	c.mu.Lock()
	p := c.stdio
	c.stdio = nil
	c.mu.Unlock()
	if p != nil {
		p.end("The app's client has ended.")
	}
	c.log("mcp client: " + c.server.Name + " ended")
}

// listTools is every tool, as the server described it, through nextCursor: at most 10
// pages and 500 tools.
func (c *mcpClient) listTools() ([]any, error) {
	var items []any
	var cursor string
	for range 10 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := c.request("tools/list", params, true, 0)
		if err != nil {
			return nil, err
		}
		if result == nil {
			break
		}
		page, _ := arr(get(result, "tools"))
		items = append(items, page...)
		cursor, _ = str(get(result, "nextCursor"))
		if cursor == "" || len(items) >= 500 {
			break
		}
	}
	if len(items) > 500 {
		items = items[:500]
	}
	var out []any
	for _, raw := range items {
		if _, ok := str(get(raw, "name")); ok {
			out = append(out, raw)
		}
	}
	return out, nil
}

// toolNames is every tool's name.
func (c *mcpClient) toolNames() ([]string, error) {
	tools, err := c.listTools()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, strOr(get(t, "name")))
	}
	return names, nil
}

// callTool is tools/call's whole result; timeout overrides the client's own.
func (c *mcpClient) callTool(name string, arguments any, timeout time.Duration) (any, error) {
	result, err := c.request("tools/call", map[string]any{"name": name, "arguments": arguments}, false, timeout)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, &mcpFailure{kind: "notMCP"}
	}
	return result, nil
}

// request is the result of method: nil only when optional and the server has no such method.
func (c *mcpClient) request(method string, params any, optional bool, timeout time.Duration) (any, error) {
	if timeout <= 0 {
		timeout = c.timeout
	}
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	reply, err := c.exchange(body, id, timeout)
	if err != nil {
		return nil, err
	}
	message, err := parseJSON(reply)
	if err != nil {
		return nil, &mcpFailure{kind: "notMCP"}
	}
	if got, ok := integer(get(message, "id")); !ok || got != id {
		return nil, &mcpFailure{kind: "notMCP"}
	}
	if e := get(message, "error"); e != nil {
		code, _ := integer(get(e, "code"))
		if optional && code == CodeMethodNotFound {
			return nil, nil
		}
		c.mu.Lock()
		p := c.stdio
		c.mu.Unlock()
		if code == -32000 && p != nil {
			if why, stopped := p.stoppedBecause(); stopped {
				return nil, &mcpFailure{kind: "stopped", words: why}
			}
		}
		return nil, &mcpFailure{kind: "refused", words: strOr(get(e, "message")), code: code}
	}
	if !has(message, "result") {
		return nil, &mcpFailure{kind: "notMCP"}
	}
	return get(message, "result"), nil
}

func (c *mcpClient) notify(method string) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	c.mu.Lock()
	p := c.stdio
	c.mu.Unlock()
	if p != nil {
		p.write(body)
	}
	return nil
}

// exchange is one request out and its answer back, within timeout.
func (c *mcpClient) exchange(body []byte, id int, timeout time.Duration) ([]byte, error) {
	c.mu.Lock()
	p := c.stdio
	c.mu.Unlock()
	if p == nil {
		return nil, &mcpFailure{kind: "stopped", words: "The server is not running."}
	}
	return p.send(body, id, timeout)
}

// mcpEnvironment is RuntimeEnvironment.forMCPServers in the control plane: only what any
// program needs of its own environment, never all of it (#727).
func mcpEnvironment() map[string]string {
	allowed := []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TZ",
		"LANG", "LANGUAGE", "LC_*", "TERM", "COLORTERM", "XDG_*", "__CF_USER_TEXT_ENCODING",
		"EDITOR", "VISUAL", "PAGER", "SSH_AUTH_SOCK",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
		"DEVELOPER_DIR", "JAVA_HOME", "GOPATH", "GOROOT", "CARGO_HOME", "RUSTUP_HOME", "PYENV_ROOT", "NVM_DIR",
		"AGENTS_*",
	}
	out := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		for _, pattern := range allowed {
			if prefix, ok := strings.CutSuffix(pattern, "*"); ok && strings.HasPrefix(name, prefix) || name == pattern {
				out[name] = value
				break
			}
		}
	}
	return out
}

// stdioProcess is MCPStdioProcess: a stdio MCP server, and the requests waiting for their
// answers, matched by id.
type stdioProcess struct {
	name string
	log  func(string)
	cmd  *exec.Cmd
	in   io.WriteCloser

	mu        sync.Mutex
	writeMu   sync.Mutex
	waiting   map[int]chan []byte
	ended     bool
	endReason string
	exited    chan struct{}
}

func startStdio(server mcpServer, cwd string, log func(string)) *stdioProcess {
	p := &stdioProcess{name: server.Name, log: log, waiting: map[int]chan []byte{}, endReason: "The server stopped.",
		exited: make(chan struct{})}
	cmd := exec.Command("/usr/bin/env", append([]string{server.Command}, server.Args...)...)
	env := mcpEnvironment()
	for k, v := range server.Env {
		env[k] = v
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if info, err := os.Stat(cwd); err == nil && info.IsDir() {
		cmd.Dir = cwd
	}
	in, err1 := cmd.StdinPipe()
	out, err2 := cmd.StdoutPipe()
	if err1 != nil || err2 != nil || cmd.Start() != nil {
		log("mcp client: " + server.Name + " could not start")
		p.ended, p.endReason = true, "The server could not be started."
		close(p.exited)
		return p
	}
	p.cmd, p.in = cmd, in
	log(fmt.Sprintf("mcp client: %s started (pid %d)", server.Name, cmd.Process.Pid))
	go p.read(out)
	return p
}

func (p *stdioProcess) stoppedBecause() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.endReason, p.ended
}

func (p *stdioProcess) read(out io.Reader) {
	r := bufio.NewReaderSize(out, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			p.handle(bytes.TrimSpace(line))
		}
		if err != nil {
			break
		}
	}
	err := p.cmd.Wait()
	reason := "The server stopped."
	var exit *exec.ExitError
	// env says 127 when it found no such command.
	if errors.As(err, &exit) && exit.ExitCode() == 127 {
		reason = "The command was not found."
	}
	close(p.exited)
	p.end(reason)
}

func (p *stdioProcess) handle(line []byte) {
	message, err := parseJSON(line)
	if err != nil {
		return
	}
	if has(message, "method") {
		// The server speaking first: a request is told no, so it does not wait forever; a
		// notification is dropped.
		if has(message, "id") {
			reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": get(message, "id"),
				"error": map[string]any{"code": CodeMethodNotFound, "message": "Not passed on by the app."}})
			p.write(reply)
		}
		return
	}
	id, ok := integer(get(message, "id"))
	if !ok {
		return
	}
	p.mu.Lock()
	done := p.waiting[id]
	delete(p.waiting, id)
	p.mu.Unlock()
	if done != nil {
		done <- line
	}
}

func (p *stdioProcess) write(body []byte) {
	if p.in == nil {
		return
	}
	line := append(bytes.TrimRight(body, "\n"), '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	// A server that cannot read its stdin has ended: Exited says so.
	_, _ = p.in.Write(line)
}

// send writes one request and waits for the answer with the same id, or until timeout.
func (p *stdioProcess) send(body []byte, id int, timeout time.Duration) ([]byte, error) {
	done := make(chan []byte, 1)
	p.mu.Lock()
	if p.ended {
		reason := p.endReason
		p.mu.Unlock()
		return stdioError(id, reason), nil
	}
	p.waiting[id] = done
	p.mu.Unlock()
	p.write(body)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case reply := <-done:
		return reply, nil
	case <-timer.C:
		// Abandoned: an answer that comes later is dropped.
		p.mu.Lock()
		delete(p.waiting, id)
		p.mu.Unlock()
		return nil, &mcpFailure{kind: "timeout"}
	}
}

func stdioError(id int, reason string) []byte {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": -32000, "message": reason}})
	return data
}

// end stops it: every waiting request is answered with why, and the process is told to
// end, then killed 2 seconds later.
func (p *stdioProcess) end(reason string) {
	p.mu.Lock()
	if !p.ended {
		p.endReason = reason
	}
	p.ended = true
	pending := p.waiting
	p.waiting = map[int]chan []byte{}
	reason = p.endReason
	p.mu.Unlock()
	for id, done := range pending {
		done <- stdioError(id, reason)
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	select {
	case <-p.exited:
		return
	default:
	}
	if p.in != nil {
		p.in.Close()
	}
	// It may have ended already; the Kill below is the fallback.
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	go func() {
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			// It may have ended between the two.
			_ = p.cmd.Process.Kill()
		}
	}()
}
