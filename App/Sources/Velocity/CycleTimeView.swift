import AgentsKitCore
import SwiftUI

struct CycleTimeView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            CycleTimeChartsSection(load: { await model.cycleTimeHistory() }, refresh: model.work.multiplier)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(24)
        }
        .navigationTitle("Cycle time")
    }
}

struct CycleTimeRow: View {
    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text("Cycle time").foregroundStyle(.primary)
            Spacer()
        }
        .help("How long the person's work took to land, over time. Opens Cycle time.")
    }
}
