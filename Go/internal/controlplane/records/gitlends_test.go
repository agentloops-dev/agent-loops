package records

import (
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// A machine record that still holds the git lends of #1122 loses them on the next start
// (#7286), and keeps every other field; a record with none is not written.
func TestDropGitLendsDeletesOnlyTheLends(t *testing.T) {
	s := store.NewMemory()
	lent := `{"id":"box","name":"Box","reach":{"dialOut":{}},"platform":"linux","version":"1","installed":false,"rev":3,` +
		`"signInFrom":{"claude":"mac"},"gitFrom":{"github.com":"mac"},"later":7}`
	plain := `{"id":"mac","name":"Mac","reach":{"dialOut":{}},"platform":"macOS arm64","version":"1","installed":false,"rev":1}`
	_, err := s.Put(ctx, HostKey("box"), []byte(lent), store.Absent)
	must(t, err)
	_, err = s.Put(ctx, HostKey("mac"), []byte(plain), store.Absent)
	must(t, err)
	r := New(s)
	must(t, r.Load(ctx))
	if h := r.Host("box"); h == nil || !h.gitLends {
		t.Fatalf("the lends were not read: %+v", h)
	}
	must(t, r.DropGitLends(ctx))
	box, err2 := s.Get(ctx, HostKey("box"))
	must(t, err2)
	var written map[string]any
	must(t, json.Unmarshal(box.Data, &written))
	if _, ok := written["gitFrom"]; ok || written["later"] != 7.0 || written["rev"] != 4.0 ||
		written["signInFrom"].(map[string]any)["claude"] != "mac" {
		t.Fatalf("box as written: %s", box.Data)
	}
	mac, err := s.Get(ctx, HostKey("mac"))
	must(t, err)
	if string(mac.Data) != plain {
		t.Fatalf("a record with no lends was written: %s", mac.Data)
	}
	// Again, and on a fresh read: nothing is left to write.
	must(t, r.DropGitLends(ctx))
	again := New(s)
	must(t, again.Load(ctx))
	if h := again.Host("box"); h == nil || h.gitLends || h.Rev != 4 {
		t.Fatalf("read again: %+v", h)
	}
}
