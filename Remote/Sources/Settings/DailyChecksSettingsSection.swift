import AgentsKitCore
import SwiftUI

/// Settings ▸ Agent Configuration ▸ Workflows on the Remote (#3355, #5853): the daily checks' one
/// control, kept on the Mac's host, the same on every client. It was the daily summary's (#3351).
struct DailyChecksSettingsSection: View {
    @Environment(RemoteModel.self) private var model
    @State private var problem: String?

    var body: some View {
        Section {
            if let settings = model.dailyChecksSettings {
                DailyChecksControls(settings: settings) { changed in
                    Task { problem = await model.setDailyChecksSettings(changed) }
                }
            } else {
                Text("The Mac's host does not say. It may be too old for the daily checks.")
                    .foregroundStyle(Paper.secondary)
            }
        } header: {
            Text("Daily checks")
        } footer: {
            VStack(alignment: .leading, spacing: 4) {
                // The choice's note (#4737), on a line that is kept.
                FieldNote(text: problem)
                Text(DailyChecksControls.footer)
            }
            .appText(.fine)
            .foregroundStyle(Paper.secondary)
        }
        .paperListRow()
        .task { await model.refreshDailyChecksSettings() }
    }
}
