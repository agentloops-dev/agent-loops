// A folder connector's row says "Approved by you on <machine>, <date>" when this machine took the
// person's approval from another of their machines (#5813), as the window's row (#5543).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const { runtimesAccountsMethods: m } = await load("src/model/store/runtimesAccounts.ts");
const { approvedFromLine } = await load("src/model/hostedMCP.ts");

function fake(answer) {
  const calls = [];
  const store = {
    link: {
      call: async (method, params, host) => {
        calls.push({ method, params, host });
        if (answer instanceof Error) throw answer;
        return answer;
      },
    },
  };
  return { store, calls };
}

test("the folder's own host says which connectors it approved from another machine", async () => {
  const { store, calls } = fake({ servers: [
    { name: "linear", approval: { approved: {} }, approvedFrom: "Approved by you on Studio, 2026-11-14" },
    { name: "github", approval: { approved: {} } },
  ] });
  assert.deepEqual(await m.connectorsApprovedFrom.call(store, "devbox", "/srv/app"),
    { linear: "Approved by you on Studio, 2026-11-14" });
  assert.equal(calls[0].method, "mcp/list");
  assert.equal(calls[0].host, "devbox");
  assert.equal(calls[0].params.destination.kind, "folder");
});

test("a host that does not answer gives no line", async () => {
  const { store } = fake(new Error("offline"));
  assert.deepEqual(await m.connectorsApprovedFrom.call(store, "devbox", "/srv/app"), {});
});

test("the words are the window's", () => {
  assert.equal(approvedFromLine("Approved by you on Studio, 2026-11-14"),
    "Approved by you on Studio, 2026-11-14. The entry is the same as when you approved it there; a change to it waits for your OK.");
});
