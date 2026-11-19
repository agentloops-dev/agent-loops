// Markdown a renderer may not expect does not stop the chat, and no word of it is lost (#6189). The
// corpus is Packages/AgentsKit/Tests/AgentsKitTests/Fixtures/markdown-hard, which AgentsKit's
// MarkdownHardTests read too (the Mac window and the Remote): deep lists and quotes, huge and ragged
// tables, fences that never close, text with no breaks. Each word of a file is marked `k<n>`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { load } from "./load.mjs";

const { renderMarkdown, markdownBlocks } = await load("src/render/markdown.ts");

const dir = fileURLToPath(new URL("../../Packages/AgentsKit/Tests/AgentsKitTests/Fixtures/markdown-hard/", import.meta.url));
const files = readdirSync(dir).filter((f) => f.endsWith(".md")).sort();

/** Every word the nodes draw: their text, and a component's own text props (a fence, a picture's alt). */
function text(nodes) {
  return [nodes].flat(Infinity).map((node) => {
    if (node === null || node === undefined || typeof node === "boolean") return "";
    if (typeof node === "string" || typeof node === "number") return String(node);
    const { children, ...props } = node.props ?? {};
    const own = typeof node.type === "function" ? Object.values(props).filter((v) => typeof v === "string").join(" ") : "";
    return `${own} ${text(children ?? [])}`;
  }).join(" ");
}

const marks = (s) => new Set(s.match(/k\d+/g) ?? []);
const missing = (words, drawn) => [...words].filter((w) => !drawn.has(w));
/** As the chat draws a message: cut into blocks, each parsed alone (`Markdown`). */
const drawn = (source) => marks(markdownBlocks(source).map((block) => text(renderMarkdown(block))).join("\n"));

test("the corpus is there", () => assert.ok(files.length >= 4));

for (const file of files) {
  const source = readFileSync(dir + file, "utf8");

  test(`${file}: every word is drawn`, () => {
    const words = marks(source);
    assert.ok(words.size > 10);
    assert.deepEqual(missing(words, marks(text(renderMarkdown(source)))), [], "read whole");
    assert.deepEqual(missing(words, drawn(source)), [], "read in blocks");
  });

  // A reply that streams is drawn again at each chunk, so every cut of it must draw too.
  test(`${file}: every cut of a streamed reply draws`, () => {
    const cuts = 8;
    for (let cut = 1; cut <= cuts; cut++) {
      const prefix = source.slice(0, Math.floor((source.length * cut) / cuts));
      // The word at the cut may be cut too ("k12" of "k123"), so only whole words count.
      const whole = new Set([...prefix.matchAll(/k\d+(?=\D)/g)].map((m) => m[0]));
      assert.deepEqual(missing(whole, drawn(prefix)), [], `cut ${cut} of ${cuts}`);
    }
  });
}
