// A list of machine names stops at three: "a, b, c and 97 more" (#5289, epic #5281). The "and N more" opens the Machines
// page with that set as its filter (`#/machines/machines?hosts=id,id`). The Swift clients say the same words (MachineNames).
import type { Route } from "../route";

export const nameLimit = 3;

export interface NamedMachine { id: string; name: string }

/** "a, b, c" for up to three names (as the lists said before), then "a, b, c and 97 more": the rest counted. */
export function namesWords(names: readonly string[]): string {
  if (names.length <= nameLimit) return names.join(", ");
  return `${names.slice(0, nameLimit).join(", ")} and ${names.length - nameLimit} more`;
}

/** "a, b and c" for up to three names, then "a, b, c and 97 more", for a line that says "and" before the last (#5620). */
export function namesAnd(names: readonly string[]): string {
  if (names.length > nameLimit) return namesWords(names);
  if (names.length < 2) return names[0] ?? "";
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** The items a per-machine section shows (#5620): all of them when `all` or when there are at most three, else the
 * first three (the order of the list says which matter most) and the one that is open, so opening one never hides it. */
export function firstOf<T extends { id: string }>(items: readonly T[], all: boolean, open?: string | null): T[] {
  if (all || items.length <= nameLimit) return [...items];
  const shown = items.slice(0, nameLimit);
  const opened = open ? items.find((i) => i.id === open) : undefined;
  return opened && !shown.includes(opened) ? [...shown, opened] : shown;
}

/** The names left out, and where "and N more" goes: the Machines page filtered to these machines. */
export function moreOf(machines: readonly NamedMachine[]): { count: number; to: Route } | null {
  if (machines.length <= nameLimit) return null;
  return { count: machines.length - nameLimit, to: filteredTo(machines.map((m) => m.id)) };
}

/** The Machines page, showing only these machines. */
export function filteredTo(ids: readonly string[]): Route {
  return { nav: "machines", section: "machines", query: `hosts=${ids.map(encodeURIComponent).join(",")}` };
}

/** The `hosts` of a Machines page address: the machines it is narrowed to, none when it is not. */
export function hostsOf(query: string | undefined): string[] {
  if (!query) return [];
  const value = new URLSearchParams(query).get("hosts");
  return value ? value.split(",").filter(Boolean) : [];
}
