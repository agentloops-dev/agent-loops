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
