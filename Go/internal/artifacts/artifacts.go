// Package artifacts is the artifacts' records and versions (#5953, spec 5713 §1–2): each
// version is an OCI image manifest v1.1, stored as a blob, whose layers are the artifact's
// files; the record holds the name, kind, labels and head, and the head moves only by a
// conditional write whose condition is the record the writer read. A writer whose parents
// are not the heads makes a second head: nothing is ever overwritten.
//
// Layout, under the organisation's store (direction.md, "Isolation"):
//
//	<prefix>blobs/sha256/<hex>             bytes, manifests too (OCI image layout's blobs/)
//	<prefix>by-id/<id>/artifact.json       the record
//	<prefix>by-id/<id>/log/<n>.json        one line per version, created once
package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/agentloops-dev/agent-loops/Go/internal/blobstore"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// Prefix is where a control plane keeps its artifacts in its store.
const Prefix = "v1/artifacts/"

// Bounds (data principles: every store is bounded). Retention and thinning are the policy's
// (#5713 child 5); until then a store over a bound refuses the write and says so.
const (
	// MaxArtifacts is the most artifacts one organisation's store keeps.
	MaxArtifacts = 10_000
	// MaxVersions is the most versions one artifact keeps.
	MaxVersions = 10_000
	// MaxLayers is the most files in one version.
	MaxLayers = 64
	// MaxPutBytes is the most bytes of files one put takes, and one get with content sends:
	// a client's message is at most 40 MiB, and base64 is 4/3 of the bytes.
	MaxPutBytes = 24 << 20
	// MaxHeads is the most heads a diverged artifact keeps before a writer must settle them.
	MaxHeads = 8
	// MaxLabels, and their sizes.
	MaxLabels        = 32
	maxLabelKey      = 64
	maxLabelValue    = 256
	maxName          = 200
	maxTitle         = 255
	maxMediaType     = 255
	maxRecordBytes   = 64 << 10
	maxEntryBytes    = 4 << 10
	maxManifestBytes = 256 << 10
	// writeTries is how often a put reads the record again after losing the race to it.
	writeTries = 8
	// DefaultVersionsLimit and MaxVersionsLimit are a page of Versions.
	DefaultVersionsLimit = 50
	MaxVersionsLimit     = 100
)

var (
	// ErrNotFound is an artifact or version that is not there, or not in the ref's topic.
	ErrNotFound = errors.New("artifacts: not found")
	// ErrInvalid is a request that is not well formed.
	ErrInvalid = errors.New("artifacts: invalid")
	// ErrLimit is a write that would take a store past one of its bounds.
	ErrLimit = errors.New("artifacts: over a limit")
)

// The kinds (epic #5713). A version's artifactType is application/vnd.agentloops.<kind>.v1.
var Kinds = []string{"archive", "binary", "build", "document", "image", "other", "package"}

// The captures (spec 5713 §3).
var Captures = []string{"checkpoint", "manual", "settled"}

// Annotations of a version, beside OCI's own.
const (
	annotationParent  = "dev.agentloops.parent"
	annotationAuthor  = "dev.agentloops.author"
	annotationSession = "dev.agentloops.session"
	annotationCapture = "dev.agentloops.capture"
	annotationLabel   = "dev.agentloops.label."
	// annotationArtifact and annotationNumber say whose version a manifest is, and which:
	// a read by digest checks the first and answers the second with no walk of the log.
	annotationArtifact = "dev.agentloops.artifact"
	annotationNumber   = "dev.agentloops.number"
)

const tombstoneType = "application/vnd.agentloops.tombstone.v1"

// KindType is the artifactType of kind.
func KindType(kind string) string { return "application/vnd.agentloops." + kind + ".v1" }

// Record is artifact.json.
type Record struct {
	ID       string             `json:"id"`
	Topic    string             `json:"topic"`
	Name     string             `json:"name"`
	Kind     string             `json:"kind"`
	Labels   map[string]string  `json:"labels,omitempty"`
	Head     blobstore.Digest   `json:"head"`
	Heads    []blobstore.Digest `json:"heads,omitempty"`
	Versions int                `json:"versions"`
	Deleted  bool               `json:"deleted,omitempty"`
	Created  time.Time          `json:"created"`
	Updated  time.Time          `json:"updated"`
	// Last is the newest version's log line, so a line whose write was lost after the record
	// moved is written again by the next put, and read from here until then.
	Last Entry `json:"last"`
}

// Entry is one line of the log: a version, without its layers.
type Entry struct {
	Number    int                `json:"n"`
	Digest    blobstore.Digest   `json:"digest"`
	Parents   []blobstore.Digest `json:"parents,omitempty"`
	Created   time.Time          `json:"created"`
	Author    string             `json:"author"`
	Capture   string             `json:"capture,omitempty"`
	Tombstone bool               `json:"tombstone,omitempty"`
}

// Version is a version with its files.
type Version struct {
	Entry
	Layers []ocispec.Descriptor
}

// Layer is one file to put.
type Layer struct {
	MediaType, Title string
	Data             []byte
}

// Put is a new version.
type Put struct {
	Topic, ID string
	// Name and Kind make the record with the first version; Kind is required then, and a
	// later put may change Name but not Kind.
	Name, Kind string
	// Labels, when not nil, replace the record's.
	Labels  map[string]string
	Parents []blobstore.Digest
	Capture string
	// Author is who made it, recorded by the caller from who called: person:<id>,
	// session:<id> or unknown. Session is the session's id when there is one.
	Author, Session string
	Tombstone       bool
	Layers          []Layer
}

// Store is the artifacts of one organisation.
type Store struct {
	blobs   blobstore.Store
	records store.Store
	prefix  string
	now     func() time.Time
}

// New is the artifacts kept in records under prefix ("" or ending in "/"), with their bytes
// in blobs.
func New(blobs blobstore.Store, records store.Store, prefix string) *Store {
	return &Store{blobs: blobs, records: records, prefix: prefix, now: time.Now}
}

func (s *Store) dir() string { return s.prefix + "by-id/" }

func (s *Store) recordKey(id string) string { return s.dir() + id + "/artifact.json" }

func (s *Store) entryKey(id string, n int) string {
	return fmt.Sprintf("%s%s/log/%010d.json", s.dir(), id, n)
}

// Put makes a new version, and the record with the first one. It answers the record as it
// now is, the version, and whether the artifact has more than one head.
func (s *Store) Put(ctx context.Context, p Put) (Record, Version, bool, error) {
	if err := checkPut(p); err != nil {
		return Record{}, Version{}, false, err
	}
	layers, err := s.putLayers(ctx, p)
	if err != nil {
		return Record{}, Version{}, false, err
	}
	for range writeTries {
		rec, tag, err := s.read(ctx, p.ID)
		if err != nil {
			return Record{}, Version{}, false, err
		}
		if rec != nil {
			if rec.Topic != p.Topic {
				return Record{}, Version{}, false, ErrNotFound
			}
			if p.Kind != "" && p.Kind != rec.Kind {
				return Record{}, Version{}, false, fmt.Errorf("%w: %s is a %s; its kind does not change", ErrInvalid, p.ID, rec.Kind)
			}
			if rec.Versions >= MaxVersions {
				return Record{}, Version{}, false, fmt.Errorf("%w: %s has %d versions, the most an artifact keeps", ErrLimit, p.ID, MaxVersions)
			}
			if err := s.repairLast(ctx, rec); err != nil {
				return Record{}, Version{}, false, err
			}
		} else if err := s.checkNew(ctx, p); err != nil {
			return Record{}, Version{}, false, err
		}
		next, version, diverged, err := s.nextRecord(ctx, rec, p, layers)
		if err != nil {
			return Record{}, Version{}, false, err
		}
		if err := s.writeRecord(ctx, next, tag, rec == nil); err != nil {
			if store.IsConflict(err) {
				// Another writer moved it first: read it again, and make this version again
				// against what is there now (its number is the record's).
				continue
			}
			return Record{}, Version{}, false, err
		}
		// The record is the truth; a log line lost here is written by the next put.
		_ = s.writeEntry(ctx, next.ID, next.Last)
		return next, version, diverged, nil
	}
	return Record{}, Version{}, false, &store.ConflictError{Key: s.recordKey(p.ID)}
}

func (s *Store) checkNew(ctx context.Context, p Put) error {
	if p.Kind == "" {
		return fmt.Errorf("%w: the first version of %s needs a kind", ErrInvalid, p.ID)
	}
	if len(p.Parents) != 0 || p.Tombstone {
		return fmt.Errorf("%w: %s has no versions, so a put names no parent and deletes nothing", ErrNotFound, p.ID)
	}
	ids, err := store.Folders(ctx, s.records, s.dir())
	if err != nil {
		return err
	}
	if len(ids) >= MaxArtifacts {
		return fmt.Errorf("%w: this store has %d artifacts, the most it keeps", ErrLimit, MaxArtifacts)
	}
	return nil
}

// putLayers stores each file's bytes, and OCI's empty config.
func (s *Store) putLayers(ctx context.Context, p Put) ([]ocispec.Descriptor, error) {
	if _, _, err := s.blobs.Put(ctx, ocispec.DescriptorEmptyJSON.Data); err != nil {
		return nil, err
	}
	if p.Tombstone {
		// OCI asks for one layer at least; a tombstone has the empty one.
		return []ocispec.Descriptor{ocispec.DescriptorEmptyJSON}, nil
	}
	out := make([]ocispec.Descriptor, 0, len(p.Layers))
	for _, l := range p.Layers {
		info, _, err := s.blobs.Put(ctx, l.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, ocispec.Descriptor{
			MediaType:   l.MediaType,
			Digest:      digest.Digest(info.Digest),
			Size:        info.Size,
			Annotations: map[string]string{ocispec.AnnotationTitle: l.Title},
		})
	}
	return out, nil
}

// nextRecord makes the version's manifest against rec (nil for a new artifact), stores it,
// and returns the record that points at it.
func (s *Store) nextRecord(ctx context.Context, rec *Record, p Put, layers []ocispec.Descriptor) (Record, Version, bool, error) {
	now := s.now().UTC().Truncate(time.Millisecond)
	var next Record
	if rec == nil {
		next = Record{ID: p.ID, Topic: p.Topic, Name: p.ID, Kind: p.Kind, Created: now}
	} else {
		next = *rec
		next.Labels = maps.Clone(rec.Labels)
		next.Heads = slices.Clone(rec.Heads)
	}
	if p.Name != "" {
		next.Name = p.Name
	}
	if p.Labels != nil {
		next.Labels = maps.Clone(p.Labels)
		if len(next.Labels) == 0 {
			next.Labels = nil
		}
	}
	next.Versions++
	next.Updated = now

	capture := p.Capture
	if capture == "" {
		capture = "manual"
	}
	annotations := map[string]string{
		ocispec.AnnotationCreated: now.Format(time.RFC3339Nano),
		annotationAuthor:          p.Author,
		annotationCapture:         capture,
		annotationArtifact:        p.ID,
		annotationNumber:          strconv.Itoa(next.Versions),
	}
	if len(p.Parents) > 0 {
		annotations[annotationParent] = joinDigests(p.Parents)
	}
	if p.Session != "" {
		annotations[annotationSession] = p.Session
	}
	for k, v := range next.Labels {
		annotations[annotationLabel+k] = v
	}
	artifactType := KindType(next.Kind)
	if p.Tombstone {
		artifactType = tombstoneType
	}
	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: artifactType,
		Config:       ocispec.DescriptorEmptyJSON,
		Layers:       layers,
		Annotations:  annotations,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return Record{}, Version{}, false, err
	}
	if len(data) > maxManifestBytes {
		return Record{}, Version{}, false, fmt.Errorf("%w: the version's manifest is %d bytes; the most is %d", ErrLimit, len(data), maxManifestBytes)
	}
	info, _, err := s.blobs.Put(ctx, data)
	if err != nil {
		return Record{}, Version{}, false, err
	}

	heads, err := moveHeads(rec, p.Parents, info.Digest)
	if err != nil {
		return Record{}, Version{}, false, err
	}
	next.Head = info.Digest
	next.Heads = nil
	if len(heads) > 1 {
		next.Heads = heads
	}
	next.Deleted = p.Tombstone
	entry := Entry{Number: next.Versions, Digest: info.Digest, Parents: slices.Clone(p.Parents), Created: now,
		Author: p.Author, Capture: capture, Tombstone: p.Tombstone}
	next.Last = entry
	return next, Version{Entry: entry, Layers: visibleLayers(layers)}, len(heads) > 1, nil
}

// moveHeads is the heads after a version with these parents: each parent that is a head is
// replaced by it, and a version none of whose parents is a head is one more head (the
// writer saw an older one), so nothing is overwritten.
func moveHeads(rec *Record, parents []blobstore.Digest, made blobstore.Digest) ([]blobstore.Digest, error) {
	if rec == nil {
		return []blobstore.Digest{made}, nil
	}
	heads := rec.Heads
	if len(heads) == 0 {
		heads = []blobstore.Digest{rec.Head}
	}
	out := slices.DeleteFunc(slices.Clone(heads), func(h blobstore.Digest) bool { return slices.Contains(parents, h) })
	out = append(out, made)
	if len(out) > MaxHeads {
		return nil, fmt.Errorf("%w: %s has %d heads; settle them with a version whose parents are its heads", ErrLimit, rec.ID, len(heads))
	}
	return out, nil
}

func (s *Store) read(ctx context.Context, id string) (*Record, string, error) {
	// store-bounds: kind=document; count=10_000 artifacts; size=64KiB a record; age=until-deleted; applied=on write (refused)
	o, err := s.records.Get(ctx, s.recordKey(id))
	if err != nil || o == nil {
		return nil, "", err
	}
	var rec Record
	if err := json.Unmarshal(o.Data, &rec); err != nil {
		return nil, "", fmt.Errorf("artifacts: %s's record is not readable: %w", id, err)
	}
	return &rec, o.ETag, nil
}

func (s *Store) writeRecord(ctx context.Context, rec Record, tag string, isNew bool) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return fmt.Errorf("%w: %s's record would be %d bytes; the most is %d", ErrLimit, rec.ID, len(data), maxRecordBytes)
	}
	when := store.Matching(tag)
	if isNew {
		when = store.Absent
	}
	_, err = s.records.Put(ctx, s.recordKey(rec.ID), data, when)
	return err
}

func (s *Store) writeEntry(ctx context.Context, id string, e Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(data) > maxEntryBytes {
		return fmt.Errorf("%w: a log line of %d bytes", ErrLimit, len(data))
	}
	// store-bounds: kind=document; count=10_000 an artifact; size=4KiB a record; age=until-deleted; applied=on write (refused)
	_, err = s.records.Put(ctx, s.entryKey(id, e.Number), data, store.Absent)
	if store.IsConflict(err) {
		return nil
	}
	return err
}

// repairLast writes the record's newest log line when its write was lost.
func (s *Store) repairLast(ctx context.Context, rec *Record) error {
	if rec.Last.Number == 0 {
		return nil
	}
	o, err := s.records.Get(ctx, s.entryKey(rec.ID, rec.Last.Number))
	if err != nil || o != nil {
		return err
	}
	return s.writeEntry(ctx, rec.ID, rec.Last)
}

// Record is an artifact's record, by its topic and id.
func (s *Store) Record(ctx context.Context, topic, id string) (Record, error) {
	if err := checkTopic(topic); err != nil {
		return Record{}, err
	}
	if err := checkID(id); err != nil {
		return Record{}, err
	}
	rec, _, err := s.read(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if rec == nil || rec.Topic != topic {
		return Record{}, ErrNotFound
	}
	return *rec, nil
}

// Get is the record and the version a stored ref names: the head for a name, the version
// for a digest of one of this artifact's manifests. An id that is a digest names a version
// of any artifact in the topic, whose record the manifest says.
func (s *Store) Get(ctx context.Context, ref Ref) (Record, Version, error) {
	if !ref.Stored() {
		return Record{}, Version{}, fmt.Errorf("%w: %s is referenced, not stored: its bytes are elsewhere", ErrNotFound, ref)
	}
	if d, ok := ref.Digest(); ok {
		manifest, err := s.manifest(ctx, d)
		if err != nil {
			return Record{}, Version{}, err
		}
		rec, err := s.Record(ctx, ref.Topic, manifest.Annotations[annotationArtifact])
		if err != nil {
			return Record{}, Version{}, err
		}
		v, err := versionOf(d, manifest)
		return rec, v, err
	}
	rec, err := s.Record(ctx, ref.Topic, ref.ID)
	if err != nil {
		return Record{}, Version{}, err
	}
	manifest, err := s.manifest(ctx, rec.Head)
	if err != nil {
		return Record{}, Version{}, err
	}
	v, err := versionOf(rec.Head, manifest)
	return rec, v, err
}

// Content is the bytes of each of a version's files, in order, at most MaxPutBytes in all.
func (s *Store) Content(ctx context.Context, v Version) ([][]byte, error) {
	var total int64
	for _, l := range v.Layers {
		total += l.Size
	}
	if total > MaxPutBytes {
		return nil, fmt.Errorf("%w: the version's files are %d bytes; one read sends at most %d", ErrLimit, total, MaxPutBytes)
	}
	out := make([][]byte, 0, len(v.Layers))
	for _, l := range v.Layers {
		data, err := s.blobs.Get(ctx, blobstore.Digest(l.Digest))
		if err != nil {
			return nil, err
		}
		out = append(out, data)
	}
	return out, nil
}

// Versions is a page of an artifact's log, newest first: those numbered below before (0 is
// from the newest), at most limit (0 is DefaultVersionsLimit). next is the before of the next
// page, 0 on the last.
func (s *Store) Versions(ctx context.Context, topic, id string, before, limit int) ([]Entry, int, error) {
	rec, err := s.Record(ctx, topic, id)
	if err != nil {
		return nil, 0, err
	}
	switch {
	case limit <= 0:
		limit = DefaultVersionsLimit
	case limit > MaxVersionsLimit:
		limit = MaxVersionsLimit
	}
	top := rec.Versions
	if before > 0 && before-1 < top {
		top = before - 1
	}
	var out []Entry
	n := top
	for ; n >= 1 && len(out) < limit; n-- {
		if n == rec.Last.Number {
			out = append(out, rec.Last)
			continue
		}
		o, err := s.records.Get(ctx, s.entryKey(id, n))
		if err != nil {
			return nil, 0, err
		}
		if o == nil {
			// Not written, and not the newest: a lost line from before the repair. Skip it;
			// the version is still in the store by its digest.
			continue
		}
		var e Entry
		if err := json.Unmarshal(o.Data, &e); err != nil {
			return nil, 0, fmt.Errorf("artifacts: %s's log line %d is not readable: %w", id, n, err)
		}
		out = append(out, e)
	}
	next := 0
	if n >= 1 {
		next = n + 1
	}
	return out, next, nil
}

func (s *Store) manifest(ctx context.Context, d blobstore.Digest) (ocispec.Manifest, error) {
	data, err := s.blobs.Get(ctx, d)
	if errors.Is(err, blobstore.ErrNotFound) {
		return ocispec.Manifest{}, ErrNotFound
	}
	if err != nil {
		return ocispec.Manifest{}, err
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(data, &m); err != nil || m.MediaType != ocispec.MediaTypeImageManifest || m.Annotations[annotationArtifact] == "" {
		// A blob that is not one of our manifests is not a version.
		return ocispec.Manifest{}, ErrNotFound
	}
	return m, nil
}

func versionOf(d blobstore.Digest, m ocispec.Manifest) (Version, error) {
	a := m.Annotations
	n, err := strconv.Atoi(a[annotationNumber])
	if err != nil || n < 1 {
		return Version{}, fmt.Errorf("artifacts: the manifest %s has no number", d)
	}
	created, _ := time.Parse(time.RFC3339Nano, a[ocispec.AnnotationCreated])
	var parents []blobstore.Digest
	for _, p := range strings.Fields(a[annotationParent]) {
		parents = append(parents, blobstore.Digest(p))
	}
	return Version{
		Entry: Entry{Number: n, Digest: d, Parents: parents, Created: created, Author: a[annotationAuthor],
			Capture: a[annotationCapture], Tombstone: m.ArtifactType == tombstoneType},
		Layers: visibleLayers(m.Layers),
	}, nil
}

// visibleLayers leaves out OCI's empty layer, a tombstone's.
func visibleLayers(layers []ocispec.Descriptor) []ocispec.Descriptor {
	return slices.DeleteFunc(slices.Clone(layers), func(l ocispec.Descriptor) bool {
		return l.MediaType == ocispec.MediaTypeEmptyJSON
	})
}

func joinDigests(ds []blobstore.Digest) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = string(d)
	}
	return strings.Join(parts, " ")
}

var (
	labelKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	authorPattern   = regexp.MustCompile(`^((person|session):[^\s]{1,128}|unknown)$`)
)

func checkPut(p Put) error {
	if err := checkTopic(p.Topic); err != nil {
		return err
	}
	if err := checkID(p.ID); err != nil {
		return err
	}
	if p.Kind != "" && !slices.Contains(Kinds, p.Kind) {
		return fmt.Errorf("%w: %q is not a kind: %s", ErrInvalid, p.Kind, strings.Join(Kinds, ", "))
	}
	if p.Capture != "" && !slices.Contains(Captures, p.Capture) {
		return fmt.Errorf("%w: %q is not a capture: %s", ErrInvalid, p.Capture, strings.Join(Captures, ", "))
	}
	if len(p.Name) > maxName || strings.ContainsAny(p.Name, "\n\r") {
		return fmt.Errorf("%w: a name is one line of at most %d bytes", ErrInvalid, maxName)
	}
	if !authorPattern.MatchString(p.Author) {
		return fmt.Errorf("%w: an author is person:<id>, session:<id> or unknown: %q", ErrInvalid, p.Author)
	}
	if err := checkLabels(p.Labels); err != nil {
		return err
	}
	if len(p.Parents) > MaxHeads {
		return fmt.Errorf("%w: at most %d parents", ErrInvalid, MaxHeads)
	}
	for _, d := range p.Parents {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("%w: a parent: %v", ErrInvalid, err)
		}
	}
	if p.Tombstone {
		if len(p.Layers) > 0 {
			return fmt.Errorf("%w: a tombstone has no files", ErrInvalid)
		}
		return nil
	}
	return checkLayers(p.Layers)
}

func checkLabels(labels map[string]string) error {
	if len(labels) > MaxLabels {
		return fmt.Errorf("%w: at most %d labels", ErrLimit, MaxLabels)
	}
	for k, v := range labels {
		if len(k) > maxLabelKey || !labelKeyPattern.MatchString(k) || len(v) > maxLabelValue {
			return fmt.Errorf("%w: a label's key is up to %d of a-z, 0-9, '.', '_' and '-', and its value up to %d bytes: %q", ErrInvalid, maxLabelKey, maxLabelValue, k)
		}
	}
	return nil
}

func checkLayers(layers []Layer) error {
	if len(layers) == 0 || len(layers) > MaxLayers {
		return fmt.Errorf("%w: a version has 1 to %d files", ErrInvalid, MaxLayers)
	}
	var total int
	titles := map[string]bool{}
	for _, l := range layers {
		total += len(l.Data)
		if l.Title == "" || len(l.Title) > maxTitle || strings.ContainsAny(l.Title, "/\\\x00") || l.Title == "." || l.Title == ".." {
			return fmt.Errorf("%w: a file's title is a name of 1 to %d bytes with no '/': %q", ErrInvalid, maxTitle, l.Title)
		}
		if titles[l.Title] {
			return fmt.Errorf("%w: two files are named %q", ErrInvalid, l.Title)
		}
		titles[l.Title] = true
		if len(l.MediaType) > maxMediaType || !strings.Contains(l.MediaType, "/") {
			return fmt.Errorf("%w: %q is not a media type", ErrInvalid, l.MediaType)
		}
	}
	if total > MaxPutBytes {
		return fmt.Errorf("%w: the files are %d bytes; one put takes at most %d", ErrLimit, total, MaxPutBytes)
	}
	return nil
}
