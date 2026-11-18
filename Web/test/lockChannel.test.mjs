// Requests checked end to end in the page (#3492, specs/3399-machine-lock D6): the keys, MACs
// and signatures are Go's (Fixtures/lock/channel.json, from TestTheChannelVectors), and the
// link opens a channel to a machine of its lock, MACs each call, takes an answer only with the
// machine's MAC, and signs a prompt for a machine that is not there so it can be kept.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { load } from "./load.mjs";
import { eventually } from "./eventually.mjs";

const w = await load("test/lockWire.ts");
const v = JSON.parse(readFileSync(new URL("../../Packages/AgentsKit/Tests/AgentsKitTests/Fixtures/lock/channel.json", import.meta.url)));
const bytes = (text) => new Uint8Array(Buffer.from(text, "base64"));
const b64 = (u) => Buffer.from(u).toString("base64");
const b64url = (u) => Buffer.from(u).toString("base64url");

/** An ECDH private key from Go's raw scalar and its public key. */
async function ecdhPrivate(raw, publicRaw) {
  const jwk = { kty: "EC", crv: "P-256", d: b64url(raw), x: b64url(publicRaw.slice(1, 33)), y: b64url(publicRaw.slice(33, 65)) };
  return crypto.subtle.importKey("jwk", jwk, { name: "ECDH", namedCurve: "P-256" }, false, ["deriveBits"]);
}

test("Go's hellos, keys, MACs and signed request check here", async () => {
  const clientEph = bytes(v.hello.ephemeral);
  const machineEph = bytes(v.reply.ephemeral);
  assert.ok(await w.verifyHello(bytes(v.clientKey), w.clientHelloData(clientEph), bytes(v.hello.sig)));
  assert.ok(await w.verifyHello(bytes(v.machineKey), w.machineHelloData(clientEph, machineEph, bytes(v.clientKey)), bytes(v.reply.sig)));
  const mine = await ecdhPrivate(bytes(v.clientEphemeralPrivate), clientEph);
  const peer = await crypto.subtle.importKey("raw", machineEph, { name: "ECDH", namedCurve: "P-256" }, false, []);
  const shared = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: peer }, mine, 256));
  const [client, machine] = await w.channelKeys(shared, clientEph, machineEph);
  assert.equal(b64(client), v.clientSends);
  assert.equal(b64(machine), v.machineSends);
  const sending = await w.Channel.of(client, machine, 41);
  assert.equal(await sending.seal(v.message), v.sealed);
  const hearing = await w.Channel.of(machine, machine, 0, 0);
  const answers = await w.Channel.of(client, machine);
  assert.equal(await answers.open(v.answerSealed), v.answer);
  await assert.rejects(answers.open(v.answerSealed), w.ChannelError);
  await assert.rejects(hearing.open(v.sealed), w.ChannelError);
  assert.ok(await w.verifyRequest(v.request.lock, v.request.method, v.request.params));
  assert.ok(!(await w.verifyRequest(v.request.lock, v.request.method, v.request.params.replace("hi", "ho"))));
});

/** A person's lock with this page and the machine k3v9, and the machine's own side of a hello. */
async function lockWithMachine() {
  const page = await w.makeSigningKey();
  const machine = await w.makeSigningKey();
  const root = await w.sign(page.pair.privateKey, page.publicRaw, { kind: "root", person: "sam", at: w.wireNow(), key: b64(page.publicRaw), clientKind: "web" });
  let state = await w.check([root]);
  const add = await w.sign(page.pair.privateKey, page.publicRaw, { kind: "addMachine", person: "sam", previous: b64(state.head), at: w.wireNow(),
    key: b64(machine.publicRaw), sealingKey: b64(machine.publicRaw), id: "k3v9" });
  state = await w.check([root, add]);
  /** The machine's answer to a lock/hello, and its side of the channel. */
  async function answer(hello) {
    const eph = await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, false, ["deriveBits"]);
    const ephRaw = new Uint8Array(await crypto.subtle.exportKey("raw", eph.publicKey));
    const clientEph = bytes(hello.ephemeral);
    const sig = new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, machine.pair.privateKey,
      w.machineHelloData(clientEph, ephRaw, bytes(hello.key))));
    const peer = await crypto.subtle.importKey("raw", clientEph, { name: "ECDH", namedCurve: "P-256" }, false, []);
    const shared = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: peer }, eph.privateKey, 256));
    const [client, mine] = await w.channelKeys(shared, clientEph, ephRaw);
    return { reply: { key: b64(machine.publicRaw), ephemeral: b64(ephRaw), sig: b64(sig) }, channel: await w.Channel.of(mine, client) };
  }
  return { source: async () => ({ signer: { privateKey: page.pair.privateKey, publicRaw: page.publicRaw }, state }), answer, page };
}

function socket() {
  const s = { raw: [], onopen: null, onmessage: null, onclose: null, onerror: null,
    send(line) { this.raw.push(line); },
    close() {},
    line(text) { this.onmessage?.({ data: text }); } };
  return s;
}

/** A link that is open, and stopped when the test ends, also when an assert fails first. */
async function openLink(t, source) {
  const keys = new w.MemoryKeyStore();
  keys.record = { privateKey: {}, publicKey: {}, client: "C", control: "K", paired: "" };
  const s = socket();
  const l = new w.Link({ url: "ws://localhost:1/v1/connect", origin: "http://localhost:1", keys, open: () => { queueMicrotask(() => s.onopen?.({})); return s; },
    backoff: [0.01], heartbeat: { every: 60_000, within: 1_000 }, random: () => 0, authenticate: async () => ({ name: "test" }) });
  l.setLock(source);
  t.after(() => l.stop());
  l.start();
  await eventually(() => l.state.kind === "open");
  return { l, s };
}

const messageOf = (line) => line.slice(line.indexOf(',"m":') + 5, -1);

test("a call to a machine of the lock goes on a channel, with a MAC, and its answer is taken only with the machine's", async (t) => {
  const lock = await lockWithMachine();
  const { l, s } = await openLink(t, lock.source);
  const listed = l.call("folders/list", {}, "k3v9");
  await eventually(() => s.raw.length === 1);
  // First the hello, with no MAC.
  const hello = JSON.parse(s.raw[0]);
  assert.equal(hello.h, "k3v9");
  assert.equal(hello.m.method, "lock/hello");
  const { reply, channel } = await lock.answer(hello.m.params);
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id: hello.m.id, result: reply } }));
  await eventually(() => s.raw.length === 2);
  // Then the call, with the channel's MAC.
  const sent = messageOf(s.raw[1]);
  assert.ok(w.sealed(sent), sent);
  const request = JSON.parse(await channel.open(sent));
  assert.equal(request.method, "folders/list");
  // The control plane answers in the machine's name: not taken.
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id: request.id, result: [{ forged: true }] } }));
  await assert.rejects(listed, (error) => error instanceof w.CallFailed && error.code === -32104);
  // The machine's own answer is.
  const second = l.call("folders/list", {}, "k3v9");
  await eventually(() => s.raw.length === 3);
  const next = JSON.parse(await channel.open(messageOf(s.raw[2])));
  s.line(`{"h":"k3v9","m":${await channel.seal(`{"jsonrpc":"2.0","id":${next.id},"result":[]}`)}}`);
  assert.deepEqual(await second, []);
  // The control plane's own refusal has no MAC, and is taken as it is.
  const third = l.call("folders/list", {}, "k3v9");
  await eventually(() => s.raw.length === 4);
  const last = JSON.parse(await channel.open(messageOf(s.raw[3])));
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id: last.id, error: { code: -32090, message: "That machine is offline." } } }));
  await assert.rejects(third, (error) => error instanceof w.CallFailed && error.code === -32090);
});

test("once the channel is open, what the machine says unasked is taken only with its MAC (#3541)", async (t) => {
  const lock = await lockWithMachine();
  const { l, s } = await openLink(t, lock.source);
  const heard = [];
  l.onNotification((method, params, host) => heard.push({ method, params, host }));
  const listed = l.call("folders/list", {}, "k3v9");
  await eventually(() => s.raw.length === 1);
  // Before the hello is answered, a notification has no MAC, and is taken.
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", method: "agent/changed", params: { id: "before" } } }));
  const hello = JSON.parse(s.raw[0]);
  const { reply, channel } = await lock.answer(hello.m.params);
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id: hello.m.id, result: reply } }));
  // The control plane shows a card the machine did not raise: no MAC, not taken.
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", method: "agent/changed", params: { id: "forged" } } }));
  // The machine's own, MAC'd as a fan-out frame's copy is: taken without its lock member.
  s.line(`{"h":"k3v9","m":${await channel.seal('{"jsonrpc":"2.0","method":"agent/changed","params":{"id":"real"}}')}}`);
  // One whose bytes were changed on the way: not taken.
  s.line(`{"h":"k3v9","m":${(await channel.seal('{"jsonrpc":"2.0","method":"agent/changed","params":{"id":"realer"}}')).replace("realer", "forged")}}`);
  // The call goes once the hello is answered, after the lines before it are read.
  await eventually(() => s.raw.length === 2 && heard.length >= 2);
  assert.deepEqual(heard.map((n) => n.params.id), ["before", "real"]);
  assert.equal(heard[1].params.lock, undefined);
  // The call after them still opens in order.
  const sent = JSON.parse(await channel.open(messageOf(s.raw[1])));
  s.line(`{"h":"k3v9","m":${await channel.seal(`{"jsonrpc":"2.0","id":${sent.id},"result":[]}`)}}`);
  assert.deepEqual(await listed, []);
});

test("with no channel, a prompt goes signed for its machine, so it can be kept", async (t) => {
  const lock = await lockWithMachine();
  const { l, s } = await openLink(t, lock.source);
  const prompt = l.call("agents/prompt", { agentID: "A", text: "go <b>" }, "k3v9");
  await eventually(() => s.raw.length === 1);
  const hello = JSON.parse(s.raw[0]);
  // The machine is not there: the control plane says so.
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id: hello.m.id, error: { code: -32090, message: "That machine is offline." } } }));
  await eventually(() => s.raw.length === 2);
  const sent = JSON.parse(messageOf(s.raw[1]));
  assert.equal(sent.lock.to, "k3v9");
  assert.ok(await w.verifyRequest(sent.lock, "agents/prompt", JSON.stringify(sent.params)));
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id: sent.id, error: { code: -32093, message: "kept" } } }));
  await assert.rejects(prompt, (error) => error instanceof w.CallFailed);
});

test("a page that is not in its lock calls machines as before", async (t) => {
  const { l, s } = await openLink(t, async () => null);
  const listed = l.call("folders/list", {}, "k3v9");
  await eventually(() => s.raw.length === 1);
  const sent = JSON.parse(s.raw[0]);
  assert.equal(sent.m.method, "folders/list");
  assert.equal(sent.m.lock, undefined);
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id: sent.m.id, result: [] } }));
  assert.deepEqual(await listed, []);
});

/** The channel from the hello at s.raw[index], answered as the machine. */
async function answerHello(lock, s, index) {
  const hello = JSON.parse(s.raw[index]);
  assert.equal(hello.m.method, "lock/hello");
  const { reply, channel } = await lock.answer(hello.m.params);
  s.line(JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id: hello.m.id, result: reply } }));
  return channel;
}

const forgot = (id) => JSON.stringify({ h: "k3v9", m: { jsonrpc: "2.0", id, error: { code: -32104, message: "This machine takes requests only from your devices: no channel was opened with lock/hello." } } });

test("a machine that forgot the channel gets a new one, and the call goes again on it, not to the caller (#6227)", async (t) => {
  const lock = await lockWithMachine();
  const { l, s } = await openLink(t, lock.source);
  const listed = l.call("folders/list", {}, "k3v9");
  await eventually(() => s.raw.length === 1);
  const old = await answerHello(lock, s, 0);
  await eventually(() => s.raw.length === 2);
  const first = JSON.parse(await old.open(messageOf(s.raw[1])));
  // The machine's uplink went and came back: it refuses the call, with no MAC.
  s.line(forgot(first.id));
  // A new hello, then the same call again, sealed on the new channel.
  await eventually(() => s.raw.length === 3);
  const channel = await answerHello(lock, s, 2);
  await eventually(() => s.raw.length === 4);
  const sent = messageOf(s.raw[3]);
  assert.ok(w.sealed(sent), sent);
  const again = JSON.parse(await channel.open(sent));
  assert.equal(again.id, first.id);
  assert.equal(again.method, "folders/list");
  s.line(`{"h":"k3v9","m":${await channel.seal(`{"jsonrpc":"2.0","id":${again.id},"result":[]}`)}}`);
  assert.deepEqual(await listed, []);
});

test("a call goes again once: a second refusal is the caller's (#6227)", async (t) => {
  const lock = await lockWithMachine();
  const { l, s } = await openLink(t, lock.source);
  const listed = l.call("folders/list", {}, "k3v9");
  await eventually(() => s.raw.length === 1);
  const old = await answerHello(lock, s, 0);
  await eventually(() => s.raw.length === 2);
  const first = JSON.parse(await old.open(messageOf(s.raw[1])));
  s.line(forgot(first.id));
  await eventually(() => s.raw.length === 3);
  const channel = await answerHello(lock, s, 2);
  await eventually(() => s.raw.length === 4);
  const again = JSON.parse(await channel.open(messageOf(s.raw[3])));
  s.line(forgot(again.id));
  await assert.rejects(listed, (error) => error instanceof w.CallFailed && error.code === -32104);
});
