// Plays a recorded daemon stream (Packages/Wire/streams, #6187) into a client model with drops,
// duplicates and reconnects, and says what the model holds, in the form the file's `after` is in.
// The Swift runner (StreamHarnessTests) reads the same files. Another feature's test (#6188, #6189)
// brings its own `client` and reads its own stream: see the README next to the files.
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const dir = fileURLToPath(new URL("../../Packages/Wire/streams/", import.meta.url));

/** Every recorded stream, by file name. */
export function streams() {
  return readdirSync(dir).filter((f) => f.endsWith(".json")).sort()
    .map((f) => ({ file: f, ...JSON.parse(readFileSync(dir + f, "utf8")) }));
}

/** What the daemon holds after step `k` (-1: before the first): the file's own record of it. */
export const daemonAt = (stream, k) => (k < 0 ? stream.start : stream.steps[k].after);

/**
 * Runs one scenario. `client` has: `catchUp(snapshot, during)`, which reads `snapshot` as the catch-up
 * read and hears `during` (a list of [method, params]) while it is on its way; `notify(method, params)`;
 * and `view()`, what the client holds as `{agents: [{id, title, state}], entries: [{id, text}], cards: ["kind|id|agentID"]}`
 * (`cards`: the permission and question cards, sorted; #6188).
 */
export function play(stream, scenario, client) {
  const note = (i) => [stream.steps[i].method, stream.steps[i].params];
  for (const op of scenario.ops) {
    if (op.op === "deliver") client.notify(...note(op.step));
    else if (op.op === "reconnect") client.catchUp(daemonAt(stream, op.read), (op.during ?? []).map(note));
    else if (op.op !== "drop") throw new Error(`unknown op ${op.op}`);
  }
}

/** The file's snapshot as the same view a client gives. */
export function viewOf(snapshot) {
  const text = (kind) => (kind.agentMessage ?? kind.userMessage)?.text ?? kind.userMessage?._0;
  return {
    agents: snapshot.agents.map((a) => ({ id: a.id, title: a.title, state: a.state })).sort((a, b) => a.id.localeCompare(b.id)),
    entries: snapshot.transcript.entries.map((e) => ({ id: e.id, text: text(e.kind) })),
    cards: cardsOf(snapshot.permissions ?? [], snapshot.elicitations ?? []),
  };
}

/** The cards a client holds, as the view says them: each permission and question, by its id and its agent's. */
export function cardsOf(permissions, elicitations) {
  return [...permissions.map((p) => `permission|${p.id}|${p.agentID}`), ...elicitations.map((e) => `question|${e.id}|${e.agentID}`)].sort();
}
