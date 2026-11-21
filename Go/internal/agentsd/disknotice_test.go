package agentsd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func diskNotices(t *testing.T, c *conn, id int) []wire.Need {
	t.Helper()
	var pending wire.AttentionPending
	if err := json.Unmarshal(c.call(t, id, "attention/pending", nil), &pending); err != nil {
		t.Fatal(err)
	}
	var out []wire.Need
	for _, n := range pending.Needs {
		if n.Kind == wire.NeedKindNotice && n.ID.Notice != nil && strings.HasPrefix(*n.ID.Notice, diskNoticePrefix) {
			out = append(out, n)
		}
	}
	return out
}

// A volume that becomes critical is one notice of the machine's (#3682), with no chat, and
// it is taken back when the volume climbs back.
func TestACriticalDiskIsOneNoticeUntilItClimbsBack(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	alarm := wire.DiskAlarm{Reading: wire.DiskReading{Volume: "Macintosh HD", Mount: "/", FreeBytes: 1_500_000_000, TotalBytes: 100e9},
		Level: wire.DiskLevelCritical, Threshold: 2e9}
	d.postDiskNotice(alarm)
	d.postDiskNotice(alarm)
	got := diskNotices(t, c, 1)
	if len(got) != 1 || got[0].AgentID != nil || *got[0].ID.Notice != "disk:/" || got[0].Headline.H2 != diskNoticeTitle ||
		got[0].Headline.H3 != "Macintosh HD has 1.5 GB free. Agents stop when it is full." {
		t.Fatalf("%+v", got)
	}
	// Not taken from the person's bucket, and not a Needs you row.
	if n := d.waitingCount(); n != 0 {
		t.Fatalf("waiting %d", n)
	}
	d.withdrawDiskNotice("/")
	if got := diskNotices(t, c, 2); len(got) != 0 {
		t.Fatalf("still posted: %+v", got)
	}
	// Critical again: a new notice.
	d.postDiskNotice(alarm)
	if got := diskNotices(t, c, 3); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

// A test's daemon reads a disk with room (#6390): the Mac's own disk, however full, puts no
// notice in its Needs you. A test that names a full disk gets the notice from the disk check
// itself, and loses it when the disk has room again.
func TestATestDaemonReadsItsOwnDisk(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	d.checkDisk()
	if alarms := d.diskState().Alarms; len(alarms) != 0 {
		t.Fatalf("a test daemon's disk is low: %+v", alarms)
	}
	if got := diskNotices(t, c, 1); len(got) != 0 {
		t.Fatalf("a disk notice on a roomy disk: %+v", got)
	}

	diskFreeFor(t, 100_000_000)
	d.checkDisk()
	if got := diskNotices(t, c, 2); len(got) != 1 || got[0].Headline.H2 != diskNoticeTitle {
		t.Fatalf("no disk notice at 100 MB free: %+v", got)
	}

	testHooksOf(t).change(func(h *testHooks) { h.opts.diskFree = roomyDisk })
	d.checkDisk()
	if got := diskNotices(t, c, 3); len(got) != 0 {
		t.Fatalf("still posted with room: %+v", got)
	}
}
