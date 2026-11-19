// Records the scripted sessions in this folder: `node Packages/Wire/streams/record.mjs`.
// A tiny stand-in for the daemon holds its state, makes each change, and writes the notification the
// daemon would send for it and the answer its reads would give right after (README.md, "Format").
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const uuid = (n) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`.toUpperCase();
const A = uuid(1);
const B = uuid(2);

const agent = (id, title, state, at) => ({
  id, title, cwd: "file:///fixture/project/", createdAt: 100, lastActivityAt: at, runtimeID: "claude", state,
  advertisedOptions: [], availableCommands: [], startOptions: { extraArguments: [], values: {} },
});
const said = (n, text, messageID) => ({ id: uuid(100 + n), at: 100 + n, kind: { agentMessage: { messageID, text } } });
const asked = (n, text) => ({ id: uuid(100 + n), at: 100 + n, kind: { userMessage: { _0: text } } });
const deliver = (...s) => s.map((step) => ({ op: "deliver", step }));
const range = (from, to) => Array.from({ length: to - from + 1 }, (_, i) => from + i);

/**
 * A stand-in daemon. With `shared`, the entries held at the start are written once, in `start`, and each
 * later read names how many of them it begins with (`startEntries`) and lists only the rest, so a long
 * transcript does not make the file as long times each step.
 */
function daemon({ shared = false } = {}) {
  const agents = new Map();
  const entries = [];
  const steps = [];
  let base = 0;
  const snapshot = () => {
    const transcript = { firstIndex: 0, total: entries.length, entries: structuredClone(entries.slice(base)) };
    if (base > 0) transcript.startEntries = base;
    return { agents: [...agents.values()].map((a) => structuredClone(a)), transcript };
  };
  const record = (method, params) => steps.push({ method, params: structuredClone(params), after: snapshot() });
  return {
    steps,
    snapshot,
    /** The state so far is what the daemon held before the first step. */
    begin() {
      const start = snapshot();
      if (shared) base = entries.length;
      return start;
    },
    held: (e) => entries.push(e),
    hold: (a) => agents.set(a.id, a),
    change(a) { agents.set(a.id, a); record("agent/changed", a); },
    remove(id) { agents.delete(id); record("agent/removed", { agentID: id }); },
    entry(e) {
      // A message that streams is the same entry again with more text, at the same index.
      const at = entries.findIndex((x) => x.id === e.id);
      if (at >= 0) entries[at] = e; else entries.push(e);
      record("agent/entry", { agentID: A, entry: e, index: at >= 0 ? at : entries.length - 1 });
    },
  };
}

function basic() {
  const d = daemon();
  const start = d.begin();
  d.change(agent(A, "Fix the build", "running", 101));
  d.entry(asked(1, "Fix the build"));
  d.entry(said(2, "Looking", "m1"));
  d.change(agent(B, "Second chat", "running", 103));
  d.entry(said(2, "Looking at the build", "m1"));
  d.entry(said(3, "It was the cache", "m2"));
  d.remove(B);
  d.change(agent(A, "Build fixed", "finished", 108));

  const scenarios = [
    { name: "in order, nothing lost", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2, 3, 4, 5, 6, 7)], expect: 7 },
    { name: "each notification twice", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7)], expect: 7 },
    { name: "three lost, then a reconnect", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2), { op: "drop", steps: [3, 4, 5] }, { op: "reconnect", read: 5 }, ...deliver(6, 7)], expect: 7 },
    { name: "a reconnect whose read the notifications overlap", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1), { op: "drop", steps: [2, 3, 4] }, { op: "reconnect", read: 5, during: [4, 5, 6] }, ...deliver(7)], expect: 7 },
    { name: "an agent removed while the client was away", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2, 3), { op: "drop", steps: [4, 5, 6] }, { op: "reconnect", read: 6 }, ...deliver(7)], expect: 7 },
    { name: "two reconnects in a row", fault: "#6282", ops: [{ op: "reconnect", read: -1 }, ...deliver(0), { op: "drop", steps: [1, 2] }, { op: "reconnect", read: 2 }, { op: "reconnect", read: 5 }, ...deliver(6, 7)], expect: 7 },
    { name: "the daemon sends the whole session again after a reconnect", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2, 3, 4), { op: "reconnect", read: 4, during: [0, 1, 2, 3, 4] }, ...deliver(5, 6, 7)], expect: 7 },
    { name: "a reconnect to a daemon that has gone further than the last notification", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1), { op: "reconnect", read: 7 }], expect: 7 },
    { name: "a reconnect that starts from nothing held", ops: [{ op: "reconnect", read: 4 }, ...deliver(5, 6, 7)], expect: 7 },
  ];
  return { format: 1, session: A, host: "mac", start, steps: d.steps, scenarios };
}

/**
 * A long chat (#6189): 2,000 turns held before it opens, some with large tool output in a code block,
 * then a reply that streams in chunks, a daemon restart in the middle of a second reply, and more turns
 * after it. Each client model must hold each turn once, in order, with each reply's last chunk.
 */
function long() {
  const d = daemon({ shared: true });
  const turns = 1_000;
  d.hold(agent(A, "A long chat", "running", 100));
  const output = (n) => "```\n" + Array.from({ length: 60 }, (_, i) => `line ${i} of the output of tool call ${n}`).join("\n") + "\n```";
  for (let n = 0; n < turns; n++) {
    d.held(asked(2 * n, `Question ${n}`));
    d.held(said(2 * n + 1, n % 100 === 99 ? `Ran the tests:\n\n${output(n)}` : `Answer ${n}`, `h${n}`));
  }
  const start = d.begin();
  const next = 2 * turns;
  const reply = ["The", "The cache", "The cache was", "The cache was stale.", "The cache was stale. Cleared it."];

  d.entry(asked(next, "Why did it fail?")); // 0
  for (const text of reply) d.entry(said(next + 1, text, "s1")); // 1-5
  d.entry(asked(next + 2, "Run it again")); // 6
  d.entry(said(next + 3, "Running", "s2")); // 7
  d.entry(said(next + 3, "Running the suite", "s2")); // 8
  // The daemon restarts: the agent stops, comes back, and its reply goes on where it was.
  d.change(agent(A, "A long chat", "stopped", 300)); // 9
  d.change(agent(A, "A long chat", "running", 301)); // 10
  d.entry(said(next + 3, "Running the suite: 412 passed", "s2")); // 11
  d.entry(asked(next + 4, "Thanks")); // 12
  d.entry(said(next + 5, "Done", "s3")); // 13
  d.change(agent(A, "A long chat", "finished", 310)); // 14
  const last = d.steps.length - 1;

  const open = { op: "reconnect", read: -1 };
  // A client keeps a chat's end, not all of it (AgentsModel.entriesKept, the web's entriesKept: 600).
  const scenarios = [
    { name: "a long chat, in order, nothing lost", ops: [open, ...deliver(...range(0, last))], expect: last },
    { name: "a long chat, each notification twice", ops: [open, ...range(0, last).flatMap((s) => deliver(s, s))], expect: last },
    { name: "a chunk lost: the next chunk holds its text", ops: [open, ...deliver(0, 1, 2), { op: "drop", steps: [3] }, ...deliver(...range(4, last))], expect: last },
    { name: "the last chunk lost, then a reconnect", ops: [open, ...deliver(0, 1, 2, 3, 4), { op: "drop", steps: [5] }, { op: "reconnect", read: 5 }, ...deliver(...range(6, last))], expect: last },
    { name: "old chunks heard after a read that holds the newer text", ops: [open, ...deliver(0, 1), { op: "drop", steps: [2, 3, 4] }, { op: "reconnect", read: 4, during: [2, 3, 4] }, ...deliver(...range(5, last))], expect: last },
    { name: "a daemon restart in the middle of a reply", ops: [open, ...deliver(...range(0, 8)), { op: "drop", steps: [9, 10] }, { op: "reconnect", read: 10 }, ...deliver(...range(11, last))], expect: last },
    { name: "a restarted daemon sends its last entries again", ops: [open, ...deliver(...range(0, 8)), { op: "drop", steps: [9, 10, 11] }, { op: "reconnect", read: 11, during: [6, 7, 8, 11] }, ...deliver(...range(12, last))], expect: last },
    { name: "a restart while the client was away the whole time", ops: [open, { op: "drop", steps: range(0, 12) }, { op: "reconnect", read: 12 }, ...deliver(13, 14)], expect: last },
    { name: "a long chat opened at the end", ops: [{ op: "reconnect", read: last }], expect: last },
  ];
  return { format: 1, session: A, host: "mac", start, steps: d.steps, scenarios: scenarios.map((s) => ({ ...s, tail: 400 })) };
}

const write = (name, file, indent) =>
  writeFileSync(fileURLToPath(new URL(name, import.meta.url)), JSON.stringify(file, null, indent) + "\n");
write("session-basic.json", basic(), 1);
// No indent: the long chat's 2,000 turns would be 16,000 lines.
write("session-long.json", long(), 0);
