package mcpevents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentloops-dev/agent-loops/Go/internal/apicalls"
)

// The front: MCP's stdio transport, one JSON-RPC message a line. It answers events/list and
// events/poll itself, and hands every other message to a go-sdk server with the server's
// tools, and that server's messages back as they come. MCP's SDKs have no events (a
// working-group draft), so the front also adds the `events` capability and the events' words
// to its answer to initialize.

// Source answers events/poll.
type Source interface {
	Poll(ctx context.Context, raw json.RawMessage) (map[string]any, *Error)
}

// Front serves one client over stdio.
type Front struct {
	Events Source
	// List is events/list's answer.
	List     func() map[string]any
	Upstream *mcp.Server
	// Name, Title and Version are the serverInfo of the answer to initialize, and
	// Instructions its words.
	Name, Title, Version, Instructions string
	// Context, when set, is the context the server behind runs in, made from Serve's.
	Context func(context.Context) context.Context
}

// maxLine is the longest message read; a longer one ends the connection, as the SDK's does.
const maxLine = 64 << 20

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

// Serve reads in until it ends and writes to out, then stops the server behind it.
func (f *Front) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if f.Context != nil {
		ctx = f.Context(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	f.Upstream.AddReceivingMiddleware(namesTheTool)
	toUpstream, upstreamIn := io.Pipe()
	upstreamOut, fromUpstream := io.Pipe()
	upstreamDone := make(chan error, 1)
	go func() {
		upstreamDone <- f.Upstream.Run(ctx, &mcp.IOTransport{Reader: toUpstream, Writer: fromUpstream})
		_ = fromUpstream.Close()
	}()

	c := &connection{front: f, out: out, upstreamIn: upstreamIn, initIDs: map[string]bool{}}
	relayed := make(chan struct{})
	go func() {
		defer close(relayed)
		c.relay(upstreamOut)
	}()

	readErr := c.read(ctx, in)
	c.answering.Wait()
	_ = upstreamIn.Close()
	<-relayed
	cancel()
	<-upstreamDone
	return readErr
}

// connection is one client's Serve: what its reader and the relay from the server behind share.
type connection struct {
	front      *Front
	out        io.Writer
	upstreamIn io.Writer
	writeMu    sync.Mutex
	// initIDs are the ids of the initialize requests the server behind has not answered yet.
	initMu    sync.Mutex
	initIDs   map[string]bool
	answering sync.WaitGroup
}

// write puts line on out, one line at a time.
func (c *connection) write(line []byte) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, _ = c.out.Write(append(bytes.TrimRight(line, "\r\n"), '\n'))
}

// relay writes each line of the server behind to out until it ends, with the events added to
// its answer to initialize.
func (c *connection) relay(upstreamOut io.Reader) {
	r := bufio.NewReaderSize(upstreamOut, 64<<10)
	for {
		line, err := ReadLine(r)
		if len(line) > 0 {
			c.write(c.initializedIfInit(line))
		}
		if err != nil {
			return
		}
	}
}

// initializedIfInit is line with the events added when it answers an initialize request, else
// line.
func (c *connection) initializedIfInit(line []byte) []byte {
	var m message
	if json.Unmarshal(line, &m) != nil || len(m.ID) == 0 || m.Method != "" || len(m.Result) == 0 {
		return line
	}
	c.initMu.Lock()
	isInit := c.initIDs[string(m.ID)]
	delete(c.initIDs, string(m.ID))
	c.initMu.Unlock()
	if isInit {
		return c.front.initialized(line)
	}
	return line
}

// read reads in until it ends, answers the events requests and hands every other line to the
// server behind. Its error is the read's or the hand-over's; the end of in is none.
func (c *connection) read(ctx context.Context, in io.Reader) error {
	r := bufio.NewReaderSize(in, 64<<10)
	var readErr error
	for {
		line, err := ReadLine(r)
		if len(bytes.TrimSpace(line)) > 0 {
			if werr := c.handle(ctx, line); werr != nil {
				readErr = werr
			}
		}
		if err != nil || readErr != nil {
			if err != nil && err != io.EOF {
				readErr = err
			}
			return readErr
		}
	}
}

// handle answers one events request itself, in the background, or hands line to the server
// behind; its error is the hand-over's.
func (c *connection) handle(ctx context.Context, line []byte) error {
	var m message
	parsed := json.Unmarshal(line, &m) == nil
	if parsed && (m.Method == "events/list" || m.Method == "events/poll") {
		if len(m.ID) == 0 {
			return nil // a notification is answered with nothing
		}
		c.answering.Add(1)
		go func() {
			defer c.answering.Done()
			c.write(c.front.answer(ctx, m))
		}()
		return nil
	}
	if parsed && m.Method == "initialize" && len(m.ID) > 0 {
		c.initMu.Lock()
		c.initIDs[string(m.ID)] = true
		c.initMu.Unlock()
	}
	_, err := c.upstreamIn.Write(append(bytes.TrimRight(line, "\r\n"), '\n'))
	return err
}

// ReadLine is the next line without its newline; err is set at the end, and is
// io.ErrShortBuffer for a line longer than 64 MB.
func ReadLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, more, err := r.ReadLine()
		line = append(line, part...)
		if len(line) > maxLine {
			return nil, io.ErrShortBuffer
		}
		if err != nil {
			return line, err
		}
		if !more {
			return line, nil
		}
	}
}

// answer is the line answering an events request.
func (f *Front) answer(ctx context.Context, m message) []byte {
	reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	if m.Method == "events/list" {
		reply["result"] = f.List()
	} else if result, err := f.Events.Poll(pollCaller(ctx, m.Params), m.Params); err != nil {
		reply["error"] = err
	} else {
		reply["result"] = result
	}
	line, err := json.Marshal(reply)
	if err != nil {
		line, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID,
			"error": Error{Code: -32603, Message: err.Error()}})
	}
	return line
}

// pollCaller is ctx with the poll's event named as the caller of the forge's API (#6164).
func pollCaller(ctx context.Context, params json.RawMessage) context.Context {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(params, &p)
	return apicalls.WithCaller(ctx, "event "+p.Name)
}

// namesTheTool names a tool call's tool as the caller of the forge's API (#6164).
func namesTheTool(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && method == "tools/call" {
			ctx = apicalls.WithCaller(ctx, "tool "+p.Name)
		}
		return next(ctx, method, req)
	}
}

// initialized is the answer of the server behind it to initialize with the events added: the
// capability, which the Agents daemon looks for before it lists them, their words, and this
// server's name.
func (f *Front) initialized(line []byte) []byte {
	var reply map[string]json.RawMessage
	if json.Unmarshal(line, &reply) != nil {
		return line
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(reply["result"], &result) != nil {
		return line
	}
	capabilities := map[string]json.RawMessage{}
	_ = json.Unmarshal(result["capabilities"], &capabilities)
	capabilities["events"] = json.RawMessage(`{"listChanged":false}`)
	result["capabilities"], _ = json.Marshal(capabilities)
	result["instructions"], _ = json.Marshal(f.Instructions)
	result["serverInfo"], _ = json.Marshal(map[string]string{"name": f.Name, "title": f.Title, "version": f.Version})
	reply["result"], _ = json.Marshal(result)
	out, err := json.Marshal(reply)
	if err != nil {
		return line
	}
	return out
}
