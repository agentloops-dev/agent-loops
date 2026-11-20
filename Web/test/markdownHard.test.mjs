// Markdown a renderer may not expect does not stop the chat, and no word of it is lost (#6189). The
// corpus is Packages/AgentsKit/Tests/AgentsKitTests/Fixtures/markdown-hard, which AgentsKit's
// MarkdownHardTests read too (the Mac window and the Remote): deep lists and quotes, huge and ragged
// tables, fences that never close, text with no breaks. Each word of a file is marked `k<n>`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { load } from "./load.mjs";

const { renderMarkdown, markdownBlocks, markdownParts, markdownParsed, Markdown } = await load("src/render/markdown.ts");

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

/** The nodes with each component drawn, as the page would (a memo's wrapper and Fragment included). */
function expand(node) {
  if (Array.isArray(node)) return node.map(expand);
  if (node === null || typeof node !== "object") return node;
  if (typeof node.type === "function") return expand(node.type.call({}, node.props));
  const { children, ...props } = node.props ?? {};
  return { type: node.type, props: { ...props, children: expand(children ?? []) } };
}

/** The nodes as HTML-ish text, so two drawings can be compared. */
function show(nodes) {
  return [nodes].flat(Infinity).map((node) => {
    if (node === null || node === undefined || typeof node === "boolean") return "";
    if (typeof node === "string" || typeof node === "number") return String(node);
    const { children, ...props } = node.props;
    const attrs = Object.entries(props).filter(([, v]) => typeof v !== "function").map(([k, v]) => ` ${k}="${v}"`).join("");
    return `<${node.type}${attrs}>${show(children ?? [])}</${node.type}>`;
  }).join("");
}

/** A message as `Markdown` draws it, its table's bodies as one, to compare with a whole parse. */
const page = (text) => show(expand({ type: Markdown, props: { text } })).replaceAll("</tbody><tbody>", "");
const whole = (text) => `<div class="markdown">${show(renderMarkdown(text))}</div>`;

// A large table that streams is parsed again at its last rows, not whole at each chunk (#6328): the
// 40-column, 300-row table took 36 ms a parse, and its 12,000 cells were drawn again each time.
test("huge-tables.md: a streamed table is parsed at its last rows", () => {
  const source = readFileSync(dir + "huge-tables.md", "utf8");
  const chunk = 400;
  let most = 0;
  for (let end = chunk; end < source.length + chunk; end += chunk) {
    const before = markdownParsed.characters;
    assert.ok(page(source.slice(0, end)).length > 0);
    most = Math.max(most, markdownParsed.characters - before);
  }
  // A part is about 2,000 characters and the table's head, and a cut parses two parts.
  assert.ok(most < 8_000, `parsed ${most} of ${source.length} characters at one chunk`);
  assert.ok(markdownParts(source).some((part) => part.head !== undefined), "the table is cut by rows");
  assert.equal(page(source), whole(source));
});

// A long table cut by rows draws as the same table, at every row as it arrives: alignments, marks and
// escaped pipes in its cells, short and long rows, and what comes after it.
test("a table cut by rows draws as one table", () => {
  const filler = "Words enough to fill a part of the reading. ".repeat(60);
  const rows = Array.from({ length: 120 }, (_, index) => [
    `| row ${index} | *marked* \`code\` | a \\| pipe |`,
    `| short ${index} |`,
    `| long ${index} | b | c | d | e |`,
    `| **${index}** | [link](https://example.com) | ok |`,
  ][index % 4]);
  const table = "| one | two | three |\n| :-- | :-: | --: |\n" + rows.join("\n");
  const shapes = [
    `${filler}\n\n${table}\n\nAfter the table.`,
    `${table}\n\n${filler}`,
    `${filler}\n\n${table}\nlazy row after it\n\n${table}`,
    `${filler}\n\n${table}\n# A heading after it\n\nMore.`,
    `${filler}\n\nA paragraph line\n${table}`,
    `- [ ] a task\n\n${table}\n\n- [x] done`,
  ];
  assert.ok(markdownParts(shapes[0]).some((part) => part.head !== undefined));
  for (const shape of shapes) {
    assert.equal(page(shape), whole(shape), shape.slice(0, 80));
    const lines = shape.split("\n");
    for (let end = 0; end < lines.length; end++) {
      if (end % 3 !== 0 && end !== lines.length - 1) continue;
      const prefix = lines.slice(0, end + 1).join("\n");
      assert.equal(page(prefix), whole(prefix), prefix.slice(-60));
      assert.equal(markdownBlocks(prefix).join(""), prefix);
    }
  }
});
