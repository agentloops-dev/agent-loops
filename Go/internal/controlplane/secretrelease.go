package controlplane

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/kms"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/internal/secretrelease"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The release key of a Linux machine (#4058, the spike #4013): the machine seals each value
// with Ask before use to its own key and to this key, and only the control plane keeps the
// private half, sealed by the secrets keeper. It opens one value's layer only for an
// approval that a client of the machine's person's lock signed to that machine, in the last
// ten minutes, once; and it gives back only the key for that layer. It never has the
// machine's key, so it never opens the value (Principle 6).
//
//	v1/release-keys/<host>.json           the private half, sealed (kms.Sealed)
//	v1/release-keys/<host>/seen/<id>.json an approval taken, by machinelock.SignedID

// store-bounds: kind=secret; size=1MiB (4KiB a key); age=until-deleted; applied=on write, a new key is refused, a key goes with its machine
const releaseKeysPrefix = "v1/release-keys/"

// store-bounds: kind=key-value; count=1000 a machine; size=64B a mark; age=1d; applied=on write, a release is refused, on release and hourly, after twice an approval's life
const (
	// releaseKeyMostBytes is the largest key as sealed, releaseKeysMostBytes all of them (#6669,
	// gate 1). Only a new key is refused: one that is kept always opens.
	releaseKeyMostBytes  = 4 << 10
	releaseKeysMostBytes = 1 << 20
	// releaseSeenMost is how many approvals a machine has taken at once. None is evicted: a mark
	// that went early would let its approval be used again.
	releaseSeenMost = 1000
)

var releaseHost = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func releaseKeyKey(host string) string     { return releaseKeysPrefix + host + ".json" }
func releaseSeenPrefix(host string) string { return releaseKeysPrefix + host + "/seen/" }

type releaseSeen struct {
	At int64 `json:"at"`
}

func (m *Methods) secretReleaseMethod(ctx context.Context, host, method string, params json.RawMessage) (any, error) {
	if !releaseHost.MatchString(host) || m.s.records.Host(host) == nil {
		return nil, fail(notPermitted, "This machine is not joined.")
	}
	if !m.s.users.Sealing() {
		return nil, fail(notSupported, "This control plane has no key to keep a release key with, so a Linux machine of it cannot keep a secret with Ask before use.")
	}
	switch method {
	case wire.CallMachineSecretKey.Name:
		private, err := m.s.releaseKey(ctx, host, true)
		if err != nil {
			return nil, err
		}
		return wire.SecretReleaseKey{PublicKey: private.PublicKey().Bytes()}, nil
	case wire.CallMachineSecretRelease.Name:
		request, err := require[wire.SecretRelease](params, "SecretRelease")
		if err != nil {
			return nil, err
		}
		key, err := m.s.releaseSecret(ctx, host, request, time.Now())
		if err != nil {
			return nil, err
		}
		return wire.SecretReleased{Key: key}, nil
	}
	return nil, fail(methodNotFound, "No method %s.", method)
}

// releaseKey is a machine's release key: made, sealed and kept the first time create is true.
func (s *Service) releaseKey(ctx context.Context, host string, create bool) (*ecdh.PrivateKey, error) {
	at := releaseKeyKey(host)
	for range 3 {
		o, err := s.cfg.Store.Get(ctx, at)
		if err != nil {
			return nil, err
		}
		if o != nil {
			var sealed kms.Sealed
			if err := json.Unmarshal(o.Data, &sealed); err != nil {
				return nil, errors.New("the release key does not read")
			}
			raw, err := s.users.OpenFor(ctx, &sealed, []byte(at))
			if err != nil {
				return nil, err
			}
			defer clear(raw)
			return ecdh.X25519().NewPrivateKey(raw)
		}
		if !create {
			return nil, fail(invalidParams, "This machine has no release key, so nothing of it was sealed to one.")
		}
		private, err := secretrelease.NewKey()
		if err != nil {
			return nil, err
		}
		sealed, err := s.users.SealFor(ctx, private.Bytes(), []byte(at))
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(sealed)
		if err != nil {
			return nil, err
		}
		if err := s.releaseKeyRoom(ctx, b); err != nil {
			return nil, err
		}
		if _, err := s.cfg.Store.Put(ctx, at, b, store.Absent); err != nil {
			if store.IsConflict(err) {
				// Another copy made it first: read that one.
				continue
			}
			return nil, err
		}
		s.log("made a release key for " + host)
		return private, nil
	}
	return nil, errors.New("the release key changed elsewhere; ask again")
}

// releaseKeyRoom refuses a new release key that the store has no room for.
func (s *Service) releaseKeyRoom(ctx context.Context, sealed []byte) error {
	if err := fitsRecord("The release key", sealed, releaseKeyMostBytes); err != nil {
		return err
	}
	keys, err := s.cfg.Store.List(ctx, releaseKeysPrefix)
	if err != nil {
		return err
	}
	all := int64(len(sealed))
	for _, k := range keys {
		if strings.Contains(k.Key, "/seen/") {
			continue
		}
		size := k.Size
		if size < 0 { // the listing does not say
			size = releaseKeyMostBytes
		}
		all += size
	}
	if all > releaseKeysMostBytes {
		return fail(notPermitted, "There is no room for one more machine's release key. Remove a machine first.")
	}
	return nil
}

// releaseSecret checks the person's signed approval and gives the key for one value's layer.
func (s *Service) releaseSecret(ctx context.Context, host string, r wire.SecretRelease, now time.Time) ([]byte, error) {
	refuse := func(why string) error {
		s.log("refused a release for " + host + ": " + why)
		return fail(notPermitted, "Nothing was released: %s.", why)
	}
	person := s.ownerOf(host)
	if person == "" || s.locks == nil {
		return nil, refuse("this machine has no person")
	}
	lock, err := s.locks.Get(ctx, person)
	if err != nil {
		return nil, err
	}
	if len(lock.Entries) == 0 {
		return nil, refuse("its person has no lock, so no device can sign an approval")
	}
	state, err := machinelock.Check(lock.Entries)
	if err != nil {
		return nil, refuse("its person's lock does not check")
	}
	a, err := secretrelease.CheckApproval(state, host, r.Approval, now)
	if err != nil {
		return nil, refuse(strings.TrimSuffix(err.Error(), "."))
	}
	if a.Name != r.Name || a.Action == "deny" || a.Action == "" {
		return nil, refuse("the approval is for another use")
	}
	private, err := s.releaseKey(ctx, host, false)
	if err != nil {
		return nil, err
	}
	if n, err := s.forgetOldReleases(ctx, host, now); err != nil {
		return nil, err
	} else if n >= releaseSeenMost {
		return nil, refuse("this machine was given too many in the last minutes; try again in 20 minutes")
	}
	// Taken once: a second write of the same signature finds it there.
	seen, _ := json.Marshal(releaseSeen{At: r.Approval.Lock.At})
	if _, err := s.cfg.Store.Put(ctx, releaseSeenPrefix(host)+machinelock.SignedID(r.Approval.Lock)+".json", seen, store.Absent); err != nil {
		if store.IsConflict(err) {
			return nil, refuse("that approval was used before")
		}
		return nil, err
	}
	key, err := secretrelease.Release(private, r.Name, r.Encapsulated)
	if err != nil {
		return nil, fail(invalidParams, "%s", err.Error())
	}
	s.log("released " + r.Name + " for " + host + " (" + a.Use + " at " + a.Place + ")")
	return key, nil
}

// forgetOldReleases bounds what a machine's seen approvals keep: one past its lifetime is
// refused as stale anyway. It says how many are left.
func (s *Service) forgetOldReleases(ctx context.Context, host string, now time.Time) (int, error) {
	keys, err := s.cfg.Store.List(ctx, releaseSeenPrefix(host))
	if err != nil {
		return 0, err
	}
	left := len(keys)
	oldest := now.Add(-2 * secretrelease.ApprovalLifetime).Unix()
	for _, k := range keys {
		o, err := s.cfg.Store.Get(ctx, k.Key)
		if err != nil || o == nil {
			continue
		}
		var seen releaseSeen
		if json.Unmarshal(o.Data, &seen) == nil && seen.At < oldest {
			// A key left is deleted by the next sweep.
			_ = s.cfg.Store.Delete(ctx, k.Key)
			left--
		}
	}
	return left, nil
}

// forgetReleaseHost removes the secret and its replay markers when a machine leaves.
// The control-plane Store is the key-value adapter for this family (#6670).
func (s *Service) forgetReleaseHost(ctx context.Context, host string) error {
	keys, err := s.cfg.Store.List(ctx, releaseSeenPrefix(host))
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := s.cfg.Store.Delete(ctx, key.Key); err != nil {
			return err
		}
	}
	return s.cfg.Store.Delete(ctx, releaseKeyKey(host))
}

// sweepReleaseKeys removes expired replay markers and any release material left for a
// machine that is no longer joined. The key-value listing is bounded by the release-key
// collection's 1 MB organisation cap (#6669, gate 1).
func sweepReleaseKeys(ctx context.Context, st store.Store, rs *records.Records, now time.Time, most int) (int, error) {
	keys, err := st.List(ctx, releaseKeysPrefix)
	if err != nil {
		return 0, err
	}
	oldest := now.Add(-2 * secretrelease.ApprovalLifetime).Unix()
	deleted := 0
	for _, key := range keys {
		if deleted == most {
			break
		}
		removed, err := sweepReleaseKey(ctx, st, rs, key.Key, oldest)
		if err != nil {
			return deleted, err
		}
		if removed {
			deleted++
		}
	}
	return deleted, nil
}

func sweepReleaseKey(ctx context.Context, st store.Store, rs *records.Records, key string, oldest int64) (bool, error) {
	rest := strings.TrimPrefix(key, releaseKeysPrefix)
	host, _, isSeen := strings.Cut(rest, "/seen/")
	if isSeen {
		return sweepReleaseSeen(ctx, st, rs, key, host, oldest)
	}
	host = strings.TrimSuffix(rest, ".json")
	if host == rest || rs.Host(host) != nil {
		return false, nil
	}
	if err := st.Delete(ctx, key); err != nil {
		return false, err
	}
	return true, nil
}

func sweepReleaseSeen(ctx context.Context, st store.Store, rs *records.Records, key, host string, oldest int64) (bool, error) {
	if rs.Host(host) == nil {
		if err := st.Delete(ctx, key); err != nil {
			return false, err
		}
		return true, nil
	}
	o, err := st.Get(ctx, key)
	if err != nil || o == nil {
		return false, err
	}
	var seen releaseSeen
	if json.Unmarshal(o.Data, &seen) != nil || seen.At >= oldest {
		return false, nil
	}
	if err := st.Delete(ctx, key); err != nil {
		return false, err
	}
	return true, nil
}
