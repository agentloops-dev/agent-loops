#!/usr/bin/env node
// CI on main reruns 10% of the attested tasks of a batch (#5586, epic #5581 §5). A lane's
// attestation (#5584) lets CI skip a task on its pull request (#5585); this checks that the
// claim was true, on main, at the merge commit of each pull request in the batch.
//
//   node scripts/ci-sample.mjs run --before SHA --after SHA [--platform linux|macos] [--max N]
//                                  [--minutes M] [--repo OWNER/NAME] [--dry] [--output]
//   node scripts/ci-sample.mjs --selftest
//
// For each pull request merged in before..after (first-parent commits; with no --before, the
// one commit), it reads the statements that CI would have accepted, takes 10% of its
// (pull request, task) pairs, at least one when the batch has any, and runs each task with Nx,
// without the cache, in a worktree at the merge commit. Then:
// - the task passes: nothing to do.
// - it fails, and it fails at the merge commit's parent too: main was already broken, a break
//   for Fix main, not a false attestation. Nothing is revoked.
// - it fails, and it passes at the parent: the attestation was false. The lane (its session id,
//   or its branch) is added to `revoked.txt` on the orphan branch `attestation-revocations`,
//   which `scripts/ci-attested.mjs select` reads, and a `bug` names the PR, the task and the log.
//
// Each job samples only the tasks of its own platform (--platform, #6077):
// - linux (the default): a task the lane attested on linux-x64, or a task with no platform in
//   its hash (web, repo-lint, wire, design), never a task of an Xcode project.
// - macos: a task the lane attested on macOS that a macOS job of CI skips for it (GROUPS of
//   ci-attested.mjs): the Swift package tests, swift-lint:lint, mac:build and remote:build
//   (#6060), and the Go tasks of `test-go` (not go:lint, which only Linux skips). A macOS runner costs more, so its job passes --max 1 (at
//   most one task in a batch) and --minutes (a cap on each run). A run that hits the cap says
//   nothing: it revokes nothing.
// --dry --output writes `sampled` (how many it would rerun) and `before` to $GITHUB_OUTPUT, so a
// Linux job can say whether the macOS job has anything to do before a macOS runner starts.
//
// The choice is a function of the after commit, the pull request and the task, so a rerun of
// the job samples the same pairs.
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { appendFileSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { GROUPS, REVOCATION_BRANCH, accept, checkRuns, gh, prShas } from "./ci-attested.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const RATE = 0.1;
// Projects whose task hashes have no platform input, so any runner's pass is the same claim.
const PLATFORM_FREE = new Set(["web", "repo-lint", "wire", "design"]);
const XCODE_PROJECTS = new Set(["agentskit", "agentskit-repo", "codetext", "mac", "remote", "swift-lint"]);

// ---- Pure parts (the selftest covers them) ----

export const PLATFORMS = ["linux", "macos"];

/** Whether a job on this platform (linux or macos) can rerun the claim of a statement. */
export function sampleable(s, platform = "linux") {
  const project = s.task.split(":")[0];
  if (platform === "macos") {
    // A platform-free task has one hash on every runner: the Linux job samples it.
    if (!(s.platform ?? "").startsWith("macos") || PLATFORM_FREE.has(project)) return false;
    const t = { project, target: s.task.split(":")[1], tags: XCODE_PROJECTS.has(project) ? ["xcode"] : [] };
    return Object.values(GROUPS).some((g) => g.platform.startsWith("macos") && g.targets.some((x) => t.target.startsWith(x)) && g.has(t));
  }
  if (XCODE_PROJECTS.has(project)) return false;
  return s.platform === "linux-x64" || PLATFORM_FREE.has(project);
}

/** One candidate for each (pull request, task): the statements, by their task, newest first. */
export function candidates(prs, platform = "linux") {
  const out = [];
  for (const pr of prs) {
    const seen = new Set();
    for (const s of pr.statements) {
      if (!sampleable(s, platform) || seen.has(s.task)) continue;
      seen.add(s.task);
      out.push({ pr: pr.pr, sha: pr.sha, task: s.task, lane: s.run.lane, hash: s.hash, run: s.run });
    }
  }
  return out;
}

/** 10% of the candidates, at least one when there are any and at most max, in an order that depends on seed only. */
export function sample(list, seed, rate = RATE, max = Infinity) {
  if (list.length === 0 || max < 1) return [];
  const key = (c) => createHash("sha256").update(`${seed} ${c.pr} ${c.task}`).digest("hex");
  const n = Math.min(max, Math.max(1, Math.ceil(list.length * rate)));
  return [...list].sort((a, b) => (key(a) < key(b) ? -1 : 1)).slice(0, n);
}

/**
 * What a sampled rerun means: `pass`, `main-break` (the parent fails too), `false-attestation`,
 * or `timeout` (a run hit the cap in minutes: null), which says nothing and revokes nothing.
 */
export function verdict(atMerge, atParent) {
  if (atMerge === true) return "pass";
  if (atMerge === null || atParent === null) return "timeout";
  return atParent ? "false-attestation" : "main-break";
}

/** The line that revokes a lane. */
export function revocationLine(c, date) {
  return [c.lane, `#${c.pr}`, c.task, date].join("\t");
}

// ---- Impure parts ----

function git(args, cwd = root) {
  return execFileSync("git", args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] }).trim();
}

/** Run a task with Nx and no cache in a worktree at sha. Returns {ok, log}; ok is null when it ran out of time. */
function runAt(sha, task, minutes) {
  const dir = mkdtempSync(join(tmpdir(), "ci-sample-"));
  try {
    git(["worktree", "add", "--detach", dir, sha]);
    const env = { ...process.env, NX_NO_CLOUD: "true", NX_DAEMON: "false", NX_LOAD_DOT_ENV_FILES: "false", NX_TUI: "false" };
    const install = spawnSync(join(dir, "scripts/pnpm.sh"), ["install", "--frozen-lockfile", "--reporter=silent"], { cwd: dir, env, encoding: "utf8", maxBuffer: 256 * 1024 * 1024 });
    if (install.status !== 0) return { ok: false, log: `install failed\n${install.stdout}${install.stderr}` };
    const timeout = minutes ? minutes * 60 * 1000 : undefined;
    const r = spawnSync(join(dir, "scripts/nx.sh"), ["run", task, "--skip-nx-cache", "--output-style=static"], { cwd: dir, env, encoding: "utf8", maxBuffer: 256 * 1024 * 1024, timeout });
    if (r.error?.code === "ETIMEDOUT") return { ok: null, log: `ran out of time after ${minutes} minutes\n${r.stdout ?? ""}${r.stderr ?? ""}` };
    return { ok: r.status === 0, log: `${r.stdout}${r.stderr}` };
  } finally {
    spawnSync("git", ["worktree", "remove", "--force", dir], { cwd: root });
    rmSync(dir, { recursive: true, force: true });
  }
}

function pullRequestOf(repo, sha) {
  const out = gh(["api", `repos/${repo}/commits/${sha}/pulls`, "--jq", `[.[] | select(.merge_commit_sha == "${sha}")][0].number // empty`]).trim();
  return out ? Number(out) : null;
}

/** Append the revocation to revoked.txt on the orphan branch, with the job's token. */
function revoke(repo, c, date) {
  const dir = mkdtempSync(join(tmpdir(), "ci-revoke-"));
  try {
    const url = `https://github.com/${repo}.git`;
    const auth = `AUTHORIZATION: basic ${Buffer.from(`x-access-token:${process.env.GH_TOKEN}`).toString("base64")}`;
    const g = (args) => execFileSync("git", ["-c", `http.extraheader=${auth}`, "-c", "user.name=github-actions[bot]", "-c", "user.email=41898282+github-actions[bot]@users.noreply.github.com", ...args], { cwd: dir, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] });
    g(["init", "-q"]);
    g(["remote", "add", "origin", url]);
    const has = spawnSync("git", ["-c", `http.extraheader=${auth}`, "ls-remote", "--exit-code", "--heads", url, REVOCATION_BRANCH], { encoding: "utf8" }).status === 0;
    if (has) {
      g(["fetch", "-q", "origin", REVOCATION_BRANCH]);
      g(["checkout", "-q", "-B", REVOCATION_BRANCH, "FETCH_HEAD"]);
    } else {
      g(["checkout", "-q", "--orphan", REVOCATION_BRANCH]);
      writeFileSync(join(dir, "revoked.txt"), "# lane<TAB>pull request<TAB>task<TAB>date. Read by scripts/ci-attested.mjs; written by scripts/ci-sample.mjs (#5586).\n");
    }
    appendFileSync(join(dir, "revoked.txt"), `${revocationLine(c, date)}\n`);
    g(["add", "revoked.txt"]);
    g(["commit", "-q", "-m", `Revoke ${c.lane}: #${c.pr} ${c.task} (#5586)`]);
    g(["push", "-q", "origin", `${REVOCATION_BRANCH}:${REVOCATION_BRANCH}`]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function fileBug(repo, c, merge, parent, log) {
  const tail = log.split("\n").slice(-80).join("\n").slice(-12000);
  const body = [
    `A sampled rerun on main (#5586, epic #5581) shows a false attestation.`,
    "",
    `- Pull request: #${c.pr}, merge commit \`${merge}\``,
    `- Task: \`${c.task}\` (Nx hash in the statement: \`${c.hash}\`)`,
    `- The lane's statement: ${c.run.url} (lane \`${c.lane}\`)`,
    `- The task fails at the merge commit and passes at its parent \`${parent}\`.`,
    `- The lane is revoked: CI accepts no more of its attestations (\`${REVOCATION_BRANCH}\` ▸ \`revoked.txt\`).`,
    `- Log: ${process.env.GITHUB_SERVER_URL ?? "https://github.com"}/${repo}/actions/runs/${process.env.GITHUB_RUN_ID ?? ""}`,
    "",
    "<details><summary>End of the log</summary>",
    "",
    "```",
    tail,
    "```",
    "",
    "</details>",
    "",
    "Clients: none.",
  ].join("\n");
  gh(["issue", "create", "--repo", repo, "--title", `False attestation: ${c.task} fails at #${c.pr}`, "--label", "bug", "--label", "size:S", "--body", body]);
}

function main(argv) {
  const opt = {};
  for (let i = 0; i < argv.length; i++) {
    const name = argv[i].replace(/^--/, "");
    if (name === "dry" || name === "output") opt[name] = true;
    else if (["before", "after", "repo", "platform", "max", "minutes"].includes(name)) opt[name] = argv[++i];
    else throw new Error(`unknown flag ${argv[i]}`);
  }
  const platform = opt.platform ?? "linux";
  if (!PLATFORMS.includes(platform)) throw new Error(`--platform is one of ${PLATFORMS.join(", ")}, not ${platform}`);
  const max = opt.max === undefined ? Infinity : Number(opt.max);
  const minutes = opt.minutes === undefined ? undefined : Number(opt.minutes);
  if (Number.isNaN(max) || (minutes !== undefined && !(minutes > 0))) throw new Error("--max and --minutes take a number");
  const repo = opt.repo ?? process.env.GITHUB_REPOSITORY;
  const after = git(["rev-parse", opt.after ?? "HEAD"]);
  const range = opt.before && spawnSync("git", ["merge-base", "--is-ancestor", opt.before, after], { cwd: root }).status === 0 ? `${opt.before}..${after}` : `${after}^..${after}`;
  const commits = git(["rev-list", "--first-parent", range]).split("\n").filter(Boolean);
  const app = process.env.ATTEST_APP || "alexec-agents";
  const prs = [];
  for (const sha of commits) {
    const pr = pullRequestOf(repo, sha);
    if (!pr) continue;
    const { accepted } = accept(checkRuns(repo, prShas(repo, pr)), { app, revokedLanes: new Set() });
    if (accepted.length) prs.push({ pr, sha, statements: accepted });
  }
  const list = candidates(prs, platform);
  const chosen = sample(list, after, RATE, max);
  const cap = Number.isFinite(max) ? `, at most ${max}` : "";
  const out = [`### Attestation sample, ${platform} (#5586, #6077)`, "", `${commits.length} commit(s), ${prs.length} with attestations, ${list.length} attested task(s) this job can rerun, ${chosen.length} sampled (${RATE * 100}%, at least one${cap}).`, ""];
  if (opt.output && process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `sampled=${chosen.length}\nbefore=${opt.before ?? ""}\n`);
  let status = 0;
  for (const c of chosen) {
    if (opt.dry) {
      out.push(`- would rerun \`${c.task}\` of #${c.pr} at \`${c.sha.slice(0, 12)}\``);
      continue;
    }
    const merge = runAt(c.sha, c.task, minutes);
    let v = verdict(merge.ok, true);
    let parent = null;
    if (merge.ok === false) {
      const parentSha = git(["rev-parse", `${c.sha}^`]);
      parent = runAt(parentSha, c.task, minutes);
      v = verdict(false, parent.ok);
      if (v === "false-attestation") {
        try {
          revoke(repo, c, new Date().toISOString().slice(0, 10));
          fileBug(repo, c, c.sha, parentSha, merge.log);
        } catch (error) {
          // The revocation must not be lost quietly: the job is red.
          console.error(`ci-sample: ${error.message}`);
          status = 1;
        }
      }
    }
    const text = { pass: "passed", timeout: `ran out of time (${minutes} minutes): no verdict, nothing revoked`, "main-break": "failed, and fails at its parent too: a break of main, for Fix main", "false-attestation": `failed, and passes at its parent: false attestation, lane \`${c.lane}\` revoked` }[v];
    out.push(`- \`${c.task}\` of #${c.pr} at \`${c.sha.slice(0, 12)}\`: ${text}`);
  }
  if (chosen.length === 0) out.push("Nothing to sample.");
  console.log(out.join("\n"));
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${out.join("\n")}\n`);
  return status;
}

// ---- Selftest (scripts/lint.sh runs it) ----

function many25() {
  return Array.from({ length: 25 }, (_, i) => ({ pr: i, task: "go:test" }));
}

function selftest() {
  const assert = (ok, what) => {
    if (!ok) {
      console.error(`ci-sample selftest: ${what}`);
      process.exit(1);
    }
  };
  const st = (task, platform = "linux-x64", lane = "s1") => ({ task, hash: "h", platform, run: { lane, url: "u" } });
  assert(sampleable(st("go:test")), "a Linux Go task is not sampled");
  assert(!sampleable(st("go:test", "macos-arm64")), "a Mac Go task is sampled on Linux");
  assert(sampleable(st("web:test", "macos-arm64")), "a platform-free task is not sampled");
  assert(!sampleable(st("agentskit:test-unit")), "an Xcode task is sampled");
  assert(!sampleable(st("web:test", "")) === false, "a platform-free task with no platform is not sampled");
  assert(!sampleable(st("mac:build", "macos-arm64")), "an app build is sampled on Linux");
  // The macOS job (#6077): the tasks a lane attested on macOS that have a platform in their hash.
  assert(sampleable(st("agentskit:test-unit", "macos-arm64"), "macos"), "a Mac Swift task is not sampled on macOS");
  assert(sampleable(st("mac:build", "macos-arm64"), "macos"), "an app build is not sampled on macOS");
  assert(sampleable(st("swift-lint:lint", "macos-arm64"), "macos"), "SwiftLint is not sampled on macOS");
  assert(sampleable(st("go:test", "macos-arm64"), "macos"), "a Mac Go task is not sampled on macOS");
  assert(!sampleable(st("go:test"), "macos"), "a Linux Go task is sampled on macOS");
  assert(!sampleable(st("wire:gen", "macos-arm64"), "macos"), "a platform-free task is sampled on macOS");
  assert(!sampleable(st("go:lint", "macos-arm64"), "macos"), "go:lint, which only Linux skips, is sampled on macOS");
  assert(!sampleable(st("web:test", "macos-arm64"), "macos"), "a platform-free task is sampled on macOS too");
  assert(!sampleable(st("codetext:test", ""), "macos"), "a task with no platform is sampled on macOS");

  const prs = [{ pr: 1, sha: "a", statements: [st("go:test"), st("go:test"), st("go:vet"), st("codetext:test")] }, { pr: 2, sha: "b", statements: [st("web:lint", "macos-arm64")] }];
  const list = candidates(prs);
  assert(list.length === 3, `candidates: ${list.map((c) => `${c.pr}/${c.task}`)}`);
  const mac = candidates([{ pr: 1, sha: "a", statements: [st("codetext:test", "macos-arm64"), st("go:test"), st("web:lint", "macos-arm64")] }, { pr: 2, sha: "b", statements: [st("mac:build", "macos-arm64"), st("remote:build", "macos-arm64")] }], "macos");
  assert(mac.map((c) => `${c.pr}/${c.task}`).join() === "1/codetext:test,2/mac:build,2/remote:build", `macOS candidates: ${mac.map((c) => `${c.pr}/${c.task}`)}`);
  assert(sample(many25(), "x", RATE, 1).length === 1, "--max 1 samples more than one");
  assert(sample(list, "x", RATE, 0).length === 0, "--max 0 samples something");
  assert(sample([], "x").length === 0, "an empty batch samples something");
  assert(sample(list.slice(0, 1), "x").length === 1, "a batch of one samples none");
  assert(sample(list, "x").length === 1, "10% of 3 is one");
  const many = many25();
  assert(sample(many, "x").length === 3, "10% of 25 is 3");
  assert(JSON.stringify(sample(many, "x")) === JSON.stringify(sample(many, "x")), "the sample is not stable");
  assert(JSON.stringify(sample(many, "x")) !== JSON.stringify(sample(many, "y")), "the seed does not change the sample");

  assert(verdict(true, true) === "pass" && verdict(true, false) === "pass", "a pass is not a pass");
  assert(verdict(false, true) === "false-attestation", "a fail at the merge only is not a false attestation");
  assert(verdict(false, false) === "main-break", "a fail at the parent too is not a main break");
  assert(verdict(null, true) === "timeout" && verdict(false, null) === "timeout", "a run out of time is a verdict");
  assert(revocationLine({ lane: "s1", pr: 7, task: "go:test" }, "2026-11-15") === "s1\t#7\tgo:test\t2026-11-15", "the revocation line");
  console.log("ci-sample selftest: ok");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [mode, ...rest] = process.argv.slice(2);
  if (mode === "--selftest") selftest();
  else if (mode === "run") process.exit(main(rest));
  else {
    console.error("usage: ci-sample.mjs run --before SHA --after SHA [--platform linux|macos] [--max N] [--minutes M] [--repo R] [--dry] [--output] | --selftest");
    process.exit(2);
  }
}
