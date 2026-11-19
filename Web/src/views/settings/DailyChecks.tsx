// Settings ▸ Agent Configuration ▸ Workflows' daily checks (#3355, #5853): Off or On, and how
// often they run, for all folders, as the Mac's and the Remote's. The home folder's Daily checks
// workflow keeps it on the Mac's host. It was the daily summary's control (#3351, #3395).
import { useEffect } from "preact/hooks";
import type { Store } from "../../model/store";
import { checksEveryChoices, checksEveryLabel, checksHint, dailyChecksStates } from "../../model/dailyChecksWords";

export function DailyChecksControl({ store }: { store: Store }) {
  useEffect(() => store.loadDailyChecks(), [store]);
  const settings = store.dailyChecks.value;
  if (!settings) return null;
  return (
    <div class="daily-checks" role="group" aria-label="Daily checks">
      <label class="setting">
        <span>Daily checks</span>
        <select value={String(settings.enabled)}
          onChange={(e) => void store.setDailyChecks({ ...settings, enabled: e.currentTarget.value === "true" })}>
          {dailyChecksStates.map((s) => <option key={s.label} value={String(s.enabled)}>{s.label}</option>)}
        </select>
      </label>
      {settings.enabled && (
        <label class="setting">
          <span>How often</span>
          <select value={String(settings.every)}
            onChange={(e) => void store.setDailyChecks({ ...settings, every: Number(e.currentTarget.value) })}>
            {checksEveryChoices.map((h) => <option key={h} value={String(h)}>{checksEveryLabel(h)}</option>)}
          </select>
        </label>
      )}
      <p class="hint">{checksHint}</p>
    </div>
  );
}
