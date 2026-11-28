package agentsd

import "encoding/json"

// Labelled workflows (#7599, epic #6951): a workflow with agent: labelled sends a message to
// the sessions with its labels. It is behind the person's flag labelled-workflows, off by
// default. The workflow's run (#6954) calls labelledWorkflowsOn first and, with the flag off,
// sends nothing and says errLabelledWorkflowsOff.

// labelledWorkflowsFlag is the person's flag that turns labelled workflows on, in the display
// settings.
const labelledWorkflowsFlag = "flag:labelled-workflows"

// labelledWorkflowsOn says whether the person turned the flag labelled-workflows on.
func (d *Daemon) labelledWorkflowsOn() bool { return d.flagOn(labelledWorkflowsFlag) }

var errLabelledWorkflowsOff = &Error{Code: CodeNotSupported,
	Message: "labelled workflows are off: turn on Labelled workflows in Settings ▸ This app ▸ Early Features"}

// flagOn says whether the person's flag key (flag:<name>) is true in the display settings. A
// flag that is absent, or not a bool, is off.
func (d *Daemon) flagOn(key string) bool {
	var on bool
	v, ok := d.preferences()[key]
	return ok && json.Unmarshal(v, &on) == nil && on
}
