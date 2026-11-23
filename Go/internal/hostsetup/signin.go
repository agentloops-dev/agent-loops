package hostsetup

import (
	"context"
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
// keys did not do it. It then waits for the person with no time limit (#6853): a person who
// signs in an hour later, from the machine's runtimes or with the Mac's lent sign-in, still
// gets step 8 and a Ready machine. Before, it failed after d.SignInWait, and nothing could
// run steps 7 and 8 again. When the installer itself is stopped, the step says so.
func SignIn(ctx context.Context, o Options, d Deps, rep *Reporter, say func(string, ...any)) error {
	d.defaults()
	step := wire.MachineSetupStepIDSignIn
	ids := o.Runtimes
	if len(ids) == 0 {
		ids = DefaultRuntimes
	}
	begun := time.Now()
	last := ""
	var said wire.MachineSetupState
	for {
		all, err := call[[]wire.RuntimeStatus](d, wire.CallRuntimesList.Name, wire.Empty{})
		if err != nil {
			rep.Say(step, wire.MachineSetupStateFailed, "", err.Error())
			return fmt.Errorf("%s: %w", step, err)
		}
		waiting, ready, lent := splitSignedIn(all, ids)
		if len(waiting) == 0 {
			line := signedInLine(ready, lent)
			rep.Say(step, wire.MachineSetupStateDone, line, "")
			say("Sign in to runtimes: %s", line)
			return nil
		}
		// The states go in order, also when an answer is slow (#6340): the first pass says
		// running, and needs-you comes only after running was said.
		elapsed := time.Since(begun)
		state, line := wire.MachineSetupStateRunning, "Looking for keys in Secrets: "+strings.Join(waiting, ", ")
		if said != "" && elapsed > d.KeyWait {
			state, line = wire.MachineSetupStateNeedsYou, "Waiting for you: "+strings.Join(waiting, ", ")
		}
		if line != last {
			rep.Say(step, state, line, "")
			last, said = line, state
		}
		select {
		case <-ctx.Done():
			problem := fmt.Sprintf("The installer stopped before %s signed in. Run it again to finish the set-up.", strings.Join(waiting, ", "))
			rep.Say(step, wire.MachineSetupStateFailed, "", problem)
			say("%s: %s", step, problem)
			return ctx.Err()
		case <-time.After(d.Poll):
		}
	}
}

// splitSignedIn names the runtimes of ids that wait for a sign-in, those signed in with a
// sign-in of their own, and those a Mac lends its sign-in to (056, #6855), by the runtime's
// own name when the host gives one. A lent sign-in is used over the machine's own (056 D7),
// so a runtime with both is lent.
func splitSignedIn(all []wire.RuntimeStatus, ids []string) (waiting, ready, lent []string) {
	for _, id := range ids {
		name, signedIn, relayed := id, false, false
		for _, r := range all {
			if r.Runtime.ID == id {
				if r.Runtime.Name != "" {
					name = r.Runtime.Name
				}
				signedIn = signedInOn(r)
				relayed = r.SignIn != nil && r.SignIn.Relayed != nil && *r.SignIn.Relayed
			}
		}
		switch {
		case signedIn && relayed:
			lent = append(lent, name)
		case signedIn:
			ready = append(ready, name)
		default:
			waiting = append(waiting, name)
		}
	}
	return waiting, ready, lent
}

// signedInLine is step 7's line when every runtime is signed in: "Claude, Codex signed in
// (lent by a Mac)" when a Mac lends them its sign-in (#6855).
func signedInLine(own, lent []string) string {
	var parts []string
	if len(own) > 0 {
		parts = append(parts, strings.Join(own, ", ")+" signed in")
	}
	if len(lent) > 0 {
		parts = append(parts, strings.Join(lent, ", ")+" signed in (lent by a Mac)")
	}
	return strings.Join(parts, "; ")
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
