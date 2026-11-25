package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A person's approvals (#5462, epic #5281): what a machine approved, signed by that
// machine, kept at v1/approvals/<person>/<kind>-<digest>.json. The control plane keeps and
// sends them and signs nothing: a machine takes one only after it checks the signature
// against its own pinned machine lock, so this store cannot make an approval up. The first
// approval of a kind and digest is kept; a later one is dropped. An approval is current state:
// it has no age, as one that aged out would make the person approve the same file again, and
// it goes with its person (#6970).

const (
	// store-bounds: kind=document; count=2_000 a person; size=8KiB a record; age=until-deleted; applied=on write (refused), on person removal and hourly sweep
	approvalsPrefix     = "v1/approvals/"
	approvalsPutMost    = 50
	approvalsKeptMost   = 2000
	approvalRecordBytes = 8 << 10
	approvalFieldMost   = 256
	approvalSignedBytes = 256
)

var approvalDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func approvalsOf(person string) string {
	sum := sha256.Sum256([]byte(person))
	return approvalsPrefix + hex.EncodeToString(sum[:16]) + "/"
}

func approvalKey(person string, a wire.PersonApproval) string {
	return approvalsOf(person) + a.Kind + "-" + a.Digest + ".json"
}

func checkPersonApproval(a wire.PersonApproval) error {
	switch {
	case a.Kind != "workflow" && a.Kind != "connector":
		return fail(invalidParams, "An approval is of a workflow or a connector.")
	case !approvalDigest.MatchString(a.Digest):
		return fail(invalidParams, "An approval names a file by its SHA-256 digest.")
	case a.Host == "" || len(a.Host) > approvalFieldMost || len(deref(a.HostName)) > approvalFieldMost || len(deref(a.Name)) > approvalFieldMost:
		return fail(invalidParams, "An approval names the machine, and a name is at most %d characters.", approvalFieldMost)
	case len(a.Key) == 0 || len(a.Key) > approvalSignedBytes || len(a.Sig) == 0 || len(a.Sig) > approvalSignedBytes:
		return fail(invalidParams, "An approval is signed.")
	}
	return nil
}

// listApprovals is approvals/list: the person's, oldest key first.
func (s *Service) listApprovals(ctx context.Context, person string) (wire.PersonApprovals, error) {
	out := wire.PersonApprovals{Approvals: []wire.PersonApproval{}}
	keys, err := s.cfg.Store.List(ctx, approvalsOf(person))
	if err != nil {
		return out, fail(storeUnavailable, "The approvals could not be read: %v", err)
	}
	for _, k := range keys[:min(len(keys), approvalsKeptMost)] {
		o, err := s.cfg.Store.Get(ctx, k.Key)
		if err != nil {
			return out, fail(storeUnavailable, "The approvals could not be read: %v", err)
		}
		var a wire.PersonApproval
		if o == nil || json.Unmarshal(o.Data, &a) != nil || checkPersonApproval(a) != nil || !strings.HasSuffix(k.Key, a.Kind+"-"+a.Digest+".json") {
			continue
		}
		out.Approvals = append(out.Approvals, a)
	}
	return out, nil
}

// putApprovals is approvals/put: each new kind and digest is kept, for the machine's person.
func (s *Service) putApprovals(ctx context.Context, host string, p wire.PersonApprovalsPut) (wire.PersonApprovals, error) {
	if s.records.Host(host) == nil {
		return wire.PersonApprovals{}, fail(noSuchHost, "No machine is called %s.", host)
	}
	if len(p.Approvals) > approvalsPutMost {
		return wire.PersonApprovals{}, fail(invalidParams, "Send at most %d approvals at a time.", approvalsPutMost)
	}
	person := s.ownerOf(host)
	fresh := map[string][]byte{}
	for _, a := range p.Approvals {
		if err := checkPersonApproval(a); err != nil {
			return wire.PersonApprovals{}, err
		}
		key := approvalKey(person, a)
		if _, again := fresh[key]; again {
			continue
		}
		data, _ := json.Marshal(a)
		if len(data) > approvalRecordBytes {
			return wire.PersonApprovals{}, fail(invalidParams, "An approval is at most %d bytes.", approvalRecordBytes)
		}
		fresh[key] = data
	}
	keys, err := s.cfg.Store.List(ctx, approvalsOf(person))
	if err != nil {
		return wire.PersonApprovals{}, fail(storeUnavailable, "The approvals could not be written: %v", err)
	}
	for _, k := range keys {
		delete(fresh, k.Key)
	}
	// A person's approval is never deleted to make room: the machine that approved still runs
	// the file, and the person's other machines ask.
	if len(keys)+len(fresh) > approvalsKeptMost && len(fresh) > 0 {
		return wire.PersonApprovals{}, fail(invalidParams,
			"A person keeps at most %d approvals, so this one was not shared. It holds on this machine; the others ask.", approvalsKeptMost)
	}
	for key, data := range fresh {
		if _, err := s.cfg.Store.Put(ctx, key, data, store.Absent); err != nil && !store.IsConflict(err) {
			return wire.PersonApprovals{}, fail(storeUnavailable, "The approvals could not be written: %v", err)
		}
	}
	return s.listApprovals(ctx, person)
}

// forgetApprovals deletes a removed person's approvals (#6970).
func forgetApprovals(ctx context.Context, s store.Store, person string) error {
	_, err := store.DeleteAll(ctx, store.NewPrefixed(s, approvalsOf(person)))
	return err
}

// sweepApprovalsOfGone deletes, at most most, the approvals of people who are gone (#6970):
// those removed before their approvals went with them, and one a machine sent as its person
// was removed. A folder names its person only by a hash, so it stays when it is the hash of
// someone who is here. full says it stopped at most.
func sweepApprovalsOfGone(ctx context.Context, rs *records.Records, _ time.Time, most int) (deleted int, full bool, err error) {
	s := rs.Store
	keys, err := s.List(ctx, approvalsPrefix)
	if err != nil || len(keys) == 0 {
		return 0, false, err
	}
	here := map[string]bool{}
	add := func(person string) { here[approvalsOf(person)] = true }
	add("")
	add("local")
	if settings := rs.Settings(); settings != nil && settings.Owner != nil {
		add(*settings.Owner)
	}
	// The store's list has the people another copy made too, which this one has not read yet.
	listed, err := s.List(ctx, records.PeoplePrefix)
	if err != nil {
		return 0, false, err
	}
	for _, k := range listed {
		person := strings.TrimSuffix(strings.TrimPrefix(k.Key, records.PeoplePrefix), ".json")
		gone, err := personGone(ctx, rs, person)
		if err != nil {
			return 0, false, err
		}
		if !gone {
			add(person)
		}
	}
	for _, p := range rs.People() {
		add(p.ID)
	}
	for _, k := range keys {
		if i := strings.LastIndex(k.Key, "/"); here[k.Key[:i+1]] {
			continue
		}
		if deleted >= most {
			return deleted, true, nil
		}
		if err := s.Delete(ctx, k.Key); err != nil {
			return deleted, false, err
		}
		deleted++
	}
	return deleted, false, nil
}
