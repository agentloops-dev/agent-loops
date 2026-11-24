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

test("a page older than its host shows the host's level, the lowest where no one part runs it (#6579)", async () => {
  const m = await load("test/preferences-entry.ts");
  m.preferencesSaid({});
  // A part that says none is older than #6579: the page's own list.
  assert.equal(m.saidMaturity("chat.branch/1", undefined, list), "beta");
  // One that says its levels: not named is GA there, named is its level, one the page does not know is its own.
  assert.equal(m.saidMaturity("chat.branch/1", {}, list), "ga");
  assert.equal(m.saidMaturity("host.wire/1", { "host.wire/1": "alpha" }, list), "alpha");
  assert.equal(m.saidMaturity("chat.branch/1", { "chat.branch/1": "later" }, list), "beta");
  const parts = m.partsMaturity({ maturity: {} }, [{ maturity: { "chat.branch/1": "alpha" } }, {}]);
  assert.equal(parts.length, 3);
  assert.equal(m.lowestMaturity("chat.branch/1", parts, list), "alpha");
  assert.equal(m.lowestMaturity("chat.branch/1", [], list), "beta", "no part: its own list");
  assert.deepEqual(m.partsMaturity(null, [{ maturity: {} }]), [], "no status yet");
  assert.deepEqual(m.earlyRows(list, parts).map((f) => [f.capability, f.level]), [["chat.branch/1", "alpha"]]);
  assert.deepEqual(m.earlyRows(list, [{}]), [], "every part says GA");
});
