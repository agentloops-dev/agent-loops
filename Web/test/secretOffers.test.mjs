// An agent's secret that becomes yours (#5308): the associated data is Go's controllink.AgentSecretAD byte for
// byte, an offer opens only with what it was sealed with, and the words are SecretOfferWords' (Swift).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const s = await load("src/wire/sealing.ts");
const w = await load("src/model/secretOffers.ts");
const text = (bytes) => new TextDecoder().decode(bytes);

async function pageKey() {
  const pair = await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, false, ["deriveBits"]);
  const raw = new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey));
  return { record: { privateKey: pair.privateKey, publicKey: pair.publicKey, client: "3d0c4b4e-aaaa" }, raw };
}

test("agent secrets: the associated data is Go's", () => {
  assert.equal(text(s.agentSecretAD("sam", "MACHINE-30", "c:3D0C", "GITLAB_TOKEN", "OFFER-1")),
    "agentsecret.v1|sam|MACHINE-30|c:3D0C|GITLAB_TOKEN|OFFER-1");
  assert.equal(text(s.agentSecretAD("sam", "MACHINE-30", "c:3D0C", "GITLAB_TOKEN", "OFFER-1", true)),
    "agentsecret.v1|sam|MACHINE-30|c:3D0C|GITLAB_TOKEN|OFFER-1|ask");
});

test("agent secrets: an offer opens with this page's key, and not when moved to another offer or machine", async () => {
  const { record, raw } = await pageKey();
  const me = s.clientRecipient(record.client);
  const mine = await s.seal(raw, s.agentSecretPurpose, s.agentSecretAD("sam", "MACHINE-30", me, "GITLAB_TOKEN", "OFFER-1"),
    new TextEncoder().encode("glpat-agent"));
  const offer = { offer: "OFFER-1", name: "GITLAB_TOKEN", machine: "MACHINE-30", machineName: "devbox", savedAt: "2026-11-15T10:00:00Z", mine };
  assert.equal(await s.openOffer(record, "sam", offer), "glpat-agent");
  await assert.rejects(s.openOffer(record, "sam", { ...offer, offer: "OFFER-2" }));
  await assert.rejects(s.openOffer(record, "sam", { ...offer, machine: "MACHINE-31" }));
  await assert.rejects(s.openOffer(record, "sam", { ...offer, needsApproval: true }));
  await assert.rejects(s.openOffer(record, "sam", { ...offer, mine: undefined }));
});

test("agent secrets: the words", () => {
  const now = new Date("2026-11-15T12:00:00Z");
  assert.equal(w.savedByLine({ machine: "MACHINE-30", name: "devbox", at: "2026-11-15T10:00:00Z" }, now), "Saved by an agent on devbox, 2 h ago");
  assert.equal(w.savedByLine(undefined, now), null);
  assert.equal(w.offerLine({ machineName: "devbox" }), "Saved by an agent on devbox. It goes to your other machines when you next open Agents.");
  assert.match(w.offerLine({ machineName: "devbox", conflict: true }), /^Saved by an agent on devbox\. You have a secret with this name/);
  assert.equal(w.keepYours, "Keep Yours");
  assert.equal(w.useTheAgents, "Use the Agent's");
});

test("agent secrets: one card per machine and name, with its newest offer (#6626)", () => {
  const first = { offer: "OFFER-1", name: "OKTA_ADMIN_PASSWORD", machine: "MACHINE-30", machineName: "Mac", savedAt: "2026-11-22T02:48:50Z", conflict: true };
  const again = { ...first, offer: "OFFER-2", savedAt: "2026-11-22T02:48:51Z" };
  const other = { ...first, offer: "OFFER-3", machine: "MACHINE-31", savedAt: "2026-11-22T02:00:00Z" };
  assert.equal(w.offerCard(first), w.offerCard(again));
  assert.notEqual(w.offerCard(first), w.offerCard(other));
  assert.deepEqual(w.waitingOffers([again, first, other]).map((o) => o.offer), ["OFFER-3", "OFFER-2"]);
  assert.deepEqual(w.waitingOffers([]), []);
});
