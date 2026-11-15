// Paper v2's one icon set on the web page (#4996): the icons src/views/Icon.tsx draws are the ones
// design/icons.json gives a meaning, from the lucide-static that it pins.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { lock } from "./lockfile.mjs";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const spec = JSON.parse(read("../../design/icons.json"));
const meant = new Set(spec.icons.map((icon) => icon.lucide));

test("lucide-static is the version design/icons.json pins, exactly", () => {
  const pkg = JSON.parse(read("../package.json"));
  assert.equal(pkg.dependencies["lucide-static"], spec.lucide.version);
  assert.equal(lock.packages[`lucide-static@${spec.lucide.version}`].resolution.integrity, spec.lucide.integrity);
});

test("every icon the page draws has its meaning in design/icons.json", () => {
  const source = read("../src/views/Icon.tsx");
  const drawn = [...source.matchAll(/from "lucide:([a-z0-9-]+)"/g)].map((m) => m[1]);
  assert.ok(drawn.length > 0);
  assert.deepEqual(drawn.filter((name) => !meant.has(name)), [], "add the icon to design/icons.json with its meaning");
});
