package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The handover's driver side (Handover.Link): a handover session with one copy, dialled and
// proved as x:handover-<id> with the copies' key, which only the control plane's key gives.
// `agents-control handover` drives a handover with it by hand, and control/move (#6612) from
// the copy that moves.

// handoverCallPatience is how long one handover call may take.
const handoverCallPatience = 60 * time.Second

// HandoverLink is one handover session with a copy.
type HandoverLink struct {
	mu    sync.Mutex
	lines *controllink.Lines
	next  int
	// delay is added before each call: a test's link between two machines (#7394).
	delay time.Duration
}

// DialHandover opens a handover session with the copy at base, as this control plane.
func DialHandover(ctx context.Context, base, pin string, key []byte) (*HandoverLink, error) {
	public, err := controllink.PublicKey(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 4)
	_, _ = rand.Read(nonce)
	lines, _, err := controllink.Join(ctx, base, pin, controllink.Credentials{
		Identity: "x:" + handoverPrefix + hex.EncodeToString(nonce), Key: controllink.CopyKey(key), Kind: "copy", ControlKey: public})
	if err != nil {
		return nil, err
	}
	return &HandoverLink{lines: lines, next: 1}, nil
}

// Close ends the session.
func (l *HandoverLink) Close() { l.lines.Close() }

// HandoverFailure is a copy's refusal, in its own words.
type HandoverFailure struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (f *HandoverFailure) Error() string { return f.Message }

// Call is one handover call, answered in turn.
func (l *HandoverLink) Call(method string, params any) (json.RawMessage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	time.Sleep(l.delay)
	id := l.next
	l.next++
	if params == nil {
		params = map[string]any{}
	}
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), handoverCallPatience)
	defer cancel()
	if err := l.lines.Write(ctx, string(line)); err != nil {
		return nil, err
	}
	for {
		text, err := l.lines.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("the copy stopped answering: %w", err)
		}
		var m struct {
			ID     *int             `json:"id"`
			Result json.RawMessage  `json:"result"`
			Error  *HandoverFailure `json:"error"`
		}
		if json.Unmarshal([]byte(text), &m) != nil || m.ID == nil || *m.ID != id {
			continue
		}
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	}
}

// Status is handover/status: what the copy says of itself.
func (l *HandoverLink) Status() (wire.HandoverStatus, error) {
	var status wire.HandoverStatus
	reply, err := l.Call("handover/status", nil)
	if err != nil {
		return status, err
	}
	return status, json.Unmarshal(reply, &status)
}

// PeerStore is a copy's store over its handover session, read from a frozen copy and written
// into a receiving one, so CopyStore runs between two machines (#1490).
type PeerStore struct{ Link *HandoverLink }

func (p PeerStore) Get(_ context.Context, key string) (*store.Object, error) {
	reply, err := p.Link.Call("store/get", map[string]string{"key": key})
	if err != nil {
		return nil, err
	}
	var o struct{ Data, ETag string }
	if json.Unmarshal(reply, &o) != nil || o.Data == "" && o.ETag == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(o.Data)
	if err != nil {
		return nil, err
	}
	return &store.Object{Data: data, ETag: o.ETag}, nil
}

func (p PeerStore) Put(_ context.Context, key string, data []byte, when store.Condition) (string, error) {
	if !when.IsAbsent() {
		return "", errors.New("a handover only creates records")
	}
	reply, err := p.Link.Call("store/put", map[string]string{"key": key, "data": base64.StdEncoding.EncodeToString(data)})
	var refused *HandoverFailure
	if errors.As(err, &refused) && refused.Code == wire.FailureChangedElsewhere {
		return "", &store.ConflictError{Key: key}
	}
	if err != nil {
		return "", err
	}
	var tag string
	if err := json.Unmarshal(reply, &tag); err != nil {
		return "", fmt.Errorf("store/put: the tag: %w", err)
	}
	return tag, nil
}

// createAll is store/putMany: a batch of records, created in one call (#7394).
func (p PeerStore) createAll(_ context.Context, batch []copyRecord) error {
	type sent struct {
		Key  string `json:"key"`
		Data string `json:"data"`
	}
	out := make([]sent, len(batch))
	for i, r := range batch {
		out[i] = sent{Key: r.Key, Data: base64.StdEncoding.EncodeToString(r.Data)}
	}
	_, err := p.Link.Call("store/putMany", map[string]any{"records": out})
	var refused *HandoverFailure
	if errors.As(err, &refused) && refused.Code == wire.FailureChangedElsewhere {
		return &store.ConflictError{Key: batch[0].Key}
	}
	return err
}

// sums is store/sums: the SHA-256 of each key's record at the copy, in one call (#7394).
func (p PeerStore) sums(_ context.Context, keys []string) (map[string][sha256.Size]byte, error) {
	reply, err := p.Link.Call("store/sums", map[string]any{"keys": keys})
	if err != nil {
		return nil, err
	}
	var hexes map[string]string
	if err := json.Unmarshal(reply, &hexes); err != nil {
		return nil, fmt.Errorf("store/sums: %w", err)
	}
	out := make(map[string][sha256.Size]byte, len(hexes))
	for key, h := range hexes {
		var sum [sha256.Size]byte
		if b, err := hex.DecodeString(h); err == nil && len(b) == sha256.Size {
			copy(sum[:], b)
			out[key] = sum
		}
	}
	return out, nil
}

func (p PeerStore) Delete(context.Context, string) error {
	return errors.New("a handover never deletes")
}

func (p PeerStore) List(_ context.Context, prefix string) ([]store.Key, error) {
	reply, err := p.Link.Call("store/list", map[string]string{"prefix": prefix})
	if err != nil {
		return nil, err
	}
	var entries []struct{ Key, ETag string }
	if err := json.Unmarshal(reply, &entries); err != nil {
		return nil, err
	}
	keys := make([]store.Key, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, store.Key{Key: e.Key, ETag: e.ETag, Size: -1})
	}
	return keys, nil
}
