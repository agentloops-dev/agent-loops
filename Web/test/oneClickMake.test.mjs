// New Droplet on a DigitalOcean provider's card (#6577): what one click makes, the same words as the window's
// OneClickMake (OneClickMakeTests).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("src/model/oneClickMake.ts");

const schema = { type: "object", properties: {
  region: { type: "string", enum: ["sfo3", "nyc1"], default: "sfo3", "x-titles": { sfo3: "San Francisco 3", nyc1: "New York 1" } },
  size: { type: "string", enum: ["s-1vcpu-1gb", "s-2vcpu-4gb"], default: "s-2vcpu-4gb",
    "x-titles": { "s-1vcpu-1gb": "1 vCPU · 1 GB · 25 GB disk · $6/month", "s-2vcpu-4gb": "2 vCPUs · 4 GB · 80 GB disk · $24/month" } },
  image: { type: "string", default: "ubuntu-24-04-x64" },
} };

test("the card says where and what it costs", () => {
  const plan = m.oneClickPlan({ schema, id: "do", name: "machine-3", values: { region: "sfo3", size: "s-2vcpu-4gb", image: "ubuntu-24-04-x64" } });
  assert.equal(plan.name, "machine-3");
  assert.equal(plan.summary, "San Francisco 3 · 2 vCPUs · 4 GB · 80 GB disk · $24/month");
  assert.ok(m.oneClickWords.note(plan.name).startsWith("New Droplet makes machine-3 there."));
});

test("the last choice is used again, and a field left out takes its first choice or default", () => {
  const plan = m.oneClickPlan({ schema, name: "machine-4", values: { region: "nyc1", size: "s-1vcpu-1gb" } });
  assert.equal(plan.summary, "New York 1 · 1 vCPU · 1 GB · 25 GB disk · $6/month");
  assert.equal(plan.values.image, "ubuntu-24-04-x64");
  assert.equal(m.oneClickPlan({ schema, name: "machine-5" }).summary, "San Francisco 3 · 1 vCPU · 1 GB · 25 GB disk · $6/month");
  assert.equal(m.oneClickPlan({ schema }), null);
});

test("only a DigitalOcean provider that makes machines offers it", () => {
  const p = (preset, capabilities) => ({ id: "p", name: "P", kind: "commands", capabilities, preset, workspaces: [] });
  assert.ok(m.offersOneClick(p("digitalocean-api", ["create", "list"])));
  assert.ok(!m.offersOneClick(p("digitalocean-api", ["list"])));
  assert.ok(!m.offersOneClick(p("codespaces-api", ["create"])));
});
