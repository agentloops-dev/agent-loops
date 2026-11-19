package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The caller's machines answered once (#5571, epic #5281): a page that is host-local on each
// machine (its docs, its messaging conversations) is asked of every machine of the caller's
// by the control plane, in one call, and merged: the client makes one call instead of one for
// each machine. Each call is bounded: at most mostFanMachines machines are asked, each for at
// most the page's limit, for at most fanPatience; the page is the limit newest of them all,
// and the cursor says where each machine is, so the next page is asked from there.

const (
	// mostFanMachines bounds the machines one call asks; notAsked says how many were past it.
	mostFanMachines = 50
	// fanPatience bounds the time a machine has to answer; one slower is in unreached.
	fanPatience = 5 * time.Second
	// mostFanCursor bounds a cursor a client sends: 50 machines' places are far below it.
	mostFanCursor = 16 << 10
)

// fanPlace is where one machine is in a cursor: the cursor of the machine's own page that was
// last read (empty for its first), and how many of that page's items the pages sent already
// took. The machine's page is read again for the next, so nothing is kept here.
type fanPlace struct {
	Page string `json:"p,omitempty"`
	Took int    `json:"n,omitempty"`
	Done bool   `json:"d,omitempty"`
}

// fanPage is one machine's page: its items newest first, and the cursor of its next page
// (empty at its end).
type fanPage[T any] struct {
	Items []T
	Next  string
}

// fanItem is an item with the machine that has it.
type fanItem[T any] struct {
	Host string
	Item T
}

// fanAnswer is what fanOut says of a call.
type fanAnswer[T any] struct {
	Items     []fanItem[T]
	Cursor    string
	Unreached []string
	NotAsked  int
}

// fanHosts is the caller's machines that have the capability, by id, at most mostFanMachines of
// them, and how many were past it.
func (m *Methods) fanHosts(user, capability string) (asked []string, notAsked int) {
	for _, h := range m.records.Hosts() {
		if m.s.ownerOf(h.ID) != user || !slices.Contains(hostCapabilities(h), capability) {
			continue
		}
		asked = append(asked, h.ID)
	}
	if len(asked) > mostFanMachines {
		notAsked = len(asked) - mostFanMachines
		asked = asked[:mostFanMachines]
	}
	return asked, notAsked
}

func (m *Methods) hostName(host string) string {
	if h := m.records.Host(host); h != nil {
		return h.Name
	}
	return host
}

func (m *Methods) fanProjectMachines(hosts []string) []wire.ProjectMachine {
	out := []wire.ProjectMachine{}
	for _, host := range hosts {
		out = append(out, wire.ProjectMachine{Machine: wire.MachineRef(host), MachineName: m.hostName(host)})
	}
	return out
}

func decodeFanCursor(cursor string) (map[string]fanPlace, error) {
	places := map[string]fanPlace{}
	if cursor == "" {
		return places, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(cursor) > mostFanCursor || json.Unmarshal(raw, &places) != nil {
		return nil, fail(invalidParams, "That is not a cursor this answer gave.")
	}
	return places, nil
}

func encodeFanCursor(places map[string]fanPlace) string {
	for host, p := range places {
		if p.Done {
			// A finished machine is kept: dropping it would ask it again from its start.
			places[host] = fanPlace{Done: true}
		}
	}
	raw, _ := json.Marshal(places)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// fanOut asks each host for the page its place in the cursor names, merges them newest first
// (newer says a is newer than b) and takes limit. A host that does not answer is in Unreached,
// and the pages after leave it out. Cursor is empty when every asked
// host is at its end.
func fanOut[T any](hosts []string, cursor string, limit int, ask func(host, page string) (fanPage[T], bool), newer func(a, b T) bool) (fanAnswer[T], error) {
	places, err := decodeFanCursor(cursor)
	if err != nil {
		return fanAnswer[T]{}, err
	}
	pages, answered := askFanHosts(hosts, places, ask)

	out := fanAnswer[T]{Items: []fanItem[T]{}}
	// The next of each machine's items not yet taken, as an index into its page.
	next := make([]int, len(hosts))
	for i, host := range hosts {
		if places[host].Done {
			continue
		}
		if !answered[i] {
			out.Unreached = append(out.Unreached, host)
			continue
		}
		// A shorter page than before (an item went): nothing to skip past its end.
		next[i] = min(places[host].Took, len(pages[i].Items))
	}
	out.Items = mergeFanPages(out.Items, hosts, pages, answered, next, limit, newer)
	if moveFanPlaces(places, hosts, pages, answered, next) {
		out.Cursor = encodeFanCursor(places)
	}
	return out, nil
}

// askFanHosts asks each host not at its end for its page, all at once, and is the pages and
// which hosts answered, in the order of hosts.
func askFanHosts[T any](hosts []string, places map[string]fanPlace, ask func(host, page string) (fanPage[T], bool)) ([]fanPage[T], []bool) {
	pages := make([]fanPage[T], len(hosts))
	answered := make([]bool, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		if places[host].Done {
			continue
		}
		wg.Go(func() { pages[i], answered[i] = ask(host, places[host].Page) })
	}
	wg.Wait()
	return pages, answered
}

// mergeFanPages adds to items the newest of the pages' next items, one at a time, until
// there are limit items or none is left; next moves past each item taken.
func mergeFanPages[T any](items []fanItem[T], hosts []string, pages []fanPage[T], answered []bool, next []int, limit int, newer func(a, b T) bool) []fanItem[T] {
	for len(items) < limit {
		best := -1
		for i := range hosts {
			if !answered[i] || next[i] >= len(pages[i].Items) {
				continue
			}
			if best < 0 || newer(pages[i].Items[next[i]], pages[best].Items[next[best]]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		items = append(items, fanItem[T]{Host: hosts[best], Item: pages[best].Items[next[best]]})
		next[best]++
	}
	return items
}

// moveFanPlaces sets each asked host's place for the next page, and says whether any host
// is not at its end.
func moveFanPlaces[T any](places map[string]fanPlace, hosts []string, pages []fanPage[T], answered []bool, next []int) bool {
	more := false
	for i, host := range hosts {
		place := places[host]
		if place.Done {
			continue
		}
		if !answered[i] {
			// Said in Unreached on this page; the pages after leave it out, as a machine
			// that is down would else keep the cursor for ever.
			place = fanPlace{Done: true}
		} else {
			switch {
			case next[i] < len(pages[i].Items):
				place.Took = next[i]
			case pages[i].Next != "":
				place = fanPlace{Page: pages[i].Next}
			default:
				place = fanPlace{Done: true}
			}
		}
		places[host] = place
		more = more || !place.Done
	}
	return more
}

// askHost is a request to one machine, as the caller's client, and its answer's result; false
// when the machine is not here, does not answer in fanPatience, or answers with an error.
func (m *Methods) askHost(host, method string, params any, from Caller, into any) bool {
	answer := m.router.DeliverKept(method, params, from.Client, from.Kind, host, fanPatience)
	if answer == nil || len(answer.Error) > 0 {
		return false
	}
	return json.Unmarshal(answer.Result, into) == nil
}

// fanLimit is a client's limit: the default when not said, else bounded to 1 … most.
func fanLimit(said *int64, byDefault, most int) int {
	if said == nil || *said < 1 {
		return byDefault
	}
	return int(min(*said, int64(most)))
}

// fanMethod answers docs/listAll and messages/conversationsAll.
func (m *Methods) fanMethod(ctx context.Context, method string, params json.RawMessage, from Caller) (any, error) {
	user := m.s.actorFor(from.Client).User
	switch method {
	case wire.CallDocsListAll.Name:
		request, err := require[wire.DocsListAllRequest](params, "DocsListAllRequest")
		if err != nil {
			return nil, err
		}
		return m.docsListAll(request, user, from)
	case wire.CallMessagesConversationsAll.Name:
		request, err := require[wire.MessagesConversationsAllRequest](params, "MessagesConversationsAllRequest")
		if err != nil {
			return nil, err
		}
		return m.conversationsAll(request, user, from)
	}
	return nil, notFound(method)
}

const (
	// A host's docs/list takes 1 to 500, and messages/conversations 1 to 100.
	mostDocsPage         = 500
	defaultDocsPage      = 200
	mostConversationPage = 100
	defaultConversations = 20
)

// docsListAll is docs/listAll: each machine's docs/list, merged by when each was updated.
func (m *Methods) docsListAll(request wire.DocsListAllRequest, user string, from Caller) (wire.DocsListAll, error) {
	limit := fanLimit(request.Limit, defaultDocsPage, mostDocsPage)
	hosts, notAsked := m.fanHosts(user, wire.CapDocs1)
	var mu sync.Mutex
	sources := map[string][]wire.DocsSource{}
	got, err := fanOut(hosts, deref(request.Cursor), limit, func(host, page string) (fanPage[wire.Doc], bool) {
		// Each machine is asked for as many as the page has, as the newest may all be its.
		ask := wire.DocsListRequest{Limit: new(int64(limit)), Source: request.Source}
		if page != "" {
			ask.Cursor = &page
		}
		var list wire.DocsList
		if !m.askHost(host, wire.CallDocsList.Name, ask, from, &list) {
			return fanPage[wire.Doc]{}, false
		}
		mu.Lock()
		sources[host] = list.Sources
		mu.Unlock()
		return fanPage[wire.Doc]{Items: list.Docs, Next: deref(list.Cursor)}, true
	}, func(a, b wire.Doc) bool { return docUpdated(a) > docUpdated(b) })
	out := wire.DocsListAll{Docs: []wire.MachineDoc{}, Sources: []wire.MachineDocsSource{}, NotAsked: int64(notAsked)}
	if err != nil {
		return out, err
	}
	for _, item := range got.Items {
		out.Docs = append(out.Docs, wire.MachineDoc{Machine: wire.MachineRef(item.Host), MachineName: m.hostName(item.Host), Doc: item.Item})
	}
	for _, host := range hosts {
		for _, s := range sources[host] {
			out.Sources = append(out.Sources, wire.MachineDocsSource{Machine: wire.MachineRef(host), MachineName: m.hostName(host), Source: s})
		}
	}
	out.Unreached = m.fanProjectMachines(got.Unreached)
	if got.Cursor != "" {
		out.Cursor = &got.Cursor
	}
	return out, nil
}

func docUpdated(d wire.Doc) float64 {
	if d.Updated == nil {
		return 0
	}
	return float64(*d.Updated)
}

// conversationsAll is messages/conversationsAll: each machine's messages/conversations, merged
// by the newest message's time. A connector's own failure is in Errors, with its machine.
func (m *Methods) conversationsAll(request wire.MessagesConversationsAllRequest, user string, from Caller) (wire.MessagesConversationsAll, error) {
	limit := fanLimit(request.Limit, defaultConversations, mostConversationPage)
	hosts, notAsked := m.fanHosts(user, wire.CapMessages1)
	var mu sync.Mutex
	errs := map[string][]wire.MessagesError{}
	got, err := fanOut(hosts, deref(request.Cursor), limit, func(host, page string) (fanPage[wire.MessagesConversation], bool) {
		ask := wire.MessagesConversationsRequest{Limit: new(int64(limit)), Unread: request.Unread}
		if page != "" {
			ask.Cursor = &page
		}
		var list wire.MessagesConversations
		if !m.askHost(host, wire.CallMessagesConversations.Name, ask, from, &list) {
			return fanPage[wire.MessagesConversation]{}, false
		}
		mu.Lock()
		errs[host] = list.Errors
		mu.Unlock()
		return fanPage[wire.MessagesConversation]{Items: list.Conversations, Next: deref(list.Cursor)}, true
	}, func(a, b wire.MessagesConversation) bool {
		return strings.Compare(deref(a.Conversation.LastActivity), deref(b.Conversation.LastActivity)) > 0
	})
	out := wire.MessagesConversationsAll{Conversations: []wire.MachineConversation{}, Errors: []wire.MachineMessagesError{}, NotAsked: int64(notAsked)}
	if err != nil {
		return out, err
	}
	for _, item := range got.Items {
		out.Conversations = append(out.Conversations, wire.MachineConversation{Machine: wire.MachineRef(item.Host), MachineName: m.hostName(item.Host), Conversation: item.Item})
	}
	for _, host := range hosts {
		for _, e := range errs[host] {
			out.Errors = append(out.Errors, wire.MachineMessagesError{Machine: wire.MachineRef(host), MachineName: m.hostName(host), Error: e})
		}
	}
	out.Unreached = m.fanProjectMachines(got.Unreached)
	if got.Cursor != "" {
		out.Cursor = &got.Cursor
	}
	return out, nil
}
