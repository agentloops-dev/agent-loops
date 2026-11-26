package hostsetup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Root set-up (#7154): the installer reads what the first boot's root.sh left.

// rootRun is a run of the installer, joined already, on a machine whose first boot wrote
// root.sh, with root.log and root.exit as given ("" for none).
func rootRun(t *testing.T, log, exit string, deps func(*Deps)) ([]wire.MachineSetupReport, Layout, error) {
	t.Helper()
	o, l := options(t)
	must(t, os.MkdirAll(l.Root, 0o700))
	must(t, os.WriteFile(filepath.Join(l.Root, "control-host.json"), []byte("{}"), 0o600))
	started := 1
	d := fakeHost(t, l, &started)
	must(t, os.WriteFile(filepath.Join(d.RootFolder, "root.sh"), []byte("apt-get install -y gh\n"), 0o600))
	if log != "" {
		must(t, os.WriteFile(filepath.Join(d.RootFolder, "root.log"), []byte(log), 0o600))
	}
	if exit != "" {
		must(t, os.WriteFile(filepath.Join(d.RootFolder, "root.exit"), []byte(exit), 0o600))
	}
	if deps != nil {
		deps(&d)
	}
	err := Run(context.Background(), o, d)
	return reports(t, l), l, err
}

func steps(rs []wire.MachineSetupReport) []wire.MachineSetupStepID {
	var out []wire.MachineSetupStepID
	for _, r := range rs {
		if !slices.Contains(out, r.Step) {
			out = append(out, r.Step)
		}
	}
	return out
}

func TestNoRootScriptIsNoRootStep(t *testing.T) {
	o, l := options(t)
	must(t, os.MkdirAll(l.Root, 0o700))
	must(t, os.WriteFile(filepath.Join(l.Root, "control-host.json"), []byte("{}"), 0o600))
	started := 1
	if err := Run(context.Background(), o, fakeHost(t, l, &started)); err != nil {
		t.Fatal(err)
	}
	rs := reports(t, l)
	if lastOf(rs, wire.MachineSetupStepIDRoot) != nil || lastOf(rs, wire.MachineSetupStepIDRuntimes).State != wire.MachineSetupStateDone {
		t.Errorf("%v", steps(rs))
	}
	if _, err := os.Stat(filepath.Join(l.Root, RootReadyFile)); err == nil {
		t.Error("the ready file was made with no root.sh")
	}
}

func TestAGoodRootScriptIsDoneBeforeRuntimesAndProjectsBegin(t *testing.T) {
	rs, l, err := rootRun(t, "Setting up gh\n", "0\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	root := lastOf(rs, wire.MachineSetupStepIDRoot)
	if root == nil || root.State != wire.MachineSetupStateDone || *root.Detail != "root.sh · exit 0" {
		t.Fatalf("%+v", root)
	}
	order := steps(rs)
	at := slices.Index(order, wire.MachineSetupStepIDRoot)
	if at < 0 || at > slices.Index(order, wire.MachineSetupStepIDRuntimes) || at > slices.Index(order, wire.MachineSetupStepIDProjects) ||
		at < slices.Index(order, wire.MachineSetupStepIDCompare) {
		t.Errorf("the order is %v", order)
	}
	if _, err := os.Stat(filepath.Join(l.Root, RootReadyFile)); err != nil {
		t.Errorf("no ready file for the first boot's restart: %v", err)
	}
}

func TestAFailedRootScriptStopsTheRunWithItsLastLines(t *testing.T) {
	log := "one\ntwo\nthree\nfour\nfive\nE: Unable to locate package build-esential\n"
	rs, _, err := rootRun(t, log, "100\n", nil)
	if err == nil {
		t.Fatal("no error")
	}
	root := lastOf(rs, wire.MachineSetupStepIDRoot)
	if root == nil || root.State != wire.MachineSetupStateFailed || *root.Error != "root.sh ended with exit 100" {
		t.Fatalf("%+v", root)
	}
	if !strings.HasSuffix(*root.Detail, "E: Unable to locate package build-esential") || strings.Contains(*root.Detail, "one") {
		t.Errorf("the last lines are %q", *root.Detail)
	}
	if lastOf(rs, wire.MachineSetupStepIDRuntimes) != nil || lastOf(rs, wire.MachineSetupStepIDProjects) != nil {
		t.Errorf("steps after it began: %v", steps(rs))
	}
}

func TestARootScriptPastItsLimitSaysSo(t *testing.T) {
	for _, code := range []string{"124", "137"} {
		rs, _, _ := rootRun(t, "sleeping\n", code, nil)
		if root := lastOf(rs, wire.MachineSetupStepIDRoot); root.State != wire.MachineSetupStateFailed ||
			*root.Error != "root.sh passed its 10 minutes and was stopped" {
			t.Errorf("%s: %+v", code, root)
		}
	}
}

func TestARootScriptWithNoResultFailsAfterTheWait(t *testing.T) {
	rs, _, err := rootRun(t, "rebooting\n", "", func(d *Deps) { d.RootWait = 20 * time.Millisecond })
	if err == nil {
		t.Fatal("no error")
	}
	root := lastOf(rs, wire.MachineSetupStepIDRoot)
	if root.State != wire.MachineSetupStateFailed || !strings.Contains(*root.Error, "no result after") ||
		!strings.Contains(*root.Error, "restarts the machine is not supported") {
		t.Errorf("%+v", root)
	}
}

func TestTheRootStepSaysTheLastLineAsItRunsAndWaitsForTheResult(t *testing.T) {
	var folder string
	rs, _, err := rootRun(t, "Setting up build-essential (12.10ubuntu1)…\n", "", func(d *Deps) {
		folder = d.RootFolder
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = os.WriteFile(filepath.Join(folder, "root.exit"), []byte("0\n"), 0o600)
		}()
	})
	if err != nil {
		t.Fatal(err)
	}
	said := false
	for _, r := range rs {
		said = said || r.Step == wire.MachineSetupStepIDRoot && r.State == wire.MachineSetupStateRunning &&
			r.Detail != nil && *r.Detail == "Setting up build-essential (12.10ubuntu1)…"
	}
	if !said || lastOf(rs, wire.MachineSetupStepIDRoot).State != wire.MachineSetupStateDone {
		t.Errorf("%+v", rs)
	}
}
