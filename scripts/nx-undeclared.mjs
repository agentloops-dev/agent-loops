#!/usr/bin/env node
// The nightly run with no Nx cache (#5629, rule 6 of epic #5627) finds an input that nobody
// declared: a task that fails with no cache, at a hash where the cache has a pass. The pass came
// from an earlier run whose inputs hashed the same, so something the task reads is not an input.
//
//   node scripts/nx-undeclared.mjs passes > passes.txt     before the run: each hash with a pass
//   node scripts/nx-undeclared.mjs compare passes.txt      after it: "task hash" per such failure
//
// `passes` reads Nx's database in NX_WORKSPACE_DATA_DIRECTORY (the cache's index, rule 5).
// `compare` reads the run's summary, run.json in NX_CACHE_DIRECTORY. A failure at a hash with no
// pass is an ordinary failure: the run's own log has it, and this prints nothing for it.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";

function need(name) {
  const value = process.env[name];
  if (!value) {
    console.error(`nx-undeclared: ${name} is not set`);
    process.exit(2);
  }
  return value;
}

function databases(dir) {
  const found = [];
  for (const entry of readdirSync(dir, { withFileTypes: true, recursive: true })) {
    if (entry.isFile() && entry.name.endsWith(".db")) found.push(join(entry.parentPath, entry.name));
  }
  return found;
}

function passes() {
  const hashes = new Set();
  for (const file of databases(need("NX_WORKSPACE_DATA_DIRECTORY"))) {
    const db = new DatabaseSync(file, { readOnly: true });
    try {
      for (const row of db.prepare("SELECT hash FROM cache_outputs WHERE code = 0").all()) hashes.add(row.hash);
    } catch (error) {
      console.error(`nx-undeclared: ${file}: ${error.message}`);
    } finally {
      db.close();
    }
  }
  console.error(`nx-undeclared: ${hashes.size} cached passes`);
  for (const hash of [...hashes].sort()) console.log(hash);
}

function compare(passesFile) {
  const cached = new Set(readFileSync(passesFile, "utf8").split("\n").filter(Boolean));
  const summary = JSON.parse(readFileSync(join(need("NX_CACHE_DIRECTORY"), "run.json"), "utf8"));
  const tasks = summary.tasks ?? [];
  if (tasks.length === 0) {
    console.error("nx-undeclared: run.json lists no task");
    process.exit(2);
  }
  for (const task of tasks) {
    if (typeof task.status === "number" && task.status !== 0 && cached.has(task.hash)) {
      console.log(`${task.taskId} ${task.hash}`);
    }
  }
}

const [mode, arg] = process.argv.slice(2);
if (mode === "passes") passes();
else if (mode === "compare" && arg) compare(arg);
else {
  console.error("usage: nx-undeclared.mjs passes | compare <passes file>");
  process.exit(2);
}
