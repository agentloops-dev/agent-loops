package agentsd

import (
	"strings"
	"testing"
)

// The flag labelled-workflows (#7599): off by default, on once the person turns it on, off
// again when turned off or set to a value that is not a bool; and the words said while it is
// off name where to turn it on.
func TestLabelledWorkflowsFlag(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()

	if d.labelledWorkflowsOn() {
		t.Fatal("on by default")
	}
	c.call(t, 1, "preferences/set", map[string]any{labelledWorkflowsFlag: true})
	if !d.labelledWorkflowsOn() {
		t.Fatal("off after it was turned on")
	}
	c.call(t, 2, "preferences/set", map[string]any{labelledWorkflowsFlag: false})
	if d.labelledWorkflowsOn() {
		t.Fatal("on after it was turned off")
	}
	c.call(t, 3, "preferences/set", map[string]any{labelledWorkflowsFlag: "yes"})
	if d.labelledWorkflowsOn() {
		t.Fatal("on for a value that is not a bool")
	}
	if errLabelledWorkflowsOff.Code != CodeNotSupported || !strings.Contains(errLabelledWorkflowsOff.Message, "Labelled workflows") {
		t.Fatalf("%+v", errLabelledWorkflowsOff)
	}
}
