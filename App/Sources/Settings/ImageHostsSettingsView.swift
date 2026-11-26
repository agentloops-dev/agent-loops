import AgentsKitCore
import SwiftUI

/// Settings ▸ Machines ▸ Image Hosts (#7139, epic #7136): the shared pane, with the control plane's
/// machines that keep the list and the window's machine menu.
struct ImageHostsSettingsView: View {
    @Environment(AppModel.self) private var model

    private var machines: [ImageHostsMachine] {
        model.imageHostMachines.map { row in
            ImageHostsMachine(id: row.id, name: model.isOnThisMac(row.id) ? "This Mac" : row.name,
                              online: row.state != "offline" && !model.hostUnreachable(row.id))
        }
    }

    var body: some View {
        ImageHostsPane(model: model, machines: machines) { machine, choose in
            MachinePicker(label: "Machine", value: machine.id, title: machine.name,
                          keep: { $0.has(Capability.imageHosts1) }) { if let id = $0 { choose(id) } }
        }
    }
}
