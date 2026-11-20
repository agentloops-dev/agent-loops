package agentsd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The daily checks' one control (#3355, #5853): on or off, and how often they run, for all
// folders. It is kept in the home folder's ready-made Daily checks workflow itself, so a hand
// edit and the control agree: on is its enabled:, and how often is its schedules, every few
// hours from midnight. It was the daily summary's control (#3351, #3395) until #5853.

// checksDefaultEvery is how many hours apart the ready-made Daily checks run.
const checksDefaultEvery = 4

// checksEveryChoices are the hours the checks can run apart: each divides the day evenly, so
// they run at the same hours every day.
var checksEveryChoices = []int64{2, 3, 4, 6, 8, 12}

// dailyChecksSettings is dailyChecks/settings.
func (d *Daemon) dailyChecksSettings() wire.DailyChecksSettings {
	s := wire.DailyChecksSettings{Every: checksDefaultEvery}
	home := d.personalHome()
	if home == "" {
		return s
	}
	checks, err := d.oneWorkflow(home, dailyChecksID)
	if err != nil {
		return s
	}
	s.Enabled = checks.IsEnabled
	s.Every = checksEveryOf(checks.Workflow)
	return s
}

// checksEveryOf is how many hours apart a Daily checks workflow's schedules run: n schedules
// a day are 24/n hours apart. A copy made with the daily summary skipped the summary's hour,
// so its n schedules are 24/(n+1) apart. A hand edit that is no choice reads as the default.
func checksEveryOf(w wire.Workflow) int64 {
	n := int64(0)
	for _, t := range w.Triggers {
		if t.Schedule != nil {
			n++
		}
	}
	for _, k := range []int64{n, n + 1} {
		if k > 0 && 24%k == 0 && slices.Contains(checksEveryChoices, 24/k) {
			return 24 / k
		}
	}
	return checksDefaultEvery
}

// checksSchedules is the Daily checks workflow's on: block: every few hours from midnight.
func checksSchedules(every int64) []string {
	block := []string{"on:"}
	for h := int64(0); h < 24; h += every {
		at := fmt.Sprintf("%02d:00", h)
		block = append(block, "  - schedule:", "      at: "+doubleQuoted(":00"), "      between: "+doubleQuoted(at+"-"+at))
	}
	return block
}

// setDailyChecksSettings is dailyChecks/setSettings: how often first, then on or off.
func (d *Daemon) setDailyChecksSettings(r wire.DailyChecksSettings) (wire.DailyChecksSettings, error) {
	if !slices.Contains(checksEveryChoices, r.Every) {
		return wire.DailyChecksSettings{}, Invalid("the checks cannot run every %d hours: say 2, 3, 4, 6, 8 or 12", r.Every)
	}
	home := d.personalHome()
	if home == "" {
		return wire.DailyChecksSettings{}, &Error{Code: wire.FailureNotAllowed, Message: "This machine has no home folder for the Daily checks workflow."}
	}
	d.workflowMu.Lock()
	d.scanLocked()
	err := d.editWorkflowLocked(home, dailyChecksID, true, func(text string) (string, error) {
		return setFrontMatterBlock("on", checksSchedules(r.Every), text)
	})
	d.workflowMu.Unlock()
	if err != nil {
		return wire.DailyChecksSettings{}, err
	}
	if checks, err := d.oneWorkflow(home, dailyChecksID); err == nil && checks.IsEnabled != r.Enabled {
		if _, err := d.enableWorkflow(wire.WorkflowEnableRequest{Folder: store.FolderURL(home), WorkflowID: dailyChecksID, Enabled: r.Enabled}, "person"); err != nil {
			return wire.DailyChecksSettings{}, err
		}
	}
	return d.dailyChecksSettings(), nil
}

// setFrontMatterBlock puts block in place of a workflow metadata's key: and the lines under it,
// such as the whole on: list. It refuses when there is not exactly one, as setFrontMatter does.
func setFrontMatterBlock(key string, block []string, source string) (string, error) {
	lines, nl, closing, err := splitSource(source)
	if err != nil {
		return "", err
	}
	found := -1
	for i := 1; i < closing; i++ {
		if isKeyLine(lines[i], key) {
			if found >= 0 {
				return "", refuse("`" + key + ":` appears more than once in the metadata — which one is meant is not something to pick between silently")
			}
			found = i
		}
	}
	if found < 0 {
		return "", refuse("the metadata has no `" + key + ":`")
	}
	end := found + 1
	for end < closing && isContinuation(lines[end]) {
		end++
	}
	out := append(append(append([]string{}, lines[:found]...), block...), lines[end:]...)
	return strings.Join(out, nl), nil
}

func (d *Daemon) registerDailyChecks() {
	On(d, wire.CallDailyChecksSettings, func(ctx context.Context, c *Client, _ wire.Empty) (wire.DailyChecksSettings, error) {
		return d.dailyChecksSettings(), nil
	})
	On(d, wire.CallDailyChecksSetSettings, func(ctx context.Context, c *Client, r wire.DailyChecksSettings) (wire.DailyChecksSettings, error) {
		return d.setDailyChecksSettings(r)
	})
}
