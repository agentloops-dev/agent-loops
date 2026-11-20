package agentsd

import (
	"os"
	"path/filepath"
	"testing"
)

// A ready-made skill the person did not edit follows the pin; an edited copy, or a link, stays
// theirs (#5559).
func TestReadyMadeSkillsFollowThePin(t *testing.T) {
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	home := t.TempDir()
	d.PersonalHome = home
	skillPath := func(id string) string { return filepath.Join(home, ".agents", "skills", id, "SKILL.md") }
	write := func(id, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(skillPath(id)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(skillPath(id), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set := readyMadeFixture()
	newApp := set.Skills["start-a-new-app"]
	_, workSkill := set.text("work-an-issue")
	_, checksSkill := set.text("daily-checks")
	_, linkedSkill := set.text("fix-failed-checks")
	if newApp == "" || workSkill == "" || checksSkill == "" || linkedSkill == "" {
		t.Fatal("the fixture has no such skills")
	}

	// start-a-new-app as an earlier version left it, with no record of what was made (this Mac's
	// copy of 2026-11-07): it is updated.
	earlier := "---\nname: start-a-new-app\n---\nThe first copy.\n"
	saved := earlierReadyMadeSkills["start-a-new-app"]
	earlierReadyMadeSkills["start-a-new-app"] = map[string]bool{readyMadeSkillDigest(earlier): true}
	defer func() { earlierReadyMadeSkills["start-a-new-app"] = saved }()
	write("start-a-new-app", earlier)
	// A workflow's skill as the daemon made an older pin: it is updated.
	madeOlder := workSkill + "\nAn older line.\n"
	write("work-an-issue", madeOlder)
	markReadyMadeSkillMade(home, "work-an-issue", readyMadeSkillDigest(madeOlder))
	// A skill the person edited: it stays.
	edited := checksSkill + "\nMy own line.\n"
	write("daily-checks", edited)
	// A link the person made: it stays a link.
	mine := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(mine, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(skillPath("fix-failed-checks")), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mine, skillPath("fix-failed-checks")); err != nil {
		t.Fatal(err)
	}

	c.call(t, 1, "workflows/list", map[string]any{"scope": "user"})
	if !fileHas(t, skillPath("start-a-new-app"), newApp) || !fileHas(t, skillPath("work-an-issue"), workSkill) {
		t.Fatal("an unedited skill was not updated")
	}
	if !fileHas(t, skillPath("daily-checks"), edited) {
		t.Fatal("the edited skill was changed")
	}
	if fi, err := os.Lstat(skillPath("fix-failed-checks")); err != nil || fi.Mode()&os.ModeSymlink == 0 || !fileHas(t, mine, "mine\n") {
		t.Fatal("the person's link was changed")
	}
	made := madeReadyMadeSkills(home)
	if made["start-a-new-app"] != readyMadeSkillDigest(newApp) || made["work-an-issue"] != readyMadeSkillDigest(workSkill) {
		t.Fatalf("the record of what was made: %v", made)
	}
	if _, ok := made["daily-checks"]; ok {
		t.Fatal("an edited copy was recorded as made")
	}

	// The next pin writes over a copy this daemon made, with no earlier version needed.
	earlierReadyMadeSkills["start-a-new-app"] = nil
	next := newApp + "\nThe next pin.\n"
	ensureReadyMadeSkill(home, "start-a-new-app", next, madeReadyMadeSkills(home)["start-a-new-app"])
	if !fileHas(t, skillPath("start-a-new-app"), next) {
		t.Fatal("a copy the daemon made did not follow the next pin")
	}
}
