// The #6577 walk: New Droplet on a DigitalOcean card, against a fake DigitalOcean API (no droplet is paid for). The
// provider is the digitalocean-api preset with its `api` on the fake; the scratch host carries it. Shots of the button
// with its line, Change…, and the make's steps on its row go to <out dir>. The fake's droplets never join.
//
//   python3 -I <fake dir>/fake.py <port> 25 &     # regions, sizes, the ssh key, droplets that start after 25 s
//   node Web/test/walk/newdroplet6577.mjs <WEB_URL> <browser code> <fake API address> <out dir>

import { mkdirSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";
import { launch } from "./cdp.mjs";

const [webURL, code, api, out] = process.argv.slice(2);
const js = JSON.stringify;
let failed = 0;
const check = (ok, line) => { if (!ok) failed += 1; console.log(`${ok ? "ok  " : "FAIL"} ${line}`); };
mkdirSync(out, { recursive: true });
const card = `document.querySelector(".provider-card[aria-label='Scratch DigitalOcean']")`;
const dialog = `document.querySelector("dialog[open]")`;

const chrome = await launch({ profile: `${out}-chrome-${process.pid}` });
let page;
try {
  page = await chrome.page(webURL, { width: 1100, height: 820 });
  await page.onNewDocument(`window.confirm = () => true; (() => { const W = window.WebSocket; window.WebSocket = function (...a) { const s = new W(...a); window.__walkSocket = s; return s; }; window.WebSocket.prototype = W.prototype; })();`);
  await page.goto(webURL);
  await page.waitFor(`document.querySelector("textarea")`);
  await page.focus("textarea");
  await page.type(code);
  await page.press("Connect");
  await page.waitFor(`!document.querySelector("textarea") && document.body.textContent.includes("Settings")`, 20_000);
  await page.eval(`window.__walkReplies = {}; window.__walkSocket.addEventListener("message", (e) => { try { const m = JSON.parse(e.data); if (String(m.id).startsWith("walk-")) window.__walkReplies[m.id] = m; } catch {} })`);
  let n = 0;
  const call = async (method, params, wait = 60_000) => {
    const id = `walk-${n += 1}`;
    await page.eval(`window.__walkSocket.send(${js(js({ jsonrpc: "2.0", id, method, params }))})`);
    await page.waitFor(`!!window.__walkReplies[${js(id)}]`, wait);
    return JSON.parse(await page.eval(`JSON.stringify(window.__walkReplies[${js(id)}])`));
  };
  let host;
  for (let i = 0; i < 60 && !host; i += 1) {
    const hosts = await call("machines/list", {});
    host = (hosts.result?.machines ?? hosts.result ?? []).find((h) => h.state === "online");
    if (!host) await sleep(1_000);
  }
  check(!!host, "the scratch host is online");
  const listed = (await call("providers/list", {})).result?.providers ?? [];
  if (!listed.some((p) => p.name === "Scratch DigitalOcean")) {
    const added = await call("providers/add", {
      name: "Scratch DigitalOcean", kind: "commands", machine: host.id,
      commands: { preset: "digitalocean-api", tag: "agents", user: "agents", api },
      secrets: { DIGITALOCEAN_API_TOKEN: "fake-token" },
    });
    check(!added.error, `provider added: ${js(added).slice(0, 300)}`);
  }

  await page.goto(`${new URL(webURL).origin}/#/settings`);
  await page.waitFor(`${card}?.textContent.includes("New Droplet") && ${card}.textContent.includes("$24/month")`, 60_000);
  const text = (await page.text(".provider-card[aria-label='Scratch DigitalOcean']")).replace(/\s+/g, " ");
  check(text.includes("San Francisco 3 · 2 vCPUs · 4 GB · 80 GB disk · $24/month"), `the line says where and the price: ${text.slice(0, 300)}`);
  check(!text.includes("New Machine…"), "Change… is in New Machine…'s place");
  await page.eval(`${card}.scrollIntoView()`);
  await page.shot(`${out}/web-button.png`);

  await page.press("Change…");
  await page.waitFor(`${dialog}?.textContent.includes("San Francisco 3")`, 30_000);
  await page.shot(`${out}/web-change.png`);
  await page.press("Cancel");
  await page.waitFor(`!${dialog}`);

  await page.press("New Droplet");
  await page.waitFor(`${card}.querySelector(".provider-setup")`, 20_000);
  await page.eval(`${card}.scrollIntoView()`);
  await sleep(1_500);
  await page.shot(`${out}/web-making.png`);
  await page.waitFor(`${card}.querySelector(".provider-setup")?.textContent.includes("Agent Loops")`, 30_000)
    .catch(() => console.log("note: no ssh-key words yet"));
  await sleep(2_000);
  await page.shot(`${out}/web-made.png`);
  await page.waitFor(`${card}.querySelector(".provider-setup")?.textContent.includes("Started")`, 90_000)
    .catch(() => console.log("note: not started yet"));
  await sleep(4_000);
  await page.shot(`${out}/web-started.png`);
  const steps = (await page.text(".provider-card[aria-label='Scratch DigitalOcean'] .provider-setup").catch(() => "")).replace(/\s+/g, " ");
  // "Started" comes with the control plane's next listing of the provider (#6073), which can take longer than this walk.
  check(steps.includes("Made droplet"), `the steps say what DigitalOcean made: ${steps.slice(0, 400)}`);
  console.log(`steps: ${steps}`);
} finally {
  await chrome.close();
}
process.exit(failed ? 1 : 0);
