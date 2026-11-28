// Features show their maturity (#6052, epic #6552): the level of each capability that is not GA, from
// Packages/Wire/maturity.json (protocol/maturity.generated.ts), as the window's and the Remote's (Maturity.swift).
// A capability the list does not name is GA. Each one listed has a badge, and a switch in Settings ▸ This app ▸
// Features, on by default and kept with the feature flags (./featureFlags.ts, #6254), so it is the same on
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

/** What a part says of its levels (#6579): the `maturity` of control/status or of a host's row in machines/list.
 *  Undefined from a part older than that. */
export type SaidMaturity = Readonly<Record<string, string>> | undefined;

const known: readonly Maturity[] = ["pilot", "alpha", "beta", "ga"];

/** The level of `capability` as a part said it: a part that says none is older than #6579, so the page's own list
 *  stands in; a level the page does not know, too. A capability the part does not name is GA there. */
export function saidMaturity(capability: string, said: SaidMaturity, list: readonly EarlyFeatureInfo[] = earlyFeatures): Maturity {
  if (!said) return maturityOf(capability, list);
  const raw = said[capability];
  if (raw === undefined) return "ga";
  return (known as readonly string[]).includes(raw) ? raw as Maturity : maturityOf(capability, list);
}

/** The level of `capability` where no one part runs it, such as Settings: the lowest that the parts say (the control
 *  plane's and each host's), so a person is told the least ready one. With no part, the page's own list. */
export function lowestMaturity(capability: string, parts: readonly SaidMaturity[],
  list: readonly EarlyFeatureInfo[] = earlyFeatures): Maturity {
  if (parts.length === 0) return maturityOf(capability, list);
  return parts.map((p) => saidMaturity(capability, p, list)).reduce((a, b) => known.indexOf(a) <= known.indexOf(b) ? a : b);
}

/** What each part says of its levels, for Features: the control plane's and each host's. None before the control
 *  plane's status arrives, and the page reads its own list. */
export function partsMaturity(plane: { maturity?: SaidMaturity } | null, hosts: readonly { maturity?: SaidMaturity }[]): SaidMaturity[] {
  return plane ? [plane.maturity, ...hosts.map((h) => h.maturity)] : [];
}

/** The rows of Features on the page: each capability that is not GA and that the web page shows, at the lowest
 *  level the parts say (#6579). One that every part says is GA leaves the list; one the page's list does not name has
 *  no title here, so it has no row. */
export function earlyRows(list: readonly EarlyFeatureInfo[] = earlyFeatures, parts: readonly SaidMaturity[] = []): readonly EarlyFeatureInfo[] {
  return list.filter((f) => f.clients?.includes("web") ?? true).flatMap((f) => {
    const level = lowestMaturity(f.capability, parts, list);
    return level === "ga" ? [] : [{ ...f, level }];
  });
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
