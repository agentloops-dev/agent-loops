// Writes the hard-Markdown corpus (#6189): `node Packages/AgentsKit/Tests/AgentsKitTests/Fixtures/markdown-hard/make.mjs`.
// Markdown an agent can write that a renderer may not expect: deep lists and quotes, huge and ragged
// tables, fences that never close, and text with no breaks. Each file marks its words `k<n>`, so a
// runner can say that every word it was given is drawn: `MarkdownHardTests` (AgentsKit, the Mac window
// and the Remote) and `Web/test/markdownHard.test.mjs` (the web page) read the same files.
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

let word = 0;
const k = () => `k${++word}`;
const files = {};

// A list 40 deep, each level a different marker, ordered and not, with a paragraph and a fence inside.
files["deep-lists.md"] = (() => {
  const lines = [];
  for (let depth = 0; depth < 40; depth++) {
    const pad = "  ".repeat(depth);
    const mark = depth % 3 === 0 ? "-" : depth % 3 === 1 ? "1." : "*";
    lines.push(`${pad}${mark} ${k()} level ${depth}`);
    if (depth % 10 === 5) lines.push("", `${pad}  ${k()} a paragraph in the item`, "", `${pad}  \`\`\``, `${pad}  ${k()} code in the item`, `${pad}  \`\`\``);
  }
  lines.push("", "Then quotes 40 deep:", "");
  for (let depth = 1; depth <= 40; depth++) lines.push(`${"> ".repeat(depth)}${k()} quoted ${depth}`);
  lines.push("", "A list of 2,000 items:", "");
  for (let n = 0; n < 2_000; n++) lines.push(`${n + 1}. ${k()}`);
  return lines.join("\n") + "\n";
})();

// A table 40 columns wide and 300 rows long, then tables with too few and too many cells, escaped
// pipes, and a header with no rows. A row's cells past the header's are dropped, as GFM says, so
// they carry no marked word.
files["huge-tables.md"] = (() => {
  const columns = 40;
  const row = () => `| ${Array.from({ length: columns }, k).join(" | ")} |`;
  const lines = [row(), `|${" --- |".repeat(columns)}`];
  for (let n = 0; n < 300; n++) lines.push(row());
  lines.push("", `| ${k()} | ${k()} | ${k()} |`, "| :-- | :-: | --: |", `| ${k()} |`, `| ${k()} | ${k()} | ${k()} | extra | extra |`, `| ${k()} \\| ${k()} | \`${k()} \\| x\` | ${k()} |`);
  lines.push("", `| ${k()} | ${k()} |`, "| --- | --- |", "", `${k()} after a table with no rows`);
  return lines.join("\n") + "\n";
})();

// Fences that do not close where a renderer may think: the last one never closes.
files["unclosed-fences.md"] = [
  `${k()} before`,
  "",
  "````markdown",
  "```",
  `${k()} a fence inside a longer fence`,
  "```",
  "````",
  "",
  "~~~",
  "```",
  `${k()} backticks in a tilde fence`,
  "~~~",
  "",
  "   ```swift",
  `${k()} an indented fence`,
  "   ```",
  "",
  `${k()} an unclosed \`code span, and **bold that never ends, and [a link](that never closes`,
  "",
  "```",
  ...Array.from({ length: 500 }, (_, n) => `${k()} line ${n} of a fence that never closes`),
].join("\n") + "\n";

// Text a renderer has no break in, HTML, odd characters and line ends.
files["odd-text.md"] = [
  "---",
  `title: ${k()} front matter that never closes`,
  "",
  `${k()}${"x".repeat(50_000)}${k()}`,
  "",
  `<div>${k()} HTML</div>`,
  "",
  `<details><summary>${k()}</summary>`,
  "",
  `${k()} inside an unclosed details`,
  "",
  `${k()} tabs\tand\tzero​width‍spaces and RTL ‮${k()}‬ and emoji 👩‍👩‍👧‍👦`,
  "",
  `${k()} a setext heading`,
  "===",
  "",
  `${k()} [ref link][nowhere] and ![an image](nowhere.png) and <https://example.com/${k()}>`,
  "",
  `${k()} CRLF lines\r\n${k()} after a CR LF\r\n`,
  `${"#".repeat(7)} ${k()} seven hashes`,
  "",
  `${"*".repeat(200)}${k()}${"_".repeat(200)}`,
].join("\n") + "\n";

for (const [name, text] of Object.entries(files)) writeFileSync(fileURLToPath(new URL(name, import.meta.url)), text);
