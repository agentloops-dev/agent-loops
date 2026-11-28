package agentsd

import (
	"encoding/json"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/yamlnode"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

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

// workflowApplyLabelled reads to: and deliver: (#6954) for agent: labelled. The mode stays new
// on the wire, and to: beside it says labelled, so a client that does not know the mode still
// reads the list. It gives why the file cannot be read, or "".
func workflowApplyLabelled(w *wire.Workflow, m map[string]yamlnode.Node) string {
	named, _ := m["agent"].Text()
	toNode, hasTo := m["to"]
	deliverNode, hasDeliver := m["deliver"]
	if named != labelledMode {
		if hasTo || hasDeliver {
			return "`to:` and `deliver:` are only for `agent: labelled`"
		}
		return ""
	}
	if !hasTo {
		return "`agent: labelled` needs `to:`, with the labels of the sessions it sends to"
	}
	to, why := parseWorkflowTo(toNode)
	if why != "" {
		return why
	}
	w.To = &to
	if hasDeliver {
		t, _ := deliverNode.Text()
		switch d := wire.WorkflowDeliver(strings.TrimSpace(t)); d {
		case wire.WorkflowDeliverNextTurn, wire.WorkflowDeliverSteer, wire.WorkflowDeliverWake:
			w.Deliver = &d
		default:
			return "`deliver:` must be next-turn, steer or wake, not \"" + t + "\""
		}
	}
	return ""
}

// parseWorkflowTo is to:: labels (at least one), not-labels, runtime and account.
func parseWorkflowTo(n yamlnode.Node) (wire.WorkflowTo, string) {
	to := wire.WorkflowTo{Labels: []string{}}
	if !n.IsMapping() {
		return to, "`to:` must have `labels:`, and can have `not-labels:`, `runtime:` and `account:`"
	}
	for key, v := range n.Mapping {
		switch key {
		case "labels", "not-labels":
			labels, err := runsOnLabels(v)
			if err != nil {
				return to, "`to: " + key + ":` must be a label or a list of labels"
			}
			if key == "labels" {
				to.Labels = labels
			} else {
				to.NotLabels = labels
			}
		case "runtime", "account":
			t, ok := v.Text()
			if t = strings.TrimSpace(t); !ok || t == "" {
				return to, "`to: " + key + ":` must be a name"
			}
			if key == "runtime" {
				to.Runtime = &t
			} else {
				to.Account = &t
			}
		default:
			return to, "`to:` does not know `" + key + ":`; it takes labels, not-labels, runtime and account"
		}
	}
	if len(to.Labels) == 0 {
		return to, "`to:` needs `labels:`, at least one, so that it sends to the sessions with them"
	}
	return to, ""
}
