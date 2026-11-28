package artifacts

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/blobstore"
)

// System is the ref system of a stored artifact (spec 5713 §2): its bytes are in our store.
// Any other system (oci, testflight, github) is a referenced artifact, whose bytes are not.
const System = "agents"

// Ref is <system>:artifact:<topic>#<id>. For a stored artifact the id is the artifact's id,
// a name that moves with its head, or a version's "sha256:<manifest digest>", which does not.
type Ref struct {
	System, Topic, ID string
}

var (
	// An artifact's id, and a topic of a stored one: a short name that is safe as a key part.
	idPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	topicPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	// A system is a lower-case word.
	systemPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// ParseRef reads a ref. A stored ref's topic and id are checked; another system's are kept
// as they are, with no '#' in the topic.
func ParseRef(s string) (Ref, error) {
	system, rest, ok := strings.Cut(s, ":artifact:")
	if !ok || !systemPattern.MatchString(system) {
		return Ref{}, fmt.Errorf("%w: %q is not <system>:artifact:<topic>#<id>", ErrInvalid, s)
	}
	topic, id, ok := strings.Cut(rest, "#")
	if !ok || topic == "" || id == "" {
		return Ref{}, fmt.Errorf("%w: %q is not <system>:artifact:<topic>#<id>", ErrInvalid, s)
	}
	r := Ref{System: system, Topic: topic, ID: id}
	if r.Stored() {
		if err := checkTopic(topic); err != nil {
			return Ref{}, err
		}
		if _, isDigest := r.Digest(); !isDigest {
			if err := checkID(id); err != nil {
				return Ref{}, err
			}
		}
	}
	return r, nil
}

// String is the ref as text.
func (r Ref) String() string { return r.System + ":artifact:" + r.Topic + "#" + r.ID }

// Stored reports whether the ref's bytes are ours.
func (r Ref) Stored() bool { return r.System == System }

// Digest is the version the ref names, when its id is a digest.
func (r Ref) Digest() (blobstore.Digest, bool) {
	d := blobstore.Digest(r.ID)
	return d, strings.HasPrefix(r.ID, "sha256:") && d.Valid()
}

// NameRef is the moving ref of an artifact.
func NameRef(topic, id string) Ref { return Ref{System: System, Topic: topic, ID: id} }

// VersionRef is the immutable ref of one version.
func VersionRef(topic string, d blobstore.Digest) Ref {
	return Ref{System: System, Topic: topic, ID: string(d)}
}

func checkID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: an artifact's id is 1 to 64 of a-z, 0-9, '.', '_' and '-', starting with a letter or digit: %q", ErrInvalid, id)
	}
	return nil
}

func checkTopic(topic string) error {
	if !topicPattern.MatchString(topic) {
		return fmt.Errorf("%w: a topic is a project's or the organisation's id: %q", ErrInvalid, topic)
	}
	return nil
}
