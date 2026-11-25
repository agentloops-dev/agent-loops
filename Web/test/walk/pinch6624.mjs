// Pinch to zoom in Files (#6624): on a run-app root whose host `mac` has one session in a folder with
// `notes.md` (pictures of 1200, 900 and 700 pixels and a small one, under shots/, the last far down the
// page) and shown by the agent: the viewer from a picture on the page and on its edited passages, a
// trackpad's pinch (Control and the wheel) and Safari's gesture events on an image file, and a
// two-finger pinch on a touch screen of 390 px. Screenshots into <out dir>, and frames of the pinch.
//
//   node Web/test/walk/pinch6624.mjs <WEB_URL> <browser code> <out dir> <project> <agent>

import { setTimeout as sleep } from "node:timers/promises";
import { launch } from "./cdp.mjs";

const [webURL, code, out, project, agent] = process.argv.slice(2);
const chrome = await launch({ profile: `${out}-chrome` });
const page = await chrome.page(webURL, { width: 1440, height: 900 });
const ok = (what, cond) => { console.log(`${cond ? "PASS" : "FAIL"} ${what}`); if (!cond) process.exitCode = 1; };
const open = `document.querySelector("dialog.image-viewer")?.open === true`;
const tab = (name) => page.eval(`[...document.querySelectorAll("aside.files [role=tab]")].find((b) => b.textContent.trim() === ${JSON.stringify(name)})?.click()`);
const tap = (name) => page.eval(`[...document.querySelectorAll("button, [role=treeitem], [role=tab]")].find((b) => (b.getAttribute("aria-label") ?? b.textContent).trim().split(",")[0] === ${JSON.stringify(name)})?.click()`);
const viewerLevel = () => page.eval(`parseInt(document.querySelector("dialog.image-viewer .zoom-level").textContent, 10)`);
const level = () => page.eval(`parseInt(document.querySelector('.picture-bar [aria-label="Actual size"]').textContent, 10)`);
const box = (selector) => page.eval(`(() => { const b = document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect(); return { x: b.x, y: b.y, w: b.width, h: b.height }; })()`);
const rendered = () => page.eval(`visualViewport.scale`);
const touch = (type, points) => page.raw("Input.dispatchTouchEvent", { type, touchPoints: points.map(([x, y], id) => ({ x, y, id })) });
/** Two fingers from `from` to `to` apart about (x, y). */
async function pinch(x, y, from, to, each = async () => {}) {
  await touch("touchStart", [[x - from / 2, y]]);
  await touch("touchStart", [[x - from / 2, y], [x + from / 2, y]]);
  for (let i = 1; i <= 12; i++) {
    const d = from + (to - from) * i / 12;
    await touch("touchMove", [[x - d / 2, y], [x + d / 2, y]]);
    await sleep(40);
    await each(i);
  }
  await touch("touchEnd", []);
  await sleep(300);
}
const pointUnder = async (x, y, selector) => { const b = await box(selector); return [(x - b.x) / b.w, (y - b.y) / b.h]; };
/** The point stays sideways; up and down only once the picture is taller than the pane, where it can scroll. */
const near = (a, b) => { console.log(`  ${a.map((n) => n.toFixed(3))} then ${b.map((n) => n.toFixed(3))}`); return Math.abs(a[0] - b[0]) < 0.02; };
const filesPicture = async (name) => {
  await tab("Files"); await sleep(1200);
  if (!(await page.eval(`[...document.querySelectorAll("[role=treeitem], .entry")].some((r) => r.textContent.includes(${JSON.stringify(name)}))`))) {
    await page.eval(`[...document.querySelectorAll("[role=treeitem], .entry")].find((r) => r.textContent.includes("shots"))?.click()`); await sleep(900);
  }
  await page.eval(`[...document.querySelectorAll("[role=treeitem], .entry")].find((r) => r.textContent.includes(${JSON.stringify(name)}))?.click()`);
  await page.waitFor(`document.querySelector("img.picture")?.naturalWidth > 0`, 15_000);
  await sleep(500);
};

try {
  // The profile is kept, so a second run is approved already.
  await page.waitFor(`document.querySelector("textarea, .nav-item")`);
  if (!(await page.eval(`!!document.querySelector(".nav-item")`))) {
    await page.focus("textarea");
    await page.type(code);
    await page.press("Connect");
  }
  await page.waitFor(`document.querySelector(".nav-item")`, 30_000);
  // A machine with a lock takes nothing from a browser until it is approved on a device that is in.
  await page.waitFor(`document.body.textContent.includes("Approve this device")`, 8_000).catch(() => {});
  await page.waitFor(`!document.body.textContent.includes("Approve this device")`, 90_000);
  await sleep(2000);
  await page.goto(webURL);
  await page.waitFor(`document.querySelector(".nav-item")`, 30_000);
  await sleep(2000);
  await page.eval(`location.hash = "#/h/mac/p/${project}/s/${agent}"`);
  await page.waitFor(`document.querySelector(".chat .transcript")`, 30_000);
  await sleep(4000);
  await tap("Try Again");
  if (!(await page.eval(`!!document.querySelector("aside.files")`))) await tap("Files");
  await page.waitFor(`document.querySelector("aside.files")`, 10_000);
  await tab("Page");
  await page.waitFor(`[...document.querySelectorAll(".chat .transcript button")].some((b) => b.textContent.includes("Page · "))`, 30_000);
  await page.eval(`[...document.querySelectorAll(".chat .transcript button")].find((b) => b.textContent.includes("Page · "))?.click()`);
  await page.waitFor(`document.querySelectorAll(".live-page button.page-picture-open").length === 3`, 15_000).catch(() => {});
  ok("three pictures open large, and the small one does not", await page.eval(`document.querySelectorAll("img.page-picture").length === 4 && document.querySelectorAll("button.page-picture-open").length === 3`));
  await sleep(500);
  await page.shot(`${out}/web-page.png`);
  await page.eval(`document.querySelector("button.page-picture-open").click()`);
  await page.waitFor(open);
  await sleep(500);
  ok("the counter says 1 of 3", await page.eval(`document.querySelector("dialog.image-viewer .counter").textContent === "1 of 3"`));
  ok("no passage opened for typing", await page.eval(`!document.querySelector(".passage-editor")`));
  await page.shot(`${out}/web-viewer.png`);
  const stage = await box(".image-viewer-stage");
  const before = await viewerLevel();
  await page.raw("Input.dispatchMouseEvent", { type: "mouseWheel", x: stage.x + 200, y: stage.y + 200, deltaX: 0, deltaY: -60, modifiers: 2 });
  await sleep(400);
  ok(`Control and the wheel zoom in the viewer: ${before}% to ${await viewerLevel()}%`, (await viewerLevel()) > before);
  await page.eval(`document.querySelector("dialog.image-viewer .image-viewer-stage img").click()`);
  ok("a click in the viewer opens no passage", await page.eval(`!document.querySelector(".passage-editor")`));
  await page.key("Escape");
  await sleep(400);
  ok("Esc closes the viewer", !(await page.eval(open)));
  await page.eval(`document.querySelector(".live-page .passage p").click()`);
  await sleep(400);
  ok("words still open their passage", await page.eval(`!!document.querySelector(".passage-editor")`));
  await page.key("Escape");

  await filesPicture("one.png");
  await page.shot(`${out}/web-file.png`);
  const b = await box("img.picture");
  const at = [b.x + b.w * 0.7, b.y + b.h * 0.6];
  const under = await pointUnder(...at, "img.picture");
  const was = await level();
  await page.raw("Input.dispatchMouseEvent", { type: "mouseWheel", x: at[0], y: at[1], deltaX: 0, deltaY: -100, modifiers: 2 });
  await sleep(400);
  ok(`Control and the wheel zoom the file: ${was}% to ${await level()}%`, (await level()) > was);
  ok("the point under the pointer stays", near(under, await pointUnder(...at, "img.picture")));
  await page.shot(`${out}/web-file-zoomed.png`);
  await tap("Fit"); await sleep(300);
  const prevented = await page.eval(`(() => { const el = document.querySelector("img.picture"); const b = el.getBoundingClientRect();
    const fire = (type, scale) => { const e = new Event(type, { bubbles: true, cancelable: true }); Object.assign(e, { scale, clientX: b.x + 100, clientY: b.y + 100 }); el.dispatchEvent(e); return e.defaultPrevented; };
    return [fire("gesturestart", 1), fire("gesturechange", 1.8), fire("gestureend", 1.8)].every(Boolean); })()`);
  await sleep(300);
  ok(`Safari's gesture events, made by hand, zoom and are taken: ${await level()}%`, prevented && (await level()) > was);

  // A touch screen of 390 px.
  await page.raw("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
  await page.raw("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
  await sleep(1200);
  await tap("Fit"); await sleep(400);
  await page.shot(`${out}/web-phone-file.png`);
  const p = await box("img.picture");
  const c = [p.x + p.w / 2, p.y + p.h / 2];
  const there = await pointUnder(...c, "img.picture");
  const fitted = await level();
  await pinch(...c, 60, 220, (i) => page.shot(`${out}-frames/${String(i).padStart(2, "0")}.png`));
  ok(`two fingers zoom the file: ${fitted}% to ${await level()}%`, (await level()) > fitted * 2);
  ok("the point between the fingers stays", near(there, await pointUnder(...c, "img.picture")));
  ok("the page keeps its size", (await rendered()) === 1);
  await page.shot(`${out}/web-phone-file-zoomed.png`);
  const scrolled = () => page.eval(`(() => { const s = document.querySelector("img.picture").closest(".scroll"); return [s.scrollLeft, s.scrollTop]; })()`);
  const s0 = await scrolled();
  await touch("touchStart", [[200, 250]]); await touch("touchMove", [[140, 250]]); await touch("touchEnd", []);
  await sleep(200);
  ok(`one finger moves about it: ${s0} to ${await scrolled()}`, (await scrolled())[0] > s0[0]);
  await pinch(200, 250, 220, 40);
  ok(`a pinch closed stops at fitted: ${await level()}%`, (await level()) === fitted);

  await tab("Page"); await sleep(800);
  await page.waitFor(`document.querySelector("button.page-picture-open")`, 10_000);
  await page.eval(`document.querySelector("button.page-picture-open").click()`);
  await page.waitFor(open);
  await sleep(500);
  const st = await box(".image-viewer-stage");
  const v = await viewerLevel();
  await pinch(st.x + st.w / 2, st.y + st.h / 2, 60, 220);
  ok(`two fingers zoom in the viewer: ${v}% to ${await viewerLevel()}%`, (await viewerLevel()) > v);
  await page.shot(`${out}/web-phone-viewer.png`);
} catch (error) {
  console.error(error);
  await page.shot(`${out}/failed.png`);
  process.exitCode = 1;
} finally {
  await chrome.close();
}
