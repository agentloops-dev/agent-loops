// Knip's settings for the web app: `scripts/lint.sh knip`, in web:lint, fails on any unused
// file, export, type or dependency (#6337). There is no baseline: a finding kept on purpose
// is an entry below, with why.
import { dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const web = dirname(fileURLToPath(import.meta.url));

// The tests and walks read the source through test/load.mjs (`await load("src/x.ts")`), which
// bundles it with esbuild; Knip sees a string, not an import. Read each such call as the
// dynamic import it stands for, so an export that a test uses is a used export, as `deadcode
// -test` counts a Go function that a test calls (#6334).
const loads = (text, file) => {
  const up = relative(dirname(file), web) || ".";
  return text.replace(/\bload\((["'`])((?:src|test)\/)/g, (_, quote, dir) => `import(${quote}${up}/${dir}`);
};

export default {
  $schema: "https://unpkg.com/knip@6.45.1/schema.json",
  // The bundles that build.mjs makes (esbuild's API, which Knip does not read), and the tests
  // and walks, which reach the source by load() (above).
  entry: ["src/main.tsx", "src/sandbox/proxy.ts", "src/sw.ts", "test/**/*.{mjs,ts}"],
  project: ["src/**/*.{ts,tsx}", "test/**/*.{mjs,ts}"],
  compilers: { mjs: loads },
  // An export that its own file uses is live code: only the word `export` is spare, as Go's
  // deadcode reports no exported function that its package calls (#6334).
  ignoreExportsUsedInFile: true,
  ignoreIssues: {
    // Written by scripts/wire.sh gen from Packages/Wire (#1135): every type of the schema is
    // exported, whether the page reads it or not. The generator owns this file.
    "src/protocol/generated.ts": ["exports", "types"],
    "src/protocol/capabilities.generated.ts": ["exports", "types"],
    // Two names for two things that are the same length today, as the Mac has them: the
    // settle of a quiet burst and of the files pane; one turn of the ring and its whole cycle.
    "src/model/store/shared.ts": ["duplicates"],
    "src/spinnerClock.ts": ["duplicates"],
  },
  ignoreDependencies: [
    // lucide.mjs (the esbuild plugin) reads lucide-static's SVGs by require.resolve of a
    // template string, which Knip does not follow.
    "lucide-static",
    // `import … from "lucide:name"` is that plugin's own namespace, not a package.
    "lucide",
  ],
  // macOS's image tool: the walks scale their screenshots with it.
  ignoreBinaries: ["sips"],
};
