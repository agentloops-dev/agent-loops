// "Carry on" in the bar of a chat an error stopped: once for each stop, never over what is typed.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const { CarryOnOffers, carryOnFollows, carryOnStop, carryOnWords } = await load("src/model/carryOn.ts");

const agent = (state, endedReason, lastActivityAt = 1) => ({ id: "a", state, endedReason, lastActivityAt });

test("only an error stop that another turn can get past offers the words", () => {
  for (const reason of ["maxTokens", "maxTurnRequests", "processDied", "unrecognised", "runtimeError", "rateLimited"]) {
    assert.equal(carryOnFollows(reason), true, reason);
  }
  for (const reason of ["endTurn", "cancelled", "stoppedByAgent", "stoppedByAdmin", "costLimit", "daemonGone",
    "allowanceSpent", "sandboxFailed", "signInRefused", "refusal", "imported", undefined]) {
    assert.equal(carryOnFollows(reason), false, String(reason));
  }
  assert.equal(carryOnStop(agent("running", "processDied")), undefined);
  assert.equal(carryOnStop(agent("stopped", "cancelled")), undefined);
  assert.equal(carryOnStop(agent("stopped", "processDied")), "a|1");
});

test("the words come once for each stop, and never over what is typed", () => {
  const offers = new CarryOnOffers();
  const stop = carryOnStop(agent("stopped", "runtimeError", 1));
  assert.equal(offers.words(stop, "half a thought"), null);
  assert.equal(offers.words(stop, ""), carryOnWords);
  assert.equal(carryOnWords, "Carry on");
  assert.equal(offers.words(stop, ""), null);
  assert.equal(offers.words(undefined, ""), null);
  assert.equal(offers.words(carryOnStop(agent("stopped", "runtimeError", 2)), " "), carryOnWords);
});
