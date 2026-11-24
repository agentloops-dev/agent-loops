// How a diff is drawn, Unified or Side by Side, on the Review page, Files changed and a view's diff:
// one choice, the same on every client (#6806), kept with the other display choices.
import { preferenceKeys, setPreferences, stringPreference } from "./preferences";

export type DiffLayout = "unified" | "sideBySide";

export const diffLayout = (): DiffLayout => stringPreference(preferenceKeys.reviewDiffLayout) === "sideBySide" ? "sideBySide" : "unified";

export const chooseDiffLayout = (layout: DiffLayout): void => setPreferences({ [preferenceKeys.reviewDiffLayout]: layout });
