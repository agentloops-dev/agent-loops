// Project Settings ▸ Autopilot (#5904, epic #5897), as the window's ProjectAutopilotPane in
// App/Sources/Folders/ProjectSettingsSheet.swift: the person turns Autopilot off, on, or on for
// high priority only, with autopilot/set, and reads what it did with autopilot/status (#5898).
import type { AutopilotMode, AutopilotStatus } from "../protocol/generated";
import { lastUpdateAge } from "./lastUpdate";

/** The three choices, in the window's order. */
export const autopilotModes: readonly { mode: AutopilotMode; title: string }[] = [
  { mode: "off", title: "Off" },
  { mode: "high", title: "Only high priority" },
  { mode: "on", title: "On: start a lead for each epic, and a lane for each ready issue" },
];

export const autopilotWords = {
  title: "Autopilot",
  /** Turning it on, or to high priority only, asks once. */
  askTitle: "Turn on Autopilot?",
  askDetail: "Autopilot starts paid agent work with no press from you, within these limits.",
  changeLimits: "Change limits",
  seeWhatWaits: "See what waits",
  never: "Autopilot has not run in this folder yet.",
  off: "Autopilot is off: it starts nothing.",
} as const;

/** Whether choosing `to` from `from` asks first: only a step from off to something that starts work. */
export function needsConfirm(from: AutopilotMode, to: AutopilotMode): boolean {
  return from === "off" && to !== "off";
}

/** WireDate (seconds since 2001) as ms since 1970. */
export function wireMillis(date: number): number {
  return (date + 978_307_200) * 1000;
}

/** The line under the choices: the folder's helper limits, as Work at Once says them (the wire has no open-PR limit). */
export function limitsLine(helpers: string): string {
  return `Within this folder's limits: ${helpers}`;
}

/** "Last run 2 min ago · 1 started, 4 waiting"; with nothing started or waiting, only when it ran. */
export function lastRunLine(status: AutopilotStatus, now: number = Date.now()): string {
  if (status.lastRunAt === undefined) return autopilotWords.never;
  const started = status.rows.filter((r) => r.state === "started").length;
  const waiting = status.rows.filter((r) => r.state === "waiting").length;
  const paused = status.rows.filter((r) => r.state === "paused").length;
  const parts = [`${started} started`, `${waiting} waiting`, ...paused > 0 ? [`${paused} paused`] : []];
  return `Last run ${lastUpdateAge(wireMillis(status.lastRunAt), now)} · ${parts.join(", ")}`;
}
