// A machine's self-update state on its page (#6114), in the words of SelfUpdateWordsTests.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const w = await load("src/model/selfUpdate.ts");
const update = (state, o = {}) => ({ autoUpdate: true, state, running: "0.90.0", release: "0.91.0", ...o });

test("each state has its line", () => {
  assert.equal(w.updateLine(update("upToDate")), "Up to date.");
  assert.equal(w.updateLine(update("off")), "Off. Your control plane runs 0.91.0.");
  assert.equal(w.updateLine(update("off", { release: undefined })), "Off.");
  assert.equal(w.updateLine(update("downloading")), "Downloading 0.91.0…");
  assert.equal(w.updateLine(update("waiting")), "0.91.0 is ready. It installs when no agent is in a turn.");
  assert.equal(w.updateLine(update("restarting")), "Installed 0.91.0. Restarting…");
  assert.equal(w.updateLine(update("failed", { message: "No route." })), "No route.");
  assert.equal(w.updateLine(update("cannot")), "Agent Loops cannot update itself.");
  assert.equal(w.updateLine(update("new")), "Up to date.", "a state from a newer machine");
});

test("the value is on only when it updates itself, and only a failure is red", () => {
  assert.equal(w.updateValue(update("upToDate")), "On");
  assert.equal(w.updateValue(update("off", { autoUpdate: false })), "Off");
  assert.equal(w.updateValue(update("cannot")), "Off");
  assert.equal(w.updateFailed(update("failed")), true);
  assert.equal(w.updateFailed(update("cannot")), false);
  assert.equal(w.updateNoAnswer("devbox"), "devbox can’t be asked about its updates now.");
});
