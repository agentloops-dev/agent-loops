import AgentsKitCore
import SwiftUI

/// The daily checks' one control (#3355, #5853), for all folders: Off or On, and how often they
/// run. The Mac draws it above Settings ▸ Agent Configuration ▸ Workflows, the Remote in a section
/// of its Settings; the web page has the same. The home folder's Daily checks workflow keeps it.
/// It was the daily summary's control (#3351, #3395).
struct DailyChecksControls: View {
    let settings: DailyChecksSettings
    let set: (DailyChecksSettings) -> Void

    /// What the checks do, under the control.
    static let footer = "Every few hours, an agent checks the folders that had no work: "
        + "failing CI, pull requests that went stale or are in conflict, and issues no session has. "
        + "For Review lists what it finds. It only reads."

    var body: some View {
        Picker("Daily checks", selection: Binding(
            get: { settings.enabled },
            set: { enabled in set(DailyChecksSettings(enabled: enabled, every: settings.every)) })) {
            ForEach([false, true], id: \.self) { enabled in
                Text(DailyChecksSettings.stateLabel(enabled)).tag(enabled)
            }
        }
        if settings.enabled {
            Picker("How often", selection: Binding(
                get: { settings.every },
                set: { every in set(DailyChecksSettings(enabled: settings.enabled, every: every)) })) {
                ForEach(DailyChecksSettings.checksEveryChoices, id: \.self) { every in
                    Text(DailyChecksSettings.everyLabel(every)).tag(every)
                }
            }
        }
    }
}
