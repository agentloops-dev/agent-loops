import AgentsKitCore
import SwiftUI

/// Work (#3119, spec 3100), as the web page's Work section: Issues and Pull requests from every
/// machine's forges (`forge/issues`, `forge/pulls`), one row per ref, with what lineage knows
/// (`reports/nodes`) under each title; Releases from lineage alone. A row opens the
/// item's page; Back and Forward go along the places, as a browser's (on the Mac, the window's, #5784). One view for the Mac's
/// window and the Remote (#3256): on a phone the filters scroll sideways and a row wraps.
struct WorkView: View {
    let model: any WorkModel
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var sizeClass
    #endif

    var body: some View {
        let place = model.workPlace
        Group {
            if place.files {
                WorkFilesPage(model: model, place: place).id(place)
            } else if place.ref != nil, place.kind == .release {
                WorkReleasePage(model: model, place: place).id(place)
            } else if place.ref != nil {
                WorkItemPage(model: model, place: place).id(place)
            } else if place.kind == .deploy {
                #if os(macOS)
                WorkDeploysPane(model: model, state: model.workLists.deploys)
                #endif
            } else if place.kind == .timeline {
                WorkTimelinePane(model: model, state: model.workLists.timeline)
            } else {
                WorkListPane(model: model, kind: place.kind, list: model.workLists[place.kind]).id(place.kind)
            }
        }
        .modifier(WorkWindows())
        // The list's own title: its row in the sidebar chose it (#4004). On the Remote an item's
        // page says its number, *#4331*, as the bar's title (#4401).
        .navigationTitle(Self.title(place))
        #if os(iOS)
        // A phone's list has a large title (lists.md section 3); an item's page and the iPad keep it small.
        .navigationBarTitleDisplayMode(sizeClass == .compact && place.ref == nil && place.kind.fromForge ? .large : .inline)
        #endif
        .toolbar {
            // The Remote's item page has its own bar: ‹ Issues, ↑ ↓ and Open on GitHub (#4401).
            if Self.history(place) {
                ToolbarItemGroup(placement: Self.historyPlacement) {
                    Button { model.workHistory.back() } label: { Image(toolbarIcon: "chevron.left") }
                        .disabled(!model.workHistory.canGoBack)
                        .help("Back")
                    Button { model.workHistory.forward() } label: { Image(toolbarIcon: "chevron.right") }
                        .disabled(!model.workHistory.canGoForward)
                        .help("Forward")
                }
            }
        }
    }

    private static func title(_ place: WorkPlace) -> String {
        #if os(iOS)
        if let ref = place.ref { return WorkList.shortRef(ref) }
        #endif
        return place.kind.title
    }

    /// The Remote's list has Back and Forward along Work's places; the Mac window's are its own,
    /// along every page (#5784).
    private static func history(_ place: WorkPlace) -> Bool {
        #if os(iOS)
        place.ref == nil
        #else
        false
        #endif
    }

    /// Back and Forward at the bar's end on the Remote, whose start is its own way back to the sidebar.
    private static var historyPlacement: ToolbarItemPlacement {
        #if os(iOS)
        .topBarTrailing
        #else
        .automatic
        #endif
    }
}

extension WorkList.Kind {
    /// The list's row in the sidebar's Work group (#4004), in the accent as the other rows.
    var sidebarIcon: String {
        switch self {
        case .issue: "smallcircle.filled.circle"
        case .pr: "arrow.triangle.pull"
        case .release: "shippingbox"
        case .deploy: "server.rack"
        case .timeline: "timeline.selection"
        }
    }

    var sidebarHelp: String {
        switch self {
        case .issue: "Every issue in your folders’ repos, and what agents spent on each"
        case .pr: "Every pull request in your folders’ repos, and what agents spent on each"
        case .release: "The releases lineage knows, and what went into each"
        case .deploy: "Every component in every environment: what is live, since when, and what goes next"
        case .timeline: "Your epics in time: when each started, what closed, and when it is forecast to close"
        }
    }
}

/// One Work list: its filters, and a row per item (lists.md, #4393).
private struct WorkListPane: View {
    let model: any WorkModel
    let kind: WorkList.Kind
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var sizeClass
    private var compact: Bool { sizeClass == .compact }
    #else
    private let compact = false
    @Environment(\.openSettings) private var openSettings
    #endif
    /// Next ↗ on the Issues list (#5691): set only while this Mac can open the web page (#4937).
    @Environment(\.openWebPlace) private var openWebPlace
    @State private var nextProblem: String?
    /// The list's state lives in the model (#3257), so an item's page can go through it and Back finds it as it was.
    @Bindable var list: WorkListState

    private var errors: [String] {
        let machines = model.forgeMachines
        return (list.lists ?? []).flatMap { entry in
            (entry.list.errors ?? []).map { e in
                WorkList.errorWords(e, machine: machines.count > 1 ? machines.first { $0.id == entry.host }?.name : nil)
            }
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            if compact {
                filters.padding(.horizontal, 16).padding(.vertical, 6)
            } else {
                filters.padding(.horizontal, 16).padding(.vertical, 8)
                Divider()
            }
            content
        }
        #if os(iOS)
        // On a phone the search is the navigation bar's; the menus scroll under it.
        .modifier(PhoneSearch(on: compact, text: $list.filter.text, prompt: WorkLook.searchPrompt(kind)))
        #endif
        // Refresh in the toolbar; a phone has pull to refresh in its place (lists.md section 3).
        .toolbar {
            if kind == .issue, let openWebPlace {
                ToolbarItem(placement: .primaryAction) { nextLink(openWebPlace) }
            }
            if !compact {
                ToolbarItem(placement: .primaryAction) {
                    WorkRefreshButton(running: list.reading || list.loadingMore) { list.asked += 1 }
                }
            }
        }
        // The machines that read the forges are part of what the list asks (#6152, #6635): one that comes online, as at
        // the start, asks again, also when the sidebar has not read ahead.
        .onChange(of: model.workSource, initial: true) { _, source in model.workLists.source = source }
        .workTask(id: list.ask) { await list.load(model: model) }
        // A change to a kept item (#6354), from this list, another or an item's page: the kept answer again.
        .onChange(of: model.workSaves?.revision) { Task { await list.redrawCached(model: model) } }
        // A row menu's write that failed (epic #6086): the row has its old value back, and this says why.
        .alert(WorkLook.couldNotSetPriority, isPresented: Binding(get: { list.writeProblem != nil },
                                                                 set: { if !$0 { list.writeProblem = nil } })) {
            Button("OK") { list.writeProblem = nil }
        } message: {
            Text(list.writeProblem ?? "")
        }
        .onChange(of: model.workProjects, initial: true) { _, projects in list.projects = projects }
    }

    /// **Next ↗** (#5691): Work ▸ Next on the web page, for the Project filter's project, else the
    /// only one. A Web + Mac link (#4937, Lead role, #5290): the Remote sets no `openWebPlace`.
    private func nextLink(_ open: OpenWebPlace) -> some View {
        let projects = list.projectRepos.map(\.project)
        let project = projects.first { $0.id == list.filter.project } ?? (projects.count == 1 ? projects.first : nil)
        return Button("\(NextLink.title) ↗") {
            Task { nextProblem = await open.run(NextLink.place(project: project)) }
        }
        .help(NextLink.help)
        .alert(NextLink.cannotOpen, isPresented: Binding(get: { nextProblem != nil }, set: { if !$0 { nextProblem = nil } })) {
            Button("OK") { nextProblem = nil }
        } message: {
            Text(nextProblem ?? "")
        }
    }

    /// The filter bar (lists.md section 1): the search field, Project, State, Repo, Label,
    /// Assignee, Board, Clear, and Sort at the trailing edge.
    private var filters: some View {
        WorkFilterBar(kind: kind, text: $list.filter.text, compact: compact) { folded in
            choices(.bar(folded: folded))
        } more: {
            choices(.more)
        } trailing: {
            WorkFilterMenu(name: "Sort", selection: $list.sort, options: kind.sorts, words: \.title, compact: compact, named: true)
        }
    }

    /// Where a filter menu is drawn: in the bar (with `folded`, only the ones set), or in More.
    private enum Place: Equatable {
        case bar(folded: Bool)
        case more

        func shows(set: Bool) -> Bool {
            switch self {
            case let .bar(folded): set || !folded
            case .more: !set
            }
        }
    }

    @ViewBuilder
    private func choices(_ place: Place) -> some View {
        let offered = list.choices
        project(place)
        if kind.fromForge, place != .more {
            WorkFilterMenu(name: "State", selection: $list.filter.state, options: kind.states, words: \.title, compact: compact)
        }
        choice("Repo", any: "All", selection: $list.filter.repo, options: offered.repos, words: WorkList.repoName, place: place)
        choice("Label", any: "Any", selection: $list.filter.label, options: offered.labels, place: place)
        if kind.fromForge {
            choice("Assignee", any: "Any", selection: $list.filter.assignee, options: offered.assignees, place: place)
        }
        #if os(macOS)
        // Boards are the Mac's and the web page's (#3282, tier Mac + web): the machines narrow to one as they read.
        if kind == .issue, !list.boards.isEmpty || !list.filter.board.isEmpty, place.shows(set: !list.filter.board.isEmpty) {
            WorkFilterMenu(name: "Board", selection: $list.filter.board, options: boardOptions, words: { WorkList.boardName($0, in: list.boards) },
                           any: "", anyWords: "All boards", inMore: place == .more)
        }
        #endif
        // On a phone the empty list's Clear filters does it (lists.md section 3).
        if !compact, place != .more, WorkLook.narrows(list.filter) {
            WorkClearButton { list.filter = WorkLook.cleared(list.filter) }
        }
    }

    #if os(macOS)
    /// The boards read, and the one picked when the machines no longer list it.
    private var boardOptions: [String] {
        let urls = list.boards.map(\.url)
        let picked = list.filter.board
        return picked.isEmpty || urls.contains(picked) ? urls : urls + [picked]
    }
    #endif

    /// The Project filter (#3283): All projects, then each project whose folders read something.
    /// With one project it is not there, so a person with one sees Work as before.
    @ViewBuilder
    private func project(_ place: Place) -> some View {
        let projects = list.projectRepos.map(\.project)
        if projects.count > 1 || !list.filter.project.isEmpty, place.shows(set: !list.filter.project.isEmpty) {
            WorkFilterMenu(name: "Project", selection: $list.filter.project, options: projects.map(\.id),
                           words: { id in projects.first { $0.id == id }?.name ?? WorkList.allProjects },
                           any: "", anyWords: WorkList.allProjects, compact: compact, inMore: place == .more)
        }
    }

    @ViewBuilder
    private func choice(_ label: String, any: String, selection: Binding<String>, options: [String],
                        words: @escaping (String) -> String = { $0 }, place: Place) -> some View {
        if !options.isEmpty || !selection.wrappedValue.isEmpty, place.shows(set: !selection.wrappedValue.isEmpty) {
            WorkFilterMenu(name: label, selection: selection, options: options, words: words, any: "", anyWords: any,
                           compact: compact, inMore: place == .more)
        }
    }

    /// The list's body, and the footer bar under it in every state (#5968, #6639): what runs or failed, the count and
    /// Last update stay in one place at the bottom, with no strip above the rows.
    private var content: some View {
        let noMachine = kind.fromForge && model.forgeMachines.isEmpty
        // With no machine online, the kept rows stay (#6635), and the footer says why they are not new.
        let lines = [list.problem].compactMap { $0 } + errors + (noMachine && !list.all.isEmpty ? [WorkList.noForgeOnline] : [])
        return VStack(spacing: 0) {
            if noMachine, list.all.isEmpty {
                WorkEmptyState(symbol: kind.sidebarIcon, title: WorkLook.emptyTitle(kind, state: list.filter.state, filtered: false),
                               line: WorkList.noForgeHere)
            } else {
                switch WorkLook.listBody(drawn: list.lists != nil, read: list.all.count, shown: list.rows.count,
                                         reading: list.reading, failed: !lines.isEmpty) {
                case .placeholders:
                    // A read runs and there is nothing to draw: placeholder rows, never an empty state (#5925, #5968).
                    List { WorkPlaceholderRows() }.listStyle(.inset).disabled(true)
                case .failed:
                    // Nothing read: what the forge said in the middle, in place of the list.
                    WorkEmptyState(symbol: "exclamationmark.triangle", title: WorkLook.couldNotRead(kind),
                                   line: lines.joined(separator: "\n"), action: (WorkLook.tryAgain, { list.asked += 1 }),
                                   also: connectors(lines))
                        .modifier(PullToRefresh(list: list))
                case .empty:
                    empty.modifier(PullToRefresh(list: list))
                case .rows:
                    WorkRowsList(model: model, list: list, kind: kind, compact: compact)
                }
            }
            WorkListBar(model: model, list: list, kind: kind, lines: lines)
        }
    }

    /// Settings ▸ Connectors, beside Try again, when what a forge said is about its credential.
    private func connectors(_ lines: [String]) -> (title: String, run: () -> Void)? {
        #if os(macOS)
        guard lines.contains(where: WorkLook.credentialProblem) else { return nil }
        return (WorkLook.connectors, { if model.askWorkConnectors() { openSettings() } })
        #else
        nil
        #endif
    }

    /// The empty list: "No open issues" with a line under it, or "No issues match", the filters
    /// that are set, and Clear filters.
    private var empty: some View {
        let filtered = WorkLook.narrows(list.filter)
        let set = WorkLook.setFilters(list.filter, project: list.projectRepos.first { $0.project.id == list.filter.project }?.project.name,
                                      board: list.filter.board.isEmpty ? nil : WorkList.boardName(list.filter.board, in: list.boards))
        return WorkEmptyState(symbol: kind.sidebarIcon, title: WorkLook.emptyTitle(kind, state: list.filter.state, filtered: filtered),
                              line: filtered ? set : kind.fromForge ? WorkLook.emptyLine(kind, repos: Array(list.repos))
                                  : WorkList.lineageOnlyNote,
                              action: filtered ? (WorkLook.clearFilters, { list.filter = WorkLook.cleared(list.filter) }) : nil)
    }
}

/// The rows of a Work list (#6633), apart from the pane: SwiftUI's list diffs every row it holds each time its builder
/// runs, so the builder reads only the rows and what they show, not the reads, the footer or the toolbar.
private struct WorkRowsList: View {
    let model: any WorkModel
    @Bindable var list: WorkListState
    let kind: WorkList.Kind
    let compact: Bool
    /// Back from an item's page gives the list the keys, so ↑ and ↓ go on from the row left.
    @FocusState private var focused: Bool
    @Environment(\.openWorkWindow) private var openWorkWindow

    /// A row's menu: Open in New Window (#5475), then Priority and On hold.
    @ViewBuilder
    private func rowMenu(_ ref: String) -> some View {
        if openWorkWindow != nil {
            Section { WorkWindowButton(WorkPlace(kind: kind, ref: ref)) }
        }
        priorityMenu(ref)
    }

    /// A row's Priority and On hold (epic #6086), where its forge sets them; nothing for a row with neither.
    @ViewBuilder
    private func priorityMenu(_ ref: String) -> some View {
        if let row = list.all.first(where: { $0.id == ref }) {
            WorkRowPriorityMenu(row: row, sets: list.setsPriority(row, model: model),
                                setPriority: { list.set(priority: $0, on: row, model: model) },
                                setHold: { list.set(hold: $0, on: row, model: model) })
        }
    }

    private func open(_ ref: String) {
        list.opened = ref
        list.selected = ref
        model.follow(WorkPlace(kind: kind, ref: ref, fromList: true), window: openWorkWindow)
    }

    var body: some View {
        let several = list.repos.count > 1
        let latest = kind == .release ? WorkReleaseLook.latest(list.all) : []
        // The sessions this app knows, of every machine (#5453): a row shows the one that works on its item now.
        let agents = kind == .release ? [] : model.workAgents
        return rows(several: several, latest: latest, agents: agents)
    }

    private func rows(several: Bool, latest: Set<String>, agents: [Agent]) -> some View {
        ScrollViewReader { reader in
            selectable {
                // The first `drawn` rows (#6633); the footer draws 100 more as it shows.
                ForEach(list.drawnRows) { row in
                    // One view a row, with no modifier outside it: SwiftUI's list walks each element's modifiers for
                    // every row it diffs (#6633). The ForEach gives the row its id, which `scrollTo` finds; the row tags
                    // itself for the selection.
                    WorkListRow(model: model, row: row, several: several, compact: compact, latest: latest.contains(row.id), pick: pick,
                                session: kind == .release ? nil : WorkSessions.workingSession(on: row.node, in: agents),
                                open: open, menu: { rowMenu(row.id) })
                }
                WorkListFooter(model: model, list: list, kind: kind)
                if !kind.fromForge {
                    Text(WorkList.lineageOnlyNote).appText(.fine).foregroundStyle(.tertiary).selectionDisabled()
                }
            }
            .listStyle(.inset)
            #if os(iOS)
            .refreshable { list.asked += 1 }
            #endif
            // Back from an item's page: the list where it was left, at the row last opened, selected.
            .onAppear {
                guard let opened = list.opened else { return }
                list.selected = opened
                reader.scrollTo(opened, anchor: .center)
                focused = !compact
            }
        }
    }

    /// The Mac's list keeps a selection that ↑ and ↓ move and Return opens; so does an iPad's with
    /// a keyboard (lists.md section 3). On a phone a tap opens the row, with no selection.
    @ViewBuilder
    private func selectable(@ViewBuilder _ rows: () -> some View) -> some View {
        #if os(macOS)
        List(selection: $list.selected) { rows() }
            .focused($focused)
            .contextMenu(forSelectionType: String.self) { refs in
                if refs.count == 1, let ref = refs.first { rowMenu(ref) }
            } primaryAction: { refs in
                if let ref = refs.first { open(ref) }
            }
        #else
        if compact {
            List { rows() }
        } else {
            // A tap goes to the row's button; Return opens the row the keys selected.
            List(selection: $list.selected) { rows() }
                .focused($focused)
                .onKeyPress(.return) {
                    guard let ref = list.selected else { return .ignored }
                    open(ref)
                    return .handled
                }
        }
        #endif
    }

    /// A click on a chip sets the Label filter to it (Mac); on a phone a tap opens the row.
    private var pick: ((String) -> Void)? {
        #if os(macOS)
        { list.filter.label = $0 }
        #else
        nil
        #endif
    }
}

/// The end of a Work list (lists.md sections 2 and 5), apart from the rows (#6633): a page read or a save changes it
/// alone.
private struct WorkListFooter: View {
    let model: any WorkModel
    @Bindable var list: WorkListState
    let kind: WorkList.Kind

    /// The end of the list (lists.md sections 2 and 5): the next page loads when this row shows. The bar under the
    /// list says "Loading more…", a page that failed, and the count (#6639, #5968).
    var body: some View {
        Color.clear.frame(height: 1)
            .frame(maxWidth: .infinity)
            .listRowSeparator(.hidden)
            .selectionDisabled()
            // Shown, and again each time it adds rows while it still shows: 100 more of the rows read (#6633), and once
        // all are drawn, the next page.
            .workTask(id: FooterShown(rows: list.all.count, drawn: list.drawn)) {
                if list.drawMore() { return }
                guard list.hasMore, !list.loadingMore, list.pageProblem == nil else { return }
                await list.loadMore(model: model)
            }
    }

    /// What the footer's read waits on: a page read, or 100 more rows drawn.
    private struct FooterShown: Equatable {
        var rows: Int
        var drawn: Int
    }
}

/// The footer bar of a Work list (#6639, #5968; gate 2 of epic #6348), under the list, so it stays in sight. On the
/// left, a change's "Saving #6311…" or why the forge refused it, with Try again (#6354); else the read: Loading…,
/// Updating…, From this device, or what failed with Try again; else "Loading more…" or a page that failed. On the right,
/// "100 of 304 issues · Last update 2 min ago" (#5139, #5967).
private struct WorkListBar: View {
    let model: any WorkModel
    let list: WorkListState
    let kind: WorkList.Kind
    /// What the forges or the machines said, one line each.
    let lines: [String]

    var body: some View {
        let shown = list.rows.count
        let total = Self.total(list)
        let state = WorkLook.footerState(drawn: list.lists != nil, read: list.all.count, reading: list.reading,
                                         fromDevice: list.fromDevice, lines: lines)
        HStack(spacing: 6) {
            if let saves = model.workSaves, let line = saves.line {
                WorkSaveLine(line: line) {
                    if let cache = model.workCache { saves.retry(model: model, cache: cache) }
                }
            } else if state != .none {
                // A read that runs or failed says so before a next page does: a Refresh says Updating… (#5968).
                WorkReadLine(state: state) { list.asked += 1 }
            } else if list.loadingMore {
                ProgressView().controlSize(.mini)
                Text(WorkLook.loadingMore(shown: shown, total: total))
            } else if let failed = list.pageProblem {
                WorkPageFailed(why: failed) { Task { await list.loadMore(model: model) } }
            }
            Spacer(minLength: 12)
            if list.lists != nil, state != .couldNotRead, state != .loading {
                Text(WorkLook.shownOf(kind, shown: shown, more: list.hasMore, total: total))
                if let read = list.shownRead {
                    Text(verbatim: "·")
                    LastUpdateText(read: read)
                }
            }
        }
        .workFooterBar()
    }

    /// The forge's count of the list (#4396), where it gives one and the list shows all it counts.
    static func total(_ list: WorkListState) -> Int? {
        let totals = list.lists?.map(\.list.total) ?? []
        return list.cacheOn ? WorkLook.cachedListTotal(totals, filter: list.filter) : WorkLook.listTotal(totals, filter: list.filter)
    }
}

/// "Could not load more" with Try again, and why on hover.
private struct WorkPageFailed: View {
    let why: String
    let retry: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Text(WorkLook.couldNotLoadMore).help(why)
            Button(WorkLook.tryAgain, action: retry).buttonStyle(.paper)
        }
    }
}

/// The look of a Work page's footer bar (#5968, #5969): the caption step in grey on the bar's material, with a rule
/// above it.
private struct WorkFooterBarLook: ViewModifier {
    func body(content: Content) -> some View {
        content
            .appText(.fine)
            .foregroundStyle(.secondary)
            .monospacedDigit()
            .padding(.horizontal, 16)
            .padding(.vertical, 6)
            .frame(maxWidth: .infinity)
            .background(.bar)
            .overlay(alignment: .top) { Divider() }
    }
}

extension View {
    func workFooterBar() -> some View { modifier(WorkFooterBarLook()) }
}

/// The footer bar of a Work page that is not a list (Deploys, Timeline; #5969): on the left, the read (Loading…,
/// Updating…, Reading 2 of 3 machines…, From this device, or what failed with Try again); on the right, the count,
/// then "Last update 72 min ago" (#5139) once a read has ended.
struct WorkPageBar: View {
    let state: WorkLook.FooterState
    /// "4 targets", once something is drawn.
    let count: String?
    let read: Date?
    let retry: () -> Void

    var body: some View {
        HStack(spacing: 6) {
            WorkReadLine(state: state, retry: retry)
            Spacer(minLength: 12)
            if state != .couldNotRead, state != .loading {
                if let count { Text(count) }
                if let read {
                    if count != nil { Text(verbatim: "·") }
                    LastUpdateText(read: read)
                }
            }
        }
        .workFooterBar()
    }
}

/// The footer's read (#5968): a spinner and Loading…, Updating… or From this device; or a warning glyph and what
/// failed, every line on hover, with Try again where rows are shown.
struct WorkReadLine: View {
    let state: WorkLook.FooterState
    let retry: () -> Void

    var body: some View {
        if state.busy {
            ProgressView().controlSize(.mini)
        } else if state.failure {
            Image(icon: "exclamationmark.triangle").foregroundStyle(StateTint.failure.style(or: .secondary)).accessibilityHidden(true)
        }
        if state != .none {
            Text(state.words).lineLimit(1).truncationMode(.tail).help(Self.help(state))
        }
        if case .couldNotUpdate = state {
            #if os(macOS)
            Button(WorkLook.tryAgain, action: retry).buttonStyle(.link)
            #else
            Button(WorkLook.tryAgain, action: retry).buttonStyle(.borderless)
            #endif
        }
    }

    private static func help(_ state: WorkLook.FooterState) -> String {
        if case .couldNotUpdate(_, let help) = state { return help }
        return state.words
    }
}

/// Pull to refresh on the Remote's empty and failed lists (lists.md section 3); nothing on the Mac.
private struct PullToRefresh: ViewModifier {
    let list: WorkListState

    func body(content: Content) -> some View {
        #if os(iOS)
        content.refreshable { list.asked += 1 }
        #else
        content
        #endif
    }
}

/// One row of a Work list (#6633): the item's button with its look, its pictures, its dimming and its wash.
private struct WorkListRow<Menu: View>: View {
    let model: any WorkModel
    let row: WorkList.Row
    let several: Bool
    let compact: Bool
    let latest: Bool
    let pick: ((String) -> Void)?
    let session: Agent?
    let open: (String) -> Void
    @ViewBuilder let menu: () -> Menu

    var body: some View {
        Button { open(row.id) } label: {
            WorkItemRow(row: row, several: several, compact: compact, latest: latest, pick: pick, session: session,
                        openSession: { model.openWorkSession($0) })
        }
        .buttonStyle(WorkRowButtonStyle())
        #if os(iOS)
        .contextMenu { menu() }
        .workWindowDrag(WorkPlace(kind: WorkList.kind(of: row.node) ?? .issue, ref: row.id), title: row.title)
        #endif
        .environment(\.workPictures, row.host.map { WorkPictures(model: model, host: $0) })
        // A change the forge refused went back (#6354): a short red wash on the row.
        .workWashed(model.workSaves?.isWashed(row.node) == true)
        .tag(row.id)
    }
}

/// A row as a button: plain on the Mac, whose list draws the selection; on the Remote the row
/// shows pressed (grey) as a tap opens it (lists.md section 3).
private struct WorkRowButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        #if os(iOS)
        configuration.label
            .background(Color.secondary.opacity(configuration.isPressed ? 0.15 : 0).padding(.horizontal, -16).padding(.vertical, -4))
        #else
        configuration.label
        #endif
    }
}

#if os(iOS)
/// The phone's search field in the navigation bar; on an iPad the bar's own field stays.
private struct PhoneSearch: ViewModifier {
    let on: Bool
    @Binding var text: String
    let prompt: String

    func body(content: Content) -> some View {
        if on { content.searchable(text: $text, prompt: prompt) } else { content }
    }
}
#endif

/// The state as a word in a pill: open, closed, merged or draft; or the tracker's own status (a
/// Jira status, #3260), in the colour of the state it is in.
struct WorkStatePill: View {
    let state: String
    var draft = false
    var status: String?

    var body: some View {
        if !state.isEmpty {
            let word = draft && state == "open" ? "draft" : state
            Text(status ?? word)
                .appText(.fine)
                .foregroundStyle(.white)
                .padding(.horizontal, 6)
                .padding(.vertical, 1)
                .background(Capsule().fill(color(word)))
        }
    }

    /// The forge's own state colours, as the web page draws them: these say the item's state on
    /// its forge, not the app's (`StateTint`).
    private func color(_ word: String) -> Color {
        switch word {
        case "open": Paper.forgeFillOpen
        case "merged": Paper.forgeFillDone
        case "closed": Paper.forgeFillClosed
        default: Paper.forgeFillDraft
        }
    }
}
