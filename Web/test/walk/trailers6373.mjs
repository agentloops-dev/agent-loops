// The #6373 walk's organisation policy: a scratch control plane's policy that requires Made-by and keeps
// Tokens out of commits, for the capped state of Settings ▸ Agents ▸ Policy's Commit trailers group (Mac now,
// the web page in #6374). A third argument sets other trailers instead; '{}' clears them.
//
//   node Web/test/walk/trailers6373.mjs <WEB_URL> <admin browser code> ['{"Cost":"off"}']

import { load } from "../load.mjs";

const w = await load("test/wire.ts");
const [webURL, setterCode, trailers] = process.argv.slice(2);

/** Pairs as a browser at `base`, then connects again as itself; returns a raw line client (policy998.mjs). */
async function paired(base, codeText, name) {
  const origin = new URL(base).origin;
  const url = base.replace(/^http/, "ws").replace(/\/$/, "") + "/v1/connect";
  const code = w.parseCode(codeText);
  if (!code) throw new Error("not a client code");
  const keys = new w.MemoryKeyStore();
  let socket = await opened(url, origin);
  await w.pair(linesOf(socket), code, origin, name, async () => {
    const { pair, publicRaw } = await w.makeKeyPair();
    const client = w.newClientID();
    keys.record = { privateKey: pair.privateKey, publicKey: pair.publicKey, client,
      control: w.base64url(code.controlKey), paired: "" };
    return { client, publicRaw };
  }, undefined, "browser");
  socket.close();
  socket = await opened(url, origin);
  const lines = linesOf(socket);
  await w.connect(lines, keys.record, origin, undefined, "browser");
  let id = 0;
  return {
    async ask(method, params = {}, host = null) {
      id += 1;
      const m = { jsonrpc: "2.0", id, method, params };
      socket.send(JSON.stringify(host ? { h: host, m } : { m }));
      for (;;) {
        const frame = JSON.parse(await lines.next(30_000));
        const reply = typeof frame.m === "string" ? JSON.parse(frame.m) : frame.m;
        if (reply?.id === id) return reply.error ? { error: reply.error } : { result: reply.result };
      }
    },
    close() { socket.close(); },
  };
}

function opened(url, origin) {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url, { headers: { Origin: origin } });
    socket.onopen = () => resolve(socket);
    socket.onerror = (event) => reject(new Error(`could not open ${url}: ${event.message ?? "error"}`));
  });
}

function linesOf(socket) {
  const lines = new w.Lines(socket);
  socket.onmessage = (event) => lines.push(String(event.data));
  socket.onclose = () => lines.end(new Error("closed"));
  return lines;
}

const admin = await paired(webURL, setterCode, "trailers6373 admin");
try {
  const policy = { version: 0, issued: 0, changedBy: { user: "walk" }, requires: ["host.policy/1"],
    commitTrailers: trailers ? JSON.parse(trailers) : { "Made-by": "on", Tokens: "off" } };
  const kept = await admin.ask("policy/get");
  const set = await admin.ask("policy/set", { policy, rev: kept.result?.rev });
  console.log("policy/set:", set.error ? `REFUSED ${set.error.message}` : `v${set.result.policy?.version}`);
} finally {
  admin.close();
}
