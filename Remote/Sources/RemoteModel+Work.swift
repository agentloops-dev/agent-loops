import AgentsKitCore
import Foundation

/// Work on the Remote (#3256): the window's views (`Shared/UI/Work`), each call to the machine it
/// names. The home host is `.mac` here, as its folders are; another host is reached over the
/// phone's own link to it, and only while that answers.
extension RemoteModel {
    func showWork(_ place: WorkPlace) {
        // Work is off (#6572): a hidden page does not show.
        guard flags.isOn(.work) else { return }
        // The list Work opens on is no place yet: a page opened from it puts it first, so Back finds it (#3257).
        if place.fromList, workHistory.current == nil { workHistory.go(WorkPlace(kind: place.kind)) }
        if place != workHistory.current { workHistory.go(place) }
        sidebarItem = .work(place.kind)
    }

    /// Work's part of this device's copy (#4930).
    var workCopy: LocalCopy? { localCopy?.copy }

    /// Work's client cache (#6357): the lists and items' pages draw from it with no call, then ask once.
    var workCache: (any WorkCaching)? { workCacheStore }

    /// Work's changes through the cache (#6354), while the cache opens.
    var workSaves: WorkSaves? { workCacheStore == nil ? nil : workSavesStore }

    /// Every machine's live folders this phone knows (#6152): Work's lists read again when one is added or goes.
    var workFolders: [FolderKey] { folders.map(\.key) }

    var forgeMachines: [(id: MachineRef, name: String)] {
        workMachines { $0.has(Capability.forge1) && $0.state != "offline" }
    }

    var lineageMachines: [(id: MachineRef, name: String)] {
        workMachines { $0.has(Capability.lineage1) }
    }

    /// The machines that draw Work ▸ Timeline (#4173; on the Remote since #6044), the home host first.
    var timelineMachines: [(id: MachineRef, name: String)] {
        workMachines { $0.has(Capability.forge1) && $0.has(Capability.workTimeline1) && $0.state != "offline" }
    }

    /// The Timeline's projects (#6044): New Session's, each folder by the machine it is on, as the
    /// machines above name them. Work's lists have no Project filter here (Mac + web).
    var timelineProjects: [WorkList.Project] {
        newSessionProjects.map { choice in
            WorkList.Project(id: choice.id, name: choice.name, folders: Set(choice.folders.map {
                WorkList.folderKey(host: $0.host, path: $0.folder.path(percentEncoded: false))
            }))
        }
    }

    /// Every machine's sessions on these nodes, from the control plane's merged copy (#4276): Work ▸
    /// Timeline's starts; nil with no control plane, or one that does not say.
    func lineageSessionsOn(_ nodes: [DaemonAPI.LineageNode]) async -> DaemonAPI.LineageSessionsOn? {
        guard let controlPlane, !nodes.isEmpty else { return nil }
        return try? await controlPlane.call(DaemonAPI.Method.lineageSessionsOn, DaemonAPI.LineageSessionsOnRequest(nodes: nodes),
                                            returning: DaemonAPI.LineageSessionsOn.self)
    }

    /// None: boards are the Mac's and the web page's (#3282, tier Mac + web).
    var boardMachines: [(id: MachineRef, name: String)] { [] }

    /// The control plane's machines that run agents, can do `can` and answer this phone: the
    /// home host first, as `.mac`, then the rest by name. Empty without a control plane, as on
    /// the Mac.
    private func workMachines(_ can: (DaemonAPI.ControlMachine) -> Bool) -> [(id: MachineRef, name: String)] {
        controlHosts
            .filter { can($0) }
            .compactMap { row -> (id: MachineRef, name: String)? in
                let id: MachineRef = row.id == controlHome ? .mac : row.id
                return hostClient(id) == nil ? nil : (id: id, name: row.name)
            }
            .sorted { ($0.id == .mac ? 0 : 1, $0.name) < ($1.id == .mac ? 0 : 1, $1.name) }
    }

    func forgeWrites(on host: MachineRef) -> Bool {
        let id = host == .mac ? controlHome : host
        return controlHosts.first { $0.id == id }?.has(Capability.forgeWrite1) ?? false
    }

    func forgeList(_ kind: WorkList.Kind, _ request: DaemonAPI.ForgeListRequest,
                   on host: MachineRef) async throws -> DaemonAPI.ForgeList {
        try await workClient(host).call(kind == .pr ? DaemonAPI.Method.forgePulls : DaemonAPI.Method.forgeIssues,
                                        request, returning: DaemonAPI.ForgeList.self)
    }

    func forgeItem(_ node: DaemonAPI.LineageNode, cursor: String?,
                   on host: MachineRef) async throws -> DaemonAPI.ForgeItemDetail {
        try await workClient(host).call(node.kind == "pr" ? DaemonAPI.Method.forgePull : DaemonAPI.Method.forgeIssue,
                                        DaemonAPI.ForgeItemRequest(node: node, cursor: cursor),
                                        returning: DaemonAPI.ForgeItemDetail.self)
    }

    func forge<R: Decodable & Sendable>(_ method: String, _ params: some Encodable & Sendable, returning: R.Type,
                                        on host: MachineRef) async throws -> R {
        try await workClient(host).call(method, params, returning: R.self)
    }

    func reportNodes(_ query: DaemonAPI.ReportNodesQuery) async throws -> DaemonAPI.ReportNodes? {
        guard let controlPlane else { return nil }
        return try await controlPlane.call(DaemonAPI.Method.reportsNodes, query, returning: DaemonAPI.ReportNodes.self)
    }

    func lineageWalk(_ node: DaemonAPI.LineageNode, depth: Int, on host: MachineRef) async throws -> DaemonAPI.LineageGraph {
        try await workClient(host).call(DaemonAPI.Method.lineageWalk,
                                        DaemonAPI.LineageNodeRequest(node: node, depth: depth),
                                        returning: DaemonAPI.LineageGraph.self)
    }

    func lineageCost(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageCostRollup? {
        try? await workClient(host).call(DaemonAPI.Method.lineageCost, DaemonAPI.LineageNodeRequest(node: node),
                                         returning: DaemonAPI.LineageCostRollup.self)
    }

    func lineageEstimate(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageEstimate? {
        try? await workClient(host).call(DaemonAPI.Method.lineageEstimate, DaemonAPI.LineageEstimateRequest(node: node),
                                         returning: DaemonAPI.LineageEstimate.self)
    }

    func lineageForecast(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageForecast? {
        try? await workClient(host).call(DaemonAPI.Method.lineageForecast, DaemonAPI.LineageForecastRequest(node: node),
                                         returning: DaemonAPI.LineageForecast.self)
    }

    func workSessionTitle(_ id: UUID) -> String? { work.agent(id)?.title }

    func workSessionState(_ id: UUID) -> AgentState? { work.agent(id)?.state }

    func openWorkSession(_ id: UUID) { sidebarItem = .session(id) }

    func workLead(of epic: DaemonAPI.LineageNode) -> Agent? { WorkSessions.lead(of: epic, in: work.agents) }

    var workAgents: [Agent] { work.agents }

    /// The client for a Work call: the home host's, or another's while it answers.
    private func workClient(_ host: MachineRef) throws -> DaemonClient {
        guard let client = hostClient(host) else { throw HostAway(host: host) }
        return client
    }
}
