package hostsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// SignIn is step 7 (#5361, epic #5353): each runtime is signed in, and the step says which
// are not. The installer does not sign anything in itself: it holds no key. The person's own
// client does, on this machine's daemon (runtimes/signInKey with a key from Secrets, or the
// runtime's device-code sign-in), and the runtime then says it is available.
//
// The step is running for KeyWait, which is the time a client takes to write a key it has,
// and needs-you after it, with the runtimes that wait, so the person is told only when the
// keys did not do it. It fails when a runtime is not signed in after d.SignInWait: a code
// that nobody used.
func SignIn(ctx context.Context, o Options, d Deps, rep *Reporter, say func(string, ...any)) error {
	d.defaults()
	step := wire.MachineSetupStepIDSignIn
	ids := o.Runtimes
	if len(ids) == 0 {
		ids = DefaultRuntimes
	}
	begun := time.Now()
	last := ""
	for {
		raw, err := d.Call(wire.CallRuntimesList.Name, wire.Empty{})
		var all []wire.RuntimeStatus
		if err == nil {
			err = json.Unmarshal(raw, &all)
		}
		if err != nil {
			rep.Say(step, wire.MachineSetupStateFailed, "", err.Error())
			return fmt.Errorf("%s: %w", step, err)
		}
		var waiting, ready []string
		for _, id := range ids {
			name, signedIn := id, false
			for _, r := range all {
				if r.Runtime.ID == id {
					if r.Runtime.Name != "" {
						name = r.Runtime.Name
					}
					signedIn = signedInOn(r)
				}
			}
			if signedIn {
				ready = append(ready, name)
			} else {
				waiting = append(waiting, name)
			}
		}
		if len(waiting) == 0 {
			line := strings.Join(ready, ", ") + " signed in"
			rep.Say(step, wire.MachineSetupStateDone, line, "")
			say("Sign in to runtimes: %s", line)
			return nil
		}
		elapsed := time.Since(begun)
		if elapsed > d.SignInWait {
			problem := fmt.Sprintf("%s not signed in after %s. Nobody used the sign-in.", strings.Join(waiting, ", "), d.SignInWait)
			rep.Say(step, wire.MachineSetupStateFailed, "", problem)
			say("%s: %s", step, problem)
			return fmt.Errorf("%s: %s", step, problem)
		}
		state, line := wire.MachineSetupStateRunning, "Looking for keys in Secrets: "+strings.Join(waiting, ", ")
		if elapsed > d.KeyWait {
			state, line = wire.MachineSetupStateNeedsYou, "Waiting for you: "+strings.Join(waiting, ", ")
		}
		if line != last {
			rep.Say(step, state, line, "")
			last = line
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Poll):
		}
	}
}

// signedInOn says whether a runtime on this machine is signed in: available, and, for the
// runtimes the host can tell for (Claude and Codex), signed in with its own or a lent
// sign-in.
func signedInOn(r wire.RuntimeStatus) bool {
	if r.Availability.Available == nil {
		return false
	}
	if r.SignIn == nil {
		return true
	}
	return r.SignIn.SignedIn || (r.SignIn.Relayed != nil && *r.SignIn.Relayed)
}
