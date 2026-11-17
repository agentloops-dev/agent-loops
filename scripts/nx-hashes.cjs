#!/usr/bin/env node
// The Nx hash of each task, with no task run (spike #5613, criterion 11; #5584). A lane's
// attestation names each task by this hash (scripts/checks.sh attest), and CI calculates it
// again at the pull request's head commit to see which tasks the attestation covers (#5585).
//
//   node scripts/nx-hashes.cjs [--affected BASE] [--projects A,B] [--json] TARGET...
//
// Without --json: one "project:target hash" line per task, sorted. With --json: one array of
// {id, project, target, hash, executor, tags, root, files, runtime, environment}: files is each
// file input of the task, from the hash's own plan (for the "why": the inputs that changed). With --affected, only the projects that the change
// since BASE affects, as `nx affected` gives them. With --projects, only those of the projects
// named (`checks.sh attest` hashes mac:build and remote:build, not every build; #6060). The task graph has each task's dependencies
// too (wire:gen, design:gen, agentskit:test-unit under agentskit:test, …).
//
// The hash is the one Nx's cache uses, so the environment must be the run's: run it from
// scripts/nx.sh's environment, or set the same GOFLAGS, CGO_ENABLED and AGENTS_* variables.
// It reads Nx's internal modules (dist/src), so an Nx bump can move them: CI's "Nx task
// hashes" steps run it on every pull request, and `scripts/checks.sh attest --plan` here.
const path = require("node:path");
// Not console.log: on a pipe it is asynchronous, and the process.exit below (Nx leaves handles
// open) would cut a long output.
// writeSync on a pipe writes part of it at a time, and EAGAIN when the pipe is full.
const { writeSync } = require("node:fs");
function out(text) {
  const bytes = Buffer.from(text);
  for (let at = 0; at < bytes.length;) {
    try {
      at += writeSync(1, bytes, at);
    } catch (error) {
      if (error.code !== "EAGAIN") throw error;
    }
  }
}
const nx = path.join(__dirname, "..", "node_modules", "nx", "dist", "src");
const { createProjectGraphAsync } = require(path.join(nx, "project-graph", "project-graph"));
const { readNxJson } = require(path.join(nx, "config", "nx-json"));
const { createTaskGraph } = require(path.join(nx, "tasks-runner", "create-task-graph"));
const { createTaskHasher } = require(path.join(nx, "hasher", "create-task-hasher"));
const { hashTasks, getTaskDetails } = require(path.join(nx, "hasher", "hash-task"));

async function affectedProjects(graph, nxJson, base) {
  const { calculateFileChanges } = require(path.join(nx, "project-graph", "file-utils"));
  const { filterAffected } = require(path.join(nx, "project-graph", "affected", "affected-project-graph"));
  const { parseFiles } = require(path.join(nx, "utils", "command-line-utils"));
  const { files } = parseFiles({ base, head: "HEAD" });
  const changes = calculateFileChanges(files, { base, head: "HEAD" }, nxJson);
  const affected = await filterAffected(graph, changes);
  return Object.keys(affected.nodes);
}

(async () => {
  const args = process.argv.slice(2);
  let base;
  let json = false;
  let only = null;
  for (;;) {
    if (args[0] === "--affected") { base = args[1]; args.splice(0, 2); }
    else if (args[0] === "--projects") { only = new Set(args[1].split(",").filter(Boolean)); args.splice(0, 2); }
    else if (args[0] === "--json") { json = true; args.shift(); }
    else break;
  }
  if (args.length === 0) {
    console.error("usage: nx-hashes.cjs [--affected BASE] [--projects A,B] [--json] TARGET...");
    process.exit(2);
  }
  const started = Date.now();
  const graph = await createProjectGraphAsync({ exitOnError: true });
  const nxJson = readNxJson();
  const projects = (base ? await affectedProjects(graph, nxJson, base) : Object.keys(graph.nodes)).filter((p) => !only || only.has(p));
  // createTaskGraph refuses a project that has none of the targets.
  const withTargets = projects.filter((p) => args.some((t) => graph.nodes[p]?.data.targets?.[t]));
  const taskGraph = createTaskGraph(graph, {}, withTargets, args, undefined, {});
  const hasher = createTaskHasher(graph, nxJson);
  const tasks = Object.values(taskGraph.tasks).sort((a, b) => (a.id < b.id ? -1 : 1));
  const envs = Object.fromEntries(tasks.map((t) => [t.id, process.env]));
  await hashTasks(hasher, graph, taskGraph, envs, getTaskDetails());
  if (json) {
    // Each task's inputs, file by file (the hash's own plan), for the "why".
    const { HashPlanInspector } = require(path.join(nx, "hasher", "hash-plan-inspector"));
    const inspector = new HashPlanInspector(graph, undefined, nxJson);
    await inspector.init();
    const list = tasks.map((t) => {
      const node = graph.nodes[t.target.project].data;
      const inputs = inspector.inspectTaskInputs(t.target, {}, undefined, true)[t.id] ?? {};
      return {
        id: t.id,
        project: t.target.project,
        target: t.target.target,
        hash: t.hash ?? null,
        executor: node.targets[t.target.target].executor ?? null,
        tags: node.tags ?? [],
        root: node.root,
        files: inputs.files ?? [],
        runtime: inputs.runtime ?? [],
        environment: inputs.environment ?? [],
      };
    });
    out(JSON.stringify(list) + "\n");
  } else {
    out(tasks.map((t) => `${t.id} ${t.hash ?? "(not hashed)"}\n`).join(""));
  }
  console.error(`nx-hashes: ${tasks.length} tasks in ${((Date.now() - started) / 1000).toFixed(1)} s`);
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });
