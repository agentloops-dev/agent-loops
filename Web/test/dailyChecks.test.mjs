// The daily checks' control (#3355, #5853): the web page says what the window's and the
// Remote's DailyChecksControls.swift says, and sits on Settings ▸ Workflows. The daily summary
// that it was the control of is gone (#5853).
import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { load } from "./load.mjs";

const read = (path) => readFileSync(new URL(`../../${path}`, import.meta.url), "utf8");
// The store and its slices (#5384).
const readStore = () => [read("Web/src/model/store.ts"), ...readdirSync(new URL("../src/model/store/", import.meta.url)).map((name) => read(`Web/src/model/store/${name}`))].join("\n");
const w = await load("src/model/dailyChecksWords.ts");

test("the control's words are the window's and the Remote's", () => {
  const swift = read("Shared/UI/DailyChecksControls.swift").replace(/"\s*\+\s*"/g, "");
  assert.ok(swift.includes(w.checksHint), w.checksHint);
  const model = read("Packages/AgentsKit/Sources/AgentsKitCore/Model/DailyChecksSettings.swift");
  assert.deepEqual(w.dailyChecksStates.map((s) => s.label), ["Off", "On"]);
  assert.ok(model.includes(`checksEveryChoices = [${w.checksEveryChoices.join(", ")}]`));
  assert.ok(model.includes('"Every \\(hours) hours"'));
  assert.equal(w.checksEveryLabel(4), "Every 4 hours");
});

test("Settings ▸ Workflows draws it, and dailyChecks/changed keeps it", () => {
  assert.ok(read("Web/src/views/AgentConfigEditor.tsx").includes('kind === "workflows" && <DailyChecksControl'));
  const store = readStore();
  assert.ok(store.includes('case "dailyChecks/changed":'));
  assert.ok(store.includes('"dailyChecks/setSettings"'));
});

test("the daily summary is gone: no Summary page, no Reports ▸ Summary (#5853)", () => {
  assert.ok(!existsSync(new URL("../src/views/SummaryPage.tsx", import.meta.url)));
  assert.ok(!read("Web/src/views/Nav.tsx").includes("SummaryPage"));
  assert.ok(!read("Web/src/model/nav.ts").includes('kind: "summary"'));
  assert.ok(!readStore().includes("summaries/"));
});
