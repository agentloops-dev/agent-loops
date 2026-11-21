// Settings ▸ This App ▸ Feature Flags (#6254): a switch for each feature in work, the person's and the
// same on every client of theirs. Listed in ./sections.ts. The window has the same section.
import { flagOn, setFlag } from "../../model/featureFlags";
import { featureFlags } from "../../protocol/flags.generated";

/** The words under the section, as the window's (FeatureFlagsSettingsSection.footer). */
export const featureFlagsFooter = "Try a feature that is not ready yet. A feature can change or go away while it is in work. "
  + "The same on every client of yours.";

/** What the section says when no feature is in work, as the window's. */
export const noFeatureFlags = "No feature is in work now.";

export function FeatureFlagsSection() {
  return (
    <section aria-labelledby="settings-feature-flags">
      <h3 class="section-head" id="settings-feature-flags">Feature Flags</h3>
      {featureFlags.length === 0 && <p class="hint">{noFeatureFlags}</p>}
      {featureFlags.map((flag) => (
        <label class="setting" key={flag.name}>
          <span class="setting-words">
            <span>{flag.title}</span>
            <span class="hint" id={`feature-flag-${flag.name}`}>{flag.about}</span>
          </span>
          {/* Named by its title alone, with the line as its description. */}
          <input type="checkbox" role="switch" aria-label={flag.title} aria-describedby={`feature-flag-${flag.name}`}
            checked={flagOn(flag.name)} onChange={(e) => setFlag(flag.name, e.currentTarget.checked)} />
        </label>
      ))}
      <p class="hint">{featureFlagsFooter}</p>
    </section>
  );
}
