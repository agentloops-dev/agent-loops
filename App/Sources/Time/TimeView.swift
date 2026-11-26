import AgentsKitCore
import SwiftUI

/// Where the time of every host's running chats went (#852, #1267): the agent working, its tools,
/// waiting on others, and waiting for you, drawn as the usage charts on Cost are.
///
/// The Cost report's Time tab (#6884). Read-only.
struct TimeView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            TimeChartsSection(load: { await model.timeHistory() })
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(24)
        }
    }
}
