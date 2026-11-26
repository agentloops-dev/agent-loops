import AgentsKitCore
import SwiftUI

/// What the Work views (#3119, #3130) ask of the app that draws them: the Mac's `AppModel` and
/// the Remote's `RemoteModel` (#3256). Each reaches the machines its own way: the Mac through the
/// control plane's hosts, the phone through its home host and the others it has a link to.
@MainActor
protocol WorkModel: AnyObject, Sendable {
    /// Work's places: the list or the item's page open, and the ones before and after it.
    var workHistory: LineageHistory<WorkPlace> { get set }
    /// Each Work list's filters, sort and rows (#3257), kept while the app runs.
    var workLists: WorkLists { get }
    /// The model of a Work page in a window of its own (#5475), not of the main window.
    var isOwnWindow: Bool { get }
    /// Open Work at a place: a list, or an item's page.
    func showWork(_ place: WorkPlace)
    /// The machines that read their forges, the home one first.
    var forgeMachines: [(id: MachineRef, name: String)] { get }
    /// The machines that narrow their forges to a board (#3282), the home one first. None on the
    /// Remote: boards are the Mac's and the web page's (tier Mac + web).
    var boardMachines: [(id: MachineRef, name: String)] { get }
    /// The machines that draw Work ▸ Timeline (`work/timeline`, #4173), the home one first. Core
    /// since #6044: the Remote has them too.
    var timelineMachines: [(id: MachineRef, name: String)] { get }
    /// The projects Work ▸ Timeline can draw: `workProjects` on the Mac; on the Remote, whose lists
    /// have no Project filter, New Session's projects (#6044).
    var timelineProjects: [WorkList.Project] { get }
    /// The machines that say their deploys' targets (`deploys/targets`, #5270), the home one first.
    /// None on the Remote: Work ▸ Deploys is Mac + web.
    var deploysMachines: [(id: MachineRef, name: String)] { get }
    /// The person's projects (#3283), for Work's Project filter: Mac + web, so the Remote has none.
    var workProjects: [WorkList.Project] { get }
    /// Every machine's live folders, as this app knows them: Work's lists read again when they change (#6152).
    var workFolders: [FolderKey] { get }
    /// The machines that keep links, the home one first.
    var lineageMachines: [(id: MachineRef, name: String)] { get }
    /// Whether a machine takes the Work pages' writes (#3130).
    func forgeWrites(on host: MachineRef) -> Bool
    /// Whether a machine keeps a list of the image hosts the person allowed (#7139, #7140).
    func readsImageHosts(on host: MachineRef) -> Bool
    func forgeList(_ kind: WorkList.Kind, _ request: DaemonAPI.ForgeListRequest,
                   on host: MachineRef) async throws -> DaemonAPI.ForgeList
    func forgeItem(_ node: DaemonAPI.LineageNode, cursor: String?,
                   on host: MachineRef) async throws -> DaemonAPI.ForgeItemDetail
    func forge<R: Decodable & Sendable>(_ method: String, _ params: some Encodable & Sendable, returning: R.Type,
                                        on host: MachineRef) async throws -> R
    /// One call to the control plane that asks every machine of the person's (`forge/listAll`, `forge/boardsAll`,
    /// #5592); nil with no control plane, where the app asks each machine itself. The Remote has none: it asks its hosts.
    func controlAll<R: Decodable & Sendable>(_ method: String, _ params: some Encodable & Sendable,
                                             returning: R.Type) async throws -> R?
    /// A page of the lineage nodes of a kind, from the control plane; nil with none.
    func reportNodes(_ query: DaemonAPI.ReportNodesQuery) async throws -> DaemonAPI.ReportNodes?
    /// The sessions on every machine that lineage links to these nodes, with their state now, from
    /// the control plane's merged copy (#4276); nil with no control plane, or one too old to say.
    func lineageSessionsOn(_ nodes: [DaemonAPI.LineageNode]) async -> DaemonAPI.LineageSessionsOn?
    /// Goes up on each `control/lineageChanged` (#4276): a machine sent new lineage.
    var lineageChanges: Int { get }
    /// What a node is linked to, `depth` links away (1 to 4).
    func lineageWalk(_ node: DaemonAPI.LineageNode, depth: Int, on host: MachineRef) async throws -> DaemonAPI.LineageGraph
    func lineageCost(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageCostRollup?
    /// An issue's or an epic's estimate from past work (#3560); nil where the machine cannot make one.
    func lineageEstimate(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageEstimate?
    /// An epic's forecast (#4527): when it lands and its estimate to complete; nil where the machine cannot make one.
    func lineageForecast(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageForecast?
    /// A session's title, or nil when this app does not know the session.
    func workSessionTitle(_ id: UUID) -> String?
    /// A session's state, or nil when this app does not know the session (#3268, a plan's marks).
    func workSessionState(_ id: UUID) -> AgentState?
    /// The session that leads an epic, of those this app knows (#3269, an epic's page); nil with none.
    func workLead(of epic: DaemonAPI.LineageNode) -> Agent?
    /// The sessions this app knows, of every machine (#4115).
    var workAgents: [Agent] { get }
    /// Show a session's chat.
    func openWorkSession(_ id: UUID)
    /// The new-session form in a folder, filled in. Why it did not open, or nil.
    func offerStart(_ start: AppViewStart, in key: FolderKey) -> String?
    /// Ask Settings to show Connectors (#4393), for a forge whose credential was refused; false
    /// where the app has no such pane, and the list offers only Try again.
    func askWorkConnectors() -> Bool
    /// This device's copy (#4930): Work's last answers, drawn before the host answers; nil
    /// where the app keeps none.
    var workCopy: LocalCopy? { get }
    /// Work's client cache (#6353, epic #6348): the kept answers to the Issues and Pull requests lists and the items'
    /// pages, drawn with no forge call, then asked once. Nil where the app keeps none, or when its file does not open:
    /// then the lists and pages use `workCopy`, as before.
    var workCache: (any WorkCaching)? { get }
    /// Work's changes through the cache (#6354): shown at once, and changed back with a message when the forge refuses
    /// them. Nil while `workCache` is nil: then a change waits for the forge, as before.
    var workSaves: WorkSaves? { get }
}

extension WorkModel {
    var isOwnWindow: Bool { false }
    var workProjects: [WorkList.Project] { [] }
    var timelineProjects: [WorkList.Project] { workProjects }
    func askWorkConnectors() -> Bool { false }
    var workCopy: LocalCopy? { nil }
    var workCache: (any WorkCaching)? { nil }
    var workSaves: WorkSaves? { nil }
    func controlAll<R: Decodable & Sendable>(_ method: String, _ params: some Encodable & Sendable,
                                             returning: R.Type) async throws -> R? { nil }

    /// The last answer this device kept for what was asked (#4930); nil with none.
    func keptWork<Value: Codable & Sendable>(_: Value.Type, _ group: LocalCopy.WorkGroup, _ asked: String) async -> Value? {
        await keptWorkRead(Value.self, group, asked)?.value
    }

    /// The last answer this device kept, with when it was read from its host (#5142): the "Last update" line of a page
    /// drawn from the copy. With Work's cache on (#6573) a page's answer is in the cache, and only pictures are in the
    /// copy.
    func keptWorkRead<Value: Codable & Sendable>(_: Value.Type, _ group: LocalCopy.WorkGroup, _ asked: String) async -> (value: Value, at: Date)? {
        if let cache = workCache, group != .image {
            let start = ContinuousClock.now
            guard let kept = await cache.kept(Value.self, group.rawValue, asked: asked) else {
                WorkCacheLog.note("no kept \(group.rawValue) for \(asked)")
                return nil
            }
            WorkCacheLog.note("drew \(group.rawValue) \(asked) from the kept answer in \(WorkCacheLog.ms(since: start)) ms, 0 forge calls")
            return (kept.value, kept.read)
        }
        guard let copy = workCopy else { return nil }
        return await copy.workRead(Value.self, group, asked)
    }

    /// A host's answer, kept for the next open (#4930): in Work's cache when it is on (#6573), but pictures, which stay
    /// in the copy. Only a host's answers are kept, never what came from the copy.
    func keepWork<Value: Codable & Sendable>(_ value: Value, _ group: LocalCopy.WorkGroup, _ asked: String) {
        if let cache = workCache, group != .image {
            WorkCacheLog.note("asked \(group.rawValue) \(asked): kept the answer")
            Task { await cache.keep(value, group.rawValue, asked: asked) }
            return
        }
        guard let copy = workCopy else { return }
        Task { await copy.keepWork(value, group, asked) }
    }

    /// A forge's picture (`forge/image`, #4010): from this device's copy when it holds it, as a
    /// picture does not change at its URL (#4930); else read by the machine, and kept, unless the
    /// read went through a host that the person chose (`keep` false, #7139). `show` is the hosts
    /// the person accepts for this one read.
    func readForgeImage(_ url: String, show: [String] = [], on host: MachineRef) async -> (answer: PageImageAnswer, keep: Bool) {
        if let kept = await keptWork(Data.self, .image, url) { return (.image(kept), true) }
        // The result first, then the tuple from one place (#5946, `Result.catching`).
        let read = await Result.catching {
            try await forge(DaemonAPI.Method.forgeImage, DaemonAPI.ForgeImageRequest(url: url, show: show.isEmpty ? nil : show),
                            returning: DaemonAPI.ForgeImage.self, on: host)
        }
        let answer: PageImageAnswer
        var keep = false
        switch read {
        case .success(let got):
            answer = .image(got.data)
            keep = got.keep != false
            if keep { keepWork(got.data, .image, url) }
        case .failure(let error):
            answer = PageImageAnswer(error)
        }
        return (answer, keep)
    }

    /// The remote images of one Work page (#7139), for as long as it is open.
    func pageImages() -> PageImages<PlatformImage> {
        PageImages { [weak self] url, show, host in
            guard let self else { return .failed("the page closed", again: false) }
            return await readForgeImage(url.absoluteString, show: show, on: host).answer
        } allow: { [weak self] imageHost, host in
            guard let self else { return }
            let list = try await forge(DaemonAPI.Method.imageHostsAllow, DaemonAPI.ImageHostRequest(host: imageHost),
                                       returning: DaemonAPI.ImageHosts.self, on: host)
            keepWork(list, .imageHosts, host.rawValue)
        } decode: { PlatformImage(data: $0) }
    }

    var timelineMachines: [(id: MachineRef, name: String)] { [] }
    var deploysMachines: [(id: MachineRef, name: String)] { [] }
    func lineageSessionsOn(_ nodes: [DaemonAPI.LineageNode]) async -> DaemonAPI.LineageSessionsOn? { nil }
    var lineageChanges: Int { 0 }

    /// The boards of every machine's folders' projects (#3282), each once, by url, the first
    /// name kept; none from a machine that cannot be read. With a control plane, every machine's in
    /// one call (`forge/boardsAll`, #5592), which waits for a slow machine at most 5 s.
    func workBoards() async -> [DaemonAPI.ProjectBoard] {
        let machines = boardMachines
        let order = machines.map(\.id)
        var boards: [DaemonAPI.ProjectBoard] = []
        guard !order.isEmpty else { return boards }
        do {
            if let all = try await controlAll(DaemonAPI.Method.forgeBoardsAll, DaemonAPI.Empty(),
                                              returning: DaemonAPI.ForgeBoardsAll.self) {
                boards = WorkList.mergeBoards(all, hosts: order)
            } else {
                boards = await eachMachinesBoards(machines)
            }
        } catch {
            // The control plane did not answer: none, as from a machine that cannot be read.
        }
        // Kept in Work's cache when it is on (#6573), else in the local copy, for the Board menu's next open.
        if !boards.isEmpty { keepWork(boards, .boards, workBoardsAsked) }
        return boards
    }

    /// What the boards are kept under: the machines that narrow to a board, in their order.
    var workBoardsAsked: String { boardMachines.map(\.id.rawValue).joined(separator: ",") }

    /// Each machine's boards, a call to each (no control plane): the machines' own order, the home
    /// one first, so the same board keeps the same name.
    private func eachMachinesBoards(_ machines: [(id: MachineRef, name: String)]) async -> [DaemonAPI.ProjectBoard] {
        var got: [(host: MachineRef, boards: [DaemonAPI.ProjectBoard])] = []
        await withTaskGroup(of: (MachineRef, [DaemonAPI.ProjectBoard]).self) { group in
            for machine in machines {
                group.addTask {
                    let list = try? await self.forge(DaemonAPI.Method.forgeBoards, DaemonAPI.ForgeBoardsRequest(),
                                                     returning: DaemonAPI.ForgeBoards.self, on: machine.id)
                    return (machine.id, list?.boards ?? [])
                }
            }
            for await (host, boards) in group { got.append((host, boards)) }
        }
        let order = machines.map(\.id)
        got.sort { (order.firstIndex(of: $0.host) ?? .max) < (order.firstIndex(of: $1.host) ?? .max) }
        return WorkList.mergeBoards(got.map(\.boards))
    }

    /// The machines a list asks: those that narrow to the board when one is chosen.
    func workMachines(board: String) -> [(id: MachineRef, name: String)] {
        board.isEmpty ? forgeMachines : boardMachines
    }

    /// What Work's lists are read from (#6152): the machines that read their forges and every machine's folders. A
    /// list read before a machine joined or a folder was added is read again when this changes, as Refresh does.
    var workSource: String {
        (forgeMachines.map(\.id.rawValue) + ["|"] + workFolders.map(\.description).sorted()).joined(separator: "\n")
    }

    /// The sidebar shows Work (#4930), or what Work reads from changed (#6152): the first page of the list the Work
    /// page opens on is read now, from this device's copy and then its machines, so the page draws it at once. A list
    /// read already for the same source is not read again.
    func prefetchWork() async {
        workLists.source = workSource
        let kind = workPlace.kind
        guard [.issue, .pr, .release].contains(kind), !kind.fromForge || !forgeMachines.isEmpty else { return }
        let list = workLists[kind]
        guard !list.readingAsk else { return }
        await list.load(model: self)
    }

    /// Where Work is: Issues until something else is opened.
    var workPlace: WorkPlace { workHistory.current ?? WorkPlace(kind: .issue) }

    /// Where the item's page open stands in the list it was opened from (#3257); nil on a list,
    /// or on a page opened from a link or an address.
    var workNeighbours: WorkList.Neighbours? {
        let place = workPlace
        guard place.fromList, let ref = place.ref else { return nil }
        return workLists[place.kind].neighbours(of: ref)
    }

    /// Whether Next can go on: a row below, or another page to read.
    var workHasNext: Bool {
        guard let n = workNeighbours else { return false }
        return n.next != nil || workLists[workPlace.kind].hasMore
    }

    /// Previous or Next (#3257): the row above or below, in place of this one, so Back goes to
    /// the list and not through each item. Next at the last row read reads the next page first.
    func stepWork(forward: Bool) async {
        let place = workPlace
        guard let n = workNeighbours, let ref = place.ref else { return }
        let list = workLists[place.kind]
        let to = forward ? await list.next(after: ref, model: self) : n.previous
        guard let to, workPlace == place else { return }
        list.opened = to
        workHistory.replace(WorkPlace(kind: place.kind, ref: to, fromList: true))
    }

    /// Open a part of a page's trail (#5452): a list, or an epic's page. Each is a new place, so Back comes back to the page.
    func openTrail(_ target: WorkTrail.Target, window: OpenWorkWindow?) {
        follow(target.place, window: window)
    }
}

extension WorkTrail.Target {
    var place: WorkPlace {
        switch self {
        case .list(let kind): WorkPlace(kind: kind)
        case .item(let kind, let ref): WorkPlace(kind: kind, ref: ref)
        }
    }
}

extension View {
    /// `.task(id:)` for Work's loads, run to their end. On the Remote a collapsing split view
    /// says a page disappeared while it stays on screen (#3256): a cancelled load was never
    /// started again, and the page waited for ever. A load checks that its id is still the
    /// current one before it shows what it read, so a newer one wins.
    func workTask<ID: Equatable>(id: ID, _ work: @escaping @MainActor () async -> Void) -> some View {
        task(id: id) { await Task { await work() }.value }
    }
}
