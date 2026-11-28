// Settings ▸ Control plane ▸ Clients (#4253), as the window's: Pair a Client… (#7270), with the kind to choose in
// its dialog: an iPhone or iPad (a QR code the Remote scans), a Mac or a browser (a code to paste), each a one-use
// code from clients/startPairing. A Mac's code is a
// terminal's too, as loops auth login takes it (#6875).
import { encode } from "uqr";
import type { WebRemoteStatus } from "../protocol/generated";
import { serveLine } from "./controlOverview";

export type PairPurpose = "device" | "mac" | "browser";

export const pairPurposes: readonly PairPurpose[] = ["device", "mac", "browser"];

export const pairWords = {
  heading: "Clients",
  hint: "Each code works once, for one client. That client can then do all this page can.",
  button: "Pair a Client…",
  title: "Pair a Client",
  kindLabel: "Client",
  kind: (purpose: PairPurpose) => (purpose === "device" ? "iPhone or iPad" : purpose === "mac" ? "Mac" : "Browser"),
  terminal: "Or pair a terminal with it: run this in the terminal.",
  making: "Making a code…",
  scan: "Open Agent Loops on your iPhone or iPad and scan this code.",
  paired: (name: string) => `${name} is paired`,
  pairedNote: "It reaches your agents through the control plane, as a Device.",
  joined: (name: string) => `${name} joined with this code. It can’t be used again.`,
} as const;

/** The device code's countdown, as the window's sheet says it. */
export function remainingLine(expires: number, now: number): string {
  const seconds = Math.max(0, Math.floor((expires - now) / 1000));
  if (seconds === 0) return "This code has run out. Close this and pair again.";
  return `Good for ${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

/** What to do with a Mac's, a browser's or a terminal's code, and for how long it works. */
export function codeInstructions(purpose: "mac" | "browser", expires: number, now: number,
  web: WebRemoteStatus | undefined, publicWeb: string | undefined): string {
  const left = Math.max(0, Math.floor((expires - now) / 1000));
  const time = left === 0 ? "It has run out; close this and ask again."
    : `It works once, for ${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}.`;
  if (purpose === "mac") {
    return `On the other Mac, open Agent Loops, choose Connect to a control plane and paste this. That Mac can then do all this page can. ${time}`;
  }
  return `${browserWhere(web, publicWeb)} That browser can then do all this page can. ${time}`;
}

/** Where to paste a browser's code (#1459): the page on the control plane's own domain, and the page on its Mac. */
function browserWhere(web: WebRemoteStatus | undefined, publicWeb: string | undefined): string {
  const anywhere = publicWeb ? `In any browser, open ${publicWeb} and paste it in.` : null;
  if (!web || (!web.served && web.reason === "off")) {
    return anywhere ?? "Turn on Serve Agent Loops to browsers on this Mac in Settings ▸ Control plane first.";
  }
  // Without its scheme, as the Overview says it (test/lint.mjs).
  const address = `localhost:${web.port}`;
  const here = web.served ? `On this Mac, open ${address} and paste it in.`
    : `${serveLine(web)} Press Try Again in Settings ▸ Control plane ▸ Overview, then open ${address} on this Mac and paste it in.`;
  return anywhere ? `${anywhere} ${here}` : here;
}

/** The QR code's dark modules, one rect each as `x,y`, with a border of one, at correction M as the window's. */
export function qrModules(text: string): { size: number; dark: [number, number][] } {
  const qr = encode(text, { ecc: "M", border: 1 });
  const dark: [number, number][] = [];
  qr.data.forEach((row, y) => { row.forEach((on, x) => { if (on) dark.push([x, y]); }); });
  return { size: qr.size, dark };
}

/** A terminal's pairing (#6875): the code as loops auth login takes it, quoted for a shell. */
export function terminalLogin(code: string): string {
  return `loops auth login '${code}'`;
}

/** Settings ▸ Control plane ▸ Clients ▸ Command line (#6875): how to put loops on the PATH, as the docs say it. The
 * web page cannot write to a computer's disk, so each line is to copy. On a Mac, Agent Loops' set-up links it. */
export const commandLine = {
  heading: "Command line",
  hint: "loops starts and continues sessions from a terminal. Put it on your PATH, then pair it with a Mac's code from Pair a Client…. On a Mac with Agent Loops it needs no code: run any loops command, and approve it in Needs you on that Mac.",
  releases: "Each release has loops for darwin and linux, arm64 and amd64, on the releases page of agentloops-dev/agents-releases.",
  lines: [
    { label: "On a Mac with Agent Loops (set-up does this for you)", line: "mkdir -p ~/.local/bin && ln -sf \"/Applications/Agent Loops.app/Contents/Helpers/loops\" ~/.local/bin/loops" },
    { label: "On Linux or a Mac, from a release download", line: "tar -xzf loops-<os>-<arch>-<version>.tar.gz && install -m 755 loops ~/.local/bin/loops" },
  ],
} as const;
