package providers

import (
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7154: what a made machine's Root set-up says, from what its carrier sent.
func TestARootStepSaysWhatTheCarrierSent(t *testing.T) {
	var said []wire.MachineSetupReport
	p := &Providers{hooks: Hooks{SetupStep: func(_ string, r wire.MachineSetupReport) { said = append(said, r) }}}
	p.rootStep("h-1", "DigitalOcean", rootSent{})
	if len(said) != 0 {
		t.Fatalf("a step with no root.sh: %+v", said)
	}
	for _, c := range []struct {
		root    rootSent
		words   string
		warning bool
	}{
		{rootSent{&RootBootSent{Hash: "ab12"}, true}, "Waits for Compare digits", false},
		{rootSent{&RootBootSent{Hash: "ab12"}, false}, "DigitalOcean can’t run root set-up. Put it in the image.", true},
		{rootSent{&RootBootSent{Problem: "Root set-up needs your machine lock."}, false}, "Root set-up needs your machine lock.", true},
	} {
		said = nil
		p.rootStep("h-1", "DigitalOcean", c.root)
		if len(said) != 1 {
			t.Fatalf("%+v", said)
		}
		r := said[0]
		if r.Step != wire.MachineSetupStepIDRoot || r.State != wire.MachineSetupStateWaiting || *r.Detail != c.words ||
			(r.Warning != nil) != c.warning || (c.root.sent.Hash != "") != (r.Hash != nil) {
			t.Errorf("%+v: %+v", c.root.sent, r)
		}
	}
}
