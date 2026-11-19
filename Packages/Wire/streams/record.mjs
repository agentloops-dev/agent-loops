// Records the scripted session in session-basic.json: `node Packages/Wire/streams/record.mjs`.
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

const agents = new Map();
const entries = [];
const steps = [];
const snapshot = () => ({
  agents: [...agents.values()].map((a) => structuredClone(a)),
  transcript: { firstIndex: 0, total: entries.length, entries: structuredClone(entries) },
});
const record = (method, params) => steps.push({ method, params: structuredClone(params), after: snapshot() });

const change = (a) => { agents.set(a.id, a); record("agent/changed", a); };
const entry = (e) => {
  const held = entries.findIndex((x) => x.id === e.id);
  if (held >= 0) entries[held] = e; else entries.push(e);
  record("agent/entry", { agentID: A, entry: e, index: held >= 0 ? held : entries.length - 1 });
};

const start = snapshot();
change(agent(A, "Fix the build", "running", 101));
entry(asked(1, "Fix the build"));
entry(said(2, "Looking", "m1"));
change(agent(B, "Second chat", "running", 103));
entry(said(2, "Looking at the build", "m1"));
entry(said(3, "It was the cache", "m2"));
agents.delete(B);
record("agent/removed", { agentID: B });
change(agent(A, "Build fixed", "finished", 108));

const deliver = (...s) => s.map((step) => ({ op: "deliver", step }));
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

const file = { format: 1, session: A, host: "mac", start, steps, scenarios };
writeFileSync(fileURLToPath(new URL("session-basic.json", import.meta.url)), JSON.stringify(file, null, 1) + "\n");
