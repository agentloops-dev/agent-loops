// The person's feature flags (#6254): kept on the Mac as flag:<name>, off when absent, one list from
// Packages/Wire, and the same words as the window's section.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { load } from "./load.mjs";

test("a flag is on only when the Mac says the person turned it on, and a change keeps only what differs (#6254)", async () => {
  const m = await load("test/preferences-entry.ts");
  const sent = [];
  m.sendPreferencesWith((changes) => sent.push(changes));
  m.preferencesSaid({});
  assert.equal(m.flagKey("commit-trailers"), "flag:commit-trailers");
  assert.equal(m.flagOn("commit-trailers"), false, "off when absent");
  m.preferencesSaid({ "flag:commit-trailers": true });
  assert.equal(m.flagOn("commit-trailers"), true);
  m.setFlag("commit-trailers", false);
  assert.equal(m.flagOn("commit-trailers"), false, "drawn at once");
  // A capability's toggle (#6052) is on by default: only an off is kept.
  assert.equal(m.flagOn("chat.branch", true), true);
  m.setFlag("chat.branch", false, true);
  m.setFlag("chat.branch", true, true);
  assert.deepEqual(sent, [{ "flag:commit-trailers": null }, { "flag:chat:branch": false }, { "flag:chat:branch": null }]);
  m.sendPreferencesWith(null);
});

test("the page's flags are Packages/Wire's (#6254); Early Features draws them (#6604)", async () => {
  const { featureFlags } = await load("src/protocol/flags.generated.ts");
  const wire = JSON.parse(readFileSync(new URL("../../Packages/Wire/feature-flags.json", import.meta.url), "utf8"));
  assert.deepEqual(featureFlags, wire.flags);
});
