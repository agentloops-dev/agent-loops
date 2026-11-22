// Features show their maturity (#6052, epic #6552): the level of each capability that is not GA, from
// Packages/Wire/maturity.json (protocol/maturity.generated.ts), as the window's and the Remote's (Maturity.swift).
// A capability the list does not name is GA. Each one listed has a badge, and a switch in Settings ▸ This app ▸
// Early Features, on by default and kept with the feature flags (./featureFlags.ts, #6254), so it is the same on
// every client of the person's.
import { flagOn, setFlag } from "./featureFlags";
import { earlyFeatures, type EarlyFeatureInfo, type EarlyLevel } from "../protocol/maturity.generated";

export type Maturity = EarlyLevel | "ga";

/** A switch is on until the person turns it off. */
export const earlyToggleDefault = true;

/** The badge's word and its help line, as the window's; none at GA. */
export const maturityWords: Readonly<Record<EarlyLevel, { badge: string; help: string }>> = {
  pilot: { badge: "Pilot", help: "Pilot: this feature is a trial with a few people. It can change or go away." },
  alpha: { badge: "Alpha", help: "Alpha: this feature works, but parts are missing and it can change much." },
  beta: { badge: "Beta", help: "Beta: this feature is complete, and we still look for faults." },
};

/** The level of `capability`, from the list this page was built with; GA when it is not listed. */
export function maturityOf(capability: string, list: readonly EarlyFeatureInfo[] = earlyFeatures): Maturity {
  return list.find((f) => f.capability === capability)?.level ?? "ga";
}

/** The rows of Early Features on the page: each capability that is not GA and that the web page shows. */
export function earlyRows(list: readonly EarlyFeatureInfo[] = earlyFeatures): readonly EarlyFeatureInfo[] {
  return list.filter((f) => f.clients?.includes("web") ?? true);
}

/** Whether the page shows the feature of `capability`: a GA one always; one that is not GA when the web page is one
 *  of its clients and the person has not turned it off. A feature this says no to shows nowhere, as if the host did
 *  not have it. Reads a signal, so a view that calls it draws again when the Mac says the switch changed. */
export function showsFeature(capability: string, list: readonly EarlyFeatureInfo[] = earlyFeatures): boolean {
  const feature = list.find((f) => f.capability === capability);
  if (!feature) return true;
  if (!(feature.clients?.includes("web") ?? true)) return false;
  return flagOn(capability, earlyToggleDefault);
}

/** The person turns a feature on or off: only an off is kept. */
export function setEarlyFeature(capability: string, on: boolean): void {
  setFlag(capability, on, earlyToggleDefault);
}
