package controlplane

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7159: Try Again on a failed Root set-up asks the machine to read root.sh's result again and
// run the steps after it; a machine that does not take it keeps the step failed, and says why.
func TestTryAgainOnAFailedRootSetUpAsksTheMachineToReadItAgain(t *testing.T) {
	t.Parallel()
	for _, takes := range []bool{true, false} {
		p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
		var mu sync.Mutex
		var asked []wire.SetupRootAgain
		p.asked = func(_, method string, params json.RawMessage) (any, bool) {
			if method != wire.CallSetupRootAgain.Name || !takes {
				return nil, false
			}
			var r wire.SetupRootAgain
			fakeMust(json.Unmarshal(params, &r))
			mu.Lock()
			asked = append(asked, r)
			mu.Unlock()
			return wire.Empty{}, true
		}
		box := p.host("MACHINE-2")
		ctx, host := context.Background(), box.member.Host
		for _, id := range setupSteps[:4] {
			must(t, p.s.setupStep(ctx, host, rep(id, wire.MachineSetupStateRunning)))
			must(t, p.s.setupStep(ctx, host, rep(id, wire.MachineSetupStateDone)))
		}
		why := "root.sh ended with exit 100"
		must(t, p.s.setupStep(ctx, host, rep(wire.MachineSetupStepIDRoot, wire.MachineSetupStateRunning)))
		must(t, p.s.setupStep(ctx, host, wire.MachineSetupReport{Step: wire.MachineSetupStepIDRoot, State: wire.MachineSetupStateFailed, Error: &why}))
		must(t, p.s.retrySetup(ctx, host))
		root := p.s.machineSetup(ctx, host).Steps[p.s.mustIndex(t, ctx, host, wire.MachineSetupStepIDRoot)]
		mu.Lock()
		got := append([]wire.SetupRootAgain(nil), asked...)
		mu.Unlock()
		if takes {
			if len(got) != 1 || !got[0].Rest || root.State != wire.MachineSetupStateWaiting {
				t.Errorf("taken: %+v %+v", got, root)
			}
			continue
		}
		if root.State != wire.MachineSetupStateFailed || root.Error == nil || *root.Error == why {
			t.Errorf("not taken: %+v", root)
		}
	}
}

// mustIndex is the place of a step in host's set-up.
func (s *Service) mustIndex(t *testing.T, ctx context.Context, host string, id wire.MachineSetupStepID) int {
	t.Helper()
	for i, step := range s.machineSetup(ctx, host).Steps {
		if step.ID == id {
			return i
		}
	}
	t.Fatalf("no step %s", id)
	return -1
}
