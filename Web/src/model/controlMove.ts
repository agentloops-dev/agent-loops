// Make This the Control Plane… (#6614, epic #6608): the steps and words of a move, as the window's
// ControlMoveWords (Packages/AgentsKit/.../Model/ControlMove.swift), and the moves this page has heard:
// control/moves/list as it connects, each step from control/moveChanged. For an admin with the flag
// control-plane-move; the control plane answers admins only.
import { signal } from "@preact/signals";
import type { ControlEligible, ControlMove, ControlMovePhase } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";

export const flagName = "control-plane-move";

export const controlMoveWords = {
  make: "Make This the Control Plane…",
  confirm: "Make Control Plane",
  tryAgain: "Try Again…",
  checking: "Checking this machine…",
  section: "Control plane",
  anotherRuns: "Another move of the control plane is running. Wait for it to end.",
  unreachable: "The control plane can’t be reached.",
} as const;

/** How long a move that ended stays on the strip and the page, in ms. */
export const shownFor = 24 * 60 * 60 * 1000;

/** The steps, in the order the move runs them, with the wireframe's words. "Seal key" is part of Receive. */
export const steps: readonly { phase: ControlMovePhase; title: string }[] = [
  { phase: "checking", title: "Check" },
  { phase: "receiving", title: "Receive" },
  { phase: "frozen", title: "Freeze" },
  { phase: "copying", title: "Copy store" },
  { phase: "forwarding", title: "Forward" },
];

export type StepState = "todo" | "current" | "done" | "failed";

/** Each step's state for this move. */
export function stepStates(move: ControlMove): StepState[] {
  const index = (phase?: ControlMovePhase) => Math.max(0, steps.findIndex((s) => s.phase === phase));
  if (move.phase === "done" || move.phase === "forwarding") return steps.map(() => "done");
  if (move.phase === "failed") {
    const at = index(move.failedPhase);
    return steps.map((_, i) => i < at ? "done" : i === at ? "failed" : "todo");
  }
  const at = index(move.phase);
  return steps.map((_, i) => i < at ? "done" : i === at ? "current" : "todo");
}

/** The move is under way. */
export function running(move: ControlMove): boolean {
  return move.phase !== "done" && move.phase !== "forwarding" && move.phase !== "failed";
}

/** Under way, or ended in the last day: the strip and the page say it. */
export function shown(move: ControlMove, now = Date.now()): boolean {
  return running(move) || now - fromWireDate(move.updatedAt).getTime() < shownFor;
}

function doing(phase: ControlMovePhase): string {
  switch (phase) {
    case "checking": return "checking the machine";
    case "receiving": return "sealing the control key to it and starting its copy";
    case "frozen": return "holding every change";
    case "copying": return "copying the store";
    default: return "finishing";
  }
}

const stepTitle = (phase?: ControlMovePhase) => steps.find((s) => s.phase === phase)?.title ?? "Check";
const trimmed = (words: string) => words.trim().replace(/\.+$/, "");

/** The strip's sentence. */
export function moveLine(move: ControlMove): string {
  if (move.phase === "failed") {
    const why = move.error ? `: ${trimmed(move.error)}` : "";
    return `The move to ${move.toName} stopped at “${stepTitle(move.failedPhase)}”${why}. The control plane still serves where it was.`;
  }
  if (move.phase === "done" || move.phase === "forwarding") return `${move.toName} is the control plane.`;
  return `Moving the control plane to ${move.toName}: ${doing(move.phase)}.`;
}

/** Under a move that ended: until when the old address forwards, when it does. */
export function forwardingLine(move: ControlMove, locale?: string): string | null {
  if (move.phase !== "forwarding" || move.forwardingUntil === undefined) return null;
  const until = fromWireDate(move.forwardingUntil).toLocaleDateString(locale, { day: "numeric", month: "short", year: "numeric" });
  return `The old address forwards to it until ${until}.`;
}

/** "2 GB", in steps of 1,024, as the window's memory count. */
export function bytesWords(bytes: number): string {
  const units = ["bytes", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
  const shown = value >= 10 || unit === 0 ? Math.round(value).toString() : value.toFixed(1).replace(/\.0$/, "");
  return `${shown} ${units[unit]}`;
}

/** "It meets the checks: reachable at ams3-1.example.dev, always on, 2 GB memory, 40 GB free disk." */
export function meetsLine(eligible: ControlEligible): string {
  const parts: string[] = [];
  if (eligible.address) parts.push(`reachable at ${eligible.address}`);
  parts.push("always on");
  if (eligible.memoryBytes !== undefined) parts.push(`${bytesWords(eligible.memoryBytes)} memory`);
  if (eligible.diskFreeBytes !== undefined) parts.push(`${bytesWords(eligible.diskFreeBytes)} free disk`);
  return `It meets the checks: ${parts.join(", ")}.`;
}

export const question = (name: string) => `Make ${name} the control plane?`;

/** What the confirmation says happens. `now` is where clients reach the control plane today. */
export function happens(name: string, address: string | undefined, now: string | undefined): string[] {
  const lines = [
    `The store and the control key move to ${name}. The key is sealed to that machine.`,
    "Every window, phone, browser and machine reconnects by itself. Nobody pairs again.",
  ];
  if (address && address !== now) {
    lines.push(`The address changes to ${address}. The old address forwards to it for up to 30 days: change your DNS before then.`);
  } else if (address) {
    lines.push(`The address stays ${address}.`);
  }
  lines.push("Agents keep working. For about a minute, screens can’t reach machines.");
  return lines;
}

// The moves this page has heard, the newest first.
export const moves = signal<ControlMove[]>([]);
/** When the page last heard how the moves stand (#5139). */
export const movesHeardAt = signal<number | null>(null);
/** The moves the person closed on the strip. */
export const closedMoves = signal<ReadonlySet<string>>(new Set());

/** control/moves/list's answer. */
export function takeMoves(list: ControlMove[], at = Date.now()): void {
  moves.value = list;
  movesHeardAt.value = at;
}

/** control/moveChanged, or a move just started. */
export function takeMove(move: ControlMove, at = Date.now()): void {
  moves.value = [move, ...moves.value.filter((m) => m.id !== move.id)]
    .sort((a, b) => b.startedAt - a.startedAt);
  movesHeardAt.value = at;
}

/** The move the strip says: the newest, while it runs or for a day after, unless closed. */
export function stripMove(now = Date.now()): ControlMove | null {
  const move = moves.value[0];
  return move && shown(move, now) && !closedMoves.value.has(move.id) ? move : null;
}

/** The newest move to that machine, while it runs or for a day after. */
export function moveTo(machine: string, now = Date.now()): ControlMove | null {
  const move = moves.value.find((m) => m.to === machine);
  return move && shown(move, now) ? move : null;
}

export const runningMove = (): ControlMove | null => moves.value.find(running) ?? null;

export function closeMove(move: ControlMove): void {
  closedMoves.value = new Set([...closedMoves.value, move.id]);
}
