package controlplane

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A window puts an artifact's versions, reads the head and one version by its digest with
// its bytes, and lists them (#5953). The author is the window's person, whatever the call
// says, and a writer that saw an older head makes a second head.
func TestArtifactsPutGetAndVersions(t *testing.T) {
	t.Parallel()
	backend := store.NewMemory()
	p := startPlane(t, backend, controllink.NewPrivateKey())
	_, window := p.client("Window")

	kind := wire.ArtifactKindDocument
	var first wire.ArtifactPutResult
	must(t, json.Unmarshal(call(t, window, "", "artifacts/put", wire.ArtifactPut{Topic: "agent-loops", ID: "plan-5713", Kind: &kind,
		Layers: []wire.ArtifactLayerPut{{MediaType: "text/markdown", Title: "plan.md", Data: []byte("# Plan")}}}), &first))
	if first.Artifact.Ref != "agents:artifact:agent-loops#plan-5713" || first.Artifact.Versions != 1 || first.Artifact.Head != first.Version.Digest ||
		!strings.HasPrefix(first.Version.Author, "person:") || first.Version.Ref != "agents:artifact:agent-loops#"+first.Version.Digest {
		t.Fatalf("first: %+v", first)
	}
	var second wire.ArtifactPutResult
	must(t, json.Unmarshal(call(t, window, "", "artifacts/put", wire.ArtifactPut{Topic: "agent-loops", ID: "plan-5713", Parents: []string{first.Version.Digest},
		Layers: []wire.ArtifactLayerPut{{MediaType: "text/markdown", Title: "plan.md", Data: []byte("# Plan, again")}}}), &second))
	if second.Diverged != nil || second.Artifact.Head != second.Version.Digest || second.Artifact.Versions != 2 {
		t.Fatalf("second: %+v", second)
	}
	var stale wire.ArtifactPutResult
	must(t, json.Unmarshal(call(t, window, "", "artifacts/put", wire.ArtifactPut{Topic: "agent-loops", ID: "plan-5713", Parents: []string{first.Version.Digest},
		Layers: []wire.ArtifactLayerPut{{MediaType: "text/markdown", Title: "plan.md", Data: []byte("# Plan, from an old copy")}}}), &stale))
	if stale.Diverged == nil || !*stale.Diverged || len(stale.Artifact.Heads) != 2 {
		t.Fatalf("stale: %+v", stale)
	}

	var head wire.ArtifactGot
	must(t, json.Unmarshal(call(t, window, "", "artifacts/get", wire.ArtifactGet{Ref: "agents:artifact:agent-loops#plan-5713"}), &head))
	if head.Version.Digest != stale.Version.Digest || len(head.Version.Layers) != 1 || head.Version.Layers[0].Data != nil {
		t.Fatalf("head: %+v", head)
	}
	yes := true
	var one wire.ArtifactGot
	must(t, json.Unmarshal(call(t, window, "", "artifacts/get", wire.ArtifactGet{Ref: first.Version.Ref, Content: &yes}), &one))
	if one.Version.Number != 1 || string(one.Version.Layers[0].Data) != "# Plan" || one.Version.Layers[0].Title == nil || *one.Version.Layers[0].Title != "plan.md" {
		t.Fatalf("version 1: %+v", one)
	}

	limit := int64(2)
	var page wire.ArtifactVersionPage
	must(t, json.Unmarshal(call(t, window, "", "artifacts/versions", wire.ArtifactVersionsAsk{Ref: "agents:artifact:agent-loops#plan-5713", Limit: &limit}), &page))
	if len(page.Versions) != 2 || page.Versions[0].Number != 3 || page.Next == nil {
		t.Fatalf("page: %+v", page)
	}
	var last wire.ArtifactVersionPage
	must(t, json.Unmarshal(call(t, window, "", "artifacts/versions", wire.ArtifactVersionsAsk{Ref: "agents:artifact:agent-loops#plan-5713", Before: page.Next}), &last))
	if len(last.Versions) != 1 || last.Versions[0].Number != 1 || len(last.Versions[0].Parents) != 0 || last.Next != nil {
		t.Fatalf("last page: %+v", last)
	}

	// The records and the bytes are in the organisation's store, under v1/artifacts/.
	keys, err := backend.List(t.Context(), "v1/artifacts/")
	if err != nil || len(keys) == 0 {
		t.Fatalf("%v %v", keys, err)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k.Key, "v1/artifacts/blobs/sha256/") && !strings.HasPrefix(k.Key, "v1/artifacts/by-id/plan-5713/") {
			t.Fatalf("a key out of place: %s", k.Key)
		}
	}

	for _, c := range []struct {
		method string
		params any
		want   string
	}{
		{"artifacts/get", wire.ArtifactGet{Ref: "agents:artifact:other#plan-5713"}, "no such artifact"},
		{"artifacts/get", wire.ArtifactGet{Ref: "plan-5713"}, "is not"},
		{"artifacts/versions", wire.ArtifactVersionsAsk{Ref: first.Version.Ref}, "name ref"},
		{"artifacts/put", wire.ArtifactPut{Topic: "agent-loops", ID: "new", Layers: []wire.ArtifactLayerPut{{MediaType: "text/plain", Title: "a", Data: []byte("a")}}}, "needs a kind"},
	} {
		if got := callError(t, window, "", c.method, c.params); !strings.Contains(got, c.want) {
			t.Errorf("%s %+v: %s", c.method, c.params, got)
		}
	}
}
