import assert from "node:assert/strict";
import { test } from "node:test";
import { flakyUnderLoad, quarantined } from "./flaky.mjs";

test("a flaky test runs outside CI and is skipped in CI", () => {
  assert.equal(quarantined({}), false);
  assert.equal(quarantined({ CI: "true" }), true);
  assert.equal(quarantined({ AGENTS_QUARANTINE: "1" }), true);
  assert.equal(quarantined({ AGENTS_QUARANTINE: "0" }), false);
  assert.equal(quarantined({ CI: "true", AGENTS_RUN_FLAKY: "1" }), false);
});

test("the skip reason names the bug", () => {
  assert.equal(flakyUnderLoad("#1234", {}), false);
  assert.equal(flakyUnderLoad("#1234", { CI: "true" }), "flaky under load; quarantined in CI (#1234)");
});
