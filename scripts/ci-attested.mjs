#!/usr/bin/env node
// CI honours the attestation (#5585, epic #5581 §3). On a pull request, `go`'s "Affected
// projects" step reads the `attestation/<platform>` check runs that a lane posted with
// `scripts/checks.sh attest` (#5584), calculates the Nx hash of each task that CI would run at
// the pull request's head, and says which of them an accepted statement covers. The Nx steps
// then run only the others, and a macOS step whose tasks are all covered is skipped, so its job
// does not wait for a runner.
//
//   node scripts/ci-attested.mjs select --base SHA --pr N [--repo OWNER/NAME] [--runs FILE]
//   node scripts/ci-attested.mjs run [--dry] --group GROUP --base SHA --skip "TASK ..."
//   node scripts/ci-attested.mjs --selftest
//
// select prints `skip-<group>=<task ids>` and `all-<group>=true|false` for each group (for
// $GITHUB_OUTPUT), and writes the table of skipped tasks, with the statement that covers each,
// to $GITHUB_STEP_SUMMARY. --runs reads the check runs from a file (a JSON array, as GitHub's
// check-runs API gives them) in place of GitHub, for a test. run runs the group's tasks with
// Nx, but those in --skip, one target at a time (--dry: say the Nx runs, run none).
//
// The rules:
// - Only a pull request skips anything. A push to main and a run by hand run every task (the
//   workflow calls select only on a pull request).
// - A check run counts only when GitHub says the agents' bot posted it (its app's slug is
//   ATTEST_APP, default alexec-agents), it is completed, and its lane is not revoked
//   (revoked(), below: #5586 fills it).
// - A task is covered only by a statement whose subject is that task and the hash that CI
//   calculates now, with the result PASSED. The patch id and the head tree in the statement
//   are information only: CI never matches on them.
// - The hash names its platform (`uname -sm`, a runtime input of the Go and Xcode tasks), so a
//   macOS pass never covers a Linux task. The macOS groups' hashes are calculated here, on
//   Linux, with a `uname` that says the macOS runner's platform: an equal hash is then equal
//   inputs on that platform. A task with no platform input (the web tasks) has one hash
//   everywhere, so a Mac's pass covers it in the Linux job too.
// - Every failure here (GitHub does not answer, a statement does not parse) skips nothing: CI
//   then runs everything, as with no attestation (principle 11).
import { execFileSync, spawnSync } from "node:child_process";
import { appendFileSync, chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const STATEMENT = "https://in-toto.io/Statement/v1";
const PREDICATE = "https://in-toto.io/attestation/test-result/v0.1";
const SWIFT_PROJECTS = new Set(["agentskit", "agentskit-repo", "codetext"]);

// The Nx steps of ci.yml, each a group: its targets and projects are the step's `nx affected`,
// and its platform is its job's runner. Keep them in step with ci.yml.
// - linux: `go`'s "Nx affected" (ubuntu-latest).
// - swift: `test-swift`'s "Nx affected, Swift" (xcode-27, Apple silicon).
// - gomac: `test-go`'s "Nx affected, Go on a Mac" (#5755) (no lint: the Linux job's).
// - swiftlint: `test-swift`'s "SwiftLint" (swift-lint:lint); one task, so it runs or is skipped.
export const GROUPS = {
  linux: { platform: "linux-x64", targets: ["gen", "build", "vet", "lint", "test"], has: (t) => !t.tags.includes("xcode") },
  swift: { platform: "macos-arm64", targets: ["test"], has: (t) => SWIFT_PROJECTS.has(t.project) },
  gomac: { platform: "macos-arm64", targets: ["gen", "build", "vet", "test"], has: (t) => t.project !== "web" && !t.tags.includes("xcode") },
  swiftlint: { platform: "macos-arm64", targets: ["lint"], has: (t) => t.project === "swift-lint" },
};

// What `uname -sm` says on each platform: the runtime input that puts the platform in a hash.
const UNAME = { "linux-x64": "Linux x86_64", "linux-arm64": "Linux aarch64", "macos-arm64": "Darwin arm64", "macos-x64": "Darwin x86_64" };

// The env inputs of the Go and Xcode tasks (nx.json's go-toolchain and xcode-toolchain). The
// macOS jobs set none of them, so a macOS hash here is calculated with none.
const ENV_INPUTS = ["CGO_ENABLED", "GOFLAGS", "GOEXPERIMENT", "AGENTS_QUARANTINE", "AGENTS_RUN_FLAKY", "AGENTS_BUILD_CACHE"];

// ---- Pure parts (the selftest covers them) ----

/**
 * A lane whose attestations CI no longer accepts (#5586, epic §5). The list is the lines of
 * `revoked.txt` on the orphan branch `attestation-revocations` (scripts/ci-sample.mjs adds a
 * line when a sampled rerun shows a false attestation), and ATTEST_REVOKED_LANES (session ids
 * or branches, space-separated) for a lane revoked by hand.
 */
export function revoked(lane, list) {
  return Boolean(lane && list.has(lane));
}

/**
 * The statements CI accepts, from the check runs on the pull request's commits.
 * Returns {accepted: [{task, hash, run}], refused: [{run, reason}]}.
 */
export function accept(runs, { app, revokedLanes }) {
  const accepted = [];
  const refused = [];
  for (const run of runs) {
    if (!run.name?.startsWith("attestation/")) continue;
    const where = { name: run.name, url: run.html_url ?? "", sha: run.head_sha ?? "", lane: run.external_id ?? "" };
    if (run.app?.slug !== app) {
      refused.push({ run: where, reason: `posted by ${run.app?.slug ?? "nobody"}, not ${app}` });
      continue;
    }
    if (run.status !== "completed") {
      refused.push({ run: where, reason: `${run.status}, not completed` });
      continue;
    }
    const lines = (run.output?.text ?? "").split("\n").filter((l) => l.startsWith("{"));
    for (const line of lines) {
      let s;
      try {
        s = JSON.parse(line);
      } catch {
        refused.push({ run: where, reason: "a statement that is not JSON" });
        continue;
      }
      const conf = Array.isArray(s?.predicate?.configuration) ? s.predicate.configuration : [];
      const lane = conf.find((c) => c?.name === "run")?.annotations?.lane ?? {};
      if (revoked(lane.session, revokedLanes) || revoked(lane.branch, revokedLanes) || revoked(where.lane, revokedLanes)) {
        refused.push({ run: where, reason: `lane ${lane.session || lane.branch || where.lane} is revoked` });
        break;
      }
      if (s._type !== STATEMENT || s.predicateType !== PREDICATE || s.predicate?.result !== "PASSED") {
        refused.push({ run: where, reason: "not an in-toto test-result statement that PASSED" });
        continue;
      }
      if (!Array.isArray(s.subject) || s.subject.length !== 1) {
        refused.push({ run: where, reason: "a statement whose subject is not one task" });
        continue;
      }
      const task = s.subject[0]?.name;
      const hash = s.subject[0]?.digest?.nxTaskHash;
      if (typeof task !== "string" || typeof hash !== "string" || !hash) {
        refused.push({ run: where, reason: "a subject with no task or no nxTaskHash" });
        continue;
      }
      accepted.push({ task, hash, platform: lane.platform ?? conf.find((c) => c?.name === "run")?.annotations?.platform ?? "", run: { ...where, lane: lane.session || lane.branch || where.lane } });
    }
  }
  return { accepted, refused };
}

/** The tasks of a group, from nx-hashes.cjs --json: what its step would run (no noop). */
export function groupTasks(group, tasks) {
  const g = GROUPS[group];
  return tasks.filter((t) => t.executor !== "nx:noop" && g.has(t));
}

/** For each task of a group, the statement that covers it, or none. */
export function cover(tasks, accepted) {
  const by = new Map();
  for (const a of accepted) by.set(`${a.task} ${a.hash}`, a);
  return tasks.map((t) => ({ task: t, by: (t.hash && by.get(`${t.id} ${t.hash}`)) || null }));
}

/** The lines for $GITHUB_OUTPUT. */
export function outputs(covered) {
  const lines = [];
  for (const group of Object.keys(GROUPS)) {
    const rows = covered[group] ?? [];
    const skip = rows.filter((r) => r.by).map((r) => r.task.id);
    lines.push(`skip-${group}=${skip.join(" ")}`);
    lines.push(`all-${group}=${rows.length > 0 && skip.length === rows.length}`);
  }
  return lines;
}

/** The job summary: each skipped task and the statement that covers it. */
export function summary(covered, refused) {
  const out = ["### Attested tasks (#5585)", ""];
  const rows = [];
  for (const [group, list] of Object.entries(covered)) {
    for (const r of list) {
      const by = r.by ? `skipped: [${r.by.run.name}](${r.by.run.url}) on \`${r.by.run.sha.slice(0, 12)}\`, lane \`${r.by.run.lane || "?"}\`` : "runs";
      rows.push(`| ${group} | \`${r.task.id}\` | \`${r.task.hash}\` | ${by} |`);
    }
  }
  if (rows.length) out.push("| Step | Task | Nx hash at the head | |", "|---|---|---|---|", ...rows);
  else out.push("No accepted attestation covers a task of this change: every task runs.");
  if (refused.length) {
    out.push("", "Not accepted:", ...refused.map((r) => `- [${r.run.name}](${r.run.url}) on \`${r.run.sha.slice(0, 12)}\`: ${r.reason}`));
  }
  return out.join("\n") + "\n";
}

/** The Nx runs for the tasks of a group that are not skipped: one per target, in its order. */
export function plan(group, tasks, skip) {
  const g = GROUPS[group];
  const left = groupTasks(group, tasks).filter((t) => !skip.has(t.id));
  const byTarget = new Map();
  for (const t of left) {
    if (!byTarget.has(t.target)) byTarget.set(t.target, []);
    byTarget.get(t.target).push(t.project);
  }
  // The group's own targets first, in its order; the rest (test-unit, …) after them.
  const order = [...byTarget.keys()].sort((a, b) => rank(g, a) - rank(g, b) || (a < b ? -1 : 1));
  return order.map((target) => ({ target, projects: [...new Set(byTarget.get(target))].sort() }));
}

function rank(g, target) {
  const i = g.targets.indexOf(target);
  return i < 0 ? g.targets.length : i;
}

// ---- Impure parts ----

/** The tasks of a group's targets that the change since base affects, with their hashes. */
function hashes(targets, base, platform) {
  const env = { ...process.env, NX_NO_CLOUD: "true", NX_LOAD_DOT_ENV_FILES: "false", NX_DAEMON: "false", NX_TUI: "false" };
  let shim = null;
  if (platform) {
    // The runtime input `uname -sm` is the one place a platform enters a hash (nx.json).
    shim = mkdtempSync(join(tmpdir(), "ci-attested-"));
    writeFileSync(join(shim, "uname"), `#!/bin/sh\nif [ "$*" = "-sm" ]; then echo '${UNAME[platform]}'; else exec /usr/bin/uname "$@"; fi\n`);
    chmodSync(join(shim, "uname"), 0o755);
    env.PATH = `${shim}:${env.PATH}`;
    for (const name of ENV_INPUTS) delete env[name];
  }
  try {
    const json = execFileSync("node", [join(root, "scripts/nx-hashes.cjs"), "--affected", base, "--json", ...targets], {
      cwd: root, encoding: "utf8", env, stdio: ["ignore", "pipe", "inherit"], maxBuffer: 256 * 1024 * 1024,
    });
    return JSON.parse(json);
  } finally {
    if (shim) rmSync(shim, { recursive: true, force: true });
  }
}

export function gh(args) {
  return execFileSync("gh", args, { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"], maxBuffer: 64 * 1024 * 1024 });
}

/** The pull request's commits now and every head it had before a force push, newest first. */
export function prShas(repo, pr) {
  const [owner, name] = repo.split("/");
  const query = `query($owner:String!,$name:String!,$pr:Int!){repository(owner:$owner,name:$name){pullRequest(number:$pr){
    headRefOid
    commits(last:100){nodes{commit{oid}}}
    timelineItems(last:100,itemTypes:[HEAD_REF_FORCE_PUSHED_EVENT]){nodes{...on HeadRefForcePushedEvent{beforeCommit{oid} afterCommit{oid}}}}}}}`;
  const data = JSON.parse(gh(["api", "graphql", "-f", `query=${query}`, "-F", `owner=${owner}`, "-F", `name=${name}`, "-F", `pr=${pr}`]));
  const p = data.data.repository.pullRequest;
  const shas = [p.headRefOid];
  for (const n of [...p.commits.nodes].reverse()) shas.push(n.commit.oid);
  for (const n of [...p.timelineItems.nodes].reverse()) shas.push(n.afterCommit?.oid, n.beforeCommit?.oid);
  // At most 60 commits: each one is a call.
  return [...new Set(shas.filter(Boolean))].slice(0, 60);
}

export function checkRuns(repo, shas) {
  const runs = [];
  for (const sha of shas) {
    const r = spawnSync("gh", ["api", `repos/${repo}/commits/${sha}/check-runs?per_page=100&filter=all`, "--jq", ".check_runs[] | select(.name | startswith(\"attestation/\"))"], {
      cwd: root, encoding: "utf8", maxBuffer: 64 * 1024 * 1024,
    });
    // A commit that GitHub no longer has (a rebase dropped it) has no check runs to read.
    if (r.status !== 0) continue;
    for (const line of r.stdout.split("\n").filter(Boolean)) runs.push(JSON.parse(line));
  }
  return runs;
}

/** The revoked lanes: the first field of each line of revoked.txt on the revocation branch. */
export const REVOCATION_BRANCH = "attestation-revocations";
export function parseRevocations(text) {
  return text.split("\n").map((l) => l.split("\t")[0].trim()).filter((l) => l && !l.startsWith("#"));
}

// A branch with no list yet (404) revokes nobody; any other failure throws, and select then
// skips nothing (principle 11).
function readRevocations(repo, fromFile) {
  if (fromFile) return [];
  const r = spawnSync("gh", ["api", `repos/${repo}/contents/revoked.txt?ref=${REVOCATION_BRANCH}`, "-H", "Accept: application/vnd.github.raw"], { cwd: root, encoding: "utf8" });
  if (r.status === 0) return parseRevocations(r.stdout);
  if (/404|Not Found/.test(r.stderr)) return [];
  throw new Error(`the revocation list is not readable: ${r.stderr.trim()}`);
}

function select(argv) {
  const opt = flags(argv, ["base", "pr", "repo", "runs"]);
  const empty = () => {
    for (const l of outputs({})) console.log(l);
  };
  try {
    const repo = opt.repo ?? process.env.GITHUB_REPOSITORY;
    const runs = opt.runs ? JSON.parse(readFileSync(opt.runs, "utf8")) : checkRuns(repo, prShas(repo, Number(opt.pr)));
    const revokedLanes = new Set([...(process.env.ATTEST_REVOKED_LANES ?? "").split(/\s+/).filter(Boolean), ...readRevocations(repo, opt.runs)]);
    const { accepted, refused } = accept(runs, { app: process.env.ATTEST_APP || "alexec-agents", revokedLanes });
    const covered = {};
    if (accepted.length) {
      // One hash run per platform, with every target of its groups.
      const byPlatform = new Map();
      for (const [name, g] of Object.entries(GROUPS)) {
        if (!byPlatform.has(g.platform)) byPlatform.set(g.platform, { groups: [], targets: new Set() });
        const p = byPlatform.get(g.platform);
        p.groups.push(name);
        for (const t of g.targets) p.targets.add(t);
      }
      const here = process.platform === "linux" ? `linux-${process.arch === "arm64" ? "arm64" : "x64"}` : null;
      for (const [platform, p] of byPlatform) {
        const tasks = hashes([...p.targets], opt.base, platform === here ? null : platform);
        for (const group of p.groups) {
          // A group's tasks are those of its own targets, so filter again for each group.
          const own = hashesFor(group, tasks);
          covered[group] = cover(groupTasks(group, own), accepted);
        }
      }
    }
    for (const l of outputs(covered)) console.log(l);
    if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, summary(covered, refused));
    console.error(`ci-attested: ${runs.length} attestation check runs, ${accepted.length} statements accepted, ${refused.length} refused`);
    for (const r of refused) console.error(`ci-attested: not accepted: ${r.run.name} on ${r.run.sha.slice(0, 12)}: ${r.reason}`);
    return 0;
  } catch (error) {
    console.error(`ci-attested: ${error.message}; nothing is skipped`);
    empty();
    return 0;
  }
}

/**
 * The tasks of a hash run that belong to a group's step: those of its targets, and the tasks
 * that its targets depend on in the same projects (agentskit:test is a noop over test-unit, …).
 */
function hashesFor(group, tasks) {
  const g = GROUPS[group];
  const projects = new Set(tasks.filter((t) => g.targets.includes(t.target) && g.has(t)).map((t) => t.project));
  return tasks.filter((t) => projects.has(t.project) && (g.targets.includes(t.target) || !GROUPS_TARGETS.has(t.target)));
}
const GROUPS_TARGETS = new Set(Object.values(GROUPS).flatMap((g) => g.targets));

function run(argv) {
  const dry = argv[0] === "--dry";
  const opt = flags(dry ? argv.slice(1) : argv, ["group", "base", "skip"]);
  const g = GROUPS[opt.group];
  if (!g) {
    console.error(`ci-attested: no group ${opt.group}`);
    return 2;
  }
  const skip = new Set((opt.skip ?? "").split(/\s+/).filter(Boolean));
  const tasks = hashesFor(opt.group, hashes(g.targets, opt.base, null));
  const steps = plan(opt.group, tasks, skip);
  console.log(`ci-attested: skipped, attested: ${[...skip].join(" ") || "none"}`);
  let status = 0;
  for (const s of steps) {
    console.log(`ci-attested: nx run-many -t ${s.target} --projects=${s.projects.join(",")}`);
    if (dry) continue;
    const r = spawnSync(join(root, "scripts/nx.sh"), ["run-many", "-t", s.target, `--projects=${s.projects.join(",")}`, "--parallel=1", "--output-style=static"], { cwd: root, stdio: "inherit" });
    if (r.status !== 0) status = r.status || 1;
  }
  if (steps.length === 0) console.log("ci-attested: every task of this step is attested");
  return status;
}

function flags(argv, names) {
  const opt = {};
  for (let i = 0; i < argv.length; i++) {
    const name = argv[i].replace(/^--/, "");
    if (!names.includes(name)) throw new Error(`unknown flag ${argv[i]}`);
    opt[name] = argv[++i];
  }
  return opt;
}

// ---- Selftest (scripts/lint.sh runs it) ----

function selftest() {
  const assert = (ok, what) => {
    if (!ok) {
      console.error(`ci-attested selftest: ${what}`);
      process.exit(1);
    }
  };
  const st = (task, hash, extra = {}) => JSON.stringify({
    _type: STATEMENT, subject: [{ name: task, digest: { nxTaskHash: hash } }], predicateType: PREDICATE,
    predicate: { result: "PASSED", configuration: [{ name: "run", annotations: { patchId: "p", lane: { session: "s1", branch: "agents/x" } } }], ...extra },
  });
  const checkRun = (lines, extra = {}) => ({
    name: "attestation/macos-arm64", head_sha: "a".repeat(40), html_url: "https://x/1", status: "completed", external_id: "s1",
    app: { slug: "alexec-agents" }, output: { text: ["```jsonl", ...lines, "```"].join("\n") }, ...extra,
  });
  const opts = { app: "alexec-agents", revokedLanes: new Set() };

  const ok = accept([checkRun([st("go:test", "111"), st("web:test", "222")])], opts);
  assert(ok.accepted.length === 2 && ok.refused.length === 0, "a good check run is not accepted");
  assert(accept([checkRun([st("go:test", "111")], { app: { slug: "someone" } })], opts).accepted.length === 0, "another app's check run is accepted");
  assert(accept([checkRun([st("go:test", "111")], { app: undefined })], opts).accepted.length === 0, "a check run with no app is accepted");
  assert(accept([checkRun([st("go:test", "111")], { status: "in_progress" })], opts).accepted.length === 0, "a check run in progress is accepted");
  assert(accept([checkRun([st("go:test", "111")], { name: "lint" })], opts).refused.length === 0, "a check run that is not an attestation is read");
  assert(accept([checkRun([st("go:test", "111")])], { ...opts, revokedLanes: new Set(["s1"]) }).accepted.length === 0, "a revoked lane is accepted");
  assert(accept([checkRun([st("go:test", "111")])], { ...opts, revokedLanes: new Set(["agents/x"]) }).accepted.length === 0, "a revoked branch is accepted");
  const failed = JSON.stringify({ ...JSON.parse(st("go:test", "111")), predicate: { result: "FAILED" } });
  assert(accept([checkRun([failed])], opts).accepted.length === 0, "a FAILED statement is accepted");
  const two = JSON.stringify({ ...JSON.parse(st("go:test", "111")), subject: [{ name: "go:test", digest: { nxTaskHash: "1" } }, { name: "go:vet", digest: { nxTaskHash: "2" } }] });
  assert(accept([checkRun([two])], opts).accepted.length === 0, "a statement with two subjects is accepted");
  const other = JSON.stringify({ ...JSON.parse(st("go:test", "111")), predicateType: "https://slsa.dev/provenance/v1" });
  assert(accept([checkRun([other])], opts).accepted.length === 0, "another predicate is accepted");
  assert(accept([checkRun(["{not json"])], opts).refused.length === 1, "a broken line is not refused");

  const task = (id, hash, extra = {}) => {
    const [project, target] = id.split(":");
    return { id, project, target, hash, executor: "nx:run-commands", tags: [], ...extra };
  };
  const linux = [task("wire:gen", "g"), task("go:build", "b"), task("go:test", "111"), task("go:vet", "999"), task("web:test", "222"), task("agentskit:test-unit", "u", { tags: ["xcode"] })];
  const lt = groupTasks("linux", linux);
  assert(!lt.some((t) => t.project === "agentskit"), "a Swift task is in the Linux group");
  const rows = cover(lt, ok.accepted);
  const skipped = rows.filter((r) => r.by).map((r) => r.task.id);
  // go:vet's hash is not the attested one: an input changed since, so it runs.
  assert(skipped.join() === "go:test,web:test", `the covered tasks are ${skipped}`);
  assert(cover([task("go:test", null)], ok.accepted)[0].by === null, "a task with no hash is covered");

  const swift = [task("agentskit:test", "n", { executor: "nx:noop", tags: ["xcode"] }), task("agentskit:test-unit", "u", { tags: ["xcode"] }), task("codetext:test", "c", { tags: ["xcode"] })];
  const all = cover(groupTasks("swift", swift), [{ task: "agentskit:test-unit", hash: "u", run: {} }, { task: "codetext:test", hash: "c", run: {} }]);
  const half = cover(groupTasks("swift", swift), [{ task: "codetext:test", hash: "c", run: {} }]);
  const out = outputs({ linux: rows, swift: all, gomac: [] });
  assert(out.includes("skip-linux=go:test web:test") && out.includes("all-linux=false"), `the linux outputs: ${out}`);
  assert(out.includes("all-swift=true") && out.includes("skip-swift=agentskit:test-unit codetext:test"), `the swift outputs: ${out}`);
  assert(out.includes("all-gomac=false") && out.includes("skip-gomac="), "a group with no task is all covered");
  assert(outputs({ swift: half }).includes("all-swift=false"), "a half-covered group is all covered");
  assert(outputs({}).every((l) => /=(false)?$/.test(l)), "no attestation skips something");

  const p = plan("linux", linux, new Set(skipped));
  assert(JSON.stringify(p) === JSON.stringify([{ target: "gen", projects: ["wire"] }, { target: "build", projects: ["go"] }, { target: "vet", projects: ["go"] }]), `the plan: ${JSON.stringify(p)}`);
  const ps = plan("swift", swift, new Set(["codetext:test"]));
  assert(JSON.stringify(ps) === JSON.stringify([{ target: "test-unit", projects: ["agentskit"] }]), `the Swift plan: ${JSON.stringify(ps)}`);
  assert(plan("swift", swift, new Set(["agentskit:test-unit", "codetext:test"])).length === 0, "a covered task runs");

  const deps = [task("agentskit-repo:test", "r", { tags: ["xcode"] }), task("web:build", "w"), task("wire:gen", "g"), task("agentskit:test-unit", "u", { tags: ["xcode"] }), task("agentskit:test", "n", { executor: "nx:noop", tags: ["xcode"] })];
  const ids = hashesFor("swift", deps).map((t) => t.id).sort();
  assert(ids.join() === "agentskit-repo:test,agentskit:test,agentskit:test-unit", `the Swift step's tasks: ${ids}`);

  assert(parseRevocations("# c\ns1\t12\tgo:test\t2026\n\nagents/x\n").join() === "s1,agents/x", "the revocation list");
  const text = summary({ linux: rows }, [{ run: { name: "attestation/macos-arm64", url: "u", sha: "b".repeat(40) }, reason: "posted by someone" }]);
  assert(text.includes("`go:test`") && text.includes("skipped: [attestation/macos-arm64]") && text.includes("posted by someone"), "the summary");
  console.log("ci-attested selftest: ok");
}

// Run as a script, not when scripts/ci-sample.mjs imports it.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [mode, ...rest] = process.argv.slice(2);
  if (mode === "--selftest") selftest();
  else if (mode === "select") process.exit(select(rest));
  else if (mode === "run") process.exit(run(rest));
  else {
    console.error("usage: ci-attested.mjs select --base SHA --pr N [--repo R] [--runs FILE] | run --group G --base SHA --skip IDS | --selftest");
    process.exit(2);
  }
}
