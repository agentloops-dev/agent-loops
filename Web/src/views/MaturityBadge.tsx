// A feature's maturity badge (#6052): Pilot, Alpha or Beta in a chip, with its help line; nothing at GA. It goes where
// a person goes into the feature (its sidebar row, menu item, Settings section title, page or sheet title), as the
// window's MaturityBadge (Shared/UI/EarlyFeaturesSection.swift).
import { maturityOf, maturityWords, type Maturity } from "../model/maturity";

export function MaturityBadge({ capability, level }: { capability?: string; level?: Maturity }) {
  const l = level ?? (capability ? maturityOf(capability) : "ga");
  if (l === "ga") return null;
  const { badge, help } = maturityWords[l];
  return <span class="chip maturity-badge" title={help}>{badge}</span>;
}
