import AgentsKitCore
import Foundation

/// Status ▸ Environment (#5260): the machines whose files of variables the page shows, and their hosts.
extension AppModel: EnvironmentSource {
    /// This Mac first, then the control plane's other machines by name, each whose host knows `env/1`. With no control
    /// plane, this Mac alone.
    var environmentMachines: [EnvironmentMachine] {
        guard let hosts = controlPlaneHosts, !hosts.isEmpty else {
            return host(.mac, has: Capability.env1) ? [EnvironmentMachine(id: .mac, name: "This Mac")] : []
        }
        let mine = thisMacHostID
        return hosts.filter { $0.has(Capability.env1) }
            .map { EnvironmentMachine(id: $0.id, name: $0.id == mine ? "This Mac" : $0.name) }
            .sorted { ($0.id == mine ? 0 : 1, $0.name) < ($1.id == mine ? 0 : 1, $1.name) }
    }

    func environmentClient(_ host: MachineRef) -> DaemonClient? {
        host == .mac || host == thisMacHostID ? macClient : client(for: host)
    }

    func environmentSessions(_ host: MachineRef) -> [EnvironmentSession] {
        let mine = host == thisMacHostID ? MachineRef.mac : host
        return work.agents
            .filter { ($0.host == host || $0.host == mine) && $0.archivedAt == nil && [.starting, .running, .waitingOnUser].contains($0.state) }
            .map { EnvironmentSession(id: $0.id, title: $0.title ?? $0.cwd.lastPathComponent) }
    }

    func environmentFolders(_ host: MachineRef) -> [String] {
        let mine = host == thisMacHostID ? MachineRef.mac : host
        return work.folders.filter { $0.host == host || $0.host == mine }.map { $0.folder.folder.path }
    }
}
