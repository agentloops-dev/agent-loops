// Drafts kept across a reload (#254), bounded: a count, an age, and big attachments let go.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

class Memory {
  items = new Map();
  getItem(key) { return this.items.get(key) ?? null; }
  setItem(key, value) { this.items.set(key, String(value)); }
  removeItem(key) { this.items.delete(key); }
}

const { Drafts, draftsKept, draftAge, draftsFull, attachmentsKept, newSessionDraftKey } = await load("src/model/drafts.ts");
const { promptPlaceholder, willQueue } = await load("src/model/promptWords.ts");

test("a draft comes back on the next visit; an emptied or sent one does not", () => {
  const storage = new Memory();
  const first = new Drafts(storage);
  first.set("mac|a", { text: "half a thought", attachments: [] });
  first.set("mac|b", { text: "gone", attachments: [] });
  first.delete("mac|b");
  first.set("mac|c", { text: "", attachments: [] });
  const next = new Drafts(storage);
  assert.deepEqual(next.get("mac|a"), { text: "half a thought", attachments: [] });
  assert.equal(next.get("mac|b"), undefined);
  assert.equal(next.get("mac|c"), undefined);
});

test("a full store refuses a new draft and says so once; room saves it", () => {
  const storage = new Memory();
  const said = [];
  const drafts = new Drafts(storage, (sentence) => said.push(sentence));
  for (let i = 0; i < draftsKept; i++) drafts.set(`mac|${i}`, { text: `draft ${i}`, attachments: [] });
  drafts.set("mac|0", { text: "edited", attachments: [] });
  assert.deepEqual(said, [], "an edit of a kept draft is not refused");
  drafts.set("mac|new", { text: "on", attachments: [] });
  drafts.set("mac|new", { text: "one more", attachments: [] });
  assert.deepEqual(said, [draftsFull]);
  assert.equal(drafts.get("mac|new").text, "one more", "held while the page is open");
  const next = new Drafts(storage);
  assert.equal(next.get("mac|new"), undefined);
  assert.equal(next.get("mac|0").text, "edited");
  assert.equal(next.get("mac|1").text, "draft 1", "nothing made room for it");
  drafts.delete("mac|1");
  assert.equal(new Drafts(storage).get("mac|new").text, "one more");
});

test("a big attachment keeps its words only", () => {
  const storage = new Memory();
  const drafts = new Drafts(storage);
  const big = [{ id: "x", displayName: "big.png", block: { type: "image", mimeType: "image/png", data: "A".repeat(attachmentsKept) } }];
  drafts.set("mac|big", { text: "with a picture", attachments: big });
  assert.deepEqual(new Drafts(storage).get("mac|big"), { text: "with a picture", attachments: [] });
  assert.equal(drafts.get("mac|big").attachments.length, 1, "held whole while the page is open");
});

test("a draft goes 30 days after its last edit: at the start, on write and in the sweep (#6793)", () => {
  assert.equal(draftAge, 30 * 24 * 60 * 60 * 1000);
  const storage = new Memory();
  let now = 1_000_000;
  const open = () => new Drafts(storage, undefined, () => now);
  const keys = () => JSON.parse(storage.getItem("agents.drafts")).map(([key]) => key);
  const drafts = open();
  drafts.set("mac|old", { text: "old", attachments: [] });
  drafts.set("mac|edited", { text: "a", attachments: [] });
  now += draftAge - 1;
  drafts.set("mac|edited", { text: "b", attachments: [] });
  assert.equal(drafts.get("mac|old").text, "old");
  now += 1;
  assert.equal(drafts.get("mac|old"), undefined);
  const kept = storage.getItem("agents.drafts");
  drafts.sweep();
  assert.deepEqual(keys(), ["mac|edited"], "the sweep");
  storage.setItem("agents.drafts", kept);
  open();
  assert.deepEqual(keys(), ["mac|edited"], "the start");
  storage.setItem("agents.drafts", kept);
  drafts.set("mac|b", { text: "b", attachments: [] });
  assert.deepEqual(keys(), ["mac|edited", "mac|b"], "a write");
});

test("the sweep keeps what another tab saved; a move is not an edit (#6793)", () => {
  const storage = new Memory();
  let now = 1_000_000;
  const open = () => new Drafts(storage, undefined, () => now);
  const one = open();
  one.set("mac|a", { text: "a", attachments: [] });
  const two = open();
  two.move("mac|a", "mac|moved");
  now += draftAge - 1;
  two.set("mac|b", { text: "b", attachments: [] });
  now += 1;
  one.sweep();
  assert.deepEqual(JSON.parse(storage.getItem("agents.drafts")).map(([key]) => key), ["mac|b"]);
});

test("a draft kept before drafts had a time ages from when it is first read (#6793)", () => {
  const storage = new Memory();
  storage.setItem("agents.drafts", JSON.stringify([["mac|a", { text: "a", attachments: [] }]]));
  let now = 1_000_000;
  const open = () => new Drafts(storage, undefined, () => now);
  assert.equal(open().get("mac|a").text, "a");
  now += draftAge - 1;
  assert.equal(open().get("mac|a").text, "a");
  now += 1;
  assert.equal(open().get("mac|a"), undefined);
});

test("an unreadable store starts empty; no store keeps drafts in memory", () => {
  const storage = new Memory();
  storage.setItem("agents.drafts", "{not json");
  assert.equal(new Drafts(storage).get("mac|a"), undefined);
  const memory = new Drafts(undefined);
  memory.set("mac|a", { text: "here", attachments: [] });
  assert.equal(memory.get("mac|a").text, "here");
});

test("the empty field's words, and when what is typed waits (PromptWords)", () => {
  assert.equal(promptPlaceholder(undefined), "What do you want to do?");
  assert.equal(promptPlaceholder({ state: "running" }), "What do you want to do next?");
  assert.equal(promptPlaceholder({ state: "finished" }), "What do you want to do?");
  assert.equal(promptPlaceholder({ state: "archived" }), "Say what next, and this comes back");
  assert.equal(willQueue({ state: "waitingOnUser" }), true);
  assert.equal(willQueue({ state: "finished", queuedPrompts: [{}] }), true);
  assert.equal(willQueue({ state: "finished" }), false);
});

test("a forget clears every draft, held and kept (#3670)", () => {
  const storage = new Memory();
  const drafts = new Drafts(storage);
  drafts.set("mac|a", { text: "to the old control plane", attachments: [] });
  drafts.clear();
  assert.equal(drafts.get("mac|a"), undefined);
  assert.equal(new Drafts(storage).get("mac|a"), undefined);
  assert.equal(storage.getItem("agents.drafts"), null);
});

test("New Session's draft is the project's, so another machine's folder of it has the same words (#5376)", () => {
  assert.equal(newSessionDraftKey("mac", "file:///a/", "p1"), newSessionDraftKey("box", "file:///home/a/", "p1"));
  assert.notEqual(newSessionDraftKey("mac", "file:///a/", "p1"), newSessionDraftKey("mac", "file:///a/", "p2"));
  assert.equal(newSessionDraftKey("mac", "file:///a/", undefined), "new|mac|file:///a/");
  assert.notEqual(newSessionDraftKey("mac", "file:///a/", undefined), newSessionDraftKey("box", "file:///a/", undefined));
});

test("a folder's old draft moves to its project's, never over one there (#5376)", () => {
  const storage = new Memory();
  const drafts = new Drafts(storage);
  drafts.set("new|mac|a", { text: "from before", attachments: [] });
  drafts.move("new|mac|a", "new|project:p1");
  assert.deepEqual(drafts.get("new|project:p1"), { text: "from before", attachments: [] });
  assert.equal(drafts.get("new|mac|a"), undefined);
  drafts.set("new|box|a", { text: "older", attachments: [] });
  drafts.move("new|box|a", "new|project:p1");
  assert.equal(drafts.get("new|project:p1").text, "from before");
  assert.equal(new Drafts(storage).get("new|project:p1").text, "from before");
});

test("largest record: one draft too large for the store does not stop the others being saved, and is said (#6908)", { skip: "#7199" }, () => {
  class Quota extends Memory {
    setItem(key, value) {
      if (value.length > 5 * 1024 * 1024) throw new Error("QuotaExceededError");
      super.setItem(key, value);
    }
  }
  const storage = new Quota();
  const said = [];
  const drafts = new Drafts(storage, (sentence) => said.push(sentence));
  drafts.set("mac|a", { text: "first", attachments: [] });
  drafts.set("mac|big", { text: "x".repeat(6 * 1024 * 1024), attachments: [] });
  drafts.set("mac|a", { text: "edited", attachments: [] });
  assert.equal(new Drafts(storage).get("mac|a").text, "edited");
  assert.equal(said.length, 1);
});
