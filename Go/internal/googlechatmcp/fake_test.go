package googlechatmcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGoogle is Pub/Sub and the Chat API on this machine, as googlechat-mcp calls them: a
// subscription to pull and acknowledge, the app's spaces, and its messages posted and edited.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	queue  []map[string]any // waiting to be pulled
	acked  []string
	posted []map[string]any
	edited []map[string]any
	nextID int
	refuse int // the status every call gets, when set
	pulled chan struct{}
	spaces []map[string]any
	// history is each space's messages, the oldest first, for spaces.messages.list; nil: the
	// app may not read them (no chat.app.messages.readonly), and the list is refused.
	history map[string][]map[string]any
	listed  []string // each spaces.messages.list's query
}

const fakeSubscription = "projects/agents-fake/subscriptions/agents-chat"

func newFakeGoogle(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{t: t, pulled: make(chan struct{}, 100),
		spaces: []map[string]any{{"name": "spaces/AAA", "displayName": "Team", "spaceType": "SPACE"}, {"name": "spaces/DM1", "spaceType": "DIRECT_MESSAGE"}}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) account() Account {
	return Account{Subscription: fakeSubscription, ChatURL: f.srv.URL + "/", PubSubURL: f.srv.URL + "/"}
}

func (f *fakeGoogle) google() *Google {
	return &Google{Account: func() (Account, error) { return f.account(), nil }}
}

// send queues a Chat event as Pub/Sub gives it.
func (f *fakeGoogle) send(event map[string]any) string {
	data, _ := json.Marshal(event)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("%d", 1000+f.nextID)
	f.queue = append(f.queue, map[string]any{"ackId": "ack-" + id, "message": map[string]any{
		"messageId": id, "publishTime": time.Now().UTC().Format(time.RFC3339Nano), "data": base64.StdEncoding.EncodeToString(data)}})
	return id
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	f.mu.Lock()
	refuse := f.refuse
	f.mu.Unlock()
	if refuse != 0 {
		w.WriteHeader(refuse)
		_, _ = w.Write([]byte(`{"error":{"code":` + fmt.Sprint(refuse) + `,"message":"refused"}}`))
		return
	}
	path := r.URL.Path
	messages := strings.HasPrefix(path, "/v1/spaces/") && strings.HasSuffix(path, "/messages")
	switch {
	case r.Method == http.MethodPost && path == "/v1/"+fakeSubscription+":pull":
		f.servePull(w, r)
	case r.Method == http.MethodPost && path == "/v1/"+fakeSubscription+":acknowledge":
		f.serveAcknowledge(w, in)
	case r.Method == http.MethodGet && path == "/v1/spaces":
		fakeAnswer(w, map[string]any{"spaces": f.spaces})
	case r.Method == http.MethodGet && messages:
		f.serveList(w, r)
	case r.Method == http.MethodPost && messages:
		f.servePost(w, r, in)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/v1/spaces/"):
		f.serveEdit(w, r, in)
	default:
		f.t.Errorf("fake Google: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

// fakeAnswer writes v as the JSON answer.
func fakeAnswer(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// servePull gives the queued events, after a short wait when there are none.
func (f *fakeGoogle) servePull(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	got := f.queue
	f.queue = nil
	f.mu.Unlock()
	if len(got) == 0 {
		select {
		case <-r.Context().Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
	select {
	case f.pulled <- struct{}{}:
	default:
	}
	fakeAnswer(w, map[string]any{"receivedMessages": got})
}

func (f *fakeGoogle) serveAcknowledge(w http.ResponseWriter, in map[string]any) {
	f.mu.Lock()
	for _, id := range in["ackIds"].([]any) {
		f.acked = append(f.acked, id.(string))
	}
	f.mu.Unlock()
	fakeAnswer(w, map[string]any{})
}

// serveList is spaces.messages.list: a page of a space's messages, the newest first, of one
// thread when the filter names it.
func (f *fakeGoogle) serveList(w http.ResponseWriter, r *http.Request) {
	space := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/messages")
	q := r.URL.Query()
	f.mu.Lock()
	f.listed = append(f.listed, r.URL.RawQuery)
	history, allowed := f.history[space]
	allowed = allowed || f.history != nil
	f.mu.Unlock()
	if !allowed {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"This method requires admin approval","status":"PERMISSION_DENIED"}}`))
		return
	}
	if q.Get("orderBy") != "createTime DESC" {
		f.t.Errorf("fake Google: messages listed in order %q", q.Get("orderBy"))
	}
	newest := newestOfThread(history, q.Get("filter"))
	from := 0
	_, _ = fmt.Sscan(q.Get("pageToken"), &from)
	size := 25
	_, _ = fmt.Sscan(q.Get("pageSize"), &size)
	page := map[string]any{"messages": newest[min(from, len(newest)):min(from+size, len(newest))]}
	if from+size < len(newest) {
		page["nextPageToken"] = fmt.Sprint(from + size)
	}
	fakeAnswer(w, page)
}

// newestOfThread is history the newest first, only the thread's when filter names one.
func newestOfThread(history []map[string]any, filter string) []map[string]any {
	newest := []map[string]any{}
	thread := strings.TrimPrefix(filter, "thread.name = ")
	for i := len(history) - 1; i >= 0; i-- {
		if m := history[i]; filter == "" || m["thread"].(map[string]any)["name"] == thread {
			newest = append(newest, m)
		}
	}
	return newest
}

// servePost keeps a posted message and answers with its name and thread.
func (f *fakeGoogle) servePost(w http.ResponseWriter, r *http.Request, in map[string]any) {
	space := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/messages")
	f.mu.Lock()
	f.nextID++
	name := fmt.Sprintf("%s/messages/m%d", space, f.nextID)
	in["_name"], in["_replyOption"] = name, r.URL.Query().Get("messageReplyOption")
	f.posted = append(f.posted, in)
	f.mu.Unlock()
	thread := space + "/threads/t1"
	if th, ok := in["thread"].(map[string]any); ok {
		thread = th["name"].(string)
	}
	fakeAnswer(w, map[string]any{"name": name, "thread": map[string]any{"name": thread}, "createTime": time.Now().UTC().Format(time.RFC3339Nano),
		"sender": map[string]any{"name": "users/app", "type": "BOT"}})
}

func (f *fakeGoogle) serveEdit(w http.ResponseWriter, r *http.Request, in map[string]any) {
	f.mu.Lock()
	in["_name"], in["_mask"] = strings.TrimPrefix(r.URL.Path, "/v1/"), r.URL.Query().Get("updateMask")
	f.edited = append(f.edited, in)
	f.mu.Unlock()
	fakeAnswer(w, in)
}

func (f *fakeGoogle) snapshot() (acked []string, posted, edited []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acked...), append([]map[string]any(nil), f.posted...), append([]map[string]any(nil), f.edited...)
}

// chatEvent is a Chat app's interaction event.
func chatEvent(kind, space, spaceType string, user map[string]any, extra map[string]any) map[string]any {
	e := map[string]any{"type": kind, "eventTime": time.Now().UTC().Format(time.RFC3339Nano),
		"space": map[string]any{"name": space, "spaceType": spaceType, "displayName": "Team"}, "user": user}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

var sam = map[string]any{"name": "users/111", "displayName": "Sam", "email": "Sam@Example.com", "type": "HUMAN"}
