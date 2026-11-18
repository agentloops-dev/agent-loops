package mcpevents

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentloops-dev/agent-loops/Go/internal/apicalls"
)

// callers is a Source that keeps the caller each poll's context names.
type callers struct {
	mu   sync.Mutex
	seen []string
}

func (c *callers) Poll(ctx context.Context, _ json.RawMessage) (map[string]any, *Error) {
	c.add(apicalls.Caller(ctx))
	return map[string]any{"events": []any{}}, nil
}

func (c *callers) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, s)
}

// A poll and a tool call each name their caller of the forge's API (#6164).
func TestFrontNamesThePollAndTheToolAsTheCaller(t *testing.T) {
	seen := &callers{}
	upstream := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0"}, nil)
	mcp.AddTool(upstream, &mcp.Tool{Name: "show_pull_request"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		seen.add(apicalls.Caller(ctx))
		return &mcp.CallToolResult{}, nil, nil
	})
	front := &Front{Events: seen, List: func() map[string]any { return map[string]any{"events": []any{}} }, Upstream: upstream}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"events/poll","params":{"name":"pr.blocked"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"show_pull_request","arguments":{}}}`,
	}, "\n") + "\n"
	inR, inW := io.Pipe()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- front.Serve(context.Background(), inR, pw); _ = pw.Close() }()
	go func() { _, _ = io.WriteString(inW, in) }()
	r := bufio.NewReader(pr)
	answered := map[string]bool{}
	for len(answered) < 3 {
		line, err := ReadLine(r)
		if err != nil {
			t.Fatalf("after %v: %v", answered, err)
		}
		var m map[string]any
		_ = json.Unmarshal(line, &m)
		if id, ok := m["id"]; ok {
			b, _ := json.Marshal(id)
			answered[string(b)] = true
		}
	}
	_ = inW.Close()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	got := strings.Join(seen.seen, ", ")
	if !strings.Contains(got, "event pr.blocked") || !strings.Contains(got, "tool show_pull_request") {
		t.Fatalf("callers %q", got)
	}
}
