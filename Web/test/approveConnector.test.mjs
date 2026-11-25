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

const reading = { path: ".agents/mcp.json", digest: "d1", current: '{\n  "command": "x"\n}', lines: [] };

test("Approve shows the whole entry, then approves the version it showed on the folder's own host, then checks again", async () => {
  const { store, calls } = fake({ "approvals/read": reading, "mcp/approve": {} });
  const shown = [];
  assert.equal(await m.approveConnector.call(store, "devbox", "pulled", "/srv/app", async (entry) => { shown.push(entry); return true; }), undefined);
  assert.deepEqual(shown, [reading.current]);
  assert.deepEqual(calls.map((c) => [c.method, c.host]), [["approvals/read", "devbox"], ["mcp/approve", "devbox"], ["check", "devbox"]]);
  assert.deepEqual(calls[0].params, { folder: "file:///srv/app", kind: "mcpServer", key: "pulled" });
  assert.deepEqual(calls[1].params, { destination: { kind: "folder", folder: "/srv/app" }, name: "pulled", digest: "d1" });
});

test("the person's own is read with no folder", async () => {
  const { store, calls } = fake({ "approvals/read": reading, "mcp/approve": {} });
  await m.approveConnector.call(store, "devbox", "notes", undefined, async () => true);
  assert.equal(calls[0].params.key, "~|notes");
  assert.deepEqual(calls[1].params, { destination: { kind: "personal" }, name: "notes", digest: "d1" });
});

test("Cancel, or an entry that did not come, approves nothing", async () => {
  for (const [answer, yes] of [[reading, false], [{ ...reading, current: undefined }, true]]) {
    const { store, calls } = fake({ "approvals/read": answer });
    assert.equal(await m.approveConnector.call(store, "devbox", "pulled", "/srv/app", async () => yes), undefined);
    assert.deepEqual(calls.map((c) => c.method), ["approvals/read"]);
  }
});

test("a refusal comes back as words", async () => {
  const { store } = fake({ "approvals/read": reading, "mcp/approve": new Error("It changed again.") });
  const why = await m.approveConnector.call(store, "devbox", "pulled", "/srv/app", async () => true);
  assert.equal(typeof why, "string");
  assert.equal(notApproved("pulled", "It changed again."), "pulled was not approved. It changed again.");
});
