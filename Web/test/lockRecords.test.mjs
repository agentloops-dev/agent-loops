// Signed records in the page, against what Go signed (Fixtures/lock/records.json, from
// TestTheRecordVectors, which AgentsKit's LockRecordTests reads too).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { load } from "./load.mjs";

const w = await load("test/lockWire.ts");
const v = JSON.parse(readFileSync(new URL("../../Packages/AgentsKit/Tests/AgentsKitTests/Fixtures/lock/records.json", import.meta.url)));
const bytes = (text) => new Uint8Array(Buffer.from(text, "base64"));
const b64 = (u) => Buffer.from(u).toString("base64");
const state = await w.check(v.lock.entries);

async function why(one) {
  try {
    await w.checkRecord(state, one.record);
  } catch (e) {
    assert.ok(e instanceof w.LockRecordError, e);
    return e.failure;
  }
  return w.takesRecord(one.record, one.host, one.made) ? "" : "otherPlace";
}

test("Go's records check here, and so do its refusals", async () => {
  assert.deepEqual(Object.fromEntries(w.recordKinds), v.kinds);
  assert.ok(v.records.length > 0);
  for (const one of v.records) {
    assert.equal(b64(await w.recordData(one.record)), one.signed, one.name);
    assert.equal(await why(one), one.why, one.name);
  }
});

test("a number follows another as Go says", () => {
  assert.ok(v.steps.length > 0);
  for (const step of v.steps) assert.equal(w.seqFailure(step.had, step.seq) ?? "", step.why, `${step.seq} after ${step.had}`);
});

test("a part that is not base64 is refused", async () => {
  for (const part of ["root", "content", "key", "sig"]) {
    await assert.rejects(w.checkRecord(state, { ...v.records[0].record, [part]: "%" }), { failure: "invalid" }, part);
  }
});

test("a signature of one domain is not one of another", async () => {
  const base = v.records[0].record;
  assert.equal(v.foreign.length, 5);
  for (const one of v.foreign) {
    assert.ok(Buffer.from(one.data, "base64").toString("latin1").startsWith(one.domain + "\n"));
    assert.ok(await w.verify(bytes(base.key), bytes(one.data), bytes(one.sig)), one.domain);
    assert.ok(!(await w.verify(bytes(base.key), bytes(one.data), bytes(base.sig))), one.domain);
    await assert.rejects(w.checkRecord(state, { ...base, sig: one.sig }), { failure: "forged" }, one.domain);
  }
  assert.ok(!(await w.verifyRequest(v.request, "agents/start", "{}")));
  await assert.rejects(w.check([v.entry]), w.LockCheckError);
});

test("a record signed here checks", async () => {
  const page = await w.makeSigningKey();
  const root = await w.sign(page.pair.privateKey, page.publicRaw, { kind: "root", person: "sam", at: w.wireNow(), key: b64(page.publicRaw), clientKind: "web" });
  const mine = await w.check([root]);
  const signer = { privateKey: page.pair.privateKey, publicRaw: page.publicRaw };
  const r = await w.signRecord(signer, mine, { kind: "setup.script", subject: "setup", scope: "made", seq: 1 }, new Uint8Array([123, 125]));
  assert.equal((await w.checkRecord(mine, r)).kind, "root");
  assert.ok(w.takesRecord(r, "a", true) && !w.takesRecord(r, "a"));
  await assert.rejects(w.checkRecord(mine, { ...r, seq: 2 }), { failure: "forged" });
  const other = { kind: "setup.script", subject: "setup", scope: "all", seq: 1 };
  await assert.rejects(w.signRecord(signer, mine, other, new Uint8Array()), { failure: "invalid" });
  await assert.rejects(w.signRecord(signer, mine, { ...other, scope: "m:a" }, new Uint8Array(w.mostContent + 1)), { failure: "invalid" });
});
