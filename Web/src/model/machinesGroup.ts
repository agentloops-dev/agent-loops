// The sidebar's Machines group (#5286, epic #5281), as `MachinesGroup` in AgentsKitCore: at most seven rows, in this order:
// This Mac, the pinned machines, the machines with something going on (it needs something, or sessions work or wait there),
// the machines being made, then the rest. With more than seven, one row All Machines (N) comes after them and opens the
// Machines page. A person with seven or fewer sees every one, and no such row. Always seven, also for 2,000 machines.
import { signal } from "@preact/signals";
import { keepPins, pinnedMachines, togglePin } from "./machinePicker";

export const machinesGroupLimit = 7;

export interface GroupMachine {
  id: string;
  pinned: boolean;
  /** It needs something, it is starting, or sessions work or wait for the person there. */
  active: boolean;
}

export interface GroupRows {
  /** The machines' ids, in the order drawn. */
  machines: string[];
  /** How many of the machines being made get a row, after the machines'. */
  making: number;
  /** Every machine, with the ones being made: the N of All Machines (N). */
  total: number;
  /** All Machines (N) is drawn. */
  bounded: boolean;
}

/** The group's rows. `machines` in the order the page lists them (This Mac, then by name). */
export function machinesGroupRows(machines: readonly GroupMachine[], making: number, macID: string): GroupRows {
  const total = machines.length + making;
  if (total <= machinesGroupLimit) return { machines: machines.map((m) => m.id), making, total, bounded: false };
  const rank = (m: GroupMachine) => (m.id === macID ? 0 : m.pinned ? 1 : m.active ? 2 : 4);
  const ranked = machines.map((m, i) => ({ m, i })).sort((a, b) => rank(a.m) - rank(b.m) || a.i - b.i).map((x) => x.m);
  const first = ranked.filter((m) => rank(m) < 4).slice(0, machinesGroupLimit);
  const madeRows = Math.min(making, machinesGroupLimit - first.length);
  const rest = ranked.filter((m) => rank(m) === 4).slice(0, machinesGroupLimit - first.length - madeRows);
  const shown = [...first, ...rest].map((m) => m.id);
  return { machines: shown, making: madeRows, total, bounded: shown.length + madeRows < total };
}

export const machinesGroupWords = {
  all: (total: number) => `All Machines (${total})`,
  pin: (pinned: boolean) => (pinned ? "Unpin from Sidebar" : "Pin to Sidebar"),
};

/** The machines pinned on this browser, shared with the machine picker (#5287); a pin from a row's menu redraws the group. */
export const sidebarPins = signal<string[]>(pinnedMachines(globalThis.localStorage));

/** Whether the pin was taken: false when there is no room for it. */
export function toggleSidebarPin(id: string): boolean {
  const next = togglePin(globalThis.localStorage, id);
  if (next) sidebarPins.value = next;
  return !!next;
}

/** The control plane listed its machines, or one left: the pin of a machine that is gone goes (#6797). An offline one is
 * listed, so its pin stays. */
export function pruneSidebarPins(there: (id: string) => boolean): void {
  const next = keepPins(globalThis.window?.localStorage, there);
  if (next.join() !== sidebarPins.peek().join()) sidebarPins.value = next;
}
