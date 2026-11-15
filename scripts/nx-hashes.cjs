#!/usr/bin/env node
// Spike #5613, criterion 11: print the Nx hash of each task, with no task run.
//
//   node scripts/nx-hashes.cjs [--affected BASE] TARGET...   one "task hash" line per task
//
// With --affected, only the projects that the change since BASE affects (as `nx affected`).
// The hash is the one the cache uses: a lane posts it, and CI calculates it again.
const path = require("node:path");
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
  if (args[0] === "--affected") { base = args[1]; args.splice(0, 2); }
  const started = Date.now();
  const graph = await createProjectGraphAsync({ exitOnError: true });
  const nxJson = readNxJson();
  const projects = base ? await affectedProjects(graph, nxJson, base) : Object.keys(graph.nodes);
  const taskGraph = createTaskGraph(graph, {}, projects, args, undefined, {});
  const hasher = createTaskHasher(graph, nxJson);
  const tasks = Object.values(taskGraph.tasks);
  const envs = Object.fromEntries(tasks.map((t) => [t.id, process.env]));
  await hashTasks(hasher, graph, taskGraph, envs, getTaskDetails());
  for (const task of tasks.sort((a, b) => (a.id < b.id ? -1 : 1))) {
    console.log(`${task.id} ${task.hash ?? "(not hashed)"}`);
  }
  console.error(`nx-hashes: ${tasks.length} tasks in ${((Date.now() - started) / 1000).toFixed(1)} s`);
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });
