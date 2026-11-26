package agentsd

import (
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Autopilot (#5898): absent is off with no rows; the mode is kept; the status keeps the newest 100 rows.
func TestAutopilotSettingAndStatus(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	url := store.FolderURL(dir)

	var st wire.AutopilotStatus
	mustUnmarshal(t, c.call(t, 2, "autopilot/status", map[string]any{"folder": url}), &st)
	if st.Mode != wire.AutopilotModeOff || len(st.Rows) != 0 {
		t.Fatalf("%+v", st)
	}
	mustUnmarshal(t, c.call(t, 3, "autopilot/set", map[string]any{"folder": url, "mode": "high"}), &st)
	if st.Mode != wire.AutopilotModeHigh {
		t.Fatalf("%+v", st)
	}
	if p, _, _ := d.folderRecord(dir); p.Autopilot == nil || p.Autopilot.Mode != wire.AutopilotModeHigh || p.Autopilot.SetBy == "" {
		t.Fatalf("%+v", p.Autopilot)
	}
	for i := range 120 {
		ref := "#" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		if err := d.autopilotNote(dir, wire.AutopilotRow{Ref: ref, Kind: wire.AutopilotRowKindLane, State: wire.AutopilotRowStateWaiting, Reason: "on hold"}); err != nil {
			t.Fatal(err)
		}
	}
	mustUnmarshal(t, c.call(t, 4, "autopilot/status", map[string]any{"folder": url}), &st)
	if len(st.Rows) != 100 || st.LastRunAt == nil {
		t.Fatalf("%d rows", len(st.Rows))
	}
	mustUnmarshal(t, c.call(t, 5, "autopilot/set", map[string]any{"folder": url, "mode": "off"}), &st)
	if st.Mode != wire.AutopilotModeOff || len(st.Rows) != 0 {
		t.Fatalf("%+v", st)
	}
	if p, _, _ := d.folderRecord(dir); p.Autopilot != nil {
		t.Fatalf("%+v", p.Autopilot)
	}
}
