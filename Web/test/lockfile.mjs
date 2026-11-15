// The web app's packages, read from the workspace's pnpm-lock.yaml (it was
// Web/package-lock.json). pnpm 12 writes two YAML documents; the last is the lock.
import { existsSync, lstatSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseAllDocuments } from "yaml";

export const web = dirname(dirname(fileURLToPath(import.meta.url)));
export const repo = dirname(web);
const docs = parseAllDocuments(readFileSync(join(repo, "pnpm-lock.yaml"), "utf8"));
export const lock = docs[docs.length - 1].toJSON();

// name@version without a peer suffix: "a@1.0.0(b@2.0.0)" is "a@1.0.0".
const bare = (key) => key.replace(/\(.*$/, "");
const nameOf = (key) => bare(key).replace(/@[^@]+$/, "");

// Every package the web app uses, its own and theirs, as name@version.
export function webPackages() {
  const seen = new Set();
  const importer = lock.importers.Web;
  const stack = [...Object.entries({ ...importer.dependencies, ...importer.devDependencies })].map(
    ([name, { version }]) => `${name}@${version}`);
  while (stack.length) {
    const key = stack.pop();
    if (seen.has(key)) continue;
    seen.add(key);
    const snap = lock.snapshots[key] ?? {};
    for (const [name, version] of Object.entries({ ...snap.dependencies, ...snap.optionalDependencies })) {
      stack.push(`${name}@${version}`);
    }
  }
  return [...seen].map((key) => ({ key: bare(key), name: nameOf(key), entry: lock.packages[bare(key)] ?? {} }));
}

// The licence in each installed package.json, from pnpm's store in node_modules/.pnpm, by
// name@version. A platform's binary that is not for this host is not installed: it is left out.
let installed;
export function licenceOf(key) {
  if (!installed) {
    installed = new Map();
    const store = join(repo, "node_modules/.pnpm");
    for (const folder of readdirSync(store)) {
      const modules = join(store, folder, "node_modules");
      if (!existsSync(modules)) continue;
      for (const top of readdirSync(modules)) {
        const names = top.startsWith("@") ? readdirSync(join(modules, top)).map((n) => `${top}/${n}`) : [top];
        for (const name of names) {
          const path = join(modules, name);
          if (lstatSync(path).isSymbolicLink()) continue;
          const pkg = JSON.parse(readFileSync(join(path, "package.json"), "utf8"));
          installed.set(`${pkg.name}@${pkg.version}`, pkg.license ?? null);
        }
      }
    }
  }
  return installed.has(key) ? installed.get(key) ?? undefined : "not installed";
}
