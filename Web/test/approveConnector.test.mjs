// A folder's connector that waits is approved on the folder's own host, by the digest that host reads (#5717).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const { runtimesAccountsMethods: m } = await load("src/model/store/runtimesAccounts.ts");
const { notApproved } = await load("src/model/hostedMCP.ts");

function fake(answers) {
  const calls = [];
  const store = {
    link: {
      call: async (method, params, host) => {
        calls.push({ method, params, host });
        const answer = answers[method];
        if (answer instanceof Error) throw answer;
        return answer;
      },
    },
    checkMCPHealth: async (host, folder) => { calls.push({ method: "check", host, folder }); },
  };
  return { store, calls };
}

const list = { servers: [{ name: "pulled", approval: { waiting: { digest: "d1", isNew: true } } }, { name: "ok", approval: { approved: {} } }] };

test("Approve sends mcp/approve with the digest to the folder's own host, then checks again", async () => {
  const { store, calls } = fake({ "mcp/list": list, "mcp/approve": list });
  assert.equal(await m.approveConnector.call(store, "devbox", "pulled", "/srv/app"), undefined);
  assert.deepEqual(calls.map((c) => [c.method, c.host]), [["mcp/list", "devbox"], ["mcp/approve", "devbox"], ["check", "devbox"]]);
  assert.equal(calls[1].params.digest, "d1");
  assert.equal(calls[1].params.name, "pulled");
});

test("a connector that no longer waits is not approved again", async () => {
  const { store, calls } = fake({ "mcp/list": list });
  await m.approveConnector.call(store, "devbox", "ok", "/srv/app");
  assert.deepEqual(calls.map((c) => c.method), ["mcp/list", "check"]);
});

test("a refusal comes back as words", async () => {
  const { store } = fake({ "mcp/list": list, "mcp/approve": new Error("It changed again.") });
  const why = await m.approveConnector.call(store, "devbox", "pulled", "/srv/app");
  assert.equal(typeof why, "string");
  assert.equal(notApproved("pulled", "It changed again."), "pulled was not approved. It changed again.");
});
