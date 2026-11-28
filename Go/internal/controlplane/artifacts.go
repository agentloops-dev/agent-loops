package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/agentloops-dev/agent-loops/Go/internal/artifacts"
	"github.com/agentloops-dev/agent-loops/Go/internal/blobstore"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// artifactsMethod answers artifacts/put, artifacts/get and artifacts/versions (#5953, spec
// 5713 §1–2). Any person of the control plane may put and read an artifact; who may see a
// project's artifacts, and the policy on them, are the policy tasks' (#5713 children 5, 9).
// The author of a put is the caller's person, recorded, never what the call says.
func (m *Methods) artifactsMethod(ctx context.Context, method string, params json.RawMessage, from Caller) (any, error) {
	person := m.callerPerson(from)
	if person == "" {
		return nil, fail(notPermitted, "Only a person's app reads and writes artifacts.")
	}
	switch method {
	case "artifacts/put":
		return m.putArtifact(ctx, params, person)
	case "artifacts/get":
		return m.getArtifact(ctx, params)
	case "artifacts/versions":
		return m.artifactVersions(ctx, params)
	}
	return nil, notFound(method)
}

func (m *Methods) putArtifact(ctx context.Context, params json.RawMessage, person string) (any, error) {
	request, err := require[wire.ArtifactPut](params, "ArtifactPut")
	if err != nil {
		return nil, err
	}
	put := artifactPut(request)
	put.Author = "person:" + person
	rec, v, diverged, err := m.artifacts.Put(ctx, put)
	if err != nil {
		return nil, artifactFailure(err)
	}
	out := wire.ArtifactPutResult{Artifact: artifactWire(rec), Version: versionWire(rec.Topic, v.Entry, v.Layers, nil)}
	if diverged {
		out.Diverged = &diverged
	}
	return out, nil
}

func (m *Methods) getArtifact(ctx context.Context, params json.RawMessage) (any, error) {
	request, err := require[wire.ArtifactGet](params, "ArtifactGet")
	if err != nil {
		return nil, err
	}
	ref, err := artifacts.ParseRef(request.Ref)
	if err != nil {
		return nil, artifactFailure(err)
	}
	rec, v, err := m.artifacts.Get(ctx, ref)
	if err != nil {
		return nil, artifactFailure(err)
	}
	var content [][]byte
	if deref(request.Content) {
		if content, err = m.artifacts.Content(ctx, v); err != nil {
			return nil, artifactFailure(err)
		}
	}
	return wire.ArtifactGot{Artifact: artifactWire(rec), Version: versionWire(rec.Topic, v.Entry, v.Layers, content)}, nil
}

func (m *Methods) artifactVersions(ctx context.Context, params json.RawMessage) (any, error) {
	request, err := require[wire.ArtifactVersionsAsk](params, "ArtifactVersionsAsk")
	if err != nil {
		return nil, err
	}
	ref, err := artifacts.ParseRef(request.Ref)
	if err != nil {
		return nil, artifactFailure(err)
	}
	if _, isDigest := ref.Digest(); isDigest || !ref.Stored() {
		return nil, fail(invalidParams, "Name an artifact by its name ref, agents:artifact:<topic>#<id>.")
	}
	entries, next, err := m.artifacts.Versions(ctx, ref.Topic, ref.ID, int(deref(request.Before)), int(deref(request.Limit)))
	if err != nil {
		return nil, artifactFailure(err)
	}
	out := wire.ArtifactVersionPage{Versions: make([]wire.ArtifactVersion, 0, len(entries))}
	for _, e := range entries {
		out.Versions = append(out.Versions, versionWire(ref.Topic, e, nil, nil))
	}
	if next > 0 {
		n := int64(next)
		out.Next = &n
	}
	return out, nil
}

func artifactPut(r wire.ArtifactPut) artifacts.Put {
	p := artifacts.Put{Topic: r.Topic, ID: r.ID, Name: deref(r.Name), Kind: string(deref(r.Kind)), Labels: r.Labels,
		Capture: string(deref(r.Capture)), Tombstone: deref(r.Tombstone)}
	for _, d := range r.Parents {
		p.Parents = append(p.Parents, blobstore.Digest(d))
	}
	for _, l := range r.Layers {
		p.Layers = append(p.Layers, artifacts.Layer{MediaType: l.MediaType, Title: l.Title, Data: l.Data})
	}
	return p
}

// artifactFailure is what a client is told: a bad request, a limit or a missing artifact in
// words; a store's own errors as asRPC says them.
func artifactFailure(err error) error {
	switch {
	case errors.Is(err, artifacts.ErrNotFound):
		return fail(invalidParams, "There is no such artifact or version.")
	case errors.Is(err, artifacts.ErrInvalid), errors.Is(err, artifacts.ErrLimit):
		return fail(invalidParams, "%s", strings.TrimPrefix(strings.TrimPrefix(err.Error(), "artifacts: invalid: "), "artifacts: over a limit: "))
	}
	return err
}

func artifactWire(r artifacts.Record) wire.Artifact {
	out := wire.Artifact{ID: r.ID, Topic: r.Topic, Ref: artifacts.NameRef(r.Topic, r.ID).String(), Name: r.Name,
		Kind: wire.ArtifactKind(r.Kind), Labels: r.Labels, Head: string(r.Head), Versions: int64(r.Versions),
		Created: r.Created.UTC().Format(time.RFC3339Nano), Updated: r.Updated.UTC().Format(time.RFC3339Nano)}
	for _, h := range r.Heads {
		out.Heads = append(out.Heads, string(h))
	}
	if r.Deleted {
		out.Deleted = &r.Deleted
	}
	return out
}

func versionWire(topic string, e artifacts.Entry, layers []ocispec.Descriptor, content [][]byte) wire.ArtifactVersion {
	out := wire.ArtifactVersion{Number: int64(e.Number), Digest: string(e.Digest), Ref: artifacts.VersionRef(topic, e.Digest).String(),
		Created: e.Created.UTC().Format(time.RFC3339Nano), Author: e.Author}
	for _, p := range e.Parents {
		out.Parents = append(out.Parents, string(p))
	}
	if e.Capture != "" {
		out.Capture = &e.Capture
	}
	if e.Tombstone {
		out.Tombstone = &e.Tombstone
	}
	for i, l := range layers {
		layer := wire.ArtifactLayer{MediaType: l.MediaType, Digest: l.Digest.String(), Size: l.Size}
		if title := l.Annotations[ocispec.AnnotationTitle]; title != "" {
			layer.Title = &title
		}
		if i < len(content) {
			layer.Data = content[i]
		}
		out.Layers = append(out.Layers, layer)
	}
	return out
}
