// Labels on a machine (#5284), as MachineLabelsTests in Swift.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const l = await load("src/model/machineLabels.ts");

test("a person's labels are the ones the provider did not set", () => {
  assert.deepEqual(l.ownMachineLabels({ labels: ["arch=arm64", "gpu", "os=linux", "team=a"], autoLabels: ["os=linux", "arch=arm64"] }), ["gpu", "team=a"]);
});

test("typed labels are split and kept once", () => {
  assert.deepEqual(l.parseMachineLabels("gpu, team=a  GPU"), ["gpu", "team=a"]);
});

test("a provider key or an odd label is refused", () => {
  assert.notEqual(l.machineLabelProblem("os=linux"), null);
  assert.notEqual(l.machineLabelProblem("has/odd chars!"), null);
  assert.equal(l.machineLabelProblem("team-a"), null);
  assert.equal(l.machineLabelProblem("client=x"), null);
});

test("every label on the machines, once, sorted", () => {
  assert.deepEqual(l.allMachineLabels([{ labels: ["gpu", "os=linux"] }, { labels: ["GPU", "a"] }]), ["a", "gpu", "os=linux"]);
});
