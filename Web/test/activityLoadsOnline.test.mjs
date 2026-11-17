// Reports ▸ Events opened by its address loads once a machine answers (#3242).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("../src/views/Activity.tsx", import.meta.url), "utf8");

test("an Activity page loads again when the online machines change", () => {
  const view = source.slice(source.indexOf("export function ActivityPageView"));
  assert.match(view, /const online = hosts\.map\(\(h\) => h\.id\)\.join\(","\);/);
  // Every machine's events in one call (#5592).
  assert.match(view, /store\.loadEventsAll\(hosts\.map\(\(h\) => h\.id\), 100\)[\s\S]*?\}, \[page, online\]\);/);
});
