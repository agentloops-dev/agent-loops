package repoalias

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

var t0 = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

func reset(t *testing.T) {
	t.Helper()
	Set(nil)
	t.Cleanup(func() { Set(nil) })
}

// #4957: a ref that names the old owner is read by the new name; others are as they were.
func TestARefIsReadByTheNameNow(t *testing.T) {
	reset(t)
	// The repo's own moves: to the organisation (#4929), then the new name (#5296).
	if !Add("github", "Alexec/Agents", "agentloops-dev/Agents", t0) || !Add("github", "agentloops-dev/agents", "agentloops-dev/agent-loops", t0) {
		t.Fatal("not added")
	}
	if Add("github", "alexec/agents", "agentloops-dev/agents", t0) {
		t.Fatal("the same alias was added again")
	}
	for in, want := range map[string]string{
		"github:pr:alexec/agents#4702":              "github:pr:agentloops-dev/agent-loops#4702",
		"github:repo:#alexec/agents":                "github:repo:#agentloops-dev/agent-loops",
		"agents:session:alexec/agents#abc":          "agents:session:agentloops-dev/agent-loops#abc",
		"gitlab:mr:alexec/agents#1":                 "gitlab:mr:alexec/agents#1",
		"github:pr:alexec/other#1":                  "github:pr:alexec/other#1",
		"github:pr:agentloops-dev/agents#4702":      "github:pr:agentloops-dev/agent-loops#4702",
		"github:pr:agentloops-dev/agent-loops#4702": "github:pr:agentloops-dev/agent-loops#4702",
	} {
		r, err := wire.ParseRef(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := Ref(r).String(); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	if !SameRepo("github", "alexec/Agents", "agentloops-dev/agent-loops") || !SameRepo("github", "agentloops-dev/Agents", "agentloops-dev/agent-loops") || SameRepo("github", "alexec/agents", "alexec/other") {
		t.Error("SameRepo")
	}
	if got := Topic("github", "Alexec/Other"); got != "Alexec/Other" {
		t.Errorf("a repo that did not move: %q", got)
	}
}

// A repo that moved twice is read by its last name; a move back removes the alias the other way.
func TestAChainOfMovesAndAMoveBack(t *testing.T) {
	reset(t)
	Add("github", "a/r", "b/r", t0)
	Add("github", "b/r", "c/r", t0)
	if got := Topic("github", "a/r"); got != "c/r" {
		t.Fatalf("a/r: %q", got)
	}
	Add("github", "c/r", "b/r", t0)
	if got := Topic("github", "b/r"); got != "b/r" {
		t.Fatalf("after the move back, b/r: %q", got)
	}
	if got := Topic("github", "a/r"); got != "b/r" {
		t.Fatalf("after the move back, a/r: %q", got)
	}
	for _, bad := range [][2]string{{"a/r", "a/r"}, {"a", "b/r"}, {"a/r", "b:r"}, {"", "b/r"}} {
		if Add("github", bad[0], bad[1], t0) {
			t.Errorf("%v was added", bad)
		}
	}
}

// #6716: past the count the oldest alias goes, and a name too long for its entry is not kept.
func TestTheBookIsBounded(t *testing.T) {
	reset(t)
	for i := range Most + 1 {
		if !Add("github", "a/r"+strconv.Itoa(i), "b/r"+strconv.Itoa(i), t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("%d was not added", i)
		}
	}
	if len(All()) != Most || Topic("github", "a/r0") != "a/r0" || Topic("github", "a/r1") != "b/r1" {
		t.Fatalf("%d kept; a/r0 is %s", len(All()), Topic("github", "a/r0"))
	}
	if Add("github", "a/"+strings.Repeat("x", maxName), "b/r", t0) {
		t.Error("a name too long was added")
	}
}
