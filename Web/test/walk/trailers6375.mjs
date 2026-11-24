// The #6375 walk: Commit Trailers in Console ▸ Organisation Policy. Pairs as the control plane's admin,
// shoots the trailers as no rule sets them, sets Made-by On and Tokens Off, shoots What Changes, saves,
// and shoots the form read back from the control plane. Screenshots go to <out dir>.
//
//   node Web/test/walk/trailers6375.mjs <WEB_URL> <browser code> <out dir>

import { launch } from "./cdp.mjs";

const [webURL, code, out] = process.argv.slice(2);
let failed = 0;
const check = (ok, line) => { if (!ok) failed += 1; console.log(`${ok ? "ok  " : "FAIL"} ${line}`); };
const section = (id) => `document.querySelector("[aria-labelledby=${id}]")`;
const select = (key) => `document.querySelector(${JSON.stringify(`form.org-policy [aria-label="${key} trailer"]`)})`;
const choose = (key, value) => `(() => {
  const el = ${select(key)};
  if (!el) return false;
  el.value = ${JSON.stringify(value)};
  el.dispatchEvent(new Event("change", { bubbles: true }));
  return true;
})()`;

const chrome = await launch({ profile: `/tmp/w6375-chrome-${process.pid}` });
try {
  const page = await chrome.page(webURL, { width: 1300, height: 1300 });
  await page.waitFor(`document.querySelector("textarea")`);
  await page.focus("textarea");
  await page.type(code);
  await page.press("Connect");
  await page.waitFor(`!document.querySelector("textarea") && document.body.textContent.includes("Settings")`, 20_000);
  await page.eval(`location.hash = "#/settings/policy"`);
  await page.waitFor(`${section("org-policy-trailers")}`, 20_000);
  check(await page.eval(`${select("Cost")}.selectedOptions[0].textContent === "Not set (Off)"`), "Cost is Not set (Off)");
  check(await page.eval(`${select("Made-by")}.selectedOptions[0].textContent === "Not set (On)"`), "Made-by is Not set (On)");
  await page.eval(`${section("org-policy-trailers")}.scrollIntoView()`);
  await page.shot(`${out}/web-not-set.png`);

  check(await page.eval(choose("Made-by", "on")), "set Made-by On");
  check(await page.eval(choose("Tokens", "off")), "set Tokens Off");
  check(await page.eval(choose("Cost", "on")), "set Cost On");
  await page.waitFor(`${section("org-policy-preview")}`);
  const preview = await page.text("[aria-labelledby=org-policy-preview]");
  for (const line of ["Made-by trailer: On, required", "Tokens trailer: Off, kept out of commits",
    "Cost trailer: On unless a folder or person turns it off"]) {
    check(preview.includes(line), `What Changes says ${line}`);
  }
  await page.eval(`${section("org-policy-trailers")}.scrollIntoView()`);
  await page.shot(`${out}/web-edited.png`);
  await page.eval(`${section("org-policy-preview")}.scrollIntoView()`);
  await page.shot(`${out}/web-what-changes.png`);

  await page.press("Save");
  await page.waitFor(`document.body.textContent.includes("Saved. Every machine has it within seconds.")`, 20_000);
  check(true, "saved");
  await page.eval(`location.hash = "#/settings"`);
  await page.eval(`location.hash = "#/settings/policy"`);
  await page.waitFor(`${section("org-policy-trailers")}`, 20_000);
  check(await page.eval(`${select("Made-by")}.value === "on"`), "Made-by On reads back");
  check(await page.eval(`${select("Tokens")}.value === "off"`), "Tokens Off reads back");
  check(await page.eval(`${select("Epic")}.value === ""`), "Epic stays Not set");
  await page.eval(`${section("org-policy-trailers")}.scrollIntoView()`);
  await page.shot(`${out}/web-saved.png`);
} finally {
  await chrome.close();
}
process.exit(failed ? 1 : 0);
