package controlplane

import (
	"context"
	"encoding/json"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/signin"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// joinCutoffsPrefix keeps, for each provider that can add people without an invite, when a
// removed person's tombstone with a sign-in of it first aged out (#6756). An account has no
// age that the control plane can check, so from then on an account of that provider that
// nobody has needs an admin's invite: a removed person's old sign-in stays refused with no
// record of it. One a live provider: Google and at most mostOrgProviders.
// store-bounds: kind=document; size=1KiB a record; count=6; age=until-deleted; applied=on write, and when the provider is removed
const joinCutoffsPrefix = "v1/signin/cutoffs/"

func joinCutoffKey(provider string) string { return joinCutoffsPrefix + provider + ".json" }

// joinCutoff is one provider's cutoff.
type joinCutoff struct {
	At records.Stamp `json:"at"`
}

// closeJoins writes the cutoff of each provider of those sign-ins that can add people
// without an invite. Call it before the tombstone that refuses the sign-ins is deleted, so
// that no reader finds neither.
func closeJoins(ctx context.Context, s store.Store, signIns []records.SignIn, at records.Stamp) error {
	for _, in := range signIns {
		can, err := canJoinByItself(ctx, s, in.Provider)
		if err != nil {
			return err
		}
		if !can {
			continue
		}
		data, err := records.Encode(joinCutoff{At: at})
		if err != nil {
			return err
		}
		if _, err := s.Put(ctx, joinCutoffKey(in.Provider), data, store.Absent); err != nil && !store.IsConflict(err) {
			return err
		}
	}
	return nil
}

// canJoinByItself says a provider can add people without an invite, now or when an admin
// changes its settings: Google, and an org provider that is not removed (its id is not
// given to another while anything names it, so a removed one never signs in again).
func canJoinByItself(ctx context.Context, s store.Store, provider string) (bool, error) {
	if provider == signin.Google {
		return true, nil
	}
	if outsideProvider(provider) || !orgProviderIDPattern.MatchString(provider) {
		return false, nil
	}
	object, err := s.Get(ctx, orgProviderKey(provider))
	if err != nil || object == nil {
		return false, err
	}
	var stored orgProviderStored
	if err := json.Unmarshal(object.Data, &stored); err != nil {
		return false, err
	}
	return stored.Removed == nil, nil
}

// joinsClosed refuses an account that nobody has, when its provider has a cutoff. Ask it
// only after reading the people: the cutoff is written before a tombstone goes.
func (si *signIns) joinsClosed(ctx context.Context, provider string) error {
	object, err := si.s.cfg.Store.Get(ctx, joinCutoffKey(provider))
	if err != nil {
		return fail(storeUnavailable, "The sign-in settings could not be read: %v", err)
	}
	if object == nil {
		return nil
	}
	return fail(notPermitted, "This provider no longer adds people to this control plane by itself. Ask an admin for an invite. Pair with its code, and then add this sign-in under Account.")
}
