import AgentsKitCore
import SwiftUI

/// Status ▸ Environment (#5260), as the window's: Yours through the control plane, each repo's through its machine's host.
struct RemoteEnvironmentPage: View {
    @Environment(RemoteModel.self) private var model
    @State private var selection: String?

    var body: some View {
        EnvironmentPageView(control: model.controlPlane, state: model.userScope, source: model, selection: $selection,
                            openSecret: { _ in model.sidebarItem = .secretsStatus })
            .navigationTitle(EnvironmentPage.title)
            .navigationBarTitleDisplayMode(.inline)
    }
}

/// The machines whose files of variables the page shows, and their hosts (#5260).
extension RemoteModel: EnvironmentSource {
    /// The Mac's host first, then the control plane's other machines by name, each whose host knows `env/1`.
    var environmentMachines: [EnvironmentMachine] {
        let home = controlHome
        var machines: [EnvironmentMachine] = host(.mac, has: Capability.env1) ? [EnvironmentMachine(id: .mac, name: "The Mac")] : []
        machines += controlHosts.filter { $0.id != home && $0.id != .mac && $0.has(Capability.env1) }
            .map { EnvironmentMachine(id: $0.id, name: $0.name) }
            .sorted { $0.name < $1.name }
        return machines
    }

    func environmentClient(_ host: MachineRef) -> DaemonClient? { hostClient(host) }

    func environmentSessions(_ host: MachineRef) -> [EnvironmentSession] {
        work.agents
            .filter { $0.host == host && $0.archivedAt == nil && [.starting, .running, .waitingOnUser].contains($0.state) }
            .map { EnvironmentSession(id: $0.id, title: $0.title ?? $0.cwd.lastPathComponent) }
    }

    func environmentFolders(_ host: MachineRef) -> [String] {
        work.folders.filter { $0.host == host }.map { $0.folder.folder.path }
    }
}
