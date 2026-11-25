// Why an agent asks to be archived (#6067), as ArchiveRequestWords.body and .detail say it.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const a = await load("src/model/archiveWords.ts");
const { applyWrite } = await load("src/model/pendingWrites.ts");
const report = { outcome: "done", message: "Shipped.", at: 1 };
const asks = (requested, more = {}) => ({ id: "a", state: "finished", endedReason: "endTurn", archiveRequest: { requested: { at: 1, ...requested } }, ...more });

test("the card says the reason, then the report, then that the work is done", () => {
  assert.equal(a.archiveBody(asks({ reason: "PR #12 merged." }, { report })), "PR #12 merged.");
  assert.equal(a.archiveBody(asks({}, { report })), "Shipped.");
  assert.equal(a.archiveBody(asks({})), a.noReport);
});

test("the mark's tooltip says why", () => {
  assert.equal(a.archiveDetail(asks({ reason: "PR #12 merged." })), "Archiving needs approval: PR #12 merged.");
  assert.equal(a.archiveDetail(asks({})), "Archiving needs approval");
});

test("archiving keeps the reason on the record, and unarchiving drops it", () => {
  const archived = applyWrite(asks({ reason: "PR #12 merged." }), { kind: "archive" });
  assert.equal(archived.archiveWhy, "PR #12 merged.");
  assert.equal(archived.archiveRequest, undefined);
  assert.equal("archiveWhy" in applyWrite(archived, { kind: "unarchive" }), false);
});
