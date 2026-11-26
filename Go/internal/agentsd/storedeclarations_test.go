package agentsd

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7349, #7350: the machine's state files that had no declaration, at their limits and over them.

func refusedForTheLimit(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, store.ErrLimit) {
		t.Fatalf("not refused for the store's limit: %v", err)
	}
}

// Each ready-made state file keeps readyMadeIDsMost ids of readyMadeIDMost bytes: one more is
// not kept, and an id it holds still changes.
func TestTheReadyMadeStateFilesKeepTheirMostIDs(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	for i := range readyMadeIDsMost {
		markReadyMadeMade(home, fmt.Sprintf("w%d", i), "d")
		markReadyMadeSkillMade(home, fmt.Sprintf("s%d", i), "d")
	}
	markReadyMadeMade(home, "one-more", "d")
	markReadyMadeSkillMade(home, "one-more", "d")
	markReadyMadeMade(home, "w0", "e")
	made, skills := madeReadyMade(home), madeReadyMadeSkills(home)
	if len(made) != readyMadeIDsMost || made["one-more"] != "" || made["w0"] != "e" {
		t.Errorf("ready-made-workflows.json: %d ids, one more %q, w0 %q", len(made), made["one-more"], made["w0"])
	}
	if len(skills) != readyMadeIDsMost || skills["one-more"] != "" {
		t.Errorf("ready-made-skills.json: %d ids", len(skills))
	}

	other := t.TempDir()
	markReadyMadeMade(other, strings.Repeat("a", readyMadeIDMost), "d")
	markReadyMadeMade(other, strings.Repeat("b", readyMadeIDMost+1), "d")
	if got := madeReadyMade(other); len(got) != 1 || got[strings.Repeat("a", readyMadeIDMost)] != "d" {
		t.Errorf("an id at its most and one over: %d kept", len(got))
	}
}

// ready-made-updated.json lets go of an update once its row no longer says it.
func TestAReadyMadeUpdateGoesWhenItIsNoLongerShown(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	markReadyMadeUpdated(home, "old", at)
	markReadyMadeUpdated(home, "new", at.Add(readyMadeUpdatedFor))
	b, err := os.ReadFile(updatedReadyMadePath(home))
	must(t, err)
	if strings.Contains(string(b), `"old"`) || !strings.Contains(string(b), `"new"`) {
		t.Errorf("ready-made-updated.json: %s", b)
	}
}

// removed-workflows.json takes readyMadeIDsMost ids: one more is refused.
func TestTheRemovedReadyMadeIDsAreRefusedPastTheirMost(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	set := &readyMadeSet{Tag: "v1", Workflows: map[string]readyMadeWorkflow{}}
	for i := range readyMadeIDsMost + 1 {
		set.Workflows[fmt.Sprintf("w%04d", i)] = readyMadeWorkflow{Workflow: "w"}
	}
	must(t, saveReadyMade(home, set))
	for i := range readyMadeIDsMost {
		must(t, markReadyMadeRemoved(home, fmt.Sprintf("w%04d", i)))
	}
	refusedForTheLimit(t, markReadyMadeRemoved(home, fmt.Sprintf("w%04d", readyMadeIDsMost)))
	must(t, markReadyMadeRemoved(home, "w0000"))
}

// A fetched copy over readyMadeCopyMost is refused, and the copy before stays.
func TestAReadyMadeCopyOverItsMostIsRefused(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	must(t, saveReadyMade(home, &readyMadeSet{Tag: "v1", Workflows: map[string]readyMadeWorkflow{"a": {Workflow: "w"}}}))
	big := &readyMadeSet{Tag: "v2", Workflows: map[string]readyMadeWorkflow{"a": {Workflow: strings.Repeat("w", readyMadeCopyMost)}}}
	refusedForTheLimit(t, saveReadyMade(home, big))
	if got := loadReadyMade(home); got == nil || got.Tag != "v1" {
		t.Fatalf("the copy before did not stay: %+v", got)
	}
}

// Claude's kept sign-in is refused over claudeSignInMost, and the error does not hold the value.
func TestTheClaudeSignInIsRefusedOverItsMost(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	const variable = "CLAUDE_CODE_OAUTH_TOKEN"
	fits := strings.Repeat("k", claudeSignInMost-len(variable+"=\n"))
	must(t, d.keepClaudeSignIn(variable, fits))
	over := fits + "x"
	err := d.keepClaudeSignIn(variable, over)
	if !errors.Is(err, store.ErrLimit) {
		t.Fatal("a sign-in over the most was not refused for the store's limit")
	}
	if strings.Contains(err.Error(), "kkkk") {
		t.Fatal("the refusal holds the value")
	}
	if b, rerr := os.ReadFile(d.claudeSignInFile()); rerr != nil || len(b) != claudeSignInMost {
		t.Fatalf("the sign-in before did not stay: %d bytes", len(b))
	}
}

// secret-ops.json keeps a last operation of secretOpMost bytes, and not one over it.
func TestASecretsLastOperationOverItsMostIsNotKept(t *testing.T) {
	t.Parallel()
	op := func(place int) wire.SecretOperation {
		p := strings.Repeat("p", place)
		return wire.SecretOperation{Name: "N", What: wire.SecretOperationWhatRead, Place: &p, At: wire.DateOf(time.Unix(0, 0))}
	}
	base := len(must2(t, op(0))) + len("N")
	last := map[string]wire.SecretOperation{"N": op(secretOpMost - base)}
	if n := boundSecretOps(last); n != 0 || len(last) != 1 {
		t.Fatalf("an operation at its most: %d dropped", n)
	}
	last["N"] = op(secretOpMost - base + 1)
	if n := boundSecretOps(last); n != 1 || len(last) != 0 {
		t.Fatalf("an operation over its most: %d dropped", n)
	}
}

func must2(t *testing.T, op wire.SecretOperation) []byte {
	t.Helper()
	b, err := json.Marshal(op)
	must(t, err)
	return b
}

// alive.json over aliveMost is not written, and the one before stays.
func TestAliveOverItsMostKeepsTheOneBefore(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	small := daemonAlive{LastAlive: wire.DateOf(time.Unix(100, 0))}
	d.writeAlive(small)
	big := daemonAlive{LastAlive: wire.DateOf(time.Unix(200, 0)), PickUp: map[wire.UUID]wire.AgentState{}}
	for i := 0; i < aliveMost/40; i++ {
		big.PickUp[wire.UUID(fmt.Sprintf("%08d-0000-4000-8000-000000000000", i))] = wire.AgentStateRunning
	}
	d.writeAlive(big)
	if got := d.readAlive(); got == nil || got.LastAlive != small.LastAlive || len(got.PickUp) != 0 {
		t.Fatalf("alive.json: %+v", got)
	}
}

// The self-update settings, suspended.json and the checks' suggestions are refused over their most.
func TestTheOneRecordFilesAreRefusedOverTheirMost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	settings := filepath.Join(dir, "self-update.json")
	fits := selfUpdateSettings{RolledBack: strings.Repeat("1", selfUpdateSettingsMost-len(`{"autoUpdate":false,"rolledBack":""}`))}
	must(t, writeSelfUpdateSettings(settings, fits))
	fits.RolledBack += "1"
	refusedForTheLimit(t, writeSelfUpdateSettings(settings, fits))

	suspended := filepath.Join(dir, userSuspendedFile)
	must(t, keepSuspension(suspended, make([]byte, userSuspendedMost)))
	refusedForTheLimit(t, keepSuspension(suspended, make([]byte, userSuspendedMost+1)))
	if b, err := os.ReadFile(suspended); err != nil || len(b) != userSuspendedMost {
		t.Fatalf("suspended.json before did not stay: %d bytes", len(b))
	}

	home := t.TempDir()
	must(t, writeChecks(home, checksRecord{Suggestions: []checkSuggestion{{Title: "t"}}}))
	refusedForTheLimit(t, writeChecks(home, checksRecord{Suggestions: []checkSuggestion{{Title: strings.Repeat("t", checksMost)}}}))
	if got := readChecks(home); len(got.Suggestions) != 1 || got.Suggestions[0].Title != "t" {
		t.Fatalf("suggestions.json before did not stay: %d", len(got.Suggestions))
	}
}

// The set-up reports' .sent offset is one number: the largest is within its 20 bytes.
func TestTheSentOffsetIsWithinItsMost(t *testing.T) {
	t.Parallel()
	if n := len(strconv.FormatInt(math.MaxInt64, 10)); n > 20 {
		t.Fatalf("the largest offset is %d bytes", n)
	}
}
