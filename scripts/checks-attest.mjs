#!/usr/bin/env node
// scripts/checks.sh attest (#5584, epic #5581 §1 and §2): run the affected test, vet and lint
// tasks of this branch, and the app builds mac:build and remote:build (#6060), with Nx and no
// cache hits, then post one check run
// `attestation/<platform>` on the pull request's head commit, as the agents' bot. Its output
// holds one in-toto Statement v1 for each task that passed on its first run, with the
// test-result predicate. On the pull request, CI skips each task it covers (#5585,
// scripts/ci-attested.mjs).
//
//   node scripts/checks-attest.mjs [run | post] [--base REF] [--plan] [--no-post]
//   node scripts/checks-attest.mjs --selftest
//
// Two steps (#6059), so the check run is there when CI's `changes` job reads it, about a minute
// after the push: `run`, before the push, runs the tasks at the head commit and keeps the check
// run on disk (`git rev-parse --git-path agents-attest/runs/<head sha>.json`); `post`, just after
// the push, reads it, checks that the pull request's head is that commit and that no task's hash
// changed, and posts it, in seconds. With no step, it does both, and needs the push first.
// --plan prints the tasks, their hashes and why each runs (the inputs that changed), and runs
// nothing. --no-post runs them, keeps them and prints the statements in place of posting them.
//
// Rules (the epic's principles):
// - The subject of a statement is the task and its Nx hash, nothing else (principle 3). The
//   patch id and the head tree are information, in an annotation.
// - Only a first run is attested (principle 9): each task runs with --skip-nx-cache, and a
//   result that Nx took from its cache is never attested. A task that fails is written to this
//   worktree's ledger (`git rev-parse --git-path agents-attest/failed`), and a later pass at the
//   same hash is not attested: only a change to its inputs (a new hash) clears it.
// - A failure in any Nx run since the branch began counts too (#6173): `nx affected`, `nx run`
//   or an attest run, from Nx's task_history, which every worktree shares (a failure at the same
//   hash in another worktree is the same inputs failing). When that history cannot be read, the
//   earlier failures cannot be seen, so nothing is attested and the run fails.
// - A task whose tool on this machine is not the version its pin file names (.xcode-version,
//   Web/.node-version, Go/go.mod) is not run and not attested: its hash reads the pin file, so
//   a pass with another tool would cover a run that CI would make with the pinned one.
// - A Node that links a library from the system (zlib, brotli, zstd, OpenSSL, ICU: a Homebrew
//   Node does) is not the pinned Node, also at the version Web/.node-version names: CI runs the
//   official build, whose own zlib gzips the web bundle to other sizes (#5774: 637216 bytes on
//   this Mac where CI measured 640413, over the budget), and whose ICU and OpenSSL are its own.
// - Nothing is attested when a task changed the tree (a stale generated file): the hashes were
//   of the tree before.
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const TARGETS = ["test", "vet", "lint"];
// The builds of CI's `apps` job (#6060): its Nx step builds only these, so only these of the
// build target are attested (go:build and web:build are the Linux job's).
const APP_PROJECTS = ["mac", "remote"];
const STATEMENT = "https://in-toto.io/Statement/v1";
const PREDICATE = "https://in-toto.io/attestation/test-result/v0.1";
// Nx's task_history statuses for a result it took from a cache: never a first run.
const CACHED = new Set(["local-cache", "local-cache-kept-existing", "remote-cache"]);

// ---- Pure parts (the selftest covers them) ----

/** The tasks to attest: each task of the graph that runs something (not a noop). */
export function attestable(tasks) {
  return tasks.filter((t) => t.executor !== "nx:noop" && t.hash);
}

/** One list of the two hash runs (the checks, the app builds): a shared dependency once. */
export function mergeTasks(...lists) {
  const by = new Map();
  for (const t of lists.flat()) if (!by.has(t.id)) by.set(t.id, t);
  return [...by.values()].sort((a, b) => (a.id < b.id ? -1 : 1));
}

/**
 * The Nx runs for the planned tasks that are not skipped: the checks' targets over their
 * projects, then the app builds; their dependencies (wire:gen, …) run as Nx's dependsOn says.
 * A check's part under a noop (agentskit:test-unit under agentskit:test, which attestable drops)
 * is named by its own target, or no run starts it (#6154). One run for the checks, so a shared
 * dependency runs once in it. A planned task that still does not run fails the run (notStarted).
 */
export function nxRuns(planned) {
  const left = planned.filter((t) => !t.skip);
  const isApp = (t) => t.target === "build" && APP_PROJECTS.includes(t.project);
  const isCheck = (t) => TARGETS.some((x) => t.target === x || t.target.startsWith(`${x}-`));
  const checks = [...new Set(left.filter(isCheck).map((t) => t.project))].sort();
  const parts = [...new Set(left.filter((t) => isCheck(t) && !TARGETS.includes(t.target)).map((t) => t.target))].sort();
  const apps = [...new Set(left.filter(isApp).map((t) => t.project))].sort();
  const runs = [];
  if (checks.length) runs.push({ targets: [...TARGETS, ...parts], projects: checks });
  if (apps.length) runs.push({ targets: ["build"], projects: apps });
  return runs;
}

/**
 * The planned tasks that did not run here, by their verdicts: no result, or a result from a cache.
 * A task skipped by its pin (another Xcode, Node or Go) is not one: the plan says "skip", and CI
 * runs it. Any other makes the run fail, by name (#6154).
 */
export function notStarted(verdicts) {
  return verdicts.filter((v) => v.result === "NOT_RUN" && !v.task.skip).map((v) => v.task.id);
}

/** The pin files a task's hash reads, from its file inputs. */
export function pinsOf(task) {
  const pins = [];
  for (const file of task.files ?? []) {
    if (file === ".xcode-version" || file === "Web/.node-version" || file === "Go/go.mod") pins.push(file);
  }
  return pins;
}

// The libraries whose bytes a test can see: compressed sizes, Intl's words, crypto.
const NODE_OWN_LIBS = ["zlib", "brotli", "zstd", "openssl"];

/**
 * The Node build as the pin means it: its version, or its version and the libraries it takes
 * from the system, which the official build (CI's, actions/setup-node) has its own copy of.
 * versions: process.versions; variables: process.config.variables.
 */
export function nodeBuild(versions, variables) {
  const shared = NODE_OWN_LIBS.filter((lib) => variables[`node_shared_${lib}`] === true);
  if (variables.icu_system === true) shared.push("icu");
  return shared.length ? `${versions.node} with the system's ${shared.join(", ")}` : versions.node;
}

/** The inputs of a task that the change touches: the "why". */
export function changedInputs(task, changedFiles) {
  const files = new Set(task.files ?? []);
  return changedFiles.filter((f) => files.has(f)).sort();
}

/**
 * The verdict for each planned task, from the run's results and the lane's ledger.
 * results: Map id -> {status, code} (the first row Nx wrote for its hash in this run).
 * ledger: Set of "id hash" for each task that failed in this lane: the attest runs' own ledger
 * and the failures in Nx's task history since the branch began, from any Nx run (#6173).
 * unreadable: null, or why Nx's task history could not be read: then the earlier failures are
 * not known, and no task is attested (#6173).
 * Returns {verdicts: [{task, attested, result, reason}], failed: ["id hash", …]}.
 */
export function decide(planned, results, ledger, unreadable = null) {
  const verdicts = [];
  const failed = [];
  for (const task of planned) {
    const key = `${task.id} ${task.hash}`;
    if (task.skip) {
      verdicts.push({ task, attested: false, result: "NOT_RUN", reason: task.skip });
      continue;
    }
    const r = results.get(task.id);
    if (!r) {
      verdicts.push({ task, attested: false, result: "NOT_RUN", reason: "no result at its planned hash (a dependency failed, or its hash changed in the run)" });
    } else if (CACHED.has(r.status)) {
      verdicts.push({ task, attested: false, result: "NOT_RUN", reason: `Nx took it from its cache (${r.status}), not a first run` });
    } else if (r.code !== 0) {
      failed.push(key);
      verdicts.push({ task, attested: false, result: "FAILED", reason: `failed (exit ${r.code})` });
    } else if (ledger.has(key)) {
      verdicts.push({ task, attested: false, result: "PASSED", reason: "failed earlier in this lane at the same inputs; a pass after a fail is not attested (principle 9)" });
    } else if (unreadable) {
      verdicts.push({ task, attested: false, result: "PASSED", reason: "passed, but Nx's task history could not be read, so earlier failures at the same inputs cannot be seen (#6173)" });
    } else {
      verdicts.push({ task, attested: true, result: "PASSED", reason: "passed on its first run" });
    }
  }
  return { verdicts, failed };
}

/** One in-toto Statement v1 with the test-result predicate, for a task that passed. */
export function statement(task, ctx) {
  const project = {
    name: `${task.root === "." ? "" : `${task.root}/`}project.json`,
    digest: { gitBlob: ctx.blobs[`${task.root === "." ? "" : `${task.root}/`}project.json`] ?? "" },
    annotations: { changedInputs: changedInputs(task, ctx.changedFiles) },
  };
  return {
    _type: STATEMENT,
    subject: [{ name: task.id, digest: { nxTaskHash: task.hash } }],
    predicateType: PREDICATE,
    predicate: {
      result: "PASSED",
      configuration: [
        { name: "nx.json", digest: { gitBlob: ctx.blobs["nx.json"] ?? "" } },
        project,
        {
          name: "run",
          uri: ctx.commitUrl,
          // For information only: CI matches on the subject's hash, never on these.
          annotations: {
            patchId: ctx.patchId,
            headTree: ctx.headTree,
            platform: ctx.platform,
            toolchain: ctx.toolchain,
            lane: ctx.lane,
          },
        },
      ],
      passedTests: [task.id],
      warnedTests: [],
      failedTests: [],
    },
  };
}

/** The check run's body (POST /repos/{repo}/check-runs). */
export function checkRun(verdicts, statements, ctx) {
  const all = verdicts.length > 0 && verdicts.every((v) => v.attested);
  const rows = verdicts.map(
    (v) => `| \`${v.task.id}\` | \`${v.task.hash}\` | ${v.attested ? "attested" : "not attested"} | ${v.reason} | ${changedInputs(v.task, ctx.changedFiles).length} |`,
  );
  const summary = [
    `${statements.length} of ${verdicts.length} tasks attested on ${ctx.platform}, at the inputs of \`${ctx.headSha.slice(0, 12)}\` (#5584). CI skips each attested task whose hash it calculates again at the head (#5585).`,
    "",
    "| Task | Nx hash | | Why | Inputs changed |",
    "|---|---|---|---|---|",
    ...rows,
  ].join("\n");
  const text = ["```jsonl", ...statements.map((s) => JSON.stringify(s)), "```"].join("\n");
  return {
    name: `attestation/${ctx.platform}`,
    head_sha: ctx.headSha,
    status: "completed",
    // Never "failure": a red check that is not required would wake Fix failed checks for a
    // result that CI runs again anyway. Neutral says "not everything is covered".
    conclusion: all ? "success" : "neutral",
    external_id: ctx.lane.session || ctx.lane.branch,
    output: {
      title: `${statements.length}/${verdicts.length} tasks attested`,
      summary: summary.slice(0, 65000),
      text: text.slice(0, 65000),
    },
  };
}

// ---- Impure parts ----

function sh(cmd, args, opts = {}) {
  return execFileSync(cmd, args, { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"], ...opts }).trim();
}

function nxEnv() {
  // As scripts/nx.sh sets them: the hashes must be the run's. AGENTS_ATTEST (no task's input)
  // makes a test that skips on a missing built file (Web/dist) fail instead, as on CI: a skip
  // is a pass to Nx, and an attested skip is a test that never ran (#5774).
  return { ...process.env, NX_NO_CLOUD: "true", NX_LOAD_DOT_ENV_FILES: "false", NX_DAEMON: "false", NX_TUI: "false", AGENTS_ATTEST: "1" };
}

function versionOf(cmd, args, env) {
  const r = spawnSync(cmd, args, { cwd: root, encoding: "utf8", env });
  return r.status === 0 ? r.stdout.trim() : null;
}

/** For each pin file, the version the file names and the one on this machine. */
function toolchain(env) {
  const tools = {};
  const xcodeWant = readFileSync(join(root, ".xcode-version"), "utf8").trim();
  const xcode = versionOf("xcodebuild", ["-version"], env);
  tools[".xcode-version"] = { want: xcodeWant, have: xcode ? xcode.split("\n")[0].replace(/^Xcode\s+/, "") : null };
  const nodeWant = readFileSync(join(root, "Web/.node-version"), "utf8").trim();
  tools["Web/.node-version"] = { want: nodeWant, have: nodeBuild(process.versions, process.config.variables) };
  const goWant = /^go\s+(\S+)/m.exec(readFileSync(join(root, "Go/go.mod"), "utf8"))?.[1] ?? null;
  const go = versionOf("Go/scripts/go.sh", ["env", "GOVERSION"], env);
  tools["Go/go.mod"] = { want: goWant, have: go ? go.replace(/^go/, "") : null };
  return tools;
}

function skipFor(task, tools) {
  for (const pin of pinsOf(task)) {
    const t = tools[pin];
    if (!t || t.have !== t.want) return `${pin} says ${t?.want}, this machine has ${t?.have ?? "none"}`;
  }
  return null;
}

/** The folder of Nx's shared databases (~/.nx/<id>/databases), as Nx itself resolves it. */
async function nxDataDirectory() {
  if (process.env.NX_WORKSPACE_DATA_DIRECTORY) return process.env.NX_WORKSPACE_DATA_DIRECTORY;
  const { createRequire } = await import("node:module");
  const require = createRequire(import.meta.url);
  return require(join(root, "node_modules", "nx", "dist", "src", "utils", "cache-directory")).sharedDataDirectory(root, "workspace-data");
}

/**
 * What Nx's task history says about the planned tasks (#6173). Every Nx run on this machine
 * (`nx affected`, `nx run`, an attest run) writes a row for each task to `task_history`, keyed by
 * the task's hash, in a database that every worktree shares; a row does not say which worktree
 * ran it, so a failure at the same hash in another worktree counts too (the same inputs failed).
 *   results: Map id -> {status, code}, the first row at the task's hash from runStart (this run).
 *   earlier: Set of "id hash" for each row at the task's hash with a non-zero code from
 *            branchStart to runStart: a failure earlier in the lane.
 *   unreadable: null, or why the history could not be read. Then earlier is not known, and no
 *            task may be attested as "passed on its first run".
 * A .db file with no task_history (an empty `*.db` from a glob that did not match) is named and
 * not read; when no file has it, the history is unreadable.
 */
export async function taskHistory(dir, planned, { branchStart, runStart }) {
  const results = new Map();
  const earlier = new Set();
  let dbs = [];
  try {
    for (const e of readdirSync(dir, { withFileTypes: true, recursive: true })) {
      if (e.isFile() && e.name.endsWith(".db")) dbs.push(join(e.parentPath, e.name));
    }
  } catch (error) {
    return { results, earlier, unreadable: `${dir}: ${error.message}` };
  }
  dbs = dbs.sort();
  const { DatabaseSync } = await import("node:sqlite");
  const problems = [];
  let read = 0;
  for (const file of dbs) {
    let db;
    try {
      db = new DatabaseSync(file, { readOnly: true });
      const table = db.prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'task_history'").get();
      if (!table) {
        console.error(`attest: ${file} has no task_history table; not Nx's history, not read`);
        continue;
      }
      const first = db.prepare("SELECT status, code FROM task_history WHERE hash = ? AND start >= ? ORDER BY start LIMIT 1");
      const failed = db.prepare("SELECT 1 FROM task_history WHERE hash = ? AND start >= ? AND start < ? AND code != 0 LIMIT 1");
      for (const t of planned) {
        if (!results.has(t.id)) {
          const row = first.get(t.hash, runStart);
          if (row) results.set(t.id, { status: row.status, code: row.code });
        }
        if (failed.get(t.hash, branchStart, runStart)) earlier.add(`${t.id} ${t.hash}`);
      }
      read++;
    } catch (error) {
      problems.push(`${file}: ${error.message}`);
    } finally {
      db?.close();
    }
  }
  if (problems.length) return { results, earlier, unreadable: problems.join("; ") };
  if (read === 0) return { results, earlier, unreadable: `no database in ${dir} has a task_history table` };
  return { results, earlier, unreadable: null };
}

/**
 * When this branch began, in ms: the earliest of the branch's creation in its reflog, the
 * merge base's commit time and the first commit on the branch. The earliest, so that a failure
 * in the lane before its first commit, or before a merge of main moved the merge base, counts.
 */
export function branchStartOf(times) {
  const known = times.filter((t) => Number.isFinite(t) && t > 0);
  return known.length ? Math.min(...known) : 0;
}

function branchTimes(base) {
  const sec = (cmd, args) => {
    const r = spawnSync(cmd, args, { cwd: root, encoding: "utf8" });
    if (r.status !== 0) return [];
    return r.stdout.split("\n").map((s) => Number(s.trim()) * 1000).filter((n) => n > 0);
  };
  const branch = sh("git", ["rev-parse", "--abbrev-ref", "HEAD"]);
  // The reflog's own times (%gd with --date=unix is "<ref>@{<seconds>}"), not the commits'.
  const reflog = spawnSync("git", ["log", "-g", "--date=unix", "--format=%gd", `refs/heads/${branch}`, "--"], { cwd: root, encoding: "utf8" });
  const created = reflog.status === 0 ? [...reflog.stdout.matchAll(/@\{(\d+)\}/g)].map((m) => Number(m[1]) * 1000) : [];
  return [
    ...created,
    ...sec("git", ["show", "-s", "--format=%ct", base]),
    ...sec("git", ["log", "--format=%at", `${base}..HEAD`]),
  ];
}

function ledgerPath() {
  return resolve(root, sh("git", ["rev-parse", "--git-path", "agents-attest/failed"]));
}

function readLedger(path) {
  return new Set(existsSync(path) ? readFileSync(path, "utf8").split("\n").filter(Boolean) : []);
}

function platform() {
  const os = { darwin: "macos", linux: "linux", win32: "windows" }[process.platform] ?? process.platform;
  return `${os}-${process.arch}`;
}

/** Where the run step keeps its result for a head commit, for the post step to read. */
function keptPath(headSha) {
  return resolve(root, sh("git", ["rev-parse", "--git-path", `agents-attest/runs/${headSha}.json`]));
}

/** The checks' tasks and the app builds (#6060), with their hashes at the head. */
function hashesAt(base, env) {
  const hash = (args) => JSON.parse(execFileSync("node", [join(root, "scripts/nx-hashes.cjs"), "--affected", base, "--json", ...args], {
    cwd: root, encoding: "utf8", env, stdio: ["ignore", "pipe", "inherit"], maxBuffer: 256 * 1024 * 1024,
  }));
  return mergeTasks(hash(TARGETS), hash(["--projects", APP_PROJECTS.join(","), "build"]));
}

/** The pull request of this branch, when its head is headSha; else a message and null. */
function pullRequestAt(headSha) {
  const view = spawnSync("gh", ["pr", "view", "--json", "number,headRefOid,url"], { cwd: root, encoding: "utf8" });
  if (view.status !== 0) {
    console.error("attest: no pull request for this branch; push and open it first, or use `attest run`");
    return null;
  }
  const pr = JSON.parse(view.stdout);
  if (pr.headRefOid !== headSha) {
    console.error(`attest: the pull request's head is ${pr.headRefOid.slice(0, 12)}, this is ${headSha.slice(0, 12)}: push first`);
    return null;
  }
  return pr;
}

/**
 * The run step (#6059): run the tasks at the head commit, before the push, and keep the check
 * run's body on disk by head sha, with each task's hash. Returns {code, body}.
 */
async function runStep({ base, planOnly, env, headSha }) {
  const changedFiles = sh("git", ["diff", "--name-only", base, "HEAD"]).split("\n").filter(Boolean);
  const tools = toolchain(env);
  const planned = attestable(hashesAt(base, env)).map((t) => ({ ...t, skip: skipFor(t, tools) }));
  if (planned.length === 0) {
    console.log("attest: the change affects no test, vet, lint or app build task; nothing to attest");
    if (!planOnly) keep(headSha, keptRun(headSha, base, null, null));
    return { code: 0, body: null };
  }
  for (const t of planned) {
    const why = changedInputs(t, changedFiles);
    console.log(`${t.skip ? "skip" : "run "} ${t.id} ${t.hash}${t.skip ? ` (${t.skip})` : ""}: ${why.length} inputs changed${why.length ? ` (${why.slice(0, 3).join(", ")}${why.length > 3 ? ", …" : ""})` : ""}`);
  }
  if (planOnly) return { code: 0, body: null };

  // Only the targets asked; their dependencies (wire:gen, …) run as Nx's dependsOn says.
  const since = Date.now();
  for (const r of nxRuns(planned)) {
    // One task at a time, as CI runs them (#5632): a first run under load is a flake's chance.
    spawnSync(join(root, "scripts/nx.sh"), [
      "run-many", "-t", ...r.targets, `--projects=${r.projects.join(",")}`,
      "--skip-nx-cache", "--nxBail=false", "--parallel=1", "--output-style=stream",
    ], { cwd: root, stdio: "inherit", env });
  }
  const branchStart = branchStartOf(branchTimes(base));
  const { results, earlier, unreadable } = await taskHistory(await nxDataDirectory(), planned.filter((t) => !t.skip), { branchStart, runStart: since });
  if (unreadable) {
    console.error(`attest: cannot read Nx's task history (${unreadable}), so this run cannot see the failures earlier in this lane; no task is attested as passed on its first run`);
  }
  for (const key of earlier) console.log(`attest: ${key} failed in an Nx run since this branch began`);
  const changedTree = sh("git", ["status", "--porcelain", "--untracked-files=normal"]);
  const path = ledgerPath();
  const ledger = readLedger(path);
  let { verdicts, failed } = decide(planned, results, new Set([...ledger, ...earlier]), unreadable);
  // The ledger keeps the history's failures too: Nx may prune its rows, the ledger is not pruned.
  const kept = new Set([...ledger, ...earlier, ...failed]);
  if (kept.size > ledger.size) {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, [...kept].sort().join("\n") + "\n");
  }
  if (changedTree) {
    console.error(`attest: the run changed the tree, so its hashes are not the head commit's; nothing is attested:\n${changedTree}`);
    verdicts = verdicts.map((v) => (v.attested ? { ...v, attested: false, reason: "the run changed the tree (a stale generated file?)" } : v));
  }

  const repo = sh("gh", ["repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"]);
  const blobFiles = ["nx.json", ...new Set(planned.map((t) => `${t.root === "." ? "" : `${t.root}/`}project.json`))];
  const blobs = Object.fromEntries(blobFiles.map((f) => [f, sh("git", ["rev-parse", `HEAD:${f}`])]));
  const diff = execFileSync("git", ["diff", base, "HEAD"], { cwd: root, maxBuffer: 1024 * 1024 * 1024 });
  const patchId = execFileSync("git", ["patch-id", "--stable"], { cwd: root, input: diff, encoding: "utf8" }).split(" ")[0] || null;
  const ctx = {
    headSha,
    headTree: sh("git", ["rev-parse", "HEAD^{tree}"]),
    patchId,
    platform: platform(),
    toolchain: Object.fromEntries(Object.entries(tools).map(([k, v]) => [k, v.have])),
    lane: {
      session: process.env.AGENTS_SESSION_ID || process.env.CLAUDE_CODE_SESSION_ID || process.env.CODEX_SESSION_ID || "",
      branch: sh("git", ["rev-parse", "--abbrev-ref", "HEAD"]),
    },
    commitUrl: `https://github.com/${repo}/commit/${headSha}`,
    blobs,
    changedFiles,
  };
  const statements = verdicts.filter((v) => v.attested).map((v) => statement(v.task, ctx));
  const body = checkRun(verdicts, statements, ctx);
  for (const v of verdicts) console.log(`${v.attested ? "attested    " : "not attested"} ${v.task.id}: ${v.reason}`);
  keep(headSha, keptRun(headSha, base, repo, body));
  // The kept run still has a statement for each task that passed; but a run with a planned task
  // that failed or did not start is not a pass (#6154).
  let code = unreadable ? 1 : 0;
  const unrun = notStarted(verdicts);
  if (unrun.length) {
    console.error(`attest: ${unrun.length} planned task${unrun.length === 1 ? "" : "s"} did not run here: ${unrun.join(", ")}`);
    code = 1;
  }
  const failedIds = verdicts.filter((v) => v.result === "FAILED").map((v) => v.task.id);
  if (failedIds.length) {
    console.error(`attest: ${failedIds.length} task${failedIds.length === 1 ? "" : "s"} failed: ${failedIds.join(", ")}`);
    code = 1;
  }
  return { code, body };
}

function keep(headSha, kept) {
  const path = keptPath(headSha);
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, JSON.stringify(kept) + "\n");
  console.log(`attest: kept the run for ${headSha.slice(0, 12)} in ${path}; push, then \`scripts/checks.sh attest post\``);
}

/** What the run step keeps: the check run, and the base it hashed from. */
export function keptRun(headSha, base, repo, body) {
  return { headSha, base, repo, body };
}

/**
 * The statements whose subject hash is not the task's hash now (hashes: [{id, hash}]): a kept
 * run is posted only when this is empty. A task that is not in hashes changed too.
 */
export function staleStatements(body, hashes) {
  const now = new Map(hashes.map((t) => [t.id, t.hash]));
  const stale = [];
  for (const line of body.output.text.split("\n").slice(1, -1)) {
    const subject = JSON.parse(line).subject[0];
    if (now.get(subject.name) !== subject.digest.nxTaskHash) stale.push(`${subject.name} ${subject.digest.nxTaskHash} (now ${now.get(subject.name) ?? "not affected"})`);
  }
  return stale;
}

/**
 * The post step (#6059): read the run kept for the head commit, check that the pull request's
 * head is that commit and that no task's hash changed, and post the check run. Seconds, not
 * minutes: it runs no task.
 */
function postStep({ env, headSha, pr }) {
  const path = keptPath(headSha);
  if (!existsSync(path)) {
    console.error(`attest: no kept run for ${headSha.slice(0, 12)}: run \`scripts/checks.sh attest run\` at this commit first`);
    return 2;
  }
  const kept = JSON.parse(readFileSync(path, "utf8"));
  if (kept.headSha !== headSha) {
    console.error(`attest: the kept run in ${path} is for ${String(kept.headSha).slice(0, 12)}, not ${headSha.slice(0, 12)}`);
    return 2;
  }
  if (!kept.body) {
    console.log("attest: the change affects no test, vet or lint task; nothing to post");
    return 0;
  }
  pr ??= pullRequestAt(headSha);
  if (!pr) return 2;
  const stale = staleStatements(kept.body, attestable(hashesAt(kept.base, env)));
  if (stale.length) {
    console.error(`attest: these hashes changed since the run (a tool, a package or the environment?); run \`scripts/checks.sh attest run\` again:\n${stale.join("\n")}`);
    return 2;
  }
  const posted = spawnSync("gh", ["api", "-X", "POST", `repos/${kept.repo}/check-runs`, "--input", "-"], {
    cwd: root, encoding: "utf8", input: JSON.stringify(kept.body),
  });
  if (posted.status !== 0) {
    const said = `${posted.stdout ?? ""}${posted.stderr ?? ""}`.trim() || `gh exited ${posted.status ?? posted.signal}`;
    const why = /not accessible by integration|HTTP 403/.test(said)
      ? "the agents' bot has no Checks: write (a bot made before #5584 has Checks: read: its owner accepts the new permission on GitHub)"
      : "it needs gh as the agents' bot, with Checks: write";
    console.error(`attest: could not post the check run: ${why}:\n${said}`);
    // The run's statements, so a lane can still see them.
    console.log(JSON.stringify(kept.body, null, 1));
    return 1;
  }
  const run = JSON.parse(posted.stdout);
  console.log(`attest: posted ${run.name} as ${run.app?.slug ?? "?"} on PR #${pr.number}: ${run.html_url}`);
  return 0;
}

async function main(argv) {
  let step = null;
  let base = null;
  let planOnly = false;
  let post = true;
  const usage = () => {
    console.error("usage: scripts/checks.sh attest [run | post] [--base REF] [--plan] [--no-post]");
    return 2;
  };
  for (let i = 0; i < argv.length; i++) {
    if ((argv[i] === "run" || argv[i] === "post") && i === 0) step = argv[i];
    else if (argv[i] === "--base") base = argv[++i];
    else if (argv[i] === "--plan") planOnly = true;
    else if (argv[i] === "--no-post") post = false;
    else return usage();
  }
  if (step === "post" && (base || planOnly || !post)) return usage();
  if (step === "run") post = false;
  const dirty = () => sh("git", ["status", "--porcelain", "--untracked-files=normal"]);
  if (!planOnly && dirty()) {
    console.error("attest: the tree has changes that are not committed; the hashes would not be the head commit's. Commit first.");
    return 2;
  }
  const env = nxEnv();
  const headSha = sh("git", ["rev-parse", "HEAD"]);
  if (step === "post") return postStep({ env, headSha });

  // Both steps: refuse before the run when the pull request's head is not this commit.
  let pr = null;
  if (post && !planOnly) {
    pr = pullRequestAt(headSha);
    if (!pr) return 2;
  }
  base ??= sh("git", ["merge-base", "origin/main", "HEAD"]);
  const { code, body } = await runStep({ base, planOnly, env, headSha });
  if (!body) return code;
  if (!post) {
    if (step !== "run") console.log(JSON.stringify(body, null, 1));
    return code;
  }
  // Post what passed also when a task failed or did not run; the exit still says so (#6154).
  const posted = postStep({ env, headSha, pr });
  return code || posted;
}

// ---- Selftest (scripts/lint.sh runs it) ----

function selftest() {
  const assert = (ok, what) => {
    if (!ok) {
      console.error(`checks-attest selftest: ${what}`);
      process.exit(1);
    }
  };
  const task = { id: "go:test", project: "go", target: "test", hash: "111", executor: "nx:run-commands", root: "Go", files: ["Go/a.go", "Go/go.mod", "nx.json"] };
  const noop = { ...task, id: "agentskit:test", executor: "nx:noop" };
  assert(attestable([task, noop]).length === 1, "a noop task is attested");
  assert(pinsOf(task).join() === "Go/go.mod", "pinsOf misses Go/go.mod");
  assert(changedInputs(task, ["Go/a.go", "Web/x.ts"]).join() === "Go/a.go", "changedInputs is not the task's changed files");
  assert(skipFor(task, { "Go/go.mod": { want: "1.26.9", have: "1.27.4" } }) !== null, "another Go is run");
  assert(skipFor(task, { "Go/go.mod": { want: "1.26.9", have: "1.26.9" } }) === null, "the pinned Go is skipped");

  const web = { ...task, id: "web:test", root: "Web", files: ["Web/a.ts", "Web/.node-version"] };
  const official = nodeBuild({ node: "26.8.2" }, { node_shared_zlib: false, icu_system: false });
  const homebrew = nodeBuild({ node: "26.8.2" }, { node_shared_zlib: true, node_shared_openssl: true, icu_system: true });
  assert(official === "26.8.2", "the official Node's build is not its version");
  assert(skipFor(web, { "Web/.node-version": { want: "26.8.2", have: official } }) === null, "the official pinned Node is skipped");
  assert(skipFor(web, { "Web/.node-version": { want: "26.8.2", have: homebrew } }) !== null, "a Node with the system's zlib is run (#5774)");

  // Fail, then pass at the same hash: no statement. A new hash then attests.
  const ledger = new Set();
  const first = decide([task], new Map([["go:test", { status: "failure", code: 1 }]]), ledger);
  assert(first.verdicts[0].attested === false && first.failed.join() === "go:test 111", "a failure is attested or not recorded");
  for (const k of first.failed) ledger.add(k);
  const second = decide([task], new Map([["go:test", { status: "success", code: 0 }]]), ledger);
  assert(second.verdicts[0].attested === false, "a pass after a fail at the same hash is attested");
  const changed = decide([{ ...task, hash: "222" }], new Map([["go:test", { status: "success", code: 0 }]]), ledger);
  assert(changed.verdicts[0].attested === true, "a pass at a new hash after a fail is not attested");
  const cached = decide([task], new Map([["go:test", { status: "local-cache", code: 0 }]]), new Set());
  assert(cached.verdicts[0].attested === false, "a cache hit is attested");
  const none = decide([task], new Map(), new Set());
  assert(none.verdicts[0].attested === false, "a task with no result is attested");
  const skipped = decide([{ ...task, skip: "Xcode differs" }], new Map([["go:test", { status: "success", code: 0 }]]), new Set());
  assert(skipped.verdicts[0].attested === false, "a skipped task is attested");

  // The app builds (#6060): mac:build and remote:build run after the checks, no other build.
  const t = (id, extra = {}) => ({ id, project: id.split(":")[0], target: id.split(":")[1], hash: id, executor: "nx:run-commands", ...extra });
  const merged = mergeTasks([t("go:test"), t("wire:gen")], [t("mac:build"), t("wire:gen"), t("web:build")]);
  assert(merged.map((x) => x.id).join() === "go:test,mac:build,web:build,wire:gen", `the merged tasks: ${merged.map((x) => x.id)}`);
  const plan = nxRuns([...merged, t("remote:build"), t("go:build"), t("codetext:test", { skip: "Xcode differs" })]);
  assert(JSON.stringify(plan) === JSON.stringify([{ targets: TARGETS, projects: ["go"] }, { targets: ["build"], projects: ["mac", "remote"] }]), `the Nx runs: ${JSON.stringify(plan)}`);
  // #6154: the tasks under a noop (agentskit:test, dropped by attestable) run by their own targets.
  const kit = attestable([
    { ...t("agentskit:test"), executor: "nx:noop" }, t("agentskit:test-unit"), t("agentskit:test-control"),
    t("agentskit:test-integration"), t("swift-lint:lint"), t("agentskit-repo:test"),
  ]);
  const kitRuns = nxRuns(kit);
  assert(JSON.stringify(kitRuns) === JSON.stringify([
    { targets: [...TARGETS, "test-control", "test-integration", "test-unit"], projects: ["agentskit", "agentskit-repo", "swift-lint"] },
  ]), `the agentskit tasks get no run: ${JSON.stringify(kitRuns)}`);
  for (const task of kit) {
    assert(kitRuns.some((r) => r.targets.includes(task.target) && r.projects.includes(task.project)), `${task.id} is planned but not run`);
  }
  const unrun = decide(kit, new Map([["swift-lint:lint", { status: "success", code: 0 }], ["agentskit:test-unit", { status: "local-cache", code: 0 }]]), new Set());
  assert(notStarted(unrun.verdicts).join() === "agentskit:test-unit,agentskit:test-control,agentskit:test-integration,agentskit-repo:test",
    `the tasks that did not start: ${notStarted(unrun.verdicts)}`);
  assert(notStarted(decide([{ ...task, skip: "Xcode differs" }], new Map(), new Set()).verdicts).length === 0, "a task skipped by its pin fails the run");
  assert(nxRuns([t("mac:build", { skip: "Xcode differs" })]).length === 0, "a skipped app build runs");
  assert(JSON.stringify(nxRuns([t("remote:build")])) === JSON.stringify([{ targets: ["build"], projects: ["remote"] }]), "an app build alone");

  const ctx = {
    headSha: "a".repeat(40), headTree: "b".repeat(40), patchId: "c".repeat(40), platform: "macos-arm64",
    toolchain: { "Go/go.mod": "1.26.9" }, lane: { session: "s1", branch: "agents/x" },
    commitUrl: "https://github.com/o/r/commit/aaa", blobs: { "nx.json": "d1", "Go/project.json": "d2" }, changedFiles: ["Go/a.go"],
  };
  const s = statement(task, ctx);
  assert(s._type === STATEMENT && s.predicateType === PREDICATE, "the statement's types");
  assert(JSON.stringify(s.subject) === JSON.stringify([{ name: "go:test", digest: { nxTaskHash: "111" } }]), "the subject is not only the task and its hash");
  const subject = JSON.stringify(s.subject);
  assert(!subject.includes(ctx.patchId) && !subject.includes(ctx.headTree), "the patch id or the head tree is in the subject");
  const run = s.predicate.configuration.find((c) => c.name === "run");
  assert(run.annotations.patchId === ctx.patchId && run.annotations.headTree === ctx.headTree, "the patch id and head tree are not annotations");
  const project = s.predicate.configuration.find((c) => c.name === "Go/project.json");
  assert(project.digest.gitBlob === "d2" && project.annotations.changedInputs.join() === "Go/a.go", "the project.json descriptor");
  const body = checkRun(second.verdicts.concat(changed.verdicts), [s], ctx);
  assert(body.name === "attestation/macos-arm64" && body.conclusion === "neutral", "the check run's name or conclusion");
  const lines = body.output.text.split("\n").slice(1, -1);
  assert(lines.length === 1 && JSON.parse(lines[0]).subject[0].name === "go:test", "the check run's statements");
  assert(staleStatements(body, [{ id: "go:test", hash: "111" }]).length === 0, "a kept run at the same hash is stale");
  assert(staleStatements(body, [{ id: "go:test", hash: "333" }]).length === 1, "a kept run at another hash is posted");
  assert(staleStatements(body, []).length === 1, "a kept run for a task no longer affected is posted");

  // #6173: the history can't be read, so nothing is "passed on its first run".
  const blind = decide([task], new Map([["go:test", { status: "success", code: 0 }]]), new Set(), "no database has a task_history table");
  assert(blind.verdicts[0].attested === false && blind.verdicts[0].result === "PASSED", "a pass is attested with no task history");
  assert(blind.verdicts[0].reason !== "passed on its first run", "a pass with no task history says it passed on its first run");
  assert(branchStartOf([3000, Number.NaN, 1000, 0, 2000]) === 1000, "the branch start is not the earliest time");
  assert(branchStartOf([]) === 0, "with no times, the branch start is not the start of all history");
}

/** #6173 with Nx's own database shape: an earlier failure from any Nx run, and no history. */
async function selftestHistory() {
  const assert = (ok, what) => {
    if (!ok) {
      console.error(`checks-attest selftest: ${what}`);
      process.exit(1);
    }
  };
  const { mkdtempSync, rmSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const { DatabaseSync } = await import("node:sqlite");
  const dir = mkdtempSync(join(tmpdir(), "checks-attest-6173-"));
  try {
    const task = { id: "agentskit:test-unit", hash: "777" };
    const other = { id: "go:test", hash: "888" };
    const branchStart = 1000;
    const runStart = 5000;
    // The stray empty file of the issue: no task_history, named and not read.
    writeFileSync(join(dir, "*.db"), "");
    // Nothing to read yet but the stray file: the history is unreadable.
    const none = await taskHistory(dir, [task], { branchStart, runStart });
    assert(none.unreadable !== null, "a folder whose only .db has no task_history is read as history");
    const missing = await taskHistory(join(dir, "nope"), [task], { branchStart, runStart });
    assert(missing.unreadable !== null, "a missing folder is read as history");

    const db = new DatabaseSync(join(dir, "MACHINE-v6.db"));
    db.exec("CREATE TABLE task_history (id INTEGER PRIMARY KEY, hash TEXT, status TEXT, code INTEGER, start INTEGER, end INTEGER)");
    const row = db.prepare("INSERT INTO task_history (hash, status, code, start, end) VALUES (?, ?, ?, ?, ?)");
    row.run("777", "failure", 1, 2000, 2100); // `nx affected` failed it, in this lane
    row.run("777", "success", 0, 6000, 6100); // the attest run passed it
    row.run("888", "failure", 1, 500, 600); // before the branch began
    row.run("888", "success", 0, 6200, 6300);
    db.close();

    const h = await taskHistory(dir, [task, other], { branchStart, runStart });
    assert(h.unreadable === null, `the history is unreadable: ${h.unreadable}`);
    assert(h.results.get(task.id)?.code === 0 && h.results.get(other.id)?.code === 0, "the run's results are not its first rows");
    assert([...h.earlier].join() === "agentskit:test-unit 777", `the earlier failures: ${[...h.earlier]}`);
    const v = decide([{ ...task, skip: null }, { ...other, skip: null }], h.results, h.earlier, h.unreadable).verdicts;
    assert(v[0].attested === false, "a task that failed in nx affected earlier in the lane is attested (#6173)");
    assert(v[1].attested === true, "a task whose only failure is before the branch began is not attested");

    // A .db that Nx's history is in but can't be read (not SQLite): unreadable, nothing attested.
    writeFileSync(join(dir, "broken.db"), "not a database");
    const bad = await taskHistory(dir, [task, other], { branchStart, runStart });
    assert(bad.unreadable !== null, "a .db that cannot be read is not reported");
    const bv = decide([{ ...other, skip: null }], bad.results, bad.earlier, bad.unreadable).verdicts;
    assert(bv[0].attested === false, "a task is attested when a history file could not be read");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  console.log("checks-attest selftest: ok");
}

const argv = process.argv.slice(2);
if (argv[0] === "--selftest") {
  selftest();
  await selftestHistory();
}
else process.exit(await main(argv));
