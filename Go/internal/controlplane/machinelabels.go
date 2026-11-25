package controlplane

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// maxMachineLabels is the most labels a person puts on one machine (#5284).
const maxMachineLabels = 20

// labelPattern is a machine label: a key, or key=value (#5284), of letters, digits and . _ / -
// in the key, and the same with : @ + in the value. It is what a workflow's runs-on: names.
var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}(=[A-Za-z0-9._/:@+-]{1,63})?$`)

// providerKeys are the label keys that only the machine's provider sets.
var providerKeys = map[string]bool{"os": true, "arch": true, "provider": true}

func labelKey(label string) string {
	key, _, _ := strings.Cut(label, "=")
	return strings.ToLower(key)
}

// autoLabels are the labels a provider sets itself: os and arch from its platform, and the
// provider's name when it has one (#5284).
func autoLabels(h records.MachineRecord) []string {
	var out []string
	fields := strings.Fields(strings.ToLower(h.Platform))
	if len(fields) > 0 {
		out = append(out, "os="+fields[0])
	}
	if len(fields) > 1 {
		arch := fields[len(fields)-1]
		switch arch {
		case "aarch64":
			arch = "arm64"
		case "x86_64":
			arch = "amd64"
		}
		out = append(out, "arch="+arch)
	}
	if link := providers.LinkOf(&h); link != nil && link.Provider != "" {
		out = append(out, "provider="+strings.ToLower(link.Provider))
	}
	return out
}

// machineLabels are all of a machine's labels, sorted: the provider's and the person's.
func machineLabels(h records.MachineRecord) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range append(autoLabels(h), h.Labels...) {
		if !seen[strings.ToLower(l)] {
			seen[strings.ToLower(l)] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// cleanLabels checks each label and drops the repeats; it says which one is wrong.
func cleanLabels(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		l := strings.TrimSpace(raw)
		if l == "" {
			continue
		}
		if !labelPattern.MatchString(l) {
			return nil, fail(invalidParams, "%q is not a label. Use a key or key=value of letters, digits and . _ / -, up to 63 characters each.", raw)
		}
		if providerKeys[labelKey(l)] {
			return nil, fail(invalidParams, "%s is set by the machine's provider. Pick another key.", labelKey(l))
		}
		if !seen[strings.ToLower(l)] {
			seen[strings.ToLower(l)] = true
			out = append(out, l)
		}
	}
	return out, nil
}

// setLabels is machines/setLabels (#5284). A machine that is not the caller's is left out, as
// if it were not there.
func (m *Methods) setLabels(ctx context.Context, request wire.MachineLabelsRequest, from Caller) (wire.MachineLabelsResult, error) {
	set, err := cleanLabels(request.Set)
	if err != nil {
		return wire.MachineLabelsResult{}, err
	}
	add, err := cleanLabels(request.Add)
	if err != nil {
		return wire.MachineLabelsResult{}, err
	}
	remove := map[string]bool{}
	for _, l := range request.Remove {
		remove[strings.ToLower(strings.TrimSpace(l))] = true
	}
	// A label is what a workflow's runs-on picks a machine by, and a control-only machine
	// runs no work (#7310): a call that names one changes nothing.
	for _, id := range request.Machines {
		if m.owns(from, string(id)) {
			if err := m.refuseControlOnly(string(id)); err != nil {
				return wire.MachineLabelsResult{}, err
			}
		}
	}
	replace := request.Set != nil
	changed := 0
	for _, id := range request.Machines {
		host := string(id)
		if m.records.Host(host) == nil || !m.owns(from, host) {
			continue
		}
		var failure error
		err := m.records.ChangeHost(ctx, host, func(h *records.MachineRecord) bool {
			next := h.Labels
			if replace {
				next = set
			}
			kept := nextLabels(next, add, remove)
			if len(kept) > maxMachineLabels {
				failure = fail(invalidParams, "%s has more than %d labels. Remove one first.", h.Name, maxMachineLabels)
				return false
			}
			sort.Strings(kept)
			if strings.Join(kept, "\n") == strings.Join(h.Labels, "\n") {
				return false
			}
			h.Labels = kept
			changed++
			return true
		})
		if failure != nil {
			return wire.MachineLabelsResult{Changed: int64(changed)}, failure
		}
		if err != nil {
			return wire.MachineLabelsResult{Changed: int64(changed)}, err
		}
	}
	return wire.MachineLabelsResult{Changed: int64(changed)}, nil
}

// nextLabels is labels without those in remove (by lowercase), then each of add that is not
// there yet, in any case.
func nextLabels(labels, add []string, remove map[string]bool) []string {
	var kept []string
	for _, l := range labels {
		if !remove[strings.ToLower(l)] {
			kept = append(kept, l)
		}
	}
	seen := map[string]bool{}
	for _, l := range kept {
		seen[strings.ToLower(l)] = true
	}
	for _, l := range add {
		if !seen[strings.ToLower(l)] {
			kept = append(kept, l)
		}
	}
	return kept
}
