import AgentsKitCore
import SwiftUI

/// Settings ▸ Machines ▸ This Mac ▸ Updates (#2178): whether Agents Host updates itself to
/// the release the control plane names, and where that is.
///
/// What the host holds, not what was last clicked. Nil from a host that cannot update
/// itself (one older than the method), and nothing is drawn.
struct SelfUpdateSection: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if let update = model.selfUpdate {
            Section {
                Toggle(SelfUpdateWords.automatic, isOn: Binding(
                    get: { model.selfUpdate?.autoUpdate ?? false },
                    set: { on in Task { await model.setAutoUpdate(on) } }))
                .disabled(update.state == .cannot)
                LabeledContent("Version", value: update.running)
                Text(SelfUpdateWords.line(update))
                    .appText(.fine)
                    .foregroundStyle((SelfUpdateWords.failed(update) ? StateTint.failure : .none).style(or: .secondary))
            } header: {
                Text("Updates")
            } footer: {
                Text("Agent Loops updates to the release your control plane runs. It never asks for an administrator password, and it restarts only when no agent is in a turn.")
                    .appText(.fine)
                    .foregroundStyle(.secondary)
            }
            .paperListRow()
        }
    }
}
