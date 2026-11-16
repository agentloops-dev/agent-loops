package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The warm pools' ties to the rest of the control plane (#5363): the providers that make and
// delete their machines, the set-up record that says when one is ready, and pool/*.

// poolsBackend is poolBackend over the providers and the machine set-up.
type poolsBackend struct{ s *Service }

func (b poolsBackend) Make(person, provider, name, size, region string) (string, error) {
	return b.s.providers.PoolMake(person, provider, name, size, region)
}

func (b poolsBackend) Delete(person, provider, workspace string) error {
	return b.s.providers.PoolDelete(person, provider, workspace)
}

func (b poolsBackend) HasProvider(provider string) bool { return b.s.providers.HasProvider(provider) }

// Stage: a pool machine is ready when its set-up has done steps 1 to 6 (create to projects);
// it fails when a step failed or it has not got so far in poolJoinGiveUp. Steps 7 and 8 are the
// claim's.
func (b poolsBackend) Stage(provider, workspace string, madeAt, now time.Time) poolStage {
	late := now.Sub(madeAt) > poolJoinGiveUp
	host := b.s.providers.PoolHost(provider, workspace)
	if host == nil {
		if late {
			return stageFailed
		}
		return stageSettingUp
	}
	stored, _, err := b.s.readMachineSetup(context.Background(), host.ID)
	if err != nil || stored == nil {
		if late {
			return stageFailed
		}
		return stageSettingUp
	}
	if stored.failed() {
		return stageFailed
	}
	for _, step := range stored.Steps[:6] {
		if step.State != wire.MachineSetupStateDone {
			if late {
				return stageFailed
			}
			return stageSettingUp
		}
	}
	return stageReady
}

// Ping: the host answers and the provider still lists the machine (#5365).
func (b poolsBackend) Ping(provider, workspace string) bool {
	host := b.s.providers.PoolHost(provider, workspace)
	return host != nil && b.s.providers.PoolOnline(host.ID) && b.s.providers.PoolListed(provider, workspace)
}

// Bind gives a pool machine to a person: its owner and Stop when idle, then the catch-up (#5365).
// Steps 1 to 5 were done in the pool; 6 (projects), 7 (sign in) and 8 (smoke) wait to run again
// on the machine, as Try Again would run them.
func (b poolsBackend) Bind(person, provider, workspace string, stopWhenIdle *int) (string, error) {
	host := b.s.providers.PoolHost(provider, workspace)
	if host == nil {
		return "", fmt.Errorf("the machine has not joined")
	}
	if err := b.s.providers.PoolBind(host.ID, person, stopWhenIdle); err != nil {
		return "", err
	}
	_, err := b.s.changeMachineSetup(context.Background(), host.ID, true, func(st *storedSetup) error {
		for i := range st.Steps {
			if i < 5 {
				st.Steps[i].FromPool = true
				continue
			}
			st.Steps[i] = storedSetupStep{ID: st.Steps[i].ID, State: wire.MachineSetupStateWaiting}
		}
		st.Ready = nil
		return nil
	})
	if err != nil {
		return "", err
	}
	b.s.setupChanged(context.Background(), host.ID)
	return host.ID, nil
}

// Version is the Agents release a new machine installs: the one this control plane is.
func (b poolsBackend) Version() string { return b.s.cfg.Version }

// Pins marks the runtime pins. Nothing pins a runtime yet, so a change cannot be seen.
func (b poolsBackend) Pins() string { return "" }

// Stopped says a pool stopped. It is in the log and in the pool's state, which clients read;
// the notice that carries it to a person's devices comes with the pool's page (#5365).
func (b poolsBackend) Stopped(person, pool, why string) {
	b.s.log(fmt.Sprintf("warm pool %s stopped: %s", pool, why))
}

// startPools starts the reconcile loop, on a control plane that has providers.
func (s *Service) startPools() {
	if s.providers == nil {
		return
	}
	s.pools = &warmPools{s: s, backend: poolsBackend{s}}
	go s.pools.every(poolEvery)
}

// poolMethod is pool/list, pool/set and pool/delete: a person's own pools.
func (m *Methods) poolMethod(ctx context.Context, method string, params json.RawMessage, from Caller) (any, error) {
	if m.s.pools == nil {
		return nil, m.s.providersOff()
	}
	switch method {
	case "pool/list":
		return m.s.pools.list(ctx, from.Person)
	case "pool/set":
		set, err := require[wire.WarmPoolSet](params, "WarmPoolSet")
		if err != nil {
			return nil, err
		}
		return m.s.pools.set(ctx, from.Person, set)
	default:
		request, err := require[wire.WarmPoolRequest](params, "WarmPoolRequest")
		if err != nil {
			return nil, err
		}
		if err := m.s.pools.delete(ctx, from.Person, request.ID); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	}
}
