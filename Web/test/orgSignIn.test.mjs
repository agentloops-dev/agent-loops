// The web page's org sign-in (#1002, FR-027): back from the provider with a ticket, the page opens
// s:<the pending client> with the pending key, sends signIn/ticket, and keeps that key as its own
// with the hello's control key. A suspension or an old pending sign-in is said, not paired.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { load } from "./load.mjs";
import { eventually } from "./eventually.mjs";

delete globalThis.BroadcastChannel;
const { Session } = await load("src/session.ts");
const w = await load("test/wire.ts");
const v = JSON.parse(readFileSync(new URL("./vectors.json", import.meta.url)));
const origin = "https://agents.example.com";
const control = w.unhex(v.control.public);

/** A control plane that proves itself to `pending`'s key, and answers `answer` on a sign-in connection. */
function controlPlane(pending, answer) {
  const made = [];
  return {
    made,
    open() {
      const serverNonce = crypto.getRandomValues(new Uint8Array(32));
      const socket = { sent: [], onopen: null, onmessage: null, onclose: null, onerror: null,
        async send(line) {
          const message = JSON.parse(line);
          socket.sent.push(message);
          if (message.auth) {
            const key = await w.clientKey(pending.privateKey, control, pending.client);
            const said = w.concat(w.utf8("agents-auth-v1"), serverNonce, w.fromBase64url(message.auth.nonce), w.utf8(message.auth.id), w.utf8(origin));
            const mac = new Uint8Array(await crypto.subtle.sign("HMAC", key, w.concat(w.utf8("s"), said)));
            return socket.say({ ok: { mac: w.base64url(mac) } });
          }
          if (message.method) socket.say({ jsonrpc: "2.0", id: message.id, ...answer(message) });
        },
        close(code = 1000, reason = "") { queueMicrotask(() => socket.onclose?.({ code, reason })); },
        say(object) { queueMicrotask(() => socket.onmessage?.({ data: JSON.stringify(object) })); } };
      made.push(socket);
      queueMicrotask(() => {
        socket.onopen?.({});
        socket.say({ hello: { v: 1, name: "Acme", control: w.base64url(control), nonce: w.base64url(serverNonce) } });
      });
      return socket;
    },
  };
}

async function pendingKey(made = Date.now()) {
  const { pair } = await w.makeKeyPair();
  return { privateKey: pair.privateKey, publicKey: pair.publicKey, client: w.newClientID(), made };
}

/** A session whose link is stopped when the test ends, also when an assert fails first. */
function session(t, net, pending) {
  const keys = new w.MemoryKeyStore();
  const store = new w.MemoryPendingStore();
  store.pending = pending;
  const s = new Session(keys, origin, () => net.open(), store);
  t.after(() => s.link.stop());
  return { keys, store, session: s };
}

test("a ticket pairs the pending key as this browser's, and the link opens with it", async (t) => {
  const pending = await pendingKey();
  const net = controlPlane(pending, (m) => m.method === "signIn/ticket" ? { result: { client: pending.client } } : { error: { code: -32601, message: "no" } });
  const { keys, store, session: s } = session(t, net, pending);
  s.start(null, { ticket: "T1" });
  await eventually(() => s.state.value.kind === "open" && net.made[1]?.sent[0]);
  const signIn = net.made[0].sent;
  assert.equal(signIn[0].auth.id, "s:" + pending.client);
  assert.equal(typeof signIn[0].auth.publicKey, "string");
  assert.deepEqual(signIn[1].params, { ticket: "T1", name: "A browser", kind: "browser" });
  assert.equal(keys.record.client, pending.client);
  assert.equal(keys.record.control, w.base64url(control));
  assert.equal(store.pending, null);
  assert.equal(net.made[1].sent[0].auth.id, "c:" + pending.client);
  assert.equal(s.state.value.kind, "open");
});

test("personSuspended on the ticket shows the suspended page, unpaired", async (t) => {
  const pending = await pendingKey();
  const net = controlPlane(pending, () => ({ error: { code: -32078, message: "off" } }));
  const { keys, session: s } = session(t, net, pending);
  s.start(null, { ticket: "T1" });
  await eventually(() => s.state.value.kind === "suspended");
  assert.deepEqual(s.state.value, { kind: "suspended", unpaired: true });
  assert.equal(keys.record, null);
});

test("orgSignInRequired asks for the provider it names", async (t) => {
  const pending = await pendingKey();
  const net = controlPlane(pending, () => ({ error: { code: -32077, message: "use Okta", data: { provider: "okta", name: "Okta" } } }));
  const { session: s } = session(t, net, pending);
  s.start(null, { ticket: "T1" });
  await eventually(() => s.orgRequired.value);
  assert.deepEqual(s.orgRequired.value, { id: "okta", name: "Okta", order: 0 });
  assert.equal(s.pairingError.value, null);
  assert.equal(s.state.value.kind, "unpaired");
});

test("an old pending sign-in, or the provider's error, is said and nothing is dialled for it", async (t) => {
  const old = await pendingKey(Date.now() - 11 * 60_000);
  const net = controlPlane(old, () => ({}));
  const { session: s, store } = session(t, net, old);
  s.start(null, { ticket: "T1" });
  await eventually(() => s.pairingError.value);
  assert.match(s.pairingError.value, /took too long/);
  assert.equal(net.made.length, 0);
  assert.equal(store.pending, null);
  const fresh = await pendingKey();
  const other = session(t, controlPlane(fresh, () => ({})), fresh);
  other.session.start(null, { error: "access_denied", message: "You said no." });
  await eventually(() => other.session.pairingError.value);
  assert.equal(other.session.pairingError.value, "You said no.");
});
