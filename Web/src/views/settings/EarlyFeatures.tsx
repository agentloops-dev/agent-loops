// Settings ▸ This app ▸ Early Features (#6052, epic #6552): a row for each feature that is not GA and that the web
// page shows, with its name, its badge, its help line and a switch, on by default. A feature turned off shows nowhere
// on the person's clients. Listed in ./sections.ts. The window and the Remote have the same section (Core).
import { earlyRows, maturityWords, partsMaturity, setEarlyFeature, showsFeature } from "../../model/maturity";
import type { Store } from "../../model/store";
import { MaturityBadge } from "../MaturityBadge";

/** The words under the section, as the window's (EarlyFeaturesSection.footer). */
export const earlyFeaturesFooter = "Features that are not ready yet. Turn one off, and it shows nowhere on your clients. "
  + "The same on every client of yours.";

/** What the section says when every feature is GA, as the window's. */
export const noEarlyFeatures = "Every feature is ready.";

/** At the lowest level that the control plane and the hosts say (#6579), so a page older than its host shows the
 *  host's level. */
export function EarlyFeaturesSection({ store }: { store: Store }) {
  const rows = earlyRows(undefined, partsMaturity(store.plane.value, store.hosts.value));
  return (
    <section aria-labelledby="settings-early-features">
      <h3 class="section-head" id="settings-early-features">Early Features</h3>
      {rows.length === 0 && <p class="hint">{noEarlyFeatures}</p>}
      {rows.map((feature) => {
        const id = `early-feature-${feature.capability.replaceAll(/[^a-z0-9-]/g, "-")}`;
        return (
          <label class="setting" key={feature.capability}>
            <span class="setting-words">
              <span>{feature.title} <MaturityBadge level={feature.level} /></span>
              <span class="hint" id={id}>{maturityWords[feature.level].help}</span>
            </span>
            {/* Named by its title alone, with the help line as its description. */}
            <input type="checkbox" role="switch" aria-label={feature.title} aria-describedby={id}
              checked={showsFeature(feature.capability)} onChange={(e) => setEarlyFeature(feature.capability, e.currentTarget.checked)} />
          </label>
        );
      })}
      <p class="hint">{earlyFeaturesFooter}</p>
    </section>
  );
}
