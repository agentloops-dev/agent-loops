// A blocked image on a Work page and Settings ▸ Image Hosts on the web (#7140, port of #7139): the same words and the
// same choices as ImageHostsTests on the Mac.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("test/image-hosts.ts");
const { PageImages, ImageHostsList, CallFailed, answerOf, renderMarkdown } = m;
const blocked = (host, from) => new CallFailed(-32109, "blocked", from ? { host, from } : { host });
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
const image = { kind: "image", bytes: new Uint8Array([1]), type: "image/png" };

test("the words are the Mac's", () => {
  assert.equal(m.allowWords("pics.example.com"), "Always allow pics.example.com");
  const long = "a-very-long-tenant-name-0000000000.media-delivery.eu-central.images.example.net";
  assert.equal(m.allowWords(long), "Always allow …images.example.net");
  assert.equal(m.allowFull(long), `Always allow ${long}`);
  assert.equal(m.blockedWords({ host: "cdn.example.net", from: "pics.example.com" }), "Image blocked: pics.example.com redirects to cdn.example.net");
  assert.equal(m.learnsWords("pics.example.com", "Desk Mac"), "If you show it, pics.example.com learns that Desk Mac read it.");
  assert.equal(m.learnsWords(long, "This Mac"), "If you show it, this host learns that this Mac read it.");
  assert.equal(m.failedWords("it is not there (404)"), "The image could not be read: it is not there (404).");
  const now = new Date(2026, 10, 25, 12).getTime();
  assert.equal(m.allowedOn(now - 3600_000, now), "Allowed today");
  assert.match(m.allowedOn(new Date(2026, 10, 3).getTime(), now, "en-GB"), /^Allowed 3 Nov$/);
});

test("only imageBlocked is blocked; a host that is not public has no Try Again", () => {
  assert.deepEqual(answerOf(blocked("cdn.example.net", "pics.example.com")),
    { kind: "blocked", blocked: { host: "cdn.example.net", from: "pics.example.com" } });
  assert.equal(answerOf(new CallFailed(-32110, "intranet.example.com is on a private network")).again, false);
  assert.equal(answerOf(new CallFailed(-32000, "not there")).again, true);
});

test("one Show accepts one host: a redirect is blocked again and names both", async () => {
  const asked = [];
  const page = new PageImages(async (url, show) => {
    asked.push(show);
    if (show.length === 0) return answerOf(blocked("pics.example.com"));
    if (show.length === 1) return answerOf(blocked("cdn.example.net", "pics.example.com"));
    return image;
  }, async () => {});
  page.load("https://pics.example.com/a.png", "m1");
  await tick();
  assert.equal(page.phase("https://pics.example.com/a.png").kind, "blocked");
  page.show("https://pics.example.com/a.png");
  assert.equal(page.phase("https://pics.example.com/a.png").kind, "reading");
  await tick();
  assert.deepEqual(page.phase("https://pics.example.com/a.png").blocked, { host: "cdn.example.net", from: "pics.example.com" });
  page.show("https://pics.example.com/a.png");
  await tick();
  assert.deepEqual(asked, [[], ["pics.example.com"], ["pics.example.com", "cdn.example.net"]]);
  assert.equal(page.phase("https://pics.example.com/a.png").kind, "image");
  assert.equal(page.shownOnce("https://pics.example.com/a.png"), "cdn.example.net");
});

test("Always allow reads each of the page's images from that host, and goes back with why when refused", async () => {
  let allowed = false;
  let refuse = true;
  const page = new PageImages(async (url) => url.includes("other") || !allowed ? answerOf(blocked(url.includes("other") ? "other.example.org" : "pics.example.com")) : image,
    async () => { if (refuse) throw new CallFailed(-32000, "The list is full (100 hosts)."); allowed = true; });
  for (const url of ["https://pics.example.com/1.png", "https://pics.example.com/2.png", "https://other.example.org/3.png"]) page.load(url, "m1");
  await tick();
  await page.allow("pics.example.com", "https://pics.example.com/1.png", "Desk Mac");
  assert.equal(page.phase("https://pics.example.com/1.png").problem, "pics.example.com was not allowed on Desk Mac. The list is full (100 hosts).");
  assert.equal(page.phase("https://pics.example.com/2.png").problem, null);
  refuse = false;
  await page.allow("pics.example.com", "https://pics.example.com/1.png", "Desk Mac");
  await tick();
  assert.equal(page.phase("https://pics.example.com/1.png").kind, "image");
  assert.equal(page.phase("https://pics.example.com/2.png").kind, "image");
  assert.equal(page.phase("https://other.example.org/3.png").kind, "blocked");
  assert.equal(page.shownOnce("https://pics.example.com/1.png"), null);
});

test("Always allow asks the machine again for a host the page allowed before (#7273)", async () => {
  let allows = 0;
  const page = new PageImages(async () => answerOf(blocked("pics.example.com")), async () => { allows++; });
  page.load("https://pics.example.com/1.png", "m1");
  await tick();
  await page.allow("pics.example.com", "https://pics.example.com/1.png", "Desk Mac");
  await tick();
  // Removed elsewhere: blocked again, and Always allow must add it again.
  await page.allow("pics.example.com", "https://pics.example.com/1.png", "Desk Mac");
  assert.equal(allows, 2);
});

test("an image that fails as it loads is its alternative text; after a press, why and Try Again", async () => {
  let pressed = false;
  const page = new PageImages(async (url, show) => {
    if (show.length === 0 && !pressed) { pressed = true; return answerOf(new CallFailed(-32000, "not there")); }
    return show.length === 0 ? answerOf(blocked("pics.example.com")) : answerOf(new CallFailed(-32000, "it is not there (404)"));
  }, async () => {});
  page.load("https://a.example.com/x.png", "m1");
  await tick();
  assert.equal(page.phase("https://a.example.com/x.png").kind, "none");
  page.load("https://pics.example.com/y.png", "m1");
  await tick();
  page.show("https://pics.example.com/y.png");
  await tick();
  assert.deepEqual(page.phase("https://pics.example.com/y.png"), { kind: "failed", why: "it is not there (404)", again: true });
});

test("Remove takes the row out at once and puts it back where it was when the machine refuses", async () => {
  const hosts = [{ host: "a.example.com", addedAt: 1 }, { host: "b.example.com", addedAt: 2 }];
  let kept;
  const list = new ImageHostsList({
    kept: async () => ({ value: { hosts }, at: 5 }),
    list: async () => ({ hosts }),
    remove: async () => { throw new CallFailed(-32000, "no"); },
    keep: (got) => { kept = got; },
  });
  await list.load(false);
  assert.equal(list.readAt.value, 5);
  assert.equal(kept, undefined);
  await list.load(true);
  assert.deepEqual(kept, { hosts });
  const removing = list.remove("a.example.com");
  assert.deepEqual(list.hosts.value.map((h) => h.host), ["b.example.com"]);
  await removing;
  assert.deepEqual(list.hosts.value.map((h) => h.host), ["a.example.com", "b.example.com"]);
  assert.equal(list.notRemoved.value.host, "a.example.com");
});

const pictures = (nodes, name) => [nodes].flat(Infinity).flatMap(function walk(node) {
  if (!node || typeof node !== "object") return [];
  const own = typeof node.type === "function" && node.type.name === name ? [node.props] : [];
  return [...own, ...[node.props?.children ?? []].flat(Infinity).flatMap(walk)];
});

test("an image in words is a chip, and one in a link has its Blocked chip beside the link, not in it", () => {
  const read = Object.assign(async () => null, { controls: { images: new PageImages(async () => image, async () => {}), machine: "m1", machineName: "Desk Mac" } });
  const alone = pictures(renderMarkdown("![chart](https://pics.example.com/c.png)", undefined, undefined, read), "ForgePicture");
  assert.equal(alone[0].inWords, false);
  const words = pictures(renderMarkdown("Status: ![build](https://badges.example.org/b.svg) on main.", undefined, undefined, read), "ForgePicture");
  assert.equal(words[0].inWords, true);
  const nodes = renderMarkdown("[![cov](https://badges.example.org/c.svg)](https://example.com/report)", undefined, undefined, read);
  const link = [nodes].flat(Infinity).flatMap(function walk(node) {
    if (!node || typeof node !== "object") return [];
    return [...(node.type === "a" ? [node] : []), ...[node.props?.children ?? []].flat(Infinity).flatMap(walk)];
  })[0];
  assert.equal(pictures(link, "BlockedChip").length, 0);
  assert.equal(pictures(nodes, "BlockedChip").length, 1);
  // Without controls, no chip.
  assert.equal(pictures(renderMarkdown("[![cov](https://badges.example.org/c.svg)](https://example.com/r)", undefined, undefined, async () => null), "BlockedChip").length, 0);
});
