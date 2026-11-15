// Labels on a machine (#5284), as MachineLabels in Swift (Model/MachineLabels.swift): a key, or key=value.
// A provider sets os, arch and provider; a person adds the rest on a machine's page.
import type { ControlMachine } from "../protocol/generated";

export const providerKeys: readonly string[] = ["os", "arch", "provider"];

export const machineLabelsHint = "A key or key=value, like gpu or team=a. Workflows can run on machines with a label.";

/** What a person typed as a list of labels: split on commas and spaces, without repeats. */
export function parseMachineLabels(text: string): string[] {
  const seen = new Set<string>();
  return text.split(/[,\s]+/u).filter((label) => label !== "" && !seen.has(label.toLowerCase()) && !!seen.add(label.toLowerCase()));
}

/** Why a label cannot be used, in words; null when it can. */
export function machineLabelProblem(label: string): string | null {
  const key = label.split("=")[0] ?? label;
  if (providerKeys.includes(key.toLowerCase())) return `${key} is set by the machine’s provider. Pick another key.`;
  if (!/^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}(=[A-Za-z0-9._/:@+-]{1,63})?$/u.test(label)) {
    return `“${label}” is not a label. Use a key or key=value of letters, digits and . _ / -.`;
  }
  return null;
}

/** The labels a person set: all of a machine's but the provider's. */
export function ownMachineLabels(machine: Pick<ControlMachine, "labels" | "autoLabels">): string[] {
  const auto = new Set((machine.autoLabels ?? []).map((label) => label.toLowerCase()));
  return (machine.labels ?? []).filter((label) => !auto.has(label.toLowerCase()));
}

/** Every label on these machines, once each and sorted, for Runs on. */
export function allMachineLabels(machines: readonly Pick<ControlMachine, "labels">[]): string[] {
  const seen = new Map<string, string>();
  for (const label of machines.flatMap((machine) => machine.labels ?? [])) if (!seen.has(label.toLowerCase())) seen.set(label.toLowerCase(), label);
  return [...seen.values()].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }));
}
