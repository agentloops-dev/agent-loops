// The person's feature flags (#6254, epic-lifecycle stage 6, #6253). The flags in work are
// Packages/Wire/feature-flags.json's (protocol/flags.generated.ts), as the window's and the
// Remote's. Each is one of the person's display choices, `flag:<name>`, a bool, kept on their
// Mac's host with the others (./preferences.ts), so a flag is the same on every client of theirs.
// The store takes any name: a capability's toggle (#6052) is kept the same way, with its own default.
import { boolPreference, setPreferences } from "./preferences";

/** The display choice a flag or a toggle of that name is kept under, as the window's FeatureFlags.key: each dot
 *  of a capability's name a colon, because the window's @AppStorage hears no change to a key with a dot. */
export function flagKey(name: string): string {
  return `flag:${name.replaceAll(".", ":")}`;
}

/** Whether the person has `name` on; `fallback` when they have not chosen. Reads a signal, so a view that
 *  calls it draws again when the Mac says the flag changed. */
export function flagOn(name: string, fallback = false): boolean {
  return boolPreference(flagKey(name)) ?? fallback;
}

/** The person turns `name` on or off: drawn at once, and kept on their Mac. A choice that is the default
 *  takes the key away, so the Mac keeps only what the person changed. */
export function setFlag(name: string, on: boolean, fallback = false): void {
  setPreferences({ [flagKey(name)]: on === fallback ? null : on });
}
