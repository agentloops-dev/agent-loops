#!/usr/bin/env node
// scripts/checks.sh attest (#5584, epic #5581 §1 and §2): run the affected test, vet and lint
// tasks of this branch with Nx and no cache hits, then post one check run
// `attestation/<platform>` on the pull request's head commit, as the agents' bot. Its output
// holds one in-toto Statement v1 for each task that passed on its first run, with the
// test-result predicate. CI ignores it until #5585.
//
//   node scripts/checks-attest.mjs [--base REF] [--plan] [--no-post]
//   node scripts/checks-attest.mjs --selftest
//
// --plan prints the tasks, their hashes and why each runs (the inputs that changed), and runs
// nothing. --no-post runs them and prints the statements in place of posting them.
//
// Rules (the epic's principles):
// - The subject of a statement is the task and its Nx hash, nothing else (principle 3). The
//   patch id and the head tree are information, in an annotation.
// - Only a first run is attested (principle 9): each task runs with --skip-nx-cache, and a
//   result that Nx took from its cache is never attested. A task that fails is written to this
//   worktree's ledger (`git rev-parse --git-path agents-attest/failed`), and a later pass at the
//   same hash is not attested: only a change to its inputs (a new hash) clears it.
// - A task whose tool on this machine is not the version its pin file names (.xcode-version,
//   Web/.node-version, Go/go.mod) is not run and not attested: its hash reads the pin file, so
//   a pass with another tool would cover a run that CI would make with the pinned one.
// - Nothing is attested when a task changed the tree (a stale generated file): the hashes were
//   of the tree before.
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const TARGETS = ["test", "vet", "lint"];
const STATEMENT = "https://in-toto.io/Statement/v1";
const PREDICATE = "https://in-toto.io/attestation/test-result/v0.1";
// Nx's task_history statuses for a result it took from a cache: never a first run.
const CACHED = new Set(["local-cache", "local-cache-kept-existing", "remote-cache"]);

// ---- Pure parts (the selftest covers them) ----

/** The tasks to attest: each task of the graph that runs something (not a noop). */
export function attestable(tasks) {
  return tasks.filter((t) => t.executor !== "nx:noop" && t.hash);
}

/** The pin files a task's hash reads, from its file inputs. */
export function pinsOf(task) {
  const pins = [];
  for (const file of task.files ?? []) {
    if (file === ".xcode-version" || file === "Web/.node-version" || file === "Go/go.mod") pins.push(file);
  }
  return pins;
}

/** The inputs of a task that the change touches: the "why". */
export function changedInputs(task, changedFiles) {
  const files = new Set(task.files ?? []);
  return changedFiles.filter((f) => files.has(f)).sort();
}

/**
 * The verdict for each planned task, from the run's results and the lane's ledger.
 * results: Map id -> {status, code} (the first row Nx wrote for its hash in this run).
 * ledger: Set of "id hash" for each task that failed in this lane.
 * Returns {verdicts: [{task, attested, result, reason}], failed: ["id hash", …]}.
 */
export function decide(planned, results, ledger) {
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
    `${statements.length} of ${verdicts.length} tasks attested on ${ctx.platform}, at the inputs of \`${ctx.headSha.slice(0, 12)}\` (#5584). CI does not read this yet (#5585).`,
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
  // As scripts/nx.sh sets them: the hashes must be the run's.
  return { ...process.env, NX_NO_CLOUD: "true", NX_LOAD_DOT_ENV_FILES: "false", NX_DAEMON: "false", NX_TUI: "false" };
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
  tools["Web/.node-version"] = { want: nodeWant, have: process.version.replace(/^v/, "") };
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

async function nxResults(planned, since) {
  const { DatabaseSync } = await import("node:sqlite");
  const nxDist = join(root, "node_modules", "nx", "dist", "src");
  const { createRequire } = await import("node:module");
  const require = createRequire(import.meta.url);
  const dir = process.env.NX_WORKSPACE_DATA_DIRECTORY || require(join(nxDist, "utils", "cache-directory")).sharedDataDirectory(root, "workspace-data");
  const dbs = [];
  for (const e of readdirSync(dir, { withFileTypes: true, recursive: true })) {
    if (e.isFile() && e.name.endsWith(".db")) dbs.push(join(e.parentPath, e.name));
  }
  const results = new Map();
  for (const file of dbs) {
    const db = new DatabaseSync(file, { readOnly: true });
    try {
      const q = db.prepare("SELECT status, code FROM task_history WHERE hash = ? AND start >= ? ORDER BY start LIMIT 1");
      for (const t of planned) {
        if (results.has(t.id)) continue;
        const row = q.get(t.hash, since);
        if (row) results.set(t.id, { status: row.status, code: row.code });
      }
    } catch (error) {
      console.error(`attest: ${file}: ${error.message}`);
    } finally {
      db.close();
    }
  }
  return results;
}

function ledgerPath() {
  return join(root, sh("git", ["rev-parse", "--git-path", "agents-attest/failed"]));
}

function readLedger(path) {
  return new Set(existsSync(path) ? readFileSync(path, "utf8").split("\n").filter(Boolean) : []);
}

function platform() {
  const os = { darwin: "macos", linux: "linux", win32: "windows" }[process.platform] ?? process.platform;
  return `${os}-${process.arch}`;
}

async function main(argv) {
  let base = null;
  let planOnly = false;
  let post = true;
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--base") base = argv[++i];
    else if (argv[i] === "--plan") planOnly = true;
    else if (argv[i] === "--no-post") post = false;
    else {
      console.error("usage: scripts/checks.sh attest [--base REF] [--plan] [--no-post]");
      return 2;
    }
  }
  const dirty = () => sh("git", ["status", "--porcelain", "--untracked-files=normal"]);
  if (!planOnly && dirty()) {
    console.error("attest: the tree has changes that are not committed; the hashes would not be the head commit's. Commit first.");
    return 2;
  }
  const env = nxEnv();
  const headSha = sh("git", ["rev-parse", "HEAD"]);
  base ??= sh("git", ["merge-base", "origin/main", "HEAD"]);
  const changedFiles = sh("git", ["diff", "--name-only", base, "HEAD"]).split("\n").filter(Boolean);

  let pr = null;
  if (post && !planOnly) {
    const view = spawnSync("gh", ["pr", "view", "--json", "number,headRefOid,url"], { cwd: root, encoding: "utf8" });
    if (view.status !== 0) {
      console.error("attest: no pull request for this branch; push and open it first, or use --no-post");
      return 2;
    }
    pr = JSON.parse(view.stdout);
    if (pr.headRefOid !== headSha) {
      console.error(`attest: the pull request's head is ${pr.headRefOid.slice(0, 12)}, this is ${headSha.slice(0, 12)}: push first`);
      return 2;
    }
  }

  const hashed = execFileSync("node", [join(root, "scripts/nx-hashes.cjs"), "--affected", base, "--json", ...TARGETS], {
    cwd: root, encoding: "utf8", env, stdio: ["ignore", "pipe", "inherit"], maxBuffer: 256 * 1024 * 1024,
  });
  const tools = toolchain(env);
  const planned = attestable(JSON.parse(hashed)).map((t) => ({ ...t, skip: skipFor(t, tools) }));
  if (planned.length === 0) {
    console.log("attest: the change affects no test, vet or lint task; nothing to attest");
    return 0;
  }
  for (const t of planned) {
    const why = changedInputs(t, changedFiles);
    console.log(`${t.skip ? "skip" : "run "} ${t.id} ${t.hash}${t.skip ? ` (${t.skip})` : ""}: ${why.length} inputs changed${why.length ? ` (${why.slice(0, 3).join(", ")}${why.length > 3 ? ", …" : ""})` : ""}`);
  }
  if (planOnly) return 0;

  // Only the targets asked; their dependencies (wire:gen, …) run as Nx's dependsOn says.
  const runs = planned.filter((t) => !t.skip && TARGETS.includes(t.target)).map((t) => t.id);
  const parts = planned.filter((t) => !t.skip && !TARGETS.includes(t.target)).map((t) => t.id);
  const projects = [...new Set(planned.filter((t) => !t.skip).map((t) => t.project))];
  const since = Date.now();
  if (runs.length + parts.length > 0) {
    // One task at a time, as CI runs them (#5632): a first run under load is a flake's chance.
    spawnSync(join(root, "scripts/nx.sh"), [
      "run-many", "-t", ...TARGETS, `--projects=${projects.join(",")}`,
      "--skip-nx-cache", "--nxBail=false", "--parallel=1", "--output-style=static",
    ], { cwd: root, stdio: "inherit", env });
  }
  const results = await nxResults(planned.filter((t) => !t.skip), since);
  const changedTree = dirty();
  const path = ledgerPath();
  const ledger = readLedger(path);
  let { verdicts, failed } = decide(planned, results, ledger);
  if (failed.length) {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, [...new Set([...ledger, ...failed])].sort().join("\n") + "\n");
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
  if (!post) {
    console.log(JSON.stringify(body, null, 1));
    return 0;
  }
  const posted = spawnSync("gh", ["api", "-X", "POST", `repos/${repo}/check-runs`, "--input", "-"], {
    cwd: root, encoding: "utf8", input: JSON.stringify(body),
  });
  if (posted.status !== 0) {
    const said = `${posted.stdout ?? ""}${posted.stderr ?? ""}`.trim() || `gh exited ${posted.status ?? posted.signal}`;
    const why = /not accessible by integration|HTTP 403/.test(said)
      ? "the agents' bot has no Checks: write (a bot made before #5584 has Checks: read: its owner accepts the new permission on GitHub)"
      : "it needs gh as the agents' bot, with Checks: write";
    console.error(`attest: could not post the check run: ${why}:\n${said}`);
    // The run's statements, so a lane can still see them.
    console.log(JSON.stringify(body, null, 1));
    return 1;
  }
  const run = JSON.parse(posted.stdout);
  console.log(`attest: posted ${run.name} as ${run.app?.slug ?? "?"} on PR #${pr.number}: ${run.html_url}`);
  return 0;
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
  console.log("checks-attest selftest: ok");
}

const argv = process.argv.slice(2);
if (argv[0] === "--selftest") selftest();
else process.exit(await main(argv));
