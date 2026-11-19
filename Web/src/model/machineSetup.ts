// A machine's set-up in words (#5354, epic #5353), as the window's MachineSetupWords: the header chip, the sidebar's
// grey line and each step's line, from the control plane's record of the eight steps (`ControlMachine.setup`).
import type { MachineSetup, MachineSetupState, MachineSetupStep } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";

/** The steps' names, as the page lists them, by the wire's id. */
export const setupStepNames: Record<string, string> = {
  create: "Create machine", join: "Join machine", install: "Install Agents", compare: "Compare digits",
  runtimes: "Install runtimes", projects: "Send projects", signIn: "Sign in to runtimes", smoke: "Smoke session",
};

export function stepName(step: MachineSetupStep): string {
  return setupStepNames[step.id] ?? step.id;
}

/** How many steps are done. */
export function doneCount(setup: MachineSetup): number {
  return setup.steps.filter((s) => s.state === "done").length;
}

/** Every step done: the machine is ready, and New Session turns on. */
export function isReady(setup: MachineSetup): boolean {
  return setup.readyAt !== undefined || (setup.steps.length > 0 && setup.steps.every((s) => s.state === "done"));
}

/** The step that stopped the run, if one failed. */
export function failedStep(setup: MachineSetup): MachineSetupStep | undefined {
  return setup.steps.find((s) => s.state === "failed");
}

/** Whether it still sets itself up or stopped on a failure; false once it is ready. */
export function isSettingUp(setup: MachineSetup | undefined): boolean {
  return !!setup && !isReady(setup);
}

/** 7 of 8: the number of the first step not done. */
export function position(setup: MachineSetup): number {
  return Math.min(doneCount(setup) + 1, Math.max(setup.steps.length, 1));
}

/** The step the run is at: the failed one, else the one that needs the person, else the first running. */
export function currentStep(setup: MachineSetup): MachineSetupStep | undefined {
  return failedStep(setup) ?? setup.steps.find((s) => s.state === "needs-you") ?? setup.steps.find((s) => s.state === "running");
}

/** *42 s*, *3 min 42 s*, *1 h 5 min*. */
export function duration(seconds: number): string {
  const total = Math.max(0, Math.round(seconds));
  if (total < 60) return `${total} s`;
  if (total < 3600) {
    const rest = total % 60;
    return rest === 0 ? `${Math.floor(total / 60)} min` : `${Math.floor(total / 60)} min ${rest} s`;
  }
  const minutes = Math.floor((total % 3600) / 60);
  return minutes === 0 ? `${Math.floor(total / 3600)} h` : `${Math.floor(total / 3600)} h ${minutes} min`;
}

const seconds = (from: Date, to: Date) => (to.getTime() - from.getTime()) / 1000;

/** The chip in the page's header: *Setting up · 7 of 8*, *Ready · 3 min 42 s*, *Set-up failed · step 6 of 8*. */
export function setupChip(setup: MachineSetup, now = new Date()): string {
  const total = setup.steps.length;
  if (isReady(setup)) {
    const ends = setup.steps.flatMap((s) => (s.endedAt === undefined ? [] : [s.endedAt]));
    const end = setup.readyAt !== undefined ? fromWireDate(setup.readyAt)
      : ends.length ? fromWireDate(Math.max(...ends) as typeof ends[number]) : now;
    return `Ready · ${duration(seconds(fromWireDate(setup.startedAt), end))}`;
  }
  const failed = failedStep(setup);
  if (failed) return `Set-up failed · step ${setup.steps.indexOf(failed) + 1} of ${total}`;
  return `Setting up · ${position(setup)} of ${total}`;
}

/** A step's name in the sidebar's one line, where room is short. */
function shortName(step: MachineSetupStep): string {
  return step.id === "signIn" ? "Sign in" : stepName(step);
}

/** The sidebar row's grey line: *Setting up · 7 of 8 · Sign in*; *Set-up failed · Install Agents*. */
export function setupSidebarLine(setup: MachineSetup): string {
  const failed = failedStep(setup);
  if (failed) return `Set-up failed · ${stepName(failed)}`;
  const line = `Setting up · ${position(setup)} of ${setup.steps.length}`;
  const step = currentStep(setup) ?? setup.steps.find((s) => s.state === "waiting");
  return step ? `${line} · ${shortName(step)}` : line;
}

/** *14:05*, or *12 Nov, 14:05* on another day. */
function clockOrDay(date: Date, now: Date): string {
  const time = date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return date.toDateString() === now.toDateString() ? time
    : `${date.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}, ${time}`;
}

/** The one line under a step's name: what it is doing, why it failed or why it waits. A step done in a warm pool says
 * so (board 4): step 1 says since when. */
export function stepLine(step: MachineSetupStep, now = new Date()): string {
  switch (step.state) {
    case "failed": return step.error ?? step.detail ?? "Failed";
    case "waiting": return step.detail ?? "Not yet";
    case "needs-you": return step.detail ?? "Waiting for you";
    case "running": {
      const words = step.detail ?? "Working…";
      return step.slower ? `${words} · Slower than usual` : words;
    }
    case "done": {
      if (!step.fromPool) return step.detail ?? "Done";
      const at = step.endedAt ?? step.startedAt;
      if (step.id === "create" && at !== undefined) {
        const made = fromWireDate(at);
        return `In the pool since ${clockOrDay(made, now)} (${duration(seconds(made, now))} ago)`;
      }
      return step.detail ?? "In the pool";
    }
  }
}

/** The time a step took, or has taken so far; null before it began; *pool* for one a warm pool did. */
export function stepTime(step: MachineSetupStep, now = new Date()): string | null {
  if (step.fromPool && step.state === "done") return "pool";
  if (step.startedAt === undefined) return null;
  return duration(seconds(fromWireDate(step.startedAt), step.endedAt !== undefined ? fromWireDate(step.endedAt) : now));
}

/** After 24 hours ready the steps fold into one line: *Set up in 3 min 42 s*; null before that. */
export function foldedLine(setup: MachineSetup, now = new Date()): string | null {
  if (!isReady(setup) || setup.readyAt === undefined) return null;
  const ready = fromWireDate(setup.readyAt);
  if (seconds(ready, now) < 24 * 3600) return null;
  return `Set up in ${duration(seconds(fromWireDate(setup.startedAt), ready))}`;
}

/** Each state's words, for a screen reader, and its mark's tone. */
export const stepStateWords: Record<MachineSetupState, string> = {
  done: "Done", running: "Running", "needs-you": "Needs you", failed: "Failed", waiting: "Not yet",
};
