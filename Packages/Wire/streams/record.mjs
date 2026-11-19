// Records the scripted sessions (session-basic.json, session-cards*.json): `node Packages/Wire/streams/record.mjs`.
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


// The cards session (#6188): a permission and a question asked and answered, a card lost to a
// daemon restart and asked again after the pick-up, and an agent removed with its card. Its
// reads hold the pending cards too (`permissions`, `elicitations`: permissions/pending and
// elicitations/pending). It is written twice: for the Mac's host, and for a second machine.
function cardsSession(host) {
  const agents = new Map();
  const permissions = new Map();
  const elicitations = new Map();
  const steps = [];
  const snapshot = () => ({
    agents: [...agents.values()].map((a) => structuredClone(a)),
    transcript: { firstIndex: 0, total: 0, entries: [] },
    permissions: [...permissions.values()].map((p) => structuredClone(p)),
    elicitations: [...elicitations.values()].map((e) => structuredClone(e)),
  });
  const record = (method, params) => steps.push({ method, params: structuredClone(params), after: snapshot() });
  const change = (a) => { agents.set(a.id, a); record("agent/changed", a); };
  const permission = (n, agentID, title) => ({
    id: uuid(200 + n), agentID, askedAt: 200 + n,
    toolCall: { toolCallID: `call_${n}`, title, kind: "edit", status: "pending", content: [], locations: [] },
    options: [{ optionID: "allow", name: "Allow", kind: "allow_once" }, { optionID: "reject", name: "Reject", kind: "reject_once" }],
  });
  const ask = (p) => { permissions.set(p.id, p); record("agent/permission", { agentID: p.agentID, requestID: p.id, request: p }); };
  const answer = (p) => { permissions.delete(p.id); record("agent/permission", { agentID: p.agentID, requestID: p.id }); };
  const question = (n, agentID) => ({ id: uuid(300 + n), agentID, askedAt: 300 + n, message: "Sign in",
    mode: { url: { _0: "https://example.com/sign-in" } }, scope: "request" });
  const form = (q) => { elicitations.set(q.id, q); record("agent/elicitation", { agentID: q.agentID, requestID: q.id, request: q }); };
  const formDown = (q) => { elicitations.delete(q.id); record("agent/elicitation", { agentID: q.agentID, requestID: q.id }); };

  const start = snapshot();
  const p1 = permission(1, A, "Write hi.txt");
  const q1 = question(1, A);
  const p2 = permission(2, A, "Write bye.txt");
  const p3 = permission(3, A, "Write bye.txt");
  const pb = permission(4, B, "Run make");
  change(agent(A, "Cards", "running", 101)); // 0
  ask(p1); // 1
  change(agent(A, "Cards", "waitingOnUser", 102)); // 2
  answer(p1); // 3
  change(agent(A, "Cards", "running", 103)); // 4
  form(q1); // 5
  change(agent(A, "Cards", "waitingOnUser", 104)); // 6
  formDown(q1); // 7
  change(agent(A, "Cards", "running", 105)); // 8
  ask(p2); // 9
  change(agent(A, "Cards", "waitingOnUser", 106)); // 10
  // The daemon restarts: the card goes with it, and nothing says so but the next read.
  permissions.delete(p2.id);
  change(agent(A, "Cards", "stopped", 107)); // 11
  change(agent(A, "Cards", "running", 108)); // 12: picked back up
  ask(p3); // 13: asked again
  change(agent(A, "Cards", "waitingOnUser", 109)); // 14
  answer(p3); // 15
  change(agent(A, "Cards", "finished", 110)); // 16
  change(agent(B, "Second chat", "running", 111)); // 17
  ask(pb); // 18
  agents.delete(B);
  permissions.delete(pb.id);
  record("agent/removed", { agentID: B }); // 19

  const deliver = (...s) => s.map((step) => ({ op: "deliver", step }));
  const all = [...Array(steps.length).keys()];
  const scenarios = [
    // A restart (step 11) is a new connection: the client hears it by its catch-up read, never alone.
    { name: "in order, nothing lost", ops: [{ op: "reconnect", read: -1 }, ...deliver(...all.slice(0, 11)), { op: "reconnect", read: 11 }, ...deliver(...all.slice(12))], expect: 19 },
    { name: "each notification twice", ops: [{ op: "reconnect", read: -1 }, ...deliver(...all.slice(0, 11).flatMap((s) => [s, s])), { op: "reconnect", read: 11 }, ...deliver(...all.slice(12).flatMap((s) => [s, s]))], expect: 19 },
    { name: "a card is shown", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1)], expect: 1 },
    { name: "a card is gone once answered", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2, 3)], expect: 3 },
    { name: "a question is shown, then gone", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2, 3, 4, 5)], expect: 5 },
    { name: "a card asked while no client was there shows after the reconnect", ops: [{ op: "reconnect", read: -1 }, ...deliver(0), { op: "drop", steps: [1, 2] }, { op: "reconnect", read: 2 }], expect: 2 },
    { name: "a card answered elsewhere while away goes after the reconnect", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2), { op: "drop", steps: [3, 4] }, { op: "reconnect", read: 4 }], expect: 4 },
    { name: "a card whose notification overlaps the read", ops: [{ op: "reconnect", read: -1 }, ...deliver(0), { op: "reconnect", read: 1, during: [1] }, ...deliver(2)], expect: 2 },
    { name: "a card raised while the read is on its way", fault: "#6291", ops: [{ op: "reconnect", read: -1 }, ...deliver(0), { op: "reconnect", read: 0, during: [1] }, ...deliver(2)], expect: 2 },
    { name: "a daemon restart while a card waits", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10), { op: "reconnect", read: 11 }], expect: 11 },
    { name: "the card asked again after the restart", ops: [{ op: "reconnect", read: -1 }, ...deliver(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10), { op: "reconnect", read: 11 }, ...deliver(12, 13, 14)], expect: 14 },
    { name: "an agent removed with its card while away", ops: [{ op: "reconnect", read: -1 }, ...deliver(...all.slice(0, 11)), { op: "reconnect", read: 11 }, ...deliver(...all.slice(12, 19)), { op: "reconnect", read: 19 }], expect: 19 },
  ];
  return { format: 1, session: A, host, start, steps, scenarios };
}

const file = { format: 1, session: A, host: "mac", start, steps, scenarios };
const write = (name, f) => writeFileSync(fileURLToPath(new URL(name, import.meta.url)), JSON.stringify(f, null, 1) + "\n");
write("session-basic.json", file);
write("session-cards.json", cardsSession("mac"));
write("session-cards-server.json", cardsSession("server-1"));
