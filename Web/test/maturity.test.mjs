// Features show their maturity (#6052): a capability Packages/Wire/maturity.json does not list is GA; one it lists has
// a badge, and a switch in Early Features, on by default and kept with the feature flags (#6254). The words are the
// window's and the Remote's.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { load } from "./load.mjs";

const list = [
  { capability: "chat.branch/1", level: "beta", title: "Branches", issue: 1 },
  { capability: "work.cache/1", level: "pilot", title: "Work cache", issue: 2, clients: ["mac"] },
];

test("a capability that is not listed is GA, and a pilot shows only on its clients (#6052)", async () => {
  const m = await load("test/preferences-entry.ts");
  m.preferencesSaid({});
  assert.equal(m.maturityOf("host.wire/1", list), "ga");
  assert.equal(m.maturityOf("chat.branch/1", list), "beta");
  assert.deepEqual(m.earlyRows(list).map((f) => f.capability), ["chat.branch/1"], "the pilot is the Mac's only");
  assert.equal(m.showsFeature("work.cache/1", list), false);
  assert.equal(m.showsFeature("host.wire/1", list), true, "GA shows everywhere");
});

test("a feature is on until the person turns it off, and only the off is kept (#6052)", async () => {
  const m = await load("test/preferences-entry.ts");
  const sent = [];
  m.sendPreferencesWith((changes) => sent.push(changes));
  m.preferencesSaid({});
  assert.equal(m.showsFeature("chat.branch/1", list), true, "on by default");
  m.setEarlyFeature("chat.branch/1", false);
  assert.equal(m.showsFeature("chat.branch/1", list), false, "drawn at once");
  m.setEarlyFeature("chat.branch/1", true);
  assert.deepEqual(sent, [{ "flag:chat:branch/1": false }, { "flag:chat:branch/1": null }]);
  m.preferencesSaid({ "flag:chat:branch/1": false });
  assert.equal(m.showsFeature("chat.branch/1", list), false, "the Mac's choice");
  m.sendPreferencesWith(null);
});

test("the page's list is Packages/Wire's, and its words are the window's (#6052)", async () => {
  const { earlyFeatures } = await load("src/protocol/maturity.generated.ts");
  const wire = JSON.parse(readFileSync(new URL("../../Packages/Wire/maturity.json", import.meta.url), "utf8"));
  assert.deepEqual(earlyFeatures, wire.capabilities);
  const { maturityWords } = await load("src/model/maturity.ts");
  const kit = readFileSync(new URL("../../Packages/AgentsKit/Sources/AgentsKitCore/Model/Maturity.swift", import.meta.url), "utf8");
  for (const [level, { badge, help }] of Object.entries(maturityWords)) {
    assert.ok(kit.includes(`case .${level}: ${JSON.stringify(badge)}`), `${level}'s badge is the window's`);
    assert.ok(kit.includes(`case .${level}: ${JSON.stringify(help)}`), `${level}'s help line is the window's`);
  }
  const { earlyFeaturesFooter, noEarlyFeatures } = await load("src/views/settings/EarlyFeatures.tsx");
  const swift = readFileSync(new URL("../../Shared/UI/EarlyFeaturesSection.swift", import.meta.url), "utf8")
    .replace(/"\s*\n\s*\+ "/g, "");
  assert.ok(swift.includes(JSON.stringify(earlyFeaturesFooter)), "the footer is the window's");
  assert.ok(swift.includes(JSON.stringify(noEarlyFeatures)), "the empty line is the window's");
});
