import Foundation
import AgentsKitCore
import Observation

/// One Work list's state (#3257): its filters, sort, the rows read and the cursors to the next
/// pages. The model keeps one per kind, so an item's page goes through the list it was opened
/// from (Previous and Next), and Back finds the list as it was left.
@MainActor
@Observable
final class WorkListState {
    let kind: WorkList.Kind
    /// The filters and the sort are kept for the next launch (#5451), and the list opens with them.
    var filter = WorkList.Filter() { didSet { remember() } }
    /// The person's projects (#3283), as the model last said them, for the Project filter.
    var projects: [WorkList.Project] = []
    var sort = WorkList.Sort.priority { didSet { remember() } }
    var lists: [(host: MachineRef, list: DaemonAPI.ForgeList)]?
    var lineage: [DaemonAPI.ReportNode] = []
    /// The boards the Board menu offers (#3282), read with the list.
    var boards: [DaemonAPI.ProjectBoard] = []
    var lineageCursor: String?
    var problem: String?
    /// Why the last page after the first did not load: one line at the end of the list, and Try again.
    var pageProblem: String?
    var loadingMore = false
    var asked = 0
    /// The row whose page was opened last, which the list scrolls to when it is shown again.
    var opened: String?
    /// What the rows are read from (#6152): the machines and their folders, as `WorkModel.workSource` says. A read
    /// made before a machine joined or a folder was added is made again when it changes.
    var source = ""
    /// The selected row on the Mac (lists.md section 2): ↑ and ↓ move it, Return opens it.
    var selected: String?
    /// Why the last Priority or On hold write from a row's menu failed (epic #6086), in the machine's
    /// words; the row has its old value back, and the list says this until it is dismissed.
    var writeProblem: String?
    /// The rows on screen came from this device's copy (#4930): the machines are asked again,
    /// and the list says so until they answer, with when the copy was read (#5142).
    var fromDeviceRead: Date?
    var fromDevice: Bool { fromDeviceRead != nil }
    /// Every machine failed over this device's copy: when the rows kept on screen were read,
    /// for the "Last update" line in the failure strip (#5142).
    var keptRead: Date?
    /// A read runs: the rows read before stay until the new ones come (#4930), and Refresh spins.
    var reading = false
    /// What the rows were read for; the list reads them again only when that changes.
    private var read: Ask?
    /// What the rows on screen were read for, without Refresh: the copy is drawn only for another.
    private var shownFor: String?

    /// How many pages of `reports/nodes` a list reads for its lineage: at most 500 nodes.
    private static let lineagePages = 5

    init(kind: WorkList.Kind) {
        self.kind = kind
        let kept = WorkRemembered.list(WorkRememberedStore.kept(kind), kind: kind)
        filter = kept.filter
        sort = kept.sort
    }

    /// Keeps what the list has now for the next launch; the defaults remove it.
    private func remember() {
        WorkRememberedStore.keep(WorkRemembered.words(filter, sort: sort, kind: kind), for: kind)
    }

    struct Ask: Equatable {
        var state: String
        var sort: DaemonAPI.ReportNodesSort
        var asked: Int
        var board: String
        var source: String
        /// The PoC's query beyond these (#6358): the label, assignee and sort the forge is asked for; empty otherwise.
        var query = ""
    }

    var ask: Ask {
        Ask(state: filter.state.forge, sort: nodesSort, asked: asked, board: filter.board, source: source,
            query: queryPoC ? cacheQuery : "")
    }

    /// A read for what is asked now is on its way: another would read the same again.
    var readingAsk: Bool { reading && read == ask }

    /// What a list's first page is kept under in this device's copy (#4930): the kind, state, sort and board.
    var copyAsk: String { [kind.rawValue, filter.state.forge, nodesSort.rawValue, filter.board].joined(separator: "|") }

    /// A list's first page as this device keeps it (#4930): each machine's page, the lineage
    /// figures, and the boards the Board menu offers.
    struct Kept: Codable, Sendable {
        var hosts: [MachineRef]
        var lists: [DaemonAPI.ForgeList]
        var lineage: [DaemonAPI.ReportNode]
        var lineageCursor: String?
        var boards: [DaemonAPI.ProjectBoard]
    }

    /// Draws the first page this device kept for what is asked, unless rows read for it are on
    /// screen. Whether there was one.
    @discardableResult
    func drawKept(model: any WorkModel) async -> Bool {
        let wanted = copyAsk
        guard shownFor != wanted, case let (kept, at)? = await model.keptWorkRead(Kept.self, .list, wanted),
              copyAsk == wanted, shownFor != wanted, kept.hosts.count == kept.lists.count else { return false }
        lists = Array(zip(kept.hosts, kept.lists)).map { (host: $0.0, list: $0.1) }
        lineage = kept.lineage
        lineageCursor = kept.lineageCursor
        if kind == .issue { boards = kept.boards }
        shownFor = wanted
        fromDeviceRead = at
        keptRead = nil
        return true
    }

    /// An issue's or a PR's lineage is joined by ref, in any order; a release's list is lineage's own.
    private var nodesSort: DaemonAPI.ReportNodesSort { !kind.fromForge && sort == .cost ? .cost : .newest }

    /// The PoC's rows (#6358): each machine's items in the server's order, once each by ref, with their figures.
    private var serverOrdered: [WorkList.Row] {
        let byRef = Dictionary(lineage.map { ($0.node.ref, $0) }, uniquingKeysWith: { first, _ in first })
        var seen = Set<String>()
        var rows: [WorkList.Row] = []
        for (host, list) in lists ?? [] {
            for item in list.items where seen.insert(item.node.ref).inserted {
                rows.append(WorkList.Row(item, on: host, lineage: byRef[item.node.ref]))
            }
        }
        return rows
    }

    var all: [WorkList.Row] {
        kind.fromForge ? WorkList.merge((lists ?? []).map { (host: $0.host, items: $0.list.items) }, lineage: lineage)
            : lineage.map(WorkList.Row.init)
    }

    /// The rows as the list shows them: filtered and sorted.
    var rows: [WorkList.Row] {
        var f = filter
        // Releases have no state in v1.
        if !kind.fromForge { f.state = .all }
        // The PoC sorts nothing here (#6358): the order is the server's; the words still narrow the rows.
        if queryPoC { return WorkList.filter(serverOrdered, f, projects: projectRepos) }
        return WorkList.sort(WorkList.filter(all, f, projects: projectRepos), by: sort)
    }

    /// Each project with the repos its folders read (#3283); none for releases,
    /// which lineage alone lists, with no folder.
    var projectRepos: [(project: WorkList.Project, repos: Set<String>)] {
        kind.fromForge ? WorkList.projectRepos(projects, lists: (lists ?? []).map { (host: $0.host, items: $0.list.items) }) : []
    }

    var hasMore: Bool { kind.fromForge ? (lists ?? []).contains { $0.list.cursor != nil } : lineageCursor != nil }

    /// Previous and Next for an item's page; nil when the list has not got the item.
    func neighbours(of ref: String) -> WorkList.Neighbours? { WorkList.neighbours(of: ref, in: rows) }

    /// The ref after `ref`, reading the next page first (as More does) when `ref` is the last row read.
    func next(after ref: String, model: any WorkModel) async -> String? {
        if let next = neighbours(of: ref)?.next { return next }
        while hasMore, !loadingMore {
            let before = rows.count
            await loadMore(model: model)
            if let next = neighbours(of: ref)?.next { return next }
            if rows.count == before || pageProblem != nil { break }
        }
        return nil
    }

    /// The lineage of the kind, at most `lineagePages` pages; none from a control plane that does not list nodes.
    private func readLineage(_ model: any WorkModel, from cursor: String? = nil) async throws
        -> (nodes: [DaemonAPI.ReportNode], cursor: String?) {
        var nodes: [DaemonAPI.ReportNode] = []
        var next = cursor
        for _ in 0 ..< Self.lineagePages {
            guard let page = try await model.reportNodes(.init(kind: kind.rawValue, sort: nodesSort, cursor: next, limit: 100)) else {
                return (nodes, nil)
            }
            nodes += page.nodes
            next = page.cursor
            if next == nil || !kind.fromForge { break }
        }
        return (nodes, next)
    }

    /// Read the list for what it is asked, unless it was read for that already. The rows on
    /// screen stay until the new ones come (#4930); a list not read yet this run draws this
    /// device's copy first, and the machines' answer replaces it.
    func load(model: any WorkModel) async {
        if queryPoC, let cache = WorkCache.shared {
            await loadQuery(cache, model: model)
            return
        }
        let mine = ask
        guard read != mine || lists == nil else { return }
        read = mine
        problem = nil
        pageProblem = nil
        reading = true
        defer { if ask == mine { reading = false } }
        await drawKept(model: model)
        guard ask == mine else { return }
        let wanted = copyAsk
        var known: (nodes: [DaemonAPI.ReportNode], cursor: String?) = ([], nil)
        do {
            known = try await readLineage(model)
            guard ask == mine else { return }
        } catch {
            guard ask == mine else { return }
            // A control plane that does not list nodes yet leaves the issues and PRs without figures.
            if !kind.fromForge { problem = ReportValueWords.sentence(error) }
        }
        guard kind.fromForge else {
            lineage = known.nodes
            lineageCursor = known.cursor
            lists = []
            answered(wanted)
            if problem == nil { model.keepWork(Kept(hosts: [], lists: [], lineage: known.nodes, lineageCursor: known.cursor, boards: []),
                                               .list, wanted) }
            return
        }
        let state = filter.state.forge
        let kind = kind
        let board = filter.board
        var readBoards: [DaemonAPI.ProjectBoard] = []
        if kind == .issue {
            readBoards = await model.workBoards()
            guard ask == mine else { return }
        }
        let machines = model.workMachines(board: board)
        let ids = machines.map(\.id)
        var got: [(host: MachineRef, list: DaemonAPI.ForgeList)] = []
        if !ids.isEmpty {
            // Every machine's list in one call to the control plane (#5592), which waits for a slow machine at most 5 s;
            // with none, a call to each machine. The rows read before stay until it answers.
            do {
                if let all = try await model.controlAll(DaemonAPI.Method.forgeListAll,
                                                        Self.listAllRequest(kind, state: state, board: board),
                                                        returning: DaemonAPI.ForgeListAll.self) {
                    got = WorkList.hostLists(all, hosts: ids)
                } else {
                    got = await readEach(machines, .init(state: state, limit: 100, board: board.isEmpty ? nil : board),
                                         model: model, mine: mine, known: known)
                }
            } catch {
                got = ids.map { ($0, .init(errors: [.init(forge: "", message: error.localizedDescription)])) }
            }
        }
        guard ask == mine else { return }
        // One machine's answer for a forge is enough: the others' errors for it go (#5449).
        let shown = WorkList.shownErrors(got.map { ($0.list.items.map(\.node.ref), $0.list.errors ?? []) })
        got = got.enumerated().map { i, entry in
            var list = entry.list
            list.errors = shown[i].isEmpty ? nil : shown[i]
            return (entry.host, list)
        }
        if kind == .issue, !readBoards.isEmpty || !fromDevice { boards = readBoards }
        // Every machine failed over this device's copy: its rows stay, dimmed under what the forges said, with no
        // next page (#4392, #4930), as the web page keeps them.
        let failed = got.allSatisfy { $0.list.items.isEmpty && !($0.list.errors ?? []).isEmpty }
        if failed, let read = fromDeviceRead, let kept = lists, kept.contains(where: { !$0.list.items.isEmpty }) {
            let said = got.flatMap { $0.list.errors ?? [] }
            lists = kept.enumerated().map { i, entry in
                (entry.host, DaemonAPI.ForgeList(items: entry.list.items, errors: i == 0 ? said : nil, forges: entry.list.forges,
                                                  total: entry.list.total))
            }
            answered(wanted)
            keptRead = read
            return
        }
        lineage = known.nodes
        lineageCursor = known.cursor
        lists = got
        answered(wanted)
        // Only a read some forge answered is kept: a failed one would draw an empty list next time.
        if got.contains(where: { ($0.list.errors ?? []).isEmpty }) {
            model.keepWork(Kept(hosts: got.map(\.host), lists: got.map(\.list), lineage: known.nodes,
                                lineageCursor: known.cursor, boards: readBoards), .list, wanted)
        }
    }

    // MARK: The query cache proof of concept (#6358, epic #6348)

    /// The PoC is on for issues and pull requests (the default WorkLocalCachePoC).
    var queryPoC: Bool { kind.fromForge && WorkCache.enabled }
    /// A read of the list's query runs, for the footer.
    var cacheReading = false
    /// When the answer on screen was read, for "Last update".
    var cacheRead: Date?

    /// The forge's order for the list's sort: priority, else the last updated first. The other sorts are not the
    /// server's yet, so they show the last updated first.
    private var forgeSort: String { sort == .priority ? "priority" : "updated" }

    /// What a list's answer is kept under: everything the forge is asked.
    private var cacheQuery: String {
        [kind.rawValue, filter.state.forge, filter.board, filter.label, filter.assignee, forgeSort].joined(separator: "|")
    }

    private func forgeRequest(cursor: String?) -> DaemonAPI.ForgeListRequest {
        .init(state: filter.state.forge, label: filter.label.isEmpty ? nil : filter.label,
              assignee: filter.assignee.isEmpty ? nil : filter.assignee, cursor: cursor, limit: 100,
              board: filter.board.isEmpty ? nil : filter.board, sort: forgeSort)
    }

    /// Draws the kept answer to the list's query at once, with no call, then asks the query's first page again (one
    /// request a machine) and keeps that answer. When no machine answers, the kept answer stays, with what failed.
    private func loadQuery(_ cache: WorkCache, model: any WorkModel) async {
        let mine = ask
        read = mine
        problem = nil
        pageProblem = nil
        let query = cacheQuery
        let start = Date()
        if let kept = await cache.answer(query, kind: kind), ask == mine {
            lists = kept.lists
            cacheRead = kept.read
            shownFor = copyAsk
            WorkCache.note("work cache: drew \(query) from the kept answer: \(kept.lists.reduce(0) { $0 + $1.list.items.count }) ids, \(Int(Date().timeIntervalSince(start) * 1000)) ms, 0 forge calls")
        } else if ask == mine {
            lists = nil
            cacheRead = nil
        }
        cacheReading = true
        reading = true
        defer {
            if ask == mine {
                cacheReading = false
                reading = false
            }
        }
        let hosts = model.workMachines(board: filter.board).map(\.id)
        guard !hosts.isEmpty else { return }
        let asked = Date()
        let got = await askEach(hosts, cursors: hosts.map { _ in nil }, model: model)
        guard ask == mine else { return }
        let took = Int(Date().timeIntervalSince(asked) * 1000)
        let failed = got.allSatisfy { $0.list.items.isEmpty && !($0.list.errors ?? []).isEmpty }
        if failed, let had = lists, had.contains(where: { !$0.list.items.isEmpty }) {
            let said = got.flatMap { $0.list.errors ?? [] }
            lists = had.enumerated().map { i, entry in
                var list = entry.list
                list.errors = i == 0 ? said : nil
                return (entry.host, list)
            }
            keptRead = cacheRead
            WorkCache.note("work cache: asked \(query) again: failed in \(took) ms; the kept answer stays")
            return
        }
        let now = Date()
        if !failed { await cache.keep(query, kind: kind, lists: got, read: now) }
        lists = got
        cacheRead = now
        keptRead = nil
        shownFor = copyAsk
        WorkCache.note("work cache: asked \(query): \(got.count) request(s), \(got.reduce(0) { $0 + $1.list.items.count }) items\(got.compactMap(\.list.total).max().map { " of \($0)" } ?? ""), \(took) ms")
        if let known = try? await readLineage(model), ask == mine {
            lineage = known.nodes
            lineageCursor = known.cursor
        }
    }

    /// The next page of each machine that has one (one request each), added to the answer on screen and kept.
    private func loadNextPage(_ cache: WorkCache, model: any WorkModel) async {
        guard let had = lists, had.contains(where: { $0.list.cursor != nil }), !loadingMore else { return }
        let mine = ask
        let query = cacheQuery
        loadingMore = true
        pageProblem = nil
        defer { loadingMore = false }
        let asked = Date()
        let got = await askEach(had.map(\.host), cursors: had.map(\.list.cursor), model: model)
        guard ask == mine else { return }
        var next = had
        var said: [String] = []
        for (i, entry) in got.enumerated() where had[i].list.cursor != nil {
            if let errors = entry.list.errors, !errors.isEmpty, entry.list.items.isEmpty {
                said += errors.map(\.message)
                continue
            }
            next[i].list.items += entry.list.items
            next[i].list.cursor = entry.list.cursor
        }
        pageProblem = said.isEmpty ? nil : said.joined(separator: "\n")
        lists = next
        await cache.keep(query, kind: kind, lists: next, read: cacheRead ?? Date())
        WorkCache.note("work cache: next page of \(query): \(got.count) machine(s), \(got.reduce(0) { $0 + $1.list.items.count }) items, \(Int(Date().timeIntervalSince(asked) * 1000)) ms")
    }

    /// The list's query on each machine, at its cursor (nil for the first page; a machine with no next page is not
    /// asked): one forge/issues or forge/pulls call each, side by side.
    private func askEach(_ hosts: [MachineRef], cursors: [String?], model: any WorkModel) async
        -> [(host: MachineRef, list: DaemonAPI.ForgeList)] {
        let kind = kind
        let first = cursors.allSatisfy { $0 == nil }
        let requests = cursors.map { forgeRequest(cursor: $0) }
        return await withTaskGroup(of: (Int, DaemonAPI.ForgeList).self) { group in
            for (i, host) in hosts.enumerated() where first || cursors[i] != nil {
                let request = requests[i]
                group.addTask {
                    let read = await Result.catching { try await model.forgeList(kind, request, on: host) }
                    let list = (try? read.get()) ?? .init(errors: [.init(forge: "", message: read.error?.localizedDescription ?? "")])
                    return (i, list)
                }
            }
            var out = hosts.map { (host: $0, list: DaemonAPI.ForgeList()) }
            for await (i, list) in group { out[i].list = list }
            return out
        }
    }

    /// forge/listAll's request (#5592) for a kind's list: its state, a page of 100, the board when one is chosen, and
    /// `pages`, each machine still to read and its cursor, for the next page.
    static func listAllRequest(_ kind: WorkList.Kind, state: String, board: String,
                               pages: [DaemonAPI.MachineCursor]? = nil) -> DaemonAPI.ForgeListAllRequest {
        .init(kind: kind == .pr ? "pulls" : "issues", state: state, board: board.isEmpty ? nil : board, limit: 100, pages: pages)
    }

    /// Each machine's list, a call to each, with no control plane. Each machine's rows draw as it answers (#5449): the
    /// first machine that answers for a forge is enough, and a slow or offline one does not hold the list. The others
    /// keep the rows they had; errors wait for all.
    private func readEach(_ machines: [(id: MachineRef, name: String)], _ request: DaemonAPI.ForgeListRequest,
                          model: any WorkModel, mine: Ask, known: (nodes: [DaemonAPI.ReportNode], cursor: String?))
        async -> [(host: MachineRef, list: DaemonAPI.ForgeList)] {
        let kind = kind
        var got: [(host: MachineRef, list: DaemonAPI.ForgeList)] = []
        await withTaskGroup(of: (MachineRef, DaemonAPI.ForgeList).self) { group in
            for machine in machines {
                group.addTask {
                    // The result first, then the tuple from one place (#5946, `Result.catching`).
                    let read = await Result.catching { try await model.forgeList(kind, request, on: machine.id) }
                    let list = (try? read.get()) ?? .init(errors: [.init(forge: "", message: read.error?.localizedDescription ?? "")])
                    return (machine.id, list)
                }
            }
            for await (host, list) in group {
                got.append((host, list))
                guard ask == mine, got.count < machines.count, !list.items.isEmpty else { continue }
                var drawn = got.map { entry in
                    var quiet = entry.list
                    quiet.errors = nil
                    return (host: entry.host, list: quiet)
                }
                drawn += (lists ?? []).filter { had in !got.contains { $0.host == had.host } }
                lineage = known.nodes
                lineageCursor = known.cursor
                lists = drawn
            }
        }
        return got
    }

    /// What a row's right-click menu offers (epic #6086): its priority and on hold where its forge sets
    /// them and its machine writes; nothing on rows from this device's copy, until the machines answer.
    func setsPriority(_ row: WorkList.Row, model: any WorkModel) -> (priority: Bool, hold: Bool) {
        guard kind == .issue, !fromDevice, let host = row.host else { return (false, false) }
        let forges = lists?.first { $0.host == host }?.list.forges
        return WorkList.rowSetsPriority(row, forges: forges, writes: model.forgeWrites(on: host))
    }

    /// A row's priority from its menu (`forge/priority`): the row shows it at once, and has its old
    /// one back when the write fails.
    func set(priority: String?, on row: WorkList.Row, model: any WorkModel) {
        guard let host = row.host, priority != row.priority?.lowercased() else { return }
        let old = row.priority
        change(row) { $0.priority = priority }
        cacheEdit(row) { $0.priority = priority }
        write(DaemonAPI.Method.forgePriority, DaemonAPI.ForgePriorityRequest(node: row.node, priority: priority),
              on: host, model: model) { $0.change(row) { $0.priority = old }; $0.cacheEdit(row) { $0.priority = old } }
    }

    /// A row's On hold from its menu (`forge/hold`), as `set(priority:)` does.
    func set(hold on: Bool, on row: WorkList.Row, model: any WorkModel) {
        guard let host = row.host, on != row.onHold else { return }
        let old = row.onHold
        change(row) { $0.onHold = on }
        cacheEdit(row) { $0.onHold = on }
        write(DaemonAPI.Method.forgeHold, DaemonAPI.ForgeHoldRequest(node: row.node, on: on),
              on: host, model: model) { $0.change(row) { $0.onHold = old }; $0.cacheEdit(row) { $0.onHold = old } }
    }

    private func write(_ method: String, _ params: some Encodable & Sendable, on host: MachineRef,
                       model: any WorkModel, undo: @escaping @MainActor (WorkListState) -> Void) {
        writeProblem = nil
        Task { [weak self] in
            do {
                _ = try await model.forge(method, params, returning: DaemonAPI.ForgeDone.self, on: host)
                // The forge's own read next (its labels too), with the rows on screen until it answers.
                self?.asked += 1
            } catch {
                guard let self else { return }
                undo(self)
                writeProblem = WorkList.failureWords(error)
            }
        }
    }

    /// The same change to the kept item (#6358), so every list that holds it draws it so.
    private func cacheEdit(_ row: WorkList.Row, _ edit: @escaping @Sendable (inout DaemonAPI.ForgeItem) -> Void) {
        guard queryPoC, let cache = WorkCache.shared, let host = row.host else { return }
        let kind = kind
        let ref = row.node.ref
        Task { await cache.edit(kind, host: host, ref: ref, edit) }
    }

    /// Changes the row's item in the list it came from: the same ref on the same machine.
    private func change(_ row: WorkList.Row, _ edit: (inout DaemonAPI.ForgeItem) -> Void) {
        guard var all = lists else { return }
        for i in all.indices where all[i].host == row.host {
            for j in all[i].list.items.indices where all[i].list.items[j].node.ref == row.node.ref {
                edit(&all[i].list.items[j])
            }
        }
        lists = all
    }

    /// The machines answered for what is asked: the rows are theirs now.
    private func answered(_ wanted: String) {
        shownFor = wanted
        fromDeviceRead = nil
        keptRead = nil
    }

    func loadMore(model: any WorkModel) async {
        if queryPoC, let cache = WorkCache.shared {
            await loadNextPage(cache, model: model)
            return
        }
        // A page after the copy's is read once the machines answered (#4930): the copy's cursors may be old.
        guard !fromDevice else { return }
        loadingMore = true
        pageProblem = nil
        defer { loadingMore = false }
        do {
            if kind.fromForge {
                let had = lists ?? []
                let board = filter.board
                let pages = had.compactMap { entry in entry.list.cursor.map { DaemonAPI.MachineCursor(machine: entry.host, cursor: $0) } }
                // Each machine's next page in one call to the control plane (#5592). A forge or a machine that could not
                // read its next page says so: the rows and the cursor stay, so Try again reads it again.
                if let all = try await model.controlAll(DaemonAPI.Method.forgeListAll,
                                                        Self.listAllRequest(kind, state: filter.state.forge, board: board,
                                                                            pages: pages),
                                                        returning: DaemonAPI.ForgeListAll.self) {
                    let (next, problem) = WorkList.addPages(all, to: had)
                    lists = next
                    pageProblem = problem
                    return
                }
                var next: [(host: MachineRef, list: DaemonAPI.ForgeList)] = []
                for entry in had {
                    guard let cursor = entry.list.cursor else { next.append(entry); continue }
                    let page = try await model.forgeList(kind, .init(state: filter.state.forge, cursor: cursor, limit: 100,
                                                                     board: board.isEmpty ? nil : board),
                                                         on: entry.host)
                    next.append((entry.host, .init(items: entry.list.items + page.items, cursor: page.cursor, errors: page.errors,
                                                   forges: WorkList.joinForges(entry.list.forges, page.forges),
                                                   total: entry.list.total)))
                }
                lists = next
            } else if let cursor = lineageCursor {
                let page = try await readLineage(model, from: cursor)
                lineage += page.nodes
                lineageCursor = page.cursor
            }
        } catch {
            pageProblem = ReportValueWords.sentence(error)
        }
    }
}

/// The Work lists, one per kind, kept by the model while the app runs (#3257).
@MainActor
final class WorkLists {
    /// Work ▸ Timeline (#4173): Core since #6044.
    let timeline = WorkTimelineState()
    #if os(macOS)
    /// Work ▸ Deploys (#5270): Mac + web, so not on the Remote.
    let deploys = WorkDeploysState()
    #endif
    private var byKind: [WorkList.Kind: WorkListState] = [:]
    /// What every list is read from (#6152), given to each: a change reads an open list again.
    var source = "" {
        didSet { if source != oldValue { for state in byKind.values { state.source = source } } }
    }

    subscript(kind: WorkList.Kind) -> WorkListState {
        if let state = byKind[kind] { return state }
        let state = WorkListState(kind: kind)
        state.source = source
        byKind[kind] = state
        return state
    }
}

/// Where the Work pages keep what they had when last left (#5451): this app's defaults.
enum WorkRememberedStore {
    static func kept(_ kind: WorkList.Kind) -> String? { WindowDefaults.store.string(forKey: WorkRemembered.key(kind)) }

    static func keep(_ words: String, for kind: WorkList.Kind) {
        let key = WorkRemembered.key(kind)
        guard WindowDefaults.store.string(forKey: key) != words else { return }
        if words.isEmpty { WindowDefaults.store.removeObject(forKey: key) } else { WindowDefaults.store.set(words, forKey: key) }
    }
}
