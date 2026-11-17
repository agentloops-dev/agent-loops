// The #5354 walk: the web page's machine set-up steps, New Machine's choices and Start from, and Warm Pools, on a scratch
// control plane. A commands provider whose machines are files in <fleet dir> makes machines (they never join), so a warm
// pool fills with machines being set up. The scratch host's own set-up is driven by writing the installer's reports to
// <root>/setup-reports.jsonl, which its agentsd sends to the control plane (#5356): running, failed, Try Again, needs
// you and ready. Screenshots go to <out dir>.
//
//   node Web/test/walk/machinesetup5354.mjs <WEB_URL> <browser code> <root> <fleet dir> <out dir>

import { appendFileSync, mkdirSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";
import { launch } from "./cdp.mjs";

const [webURL, code, root, fleet, out] = process.argv.slice(2);
const js = JSON.stringify;
let failed = 0;
const check = (ok, line) => { if (!ok) failed += 1; console.log(`${ok ? "ok  " : "FAIL"} ${line}`); };
mkdirSync(fleet, { recursive: true });
mkdirSync(out, { recursive: true });
const report = (step, state, more = {}) => appendFileSync(`${root}/setup-reports.jsonl`, `${js({ step, state, ...more })}\n`);

const chrome = await launch({ profile: `${out}-chrome-${process.pid}` });
let page;
try {
  page = await chrome.page(webURL, { width: 1300, height: 900 });
  await page.onNewDocument(`window.confirm = () => true; (() => { const W = window.WebSocket; window.WebSocket = function (...a) { const s = new W(...a); window.__walkSocket = s; return s; }; window.WebSocket.prototype = W.prototype; })();`);
  await page.goto(webURL);
  await page.waitFor(`document.querySelector("textarea")`);
  await page.focus("textarea");
  await page.type(code);
  await page.press("Connect");
  await page.waitFor(`!document.querySelector("textarea") && document.body.textContent.includes("Settings")`, 20_000);
  // The scratch window approves this browser into the machine lock (walk.py "=Compare Digits", "=Approve"), so the
  // Mac's host answers it: its folders are the projects a pool sends.
  await page.waitFor(`!document.body.textContent.includes("Approve this device")`, 120_000).catch(() => console.log("note: not approved"));
  await page.eval(`window.__walkReplies = {}; window.__walkSocket.addEventListener("message", (e) => { try { const m = JSON.parse(e.data); if (String(m.id).startsWith("walk-")) window.__walkReplies[m.id] = m; } catch {} })`);
  let n = 0;
  const call = async (method, params, wait = 60_000) => {
    const id = `walk-${n += 1}`;
    await page.eval(`window.__walkSocket.send(${js(js({ jsonrpc: "2.0", id, method, params }))})`);
    await page.waitFor(`!!window.__walkReplies[${js(id)}]`, wait);
    return JSON.parse(await page.eval(`JSON.stringify(window.__walkReplies[${js(id)}])`));
  };
  const origin = new URL(webURL).origin;
  // The scratch host, which carries the provider and has the projects, online.
  let host;
  for (let i = 0; i < 60 && !host; i += 1) {
    const hosts = await call("machines/list", {});
    host = (hosts.result?.machines ?? hosts.result ?? []).find((h) => h.state === "online");
    if (!host) await sleep(1_000);
  }
  check(!!host, "the scratch host is online");

  // A provider that makes machines: each is a file in the fleet folder, with its state in it.
  // A run before on this root may have added it, and the pool.
  const listed = (await call("providers/list", {})).result?.providers ?? [];
  const added = listed.some((p) => p.name === "Scratch cloud") ? { result: listed.find((p) => p.name === "Scratch cloud") } : await call("providers/add", {
    name: "Scratch cloud", kind: "commands",
    commands: {
      list: `for f in ${fleet}/*; do [ -f "$f" ] && echo "$(basename "$f") $(basename "$f") $(cat "$f")"; done; true`,
      columns: ["id", "name", "state"],
      start: `echo running > ${fleet}/\${id}`,
      stop: `echo stopped > ${fleet}/\${id}`,
      create: `echo running > ${fleet}/\${name}`,
      delete: `rm -f ${fleet}/\${id}`,
      options: {
        type: "object", required: ["size"],
        properties: {
          size: { type: "string", title: "Size", enum: ["small", "large"], "x-costs": { small: "$0.03/hour", large: "$0.19/hour" } },
          region: { type: "string", title: "Region", enum: ["fra1", "nyc3"] },
        },
      },
    },
  });
  check(!added.error, `provider added: ${js(added).slice(0, 300)}`);
  const provider = added.result?.id;

  // A warm pool of two: the control plane makes them at once; they never join, so they stay setting up.
  const kept = ((await call("pool/list", {})).result?.pools ?? []).find((p) => p.name === "dev-large");
  const pool = await call("pool/set", { ...(kept ? { id: kept.id } : {}), name: "dev-large", scope: "user", provider, size: "large", region: "fra1",
    minReady: 2, maxSize: 3, replaceAfter: 86400, projects: [`${root}/website`] });
  check(!pool.error, `pool set: ${js(pool).slice(0, 300)}`);

  await page.goto(`${origin}/#/settings`);
  const pools = `document.querySelector(".warm-pools")`;
  await page.waitFor(`${pools}?.textContent.includes("dev-large")`, 30_000);
  await page.waitFor(`${pools}.querySelectorAll(".warm-pool-machines li").length > 0`, 60_000).catch(() => null);
  await page.eval(`${pools}.scrollIntoView({ block: "start" })`);
  const card = (await page.text(".warm-pools")).replace(/\s+/g, " ");
  check(card.includes("Last update") && card.includes("ready (min 2, max 3)"), `the pool's card: ${card.slice(0, 400)}`);
  await page.shot(`${out}/web-warm-pools.png`);

  await page.press("Edit…");
  await page.waitFor(`document.querySelector("dialog[open]")?.textContent.includes("Edit Warm Pool")`);
  await sleep(500);
  const form = (await page.text("dialog[open]")).replace(/\s+/g, " ");
  check(form.includes("Min 2: about $0.38 an hour"), `the form's cost: ${form.slice(0, 500)}`);
  await page.shot(`${out}/web-warm-pool-form.png`);
  await page.press("Cancel");
  await page.press("New Warm Pool…");
  await page.waitFor(`document.querySelector("dialog[open]")?.textContent.includes("New Warm Pool on Scratch cloud")`);
  await page.focus("dialog[open] input[type=number]");
  await page.eval(`(() => { const i = document.querySelector("dialog[open] input[type=number]"); i.value = "5"; i.dispatchEvent(new Event("input", { bubbles: true })); })()`);
  await sleep(300);
  const refused = (await page.text("dialog[open]")).replace(/\s+/g, " ");
  check(refused.includes("Min ready (5) is above max size (2)."), `min above max is refused: ${refused.slice(0, 300)}`);
  await page.shot(`${out}/web-warm-pool-refused.png`);
  await page.press("Cancel");

  // New Machine: Start from the pool (none ready yet), the size and Stop when idle.
  await page.goto(`${origin}/#/agents`);
  await page.waitFor(`!!document.querySelector("[aria-label='New folder']")`, 15_000);
  await page.press("New folder");
  await page.waitFor(`[...document.querySelectorAll("[role=menuitem]")].some((b) => b.textContent.startsWith("New Machine"))`);
  await page.eval(`[...document.querySelectorAll("[role=menuitem]")].find((b) => b.textContent.startsWith("New Machine")).click()`);
  await page.waitFor(`document.querySelector("dialog[open]")?.textContent.includes("Stop when idle")`, 15_000);
  await page.eval(`(() => { const s = [...document.querySelectorAll("dialog[open] select")].find((x) => [...x.options].some((o) => o.value === "large")); s.value = "large"; s.dispatchEvent(new Event("change", { bubbles: true })); })()`);
  await page.waitFor(`document.querySelector("dialog[open]").textContent.includes("Start from")`, 10_000).catch(() => null);
  const sheet = (await page.text("dialog[open]")).replace(/\s+/g, " ");
  check(sheet.includes("Warm pool “dev-large”") && sheet.includes("A new machine · a few minutes") && !sheet.includes("Name"),
    `New Machine: ${sheet.slice(0, 500)}`);
  await page.shot(`${out}/web-new-machine.png`);
  await page.press("Cancel");

  // The scratch host's set-up, from the installer's reports.
  const steps = `document.querySelector(".machine-setup")`;
  report("install", "running", { detail: "Agents: 62 of 140 MB" });
  await page.goto(`${origin}/#/machines/machine-${host.id}`);
  await page.waitFor(`${steps}?.textContent.includes("Agents: 62 of 140 MB")`, 30_000);
  report("install", "done", { detail: "Agents 0.42.0" });
  report("compare", "done", { detail: "123 456: the machine that joined is the one made" });
  report("runtimes", "running", { detail: "Claude: 210 of 500 MB" });
  report("projects", "running", { detail: "website: running .agents/setup.sh" });
  await page.waitFor(`${steps}.textContent.includes("Claude: 210 of 500 MB")`, 30_000);
  const running = (await page.text("body")).replace(/\s+/g, " ");
  check(running.includes("Setting up · 5 of 8"), `the chip while it sets up: ${running.slice(0, 300)}`);
  await page.shot(`${out}/web-setting-up.png`);

  report("runtimes", "failed", { error: "The package source did not answer: registry.npmjs.org timed out after 60 s." });
  await page.waitFor(`${steps}.textContent.includes("Try Again")`, 30_000);
  const stopped = (await page.text("body")).replace(/\s+/g, " ");
  check(stopped.includes("Set-up failed · step 5 of 8") && stopped.includes("Delete Machine…"), `a failed step: ${stopped.slice(0, 300)}`);
  await page.shot(`${out}/web-failed.png`);
  await page.press("Try Again");
  await page.waitFor(`!${steps}.textContent.includes("Try Again")`, 30_000);
  check(true, "Try Again ran the step again");

  report("runtimes", "done", { detail: "Claude 2.1.9, Codex 0.98" });
  report("projects", "done", { detail: "website, on main" });
  report("signIn", "needs-you", { detail: "Waiting for you: Claude" });
  await page.waitFor(`${steps}.textContent.includes("Waiting for you: Claude")`, 30_000);
  await page.shot(`${out}/web-needs-you.png`);

  report("signIn", "done", { detail: "Claude with a key from Secrets" });
  report("smoke", "done", { detail: "Answered in 4 s" });
  await page.waitFor(`document.body.textContent.includes("Ready · ")`, 30_000);
  const line = (await page.eval(`[...document.querySelectorAll(".machine-need")].map((p) => p.textContent).join(" | ")`));
  check(true, `the sidebar's lines once ready: ${line}`);
  await page.shot(`${out}/web-ready.png`);
} catch (error) {
  failed += 1;
  console.log(`FAIL ${error.message}`);
  if (page) await page.shot(`${out}/web-failed-walk.png`).catch(() => undefined);
} finally {
  await chrome.close();
}
process.exit(failed ? 1 : 0);
