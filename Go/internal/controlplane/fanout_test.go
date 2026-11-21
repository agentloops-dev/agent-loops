package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// docsHost is a host that says it has docs and messages, as agentsd does.
func (p *plane) docsHost(machine string) *fakeHost {
	hello := fakeHello(machine)
	hello.Capabilities = append(hello.Capabilities, wire.CapDocs1, wire.CapMessages1)
	return p.hostSaying(hello, nil)
}

// docsPaging answers docs/list as a machine whose docs are numbered by their update time, newest
// first, `page` to a page, and counts its asks.
func docsPaging(updated []float64, asks *int, mu *sync.Mutex) func(json.RawMessage) any {
	return func(params json.RawMessage) any {
		var r wire.DocsListRequest
		fakeMust(json.Unmarshal(params, &r))
		mu.Lock()
		*asks++
		mu.Unlock()
		from := 0
		if r.Cursor != nil {
			if _, err := fmt.Sscan(*r.Cursor, &from); err != nil {
				fakeMust(err)
			}
		}
		size := int(*r.Limit)
		to := min(from+size, len(updated))
		docs := []map[string]any{}
		for _, u := range updated[from:to] {
			docs = append(docs, map[string]any{"ref": fmt.Sprintf("doc:%v", u), "title": "T", "source": "s", "updated": u})
		}
		out := map[string]any{"docs": docs, "sources": []map[string]any{{"id": "s", "kind": "folder", "name": "S", "state": "ok"}}}
		if to < len(updated) {
			out["cursor"] = fmt.Sprint(to)
		}
		return out
	}
}

// #5571: docs/listAll asks each of the caller's machines once, merges the docs newest first,
// cuts the page at limit, and its cursor goes on from where each machine is, with every doc once.
func TestDocsListAllMergesMachinesWithACursor(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	mac, box, off := p.docsHost("MAC"), p.docsHost("BOX"), p.docsHost("OFF")
	_, window := p.client("Window")
	var asks int
	var mu sync.Mutex
	mac.mu.Lock()
	mac.answers = map[string]func(json.RawMessage) any{"docs/list": docsPaging([]float64{90, 70, 50, 30, 10}, &asks, &mu)}
	mac.mu.Unlock()
	box.mu.Lock()
	box.answers = map[string]func(json.RawMessage) any{"docs/list": docsPaging([]float64{80, 60, 40, 20}, &asks, &mu)}
	box.mu.Unlock()
	off.stop()

	var seen []float64
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("it does not end")
		}
		ask := map[string]any{"limit": 2}
		if cursor != "" {
			ask["cursor"] = cursor
		}
		var got wire.DocsListAll
		must(t, json.Unmarshal(call(t, window, "", "docs/listAll", ask), &got))
		if len(got.Docs) > 2 {
			t.Fatalf("a page of %d", len(got.Docs))
		}
		for _, d := range got.Docs {
			seen = append(seen, float64(*d.Doc.Updated))
		}
		if pages == 0 && (len(got.Unreached) != 1 || got.Unreached[0].Machine != wire.MachineRef(off.member.Host)) {
			t.Fatalf("unreached %+v", got.Unreached)
		}
		if pages == 0 && len(got.Sources) != 2 {
			t.Fatalf("sources %+v", got.Sources)
		}
		if got.Cursor == nil {
			break
		}
		cursor = *got.Cursor
	}
	want := []float64{90, 80, 70, 60, 50, 40, 30, 20, 10}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("docs %v, want %v", seen, want)
	}
	if e := callError(t, window, "", "docs/listAll", map[string]any{"cursor": "nonsense!"}); !strings.Contains(e, "not a cursor") {
		t.Fatal(e)
	}
}

// #5571: fanOut says a machine that did not answer once, and the pages after leave it out, so the
// cursor ends when every machine that answered is at its end.
func TestFanOutSaysAnUnansweredMachineOnce(t *testing.T) {
	t.Parallel()
	ask := func(host, page string) (fanPage[int], bool) {
		if host == "b" {
			return fanPage[int]{}, false
		}
		return fanPage[int]{Items: []int{9, 7}}, true
	}
	newer := func(a, b int) bool { return a > b }
	first, err := fanOut([]string{"a", "b"}, "", 1, ask, newer)
	if err != nil || len(first.Items) != 1 || len(first.Unreached) != 1 || first.Cursor == "" {
		t.Fatalf("%+v %v", first, err)
	}
	second, err := fanOut([]string{"a", "b"}, first.Cursor, 1, ask, newer)
	if err != nil || len(second.Items) != 1 || second.Items[0].Item != 7 || len(second.Unreached) != 0 || second.Cursor != "" {
		t.Fatalf("%+v %v", second, err)
	}
}

// #5571: messages/conversationsAll merges by the newest message and says a connector's error
// with its machine.
func TestConversationsAllMergesMachines(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	mac, box := p.docsHost("MAC"), p.docsHost("BOX")
	_, window := p.client("Window")
	conv := func(id, at string) map[string]any {
		return map[string]any{"server": "slack", "folder": "file:///a/", "conversation": map[string]any{"source": "slack", "id": id, "title": id, "kind": "channel", "lastActivity": at}}
	}
	mac.mu.Lock()
	mac.answers = map[string]func(json.RawMessage) any{"messages/conversations": func(json.RawMessage) any {
		return map[string]any{"conversations": []any{conv("m1", "2026-11-14T10:00:00Z"), conv("m2", "2026-11-14T08:00:00Z")}}
	}}
	mac.mu.Unlock()
	box.mu.Lock()
	box.answers = map[string]func(json.RawMessage) any{"messages/conversations": func(json.RawMessage) any {
		return map[string]any{"conversations": []any{conv("b1", "2026-11-14T09:00:00Z")}, "errors": []any{map[string]any{"server": "email", "folder": "file:///a/", "message": "down"}}}
	}}
	box.mu.Unlock()
	var got wire.MessagesConversationsAll
	must(t, json.Unmarshal(call(t, window, "", "messages/conversationsAll", map[string]any{"limit": 20}), &got))
	var ids []string
	for _, c := range got.Conversations {
		ids = append(ids, c.Conversation.Conversation.ID)
	}
	if strings.Join(ids, ",") != "m1,b1,m2" || got.Cursor != nil || len(got.Errors) != 1 || got.Errors[0].Machine != wire.MachineRef(box.member.Host) {
		t.Fatalf("%v %+v", ids, got)
	}
}
