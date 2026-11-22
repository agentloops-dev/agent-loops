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
    var filter = WorkList.Filter() {
        didSet {
            remember()
            if filter != oldValue { narrow() }
        }
    }
    /// The person's projects (#3283), as the model last said them, for the Project filter.
    var projects: [WorkList.Project] = [] {
        didSet { if projects != oldValue { readRepos() } }
    }
    var sort = WorkList.Sort.priority {
        didSet {
            remember()
            if sort != oldValue { narrow() }
        }
    }
    var lists: [(host: MachineRef, list: DaemonAPI.ForgeList)]? { didSet { made() } }
    var lineage: [DaemonAPI.ReportNode] = [] { didSet { made() } }
    /// The boards the Board menu offers (#3282), read with the list.
    var boards: [DaemonAPI.ProjectBoard] = []
    /// The boards were asked this run, with Work's cache on (#6573): the kept ones draw first, and one ask a run follows.
    private var boardsRead = false
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
    /// When the cached list last asked its machines: an ask for the same query joins it only for a moment (#6640).
    private var readAt: ContinuousClock.Instant?

    /// How many pages of `reports/nodes` a list reads for its lineage: at most 500 nodes.
    private static let lineagePages = 5

    init(kind: WorkList.Kind) {
        self.kind = kind
        let kept = WorkRemembered.list(WorkRememberedStore.kept(kind), kind: kind)
        filter = kept.filter
        sort = kept.sort
        made()
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
        /// With Work's cache on (#6353): everything the forge is asked, so a change of label, assignee or sort asks
        /// again; nil with it off, where those narrow and sort the rows on this Mac.
        var query: WorkCache.Query?
    }

    var ask: Ask {
        Ask(state: filter.state.forge, sort: nodesSort, asked: asked, board: filter.board, source: source,
            query: cacheOn ? cacheQuery : nil)
    }

    // MARK: Work's cache (#6353, epic #6348)

    /// The list reads Work's cache: an issue or PR list, with the cache open. Set by each load.
    private(set) var cacheOn = false {
        didSet { if cacheOn != oldValue { narrow() } }
    }
    /// When the answer on screen was read from its machines: the footer's "Last update" (#5139, #5967). Releases,
    /// read from lineage, set it when their read ends (#5968).
    var cacheRead: Date?
    /// When the rows on screen were read, for the footer's "Last update": the answer's, the kept rows' over a failed
    /// read, or this device's copy's.
    var shownRead: Date? { cacheRead ?? keptRead ?? fromDeviceRead }
    /// The query whose answer is on screen: a Refresh of it keeps its rows until the new answer comes.
    private var cacheShown: WorkCache.Query?
    /// How many pages of each machine's answer were read since the last refresh: the next page is kept after them (#6636).
    private var cachePages: [MachineRef: Int] = [:]

    /// The forge's order for the list's sort (#6538): Priority is the daemon's priority order, else the last updated
    /// first; Newest and Cost sort the answer on this Mac.
    private var forgeSort: String { sort == .priority ? "priority" : "updated" }

    /// What the list asks the forge, and keeps the answer under: the words in the filter field and the project narrow
    /// the answer on this Mac.
    var cacheQuery: WorkCache.Query {
        WorkCache.Query(kind: kind, state: filter.state.forge, board: Self.word(filter.board), label: Self.word(filter.label),
                        assignee: Self.word(filter.assignee), sort: forgeSort)
    }

    private static func word(_ text: String) -> String? { text.isEmpty ? nil : text }

    /// Draws the kept answer to the list's query at once, with no forge call, then asks the query once (#6358): one
    /// `forge/listAll`, or one call a machine with no control plane. The answer replaces page 1 and keeps the pages
    /// read after it. A machine that does not answer keeps its pages, with what failed and when they were read.
    private func loadCached(_ cache: any WorkCaching, model: any WorkModel) async {
        let mine = ask
        // One ask an open (#6358, finding 3): the window's restore and the click ask the same once. An open later in
        // the run asks again (#6640).
        guard !WorkList.joinsAsk(same: read == mine, reading: reading, since: readAt.map { ContinuousClock.now - $0 }) else { return }
        // A Refresh asks the forges, not the minute-old answer each daemon keeps (#6642).
        let fresh = read.map { $0.asked != mine.asked } ?? false
        read = mine
        readAt = ContinuousClock.now
        problem = nil
        pageProblem = nil
        reading = true
        defer { if ask == mine { reading = false } }
        fromDeviceRead = nil
        let query = cacheQuery
        let machines = model.workMachines(board: filter.board)
        let ids = machines.map(\.id)
        if cacheShown != query {
            let start = ContinuousClock.now
            let kept = await cache.answer(query)
            guard ask == mine else { return }
            show(kept, hosts: ids)
            cacheShown = query
            WorkCacheLog.note("drew \(kind.rawValue) \(query.state) \(query.sort) from the kept answer: "
                + "\((lists ?? []).reduce(0) { $0 + $1.list.items.count }) items in \(WorkCacheLog.ms(since: start)) ms, 0 forge calls")
            if kind == .issue, boards.isEmpty, let keptBoards = await model.keptWork([DaemonAPI.ProjectBoard].self, .boards, model.workBoardsAsked),
               ask == mine, boards.isEmpty {
                boards = keptBoards
            }
        }
        guard !ids.isEmpty else {
            if lists == nil { lists = [] }
            return
        }
        let start = ContinuousClock.now
        // Each repo is asked once (#6641): a machine that reads only what another reads is left out, unless one asked
        // fails, when the machines it covered are asked too.
        let source = model.workSource
        var scopes = await keptScopes(cache, ids, source: source)
        let pick = WorkList.oneEach(ids, scopes: scopes)
        var got = await firstPages(pick.ask, of: ids, fresh: fresh, model: model)
        if !pick.covered.isEmpty, got.contains(where: { !Self.clean($0.list) }) {
            got += await firstPages(pick.covered, of: ids, fresh: fresh, model: model)
        }
        guard ask == mine else { return }
        for entry in got {
            scopes[entry.host] = Self.clean(entry.list) ? entry.list.scopes : nil
            if Self.clean(entry.list), let read = entry.list.scopes {
                await cache.keep(WorkList.Scopes(scopes: read, source: source), Self.scopesPage, asked: scopesAsked(entry.host))
            }
        }
        let covered = Set(WorkList.oneEach(ids, scopes: scopes).covered)
        for host in covered { await cache.forget(query, host: host) }
        got = got.filter { !covered.contains($0.host) }
        let shown = WorkList.shownErrors(got.map { ($0.list.items.map(\.node.ref), $0.list.errors ?? []) })
        for (i, entry) in got.enumerated() {
            let answered = !entry.list.items.isEmpty || (entry.list.errors ?? []).isEmpty
            if answered {
                var list = entry.list
                list.errors = shown[i].isEmpty ? nil : shown[i]
                await cache.keepPage(query, host: entry.host, page: 0, list: list)
            } else {
                await cache.keepFailure(query, host: entry.host, errors: shown[i].isEmpty ? (entry.list.errors ?? []) : shown[i])
            }
        }
        WorkCacheLog.note("asked \(kind.rawValue) \(query.state) \(query.sort): \(got.count) machine(s), \(covered.count) left out, "
            + "\(got.reduce(0) { $0 + $1.list.items.count }) items\(got.compactMap(\.list.total).max().map { " of \($0)" } ?? ""), "
            + "\(WorkCacheLog.ms(since: start)) ms")
        let answer = await cache.answer(query)
        guard ask == mine else { return }
        show(answer, hosts: ids)
        // The lineage figures (the control plane's, not a forge's) join the rows by ref, after them.
        if let known = try? await readLineage(model), ask == mine {
            lineage = known.nodes
            lineageCursor = known.cursor
        }
        // The Board menu's boards, once a run: a board does not change with each list. The kept ones drew already.
        if kind == .issue, !boardsRead {
            let read = await model.workBoards()
            guard ask == mine else { return }
            boardsRead = true
            if !read.isEmpty || boards.isEmpty { boards = read }
        }
    }

    /// The kept answer on screen: the machines asked now, in their order, then every other machine the cache holds an
    /// answer of (#6635), each with its kept pages. A machine that failed after its pages were read, or is not asked
    /// now (not online or not known yet), keeps its rows, and the strip says when they were read.
    private func show(_ answer: WorkCache.Answer?, hosts: [MachineRef]) {
        guard let answer else {
            lists = nil
            cacheRead = nil
            keptRead = nil
            cachePages = [:]
            return
        }
        let shown = answer.shown(first: hosts)
        lists = shown.map { (host: $0.host, list: $0.list) }
        cachePages = Dictionary(shown.map { ($0.host, $0.fresh) }, uniquingKeysWith: { a, _ in a })
        cacheRead = shown.compactMap(\.read).min()
        let asked = Set(hosts)
        let stale = shown.contains { $0.failed != nil || !asked.contains($0.host) }
        keptRead = stale ? cacheRead : nil
    }

    /// Where a machine's scopes are kept (#6641), by the kind and the board: a board narrows what a list reads.
    private static let scopesPage = "work-scopes"
    private func scopesAsked(_ host: MachineRef) -> String { [kind.rawValue, filter.board, host.rawValue].joined(separator: "|") }

    /// Each machine's kept scopes (#6641), of those kept since Work's machines and folders last changed.
    private func keptScopes(_ cache: any WorkCaching, _ hosts: [MachineRef], source: String) async -> [MachineRef: [String]] {
        var out: [MachineRef: [String]] = [:]
        for host in hosts {
            if let kept = await cache.kept(WorkList.Scopes.self, Self.scopesPage, asked: scopesAsked(host)), kept.value.source == source {
                out[host] = kept.value.scopes
            }
        }
        return out
    }

    /// A list that read every forge it reads.
    private static func clean(_ list: DaemonAPI.ForgeList) -> Bool { (list.errors ?? []).isEmpty }

    /// Page 1 of the cached query on `hosts`, of all the machines `all`: in one call to the control plane (naming
    /// them, when they are not all), else a call to each.
    private func firstPages(_ hosts: [MachineRef], of all: [MachineRef], fresh: Bool, model: any WorkModel) async
        -> [(host: MachineRef, list: DaemonAPI.ForgeList)] {
        guard !hosts.isEmpty else { return [] }
        do {
            let pages = hosts == all ? nil : hosts.map { DaemonAPI.MachineCursor(machine: $0, cursor: "") }
            if let got = try await model.controlAll(DaemonAPI.Method.forgeListAll, listAllRequest(pages: pages, fresh: fresh),
                                                    returning: DaemonAPI.ForgeListAll.self) {
                return WorkList.hostLists(got, hosts: hosts)
            }
            return await askEach(hosts, cursors: [:], fresh: fresh, model: model)
        } catch {
            return hosts.map { ($0, .init(errors: [.init(forge: "", message: error.localizedDescription)])) }
        }
    }

    /// forge/listAll's request for the cached query, at the cursors in `pages` for a next page.
    private func listAllRequest(pages: [DaemonAPI.MachineCursor]? = nil, fresh: Bool = false) -> DaemonAPI.ForgeListAllRequest {
        let query = cacheQuery
        return .init(kind: kind == .pr ? "pulls" : "issues", state: query.state, board: query.board, label: query.label,
                     assignee: query.assignee, sort: query.sort, limit: 100, pages: pages, fresh: fresh ? true : nil)
    }

    /// The cached query on each machine: page 1 with no cursors, else only the machines in `cursors`, at theirs. One
    /// call each, side by side.
    private func askEach(_ hosts: [MachineRef], cursors: [MachineRef: String], fresh: Bool = false, model: any WorkModel) async
        -> [(host: MachineRef, list: DaemonAPI.ForgeList)] {
        let kind = kind
        let query = cacheQuery
        let asked = cursors.isEmpty ? hosts : hosts.filter { cursors[$0] != nil }
        return await withTaskGroup(of: (MachineRef, DaemonAPI.ForgeList).self) { group in
            for host in asked {
                let request = DaemonAPI.ForgeListRequest(state: query.state, label: query.label, assignee: query.assignee,
                                                         cursor: cursors[host], limit: 100, board: query.board, sort: query.sort,
                                                         fresh: fresh ? true : nil)
                group.addTask {
                    let read = await Result.catching { try await model.forgeList(kind, request, on: host) }
                    return (host, (try? read.get()) ?? .init(errors: [.init(forge: "", message: read.error?.localizedDescription ?? "")]))
                }
            }
            var got: [MachineRef: DaemonAPI.ForgeList] = [:]
            for await (host, list) in group { got[host] = list }
            return asked.compactMap { host in got[host].map { (host, $0) } }
        }
    }

    /// The next page of each machine that has one, kept after its kept pages, so the next open draws it too (#6358,
    /// finding 2). A machine that could not read it keeps its rows and its cursor, and the footer offers Try again.
    private func loadMoreCached(_ cache: any WorkCaching, model: any WorkModel) async {
        let mine = ask
        let query = cacheQuery
        let had = lists ?? []
        let cursors = Dictionary(had.compactMap { entry in entry.list.cursor.map { (entry.host, $0) } }, uniquingKeysWith: { a, _ in a })
        guard !cursors.isEmpty else { return }
        loadingMore = true
        pageProblem = nil
        defer { loadingMore = false }
        let start = ContinuousClock.now
        var got: [(host: MachineRef, list: DaemonAPI.ForgeList)]
        do {
            let pages = had.compactMap { entry in cursors[entry.host].map { DaemonAPI.MachineCursor(machine: entry.host, cursor: $0) } }
            if let all = try await model.controlAll(DaemonAPI.Method.forgeListAll, listAllRequest(pages: pages),
                                                    returning: DaemonAPI.ForgeListAll.self) {
                got = WorkList.hostLists(all, hosts: had.map(\.host).filter { cursors[$0] != nil })
            } else {
                got = await askEach(had.map(\.host), cursors: cursors, model: model)
            }
        } catch {
            pageProblem = ReportValueWords.sentence(error)
            return
        }
        guard ask == mine else { return }
        var failed: [String] = []
        for entry in got {
            if entry.list.items.isEmpty, let errors = entry.list.errors, !errors.isEmpty {
                failed += errors.map(\.message)
                continue
            }
            await cache.keepPage(query, host: entry.host, page: cachePages[entry.host] ?? 0, list: entry.list)
        }
        WorkCacheLog.note("asked the next page of \(kind.rawValue) \(query.state) \(query.sort): \(got.count) machine(s), "
            + "\(got.reduce(0) { $0 + $1.list.items.count }) items, \(WorkCacheLog.ms(since: start)) ms")
        pageProblem = failed.isEmpty ? nil : failed.joined(separator: "; ")
        let answer = await cache.answer(query)
        guard ask == mine else { return }
        show(answer, hosts: had.map(\.host))
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

    // MARK: The rows, made once per change (#6633)

    // `body` reads these many times a draw, and draws again on each change: made there, the rows cost O(n) a read
    // (a Row and a regular expression an item), and 1,000 kept rows held the main thread for minutes. They are made
    // here when the lists, the lineage, the projects, the filter or the sort change, and a read costs nothing.

    /// Every row read, one per ref (the last updated copy), unfiltered and in no order.
    private(set) var all: [WorkList.Row] = []
    /// The rows as the list shows them: filtered and sorted.
    private(set) var rows: [WorkList.Row] = []
    /// Each project with the repos its folders read (#3283); none for releases,
    /// which lineage alone lists, with no folder.
    private(set) var projectRepos: [(project: WorkList.Project, repos: Set<String>)] = []
    /// The filter menus' choices, from every row read.
    private(set) var choices: (repos: [String], labels: [String], assignees: [String]) = ([], [], [])
    /// The repos of every row read: a row names its repo when there are several.
    private(set) var repos: Set<String> = []
    /// The rows in the server's order (#6538), for the cache's Priority and Last updated; empty with the cache off.
    @ObservationIgnored private var ordered: [WorkList.Row] = []
    /// Each ref's repo, read from the ref once (#6633): a new page or a Refresh reads only the new refs.
    @ObservationIgnored private var repoOf: [String: String] = [:]

    /// How many of `rows` the list draws (#6633): 100 at a time, and 100 more as its end shows, so a list with
    /// thousands of kept rows draws as one with 100. A new filter, sort or query draws from the top again.
    private(set) var drawn = WorkListState.drawStep
    static let drawStep = 100

    /// The rows the list draws: the first `drawn` of `rows`.
    var drawnRows: ArraySlice<WorkList.Row> { rows.prefix(drawn) }

    /// The list draws 100 more of the rows read; false when it draws them all.
    func drawMore() -> Bool {
        guard drawn < rows.count else { return false }
        drawn += Self.drawStep
        return true
    }

    private func repo(_ ref: String) -> String {
        if let known = repoOf[ref] { return known }
        let repo = WorkList.repo(of: ref)
        repoOf[ref] = repo
        return repo
    }

    /// The lists or the lineage changed: every row again, then the shown ones.
    private func made() {
        let items = (lists ?? []).map { (host: $0.host, items: $0.list.items) }
        let made = kind.fromForge ? WorkList.merge(items, lineage: lineage, repo: repo) : lineage.map(WorkList.Row.init)
        // The same rows again (a Refresh the forge answered alike) change nothing the list draws: SwiftUI's list
        // diffs every row it holds on each change (#6633).
        if made != all { all = made }
        ordered = kind.fromForge && cacheOn ? WorkList.ordered(items, lineage: lineage, repo: repo) : []
        let read = WorkList.choices(all)
        if read != choices { choices = read }
        let known = Set(all.map(\.repo))
        if known != repos { repos = known }
        readRepos(items)
    }

    /// The projects or the items changed: each project's repos again, then the shown rows.
    private func readRepos(_ items: [(host: MachineRef, items: [DaemonAPI.ForgeItem])]? = nil) {
        let read = kind.fromForge
            ? WorkList.projectRepos(projects, lists: items ?? (lists ?? []).map { (host: $0.host, items: $0.list.items) }, repo: repo)
            : []
        if read.map(\.project) != projectRepos.map(\.project) || read.map(\.repos) != projectRepos.map(\.repos) { projectRepos = read }
        shownRows()
    }

    /// The filter, the sort or the cache changed: the shown rows again, drawn from the top.
    private func narrow() {
        if cacheOn, ordered.isEmpty, !all.isEmpty { ordered = WorkList.ordered((lists ?? []).map { (host: $0.host, items: $0.list.items) },
                                                                               lineage: lineage, repo: repo) }
        drawn = Self.drawStep
        shownRows()
    }

    private func shownRows() {
        var f = filter
        // Releases have no state in v1.
        if !kind.fromForge { f.state = .all }
        // With Work's cache, the forge sorts by priority or the last updated (#6538); the Mac only narrows the answer.
        let shown = cacheOn && (sort == .priority || sort == .updated)
            ? WorkList.filter(ordered, f, projects: projectRepos)
            : WorkList.sort(WorkList.filter(all, f, projects: projectRepos), by: sort)
        if shown != rows { rows = shown }
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
        let cache = kind.fromForge ? model.workCache : nil
        cacheOn = cache != nil
        if let cache {
            await loadCached(cache, model: model)
            return
        }
        cacheShown = nil
        if kind.fromForge { cacheRead = nil }
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
            if problem == nil { cacheRead = .now }
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
        if save(.priority(priority), on: row, host: host, model: model) { return }
        let old = row.priority
        change(row) { $0.priority = priority }
        write(DaemonAPI.Method.forgePriority, DaemonAPI.ForgePriorityRequest(node: row.node, priority: priority),
              on: host, model: model) { $0.change(row) { $0.priority = old } }
    }

    /// A row's On hold from its menu (`forge/hold`), as `set(priority:)` does.
    func set(hold on: Bool, on row: WorkList.Row, model: any WorkModel) {
        guard let host = row.host, on != row.onHold else { return }
        if save(.hold(on), on: row, host: host, model: model) { return }
        let old = row.onHold
        change(row) { $0.onHold = on }
        write(DaemonAPI.Method.forgeHold, DaemonAPI.ForgeHoldRequest(node: row.node, on: on),
              on: host, model: model) { $0.change(row) { $0.onHold = old } }
    }

    /// With Work's cache on (#6354): the change through the cache, so every list and the item's page show it at once,
    /// and it goes back with the forge's words when the forge refuses it. False with the cache off.
    private func save(_ change: WorkChange, on row: WorkList.Row, host: MachineRef, model: any WorkModel) -> Bool {
        guard cacheOn, let cache = model.workCache, let saves = model.workSaves else { return false }
        saves.save(change, on: row.node, host: host, model: model, cache: cache)
        return true
    }

    /// The kept answer on screen again, after a change to a kept item (#6354): no forge call.
    func redrawCached(model: any WorkModel) async {
        guard cacheOn, let cache = model.workCache, let query = cacheShown else { return }
        let mine = ask
        let hosts = (lists ?? []).map(\.host)
        let answer = await cache.answer(query)
        guard ask == mine, cacheShown == query else { return }
        show(answer, hosts: hosts.isEmpty ? model.workMachines(board: filter.board).map(\.id) : hosts)
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
        if cacheOn, let cache = model.workCache {
            await loadMoreCached(cache, model: model)
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
