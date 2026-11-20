// The #5853 walk: the daily checks' control on Settings ▸ Workflows, which was the daily summary's
// (#3395). Pairs, shoots Off, chooses On and then Every 6 hours from the page, and shoots each; the
// Daily checks workflow file is printed after each, to show the page changed it on the Mac's host.
// Then it shoots Reports, which has no Summary page any more.
//
//   node Web/test/walk/dailychecks5853.mjs <WEB_URL> <browser code> <checks workflow file> <out dir>

import { readFileSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";
import { launch } from "./cdp.mjs";

const [webURL, code, file, out] = process.argv.slice(2);
const front = () => readFileSync(file, "utf8").split("\n---")[0];
const chrome = await launch({ profile: `/tmp/5853-dailychecks-chrome-${process.pid}` });

// A menu's choice by its visible name, sent as the person's pick is.
const choose = (page, label, name) => page.eval(`(() => {
  const s = [...document.querySelectorAll(".daily-checks label")].find((l) => l.querySelector("span")?.textContent === ${JSON.stringify(label)})?.querySelector("select");
  const o = s && [...s.options].find((o) => o.textContent === ${JSON.stringify(name)});
  if (!o) return false;
  s.value = o.value; s.dispatchEvent(new Event("change", { bubbles: true })); return true;
})()`);
const centre = (page) => page.eval(`document.querySelector(".daily-checks").scrollIntoView({ block: "center" })`);

try {
  const page = await chrome.page(webURL, { width: 1280, height: 900 });
  await page.waitFor(`document.querySelector("textarea")`);
  await page.focus("textarea");
  await page.type(code);
  await page.press("Connect");
  await page.waitFor(`!document.querySelector("textarea")`, 20_000);
  // The Mac's host answers only once the browser joined the person's devices: approve its card
  // in the scratch window (Compare Digits, then Approve) while this opens Settings again and again,
  // as the control asks the host each time it is drawn.
  for (let tries = 0; ; tries++) {
    await page.eval(`location.hash = "#/overview"`);
    await sleep(500);
    await page.eval(`location.hash = "#/settings"`);
    await sleep(4_000);
    if (await page.eval(`!!document.querySelector(".daily-checks")`)) break;
    if (tries === 40) throw new Error("no daily checks control on Settings");
  }
  await centre(page);
  await sleep(500);
  await page.shot(`${out}/web-checks-off.png`);

  if (!await choose(page, "Daily checks", "On")) throw new Error("no On");
  await page.waitFor(`[...document.querySelectorAll(".daily-checks span")].some((s) => s.textContent === "How often")`, 10_000);
  await sleep(1500);
  console.log(`on:\n${front()}`);
  await centre(page);
  await page.shot(`${out}/web-checks-on.png`);

  if (!await choose(page, "How often", "Every 6 hours")) throw new Error("no Every 6 hours");
  await sleep(1500);
  console.log(`every 6 hours:\n${front()}`);
  await page.shot(`${out}/web-checks-every-6.png`);

  await page.eval(`location.hash = "#/reports"`);
  await sleep(2500);
  await page.shot(`${out}/web-reports.png`);
} finally {
  await chrome.close();
}
