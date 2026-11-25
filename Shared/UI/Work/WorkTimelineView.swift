import AgentsKitCore
#if canImport(AppKit)
import AppKit
#else
import UIKit
#endif
import Observation
import SwiftUI

/// Work ▸ Timeline's state (#4173): the range, the project and board, and what `work/timeline` read
/// for the time around the window. The model keeps it, so Back from an issue's page finds the
/// Timeline as it was left.
@MainActor
@Observable
final class WorkTimelineState {
    /// The range, the project and the board are kept for the next launch (#5451).
    var range: WorkTimeline.Range = .twoDays { didSet { remember() } }
    /// The project, by id; "" until one is picked, then the first one.
    var project = "" { didSet { remember() } }
    /// The board the epics are narrowed to (#3282), by its url; "" for every board.
    var board = "" { didSet { remember() } }
    var boards: [DaemonAPI.ProjectBoard] = []
    /// Where the window is: Now, until the plot is scrolled past an edge.
    var anchor = Date.now
    var timeline: DaemonAPI.WorkTimeline?
    var problem: String?
    /// A read runs: the toolbar's Refresh spins.
    var reading = false
    var asked = 0
    /// The read that ended last: until it is the window's ask, a read of it is still to come, so the empty card
    /// (S11) waits, also on a re-read (a range, project or board change, Refresh; #5969).
    private var done: Ask?
    /// The machines the read that runs asks, and how many have answered: the footer says "Reading 1 of 3 machines…".
    var asking = 0
    var answered = 0
    /// Now was clicked: the plot scrolls back to it.
    var nowAsked = 0
    /// Every machine's sessions on the epics and their children, from the control plane's merged
    /// copy (#4276): a sleeping machine's sessions still start their children.
    var sessions: [DaemonAPI.LineageSessionOn] = []
    private var read: Ask?
    /// The machines' timelines as one, before the sessions start their children.
    private var fromMachines: DaemonAPI.WorkTimeline?

    struct Ask: Equatable {
        var asked: Int
        var project: String
        var board: String
        var range: WorkTimeline.Range
        var anchor: Date
    }

    var ask: Ask { Ask(asked: asked, project: project, board: board, range: range, anchor: anchor) }

    /// A read of the window runs or is still to start.
    var pending: Bool { reading || done != ask }

    init() {
        let kept = WorkRemembered.timeline(WorkRememberedStore.kept(.timeline))
        range = kept.range
        project = kept.project
        board = kept.board
    }

    private func remember() {
        WorkRememberedStore.keep(WorkRemembered.timeline(range: range, project: project, board: board), for: .timeline)
    }

    /// The plot on screen came from this device's copy (#4930), until the machines answer.
    var fromDevice = false
    /// When the plot on screen was read (#5142): this device's copy's time, or the last read
    /// that worked. The footer says it as "Last update", also when a later read failed.
    var plotRead: Date?

    /// Work ▸ Timeline as this device keeps it (#4930): the plot around now, and the boards.
    struct Kept: Codable, Sendable {
        var timeline: DaemonAPI.WorkTimeline
        var boards: [DaemonAPI.ProjectBoard]
    }

    /// What each machine read (#7029), and what Work read from then (`workSource`).
    private struct KeptScopes: Codable, Sendable {
        var scopes: [MachineRef: [String]]
        var source: String
    }

    /// A machine's answer (its index, the timeline, the board's epics, what failed), or `lateAfter` is over.
    private enum Read: Sendable {
        case answer(Int, DaemonAPI.WorkTimeline?, [String]?, String?, boardFailed: Bool)
        case late
    }

    /// What the plot is kept under: the project, board and range.
    private func copyAsk(_ project: String) -> String { [project, board, range.rawValue].joined(separator: "|") }

    /// What the plot holds: the window, and a range's width on each side of it to scroll through.
    var content: DateInterval {
        let window = range.window(now: anchor)
        return DateInterval(start: window.start.addingTimeInterval(-range.span),
                            end: window.end.addingTimeInterval(range.span))
    }

    /// The projects the Timeline can draw: those with a folder on a machine that draws it.
    func projects(model: any WorkModel) -> [WorkList.Project] {
        let machines = Set(model.timelineMachines.map(\.id.rawValue))
        return model.timelineProjects.filter { project in
            project.folders.contains { machines.contains(String($0.prefix { $0 != "|" })) }
        }
    }

    func load(model: any WorkModel) async {
        let mine = ask
        guard read != mine || timeline == nil else { return }
        read = mine
        reading = true
        defer {
            if ask == mine {
                reading = false
                done = mine
            }
        }
        let projects = projects(model: model)
        guard let project = projects.first(where: { $0.id == self.project }) ?? projects.first else {
            timeline = .init()
            return
        }
        if self.project != project.id { self.project = project.id }
        // Seen before: this device's copy at once, until the machines answer (#4930).
        let kept = copyAsk(project.id)
        if timeline == nil, case let (copy, at)? = await model.keptWorkRead(Kept.self, .timeline, kept), ask == mine, timeline == nil {
            timeline = copy.timeline
            boards = copy.boards
            plotRead = at
            fromDevice = true
        }
        // The boards beside the timeline, not before it (#5450).
        async let found = model.workBoards()
        let on = Set(project.folders.map { String($0.prefix { $0 != "|" }) })
        let machines = model.timelineMachines.filter { on.contains($0.id.rawValue) }
        // Each repo is asked of one machine (#7029). What each read last is kept for the next launch.
        let source = model.workSource
        let keptScopes = await model.keptWork(KeptScopes.self, .timeline, "scopes|\(project.id)")
        guard ask == mine else { return }
        var readers = WorkTimeline.Readers(machines.map(\.id), scopes: keptScopes?.source == source ? keptScopes?.scopes ?? [:] : [:])
        asking = 0
        answered = 0
        // The widest range's span, whatever the range: the rows are packed over it, so they stay put
        // across ranges (#5506).
        let span = WorkTimeline.packSpan(anchor)
        let board = board
        // A machine answers with its epics first, and again with the forecast when its history is read (#7027): its
        // first answer is in firsts until then, and stays there when the second fails.
        var got: [DaemonAPI.WorkTimeline] = []
        var firsts: [MachineRef: DaemonAPI.WorkTimeline] = [:]
        var epics: Set<String>?
        var failed: [String] = []
        var noBoard: Set<MachineRef> = []
        func draw(all: Bool) async {
            problem = all && got.isEmpty && !failed.isEmpty ? failed.joined(separator: "\n") : nil
            // Every machine failed: the last plot stays, or with none, what they said in the middle (#5969).
            if all, got.isEmpty, !failed.isEmpty { return }
            var merged = WorkTimeline.merge(got + firsts.values)
            if !firsts.isEmpty { merged = WorkTimeline.keepForecasts(merged, shown: timeline) }
            if let epics { merged.epics = merged.epics.filter { epics.contains($0.item.node.ref) } }
            fromMachines = merged
            // The plot draws now, with the sessions it had (#5450); the new ones start their children when they come.
            timeline = WorkTimeline.started(merged, by: sessions)
            plotRead = Date()
            fromDevice = false
            let on = await model.lineageSessionsOn(WorkTimeline.sessionNodes(merged))?.sessions ?? []
            guard ask == mine, fromMachines == merged else { return }
            sessions = on
            let drawn = WorkTimeline.started(merged, by: on)
            timeline = drawn
            plotRead = Date()
            fromDevice = false
            // Kept for the window around now, read by every machine: a plot scrolled away is not the one a person opens on.
            if all, failed.isEmpty, abs(anchor.timeIntervalSinceNow) < 3600 {
                model.keepWork(Kept(timeline: drawn, boards: boards), .timeline, kept)
            }
        }
        await withTaskGroup(of: Read.self) { group in
            func add(_ index: Int, first: Bool) {
                let machine = machines[index]
                group.addTask {
                    // The result first, then the tuple from one place (#5946, `Result.catching`).
                    let read = await Result.catching {
                        try await model.forge(DaemonAPI.Method.workTimeline,
                                              DaemonAPI.WorkTimelineRequest(project: project.id, from: span.start, to: span.end,
                                                                            first: first ? true : nil),
                                              returning: DaemonAPI.WorkTimeline.self, on: machine.id)
                    }
                    // A machine whose forge failed answers with no epic and the forge's words: a failed read, so the
                    // plot and its age stay (#5969).
                    let answer = try? read.get()
                    let forgeFailed = answer.map { $0.epics.isEmpty && !($0.errors ?? []).isEmpty } ?? false
                    let t = forgeFailed ? nil : answer
                    // A board narrows to its epics: the forge says which they are.
                    var refs: [String]?
                    var boardFailed = false
                    if first, t != nil, !board.isEmpty {
                        let list = try? await model.forgeList(.issue, .init(state: "all", label: "epic", limit: 100, board: board),
                                                              on: machine.id)
                        refs = list?.items.map(\.node.ref) ?? []
                        if list == nil || list?.items.isEmpty == true && list?.errors?.isEmpty == false { boardFailed = true }
                    }
                    let words = read.error?.localizedDescription ?? (forgeFailed ? answer?.errors?.joined(separator: "\n") : nil)
                    let problem = words.map { machines.count > 1 ? "\(machine.name): \($0)" : $0 }
                    return .answer(index, t, refs, problem, boardFailed: boardFailed)
                }
            }
            var asks = 0
            func addNext() {
                for id in readers.next() {
                    guard let index = machines.firstIndex(where: { $0.id == id }) else { continue }
                    asks += 1
                    add(index, first: true)
                }
            }
            addNext()
            asking = asks
            group.addTask {
                try? await Task.sleep(for: WorkTimeline.lateAfter)
                return .late
            }
            // The first machine to answer draws the plot (#5449), and each one after adds its epics: a slow or offline
            // machine does not hold it. A machine that failed says so only when no machine answered.
            var answered = 0
            while answered < asks, let read = await group.next() {
                guard ask == mine else { break }
                guard case let .answer(index, t, refs, problem, boardFailed) = read else {
                    if Task.isCancelled { continue }
                    readers.late()
                    addNext()
                    asking = asks
                    continue
                }
                let machine = machines[index].id
                // Its board could not be read: it covers no other machine (the next one's board can be).
                if boardFailed { noBoard.insert(machine) }
                if let refs { epics = (epics ?? []).union(refs) }
                if let t, t.partial == true {
                    readers.first(machine)
                    add(index, first: false)
                    if !t.epics.isEmpty {
                        firsts[machine] = t
                        await draw(all: false)
                    }
                    continue
                }
                answered += 1
                self.answered = answered
                if let t {
                    got.append(t)
                    firsts[machine] = nil
                }
                if let problem { failed.append(problem) }
                readers.done(machine, scopes: noBoard.contains(machine) ? nil : t?.scopes)
                addNext()
                asking = asks
                if answered < asks, t != nil { await draw(all: false) }
            }
            group.cancelAll()
        }
        let boardsFound = await found
        guard ask == mine else { return }
        boards = boardsFound
        model.keepWork(KeptScopes(scopes: readers.scopes, source: source), .timeline, "scopes|\(project.id)")
        await draw(all: true)
    }

    /// The sessions again, on `control/lineageChanged` (#4276): a session that starts on a child on
    /// any machine starts it, with no read of the machines' forges.
    func readSessions(model: any WorkModel) async {
        guard let machines = fromMachines, let got = await model.lineageSessionsOn(WorkTimeline.sessionNodes(machines)),
              fromMachines == machines else { return }
        sessions = got.sessions
        timeline = WorkTimeline.started(machines, by: got.sessions)
    }
}

/// Work ▸ Timeline (#4173): time across, each epic one block in its own row (#7058): solid from when its
/// first child started to when the work stopped (#6043), then for an open epic a ghost (a lighter tint with
/// a dashed edge) to its predicted finish, and a thin bar to where it closes 8 times in 10 (#5506, Alex,
/// 2026-11-14). A SwiftUI
/// `Canvas` (spike #4171): the rows and the marks come from `WorkTimeline` in AgentsKitCore.
/// Core since #6044: on the Remote the bar's controls are the navigation bar's, a tap shows the card
/// that a pointer's hover shows on the Mac, and a phone in portrait draws the list of epics.
struct WorkTimelinePane: View {
    let model: any WorkModel
    @Bindable var state: WorkTimelineState
    @Environment(\.colorScheme) private var scheme
    @Environment(\.openWorkWindow) private var openWorkWindow
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var widthClass
    @Environment(\.verticalSizeClass) private var heightClass
    /// A phone in portrait: the list of epics, as the web page under 700 px (note 36, #6044).
    private var listed: Bool { widthClass == .compact && heightClass == .regular }
    #else
    private let listed = false
    #endif

    @State private var position = ScrollPosition(edge: .leading)
    @State private var offset: Double = 0
    /// How far the rows are scrolled down: the band names in the gutter follow them (#6089).
    @State private var top: Double = 0
    @State private var hover: Hover?

    struct Hover: Equatable {
        var line: WorkTimeline.Line
        var at: CGPoint
    }

    nonisolated static let axisHeight: Double = 26
    nonisolated static let footHeight: Double = 26
    /// The gutter at the leading edge with each band's name (#6089).
    nonisolated static let gutterWidth: Double = 64

    var body: some View {
        VStack(spacing: 0) {
            #if os(macOS)
            bar.padding(.horizontal, 16).padding(.vertical, 8)
            Divider()
            #else
            // The Remote's bar is the navigation bar's; the title line stays above the plot.
            if let title {
                Text(title).appText(.fine).foregroundStyle(.secondary).lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 16).padding(.vertical, 6)
            }
            #endif
            content
        }
        .toolbar {
            #if os(macOS)
            ToolbarItem(placement: .primaryAction) {
                WorkRefreshButton(running: state.reading) { state.asked += 1 }
            }
            #else
            ToolbarItemGroup(placement: .topBarTrailing) { navigationMenus }
            #endif
        }
        .workTask(id: state.ask) { await state.load(model: model) }
        // A machine sent new lineage to the control plane (#4276): the sessions again, no polling.
        .task(id: model.lineageChanges) {
            guard model.lineageChanges > 0 else { return }
            try? await Task.sleep(for: .milliseconds(500))
            guard !Task.isCancelled else { return }
            await state.readSessions(model: model)
        }
        // Read again every five minutes, so closes land while the page is open.
        .task(id: state.project) {
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(300))
                state.asked += 1
            }
        }
    }

    private var projects: [WorkList.Project] { state.projects(model: model) }

    // MARK: The bar

    /// The filter bar (parts.md section 4, milestones.md notes 31 and 32): Project and Board as
    /// compact menus and the title line at the leading edge; the range and Now at the trailing edge,
    /// where Sort is on a list. Refresh is the toolbar's. The forecast is always drawn (Alex, 2026-11-10).
    private var bar: some View {
        HStack(spacing: 14) {
            if projects.count > 1 {
                WorkFilterMenu(name: "Project", selection: $state.project, options: projects.map(\.id),
                               words: { id in projects.first { $0.id == id }?.name ?? id }, named: true)
            }
            if !state.boards.isEmpty || !state.board.isEmpty {
                WorkFilterMenu(name: "Board", selection: $state.board, options: boardOptions,
                               words: { WorkList.boardName($0, in: state.boards) }, any: "", anyWords: "All boards")
            }
            if let title {
                Text(title)
                    .appText(.supporting)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.tail)
                    .help(state.timeline?.backtest.map { "The forecast after now. \(WorkTimeline.errorWords($0))." }
                        ?? "The forecast after now")
            }
            Spacer(minLength: 0)
            Picker("Range", selection: $state.range) {
                ForEach(WorkTimeline.Range.allCases) { Text($0.title).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .fixedSize()
            .onChange(of: state.range) { goToNow() }
            Button("Now") { goToNow() }
                .buttonStyle(.bordered)
                .help("Back to now")
        }
    }

    #if os(iOS)
    /// The Remote's bar (#6044): the range, Project and Board as the navigation bar's menus, and Now.
    /// Refresh is pull to refresh. Board shows only where the machines read boards (Mac + web, #3282).
    @ViewBuilder
    private var navigationMenus: some View {
        Menu {
            Picker("Range", selection: $state.range) {
                ForEach(WorkTimeline.Range.allCases) { Text($0.title).tag($0) }
            }
            if projects.count > 1 {
                Picker("Project", selection: $state.project) {
                    ForEach(projects) { Text($0.name).tag($0.id) }
                }
                .pickerStyle(.menu)
            }
            if !state.boards.isEmpty || !state.board.isEmpty {
                Picker("Board", selection: $state.board) {
                    Text("All boards").tag("")
                    ForEach(boardOptions, id: \.self) { Text(WorkList.boardName($0, in: state.boards)).tag($0) }
                }
                .pickerStyle(.menu)
            }
        } label: {
            Text(state.range.title)
        }
        .accessibilityLabel("Range, \(state.range.title)")
        .onChange(of: state.range) { goToNow() }
        Button("Now") { goToNow() }
            .accessibilityHint("Back to now")
    }
    #endif

    /// The boards read, and the one picked when the machines no longer list it.
    private var boardOptions: [String] {
        let urls = state.boards.map(\.url)
        return state.board.isEmpty || urls.contains(state.board) ? urls : urls + [state.board]
    }

    private var projectName: String { projects.first { $0.id == state.project }?.name ?? "" }

    /// "Agents · 6 epics in these 2 days", once a read has drawn something.
    private var title: String? {
        guard let timeline = state.timeline, !projects.isEmpty else { return nil }
        let lines = WorkTimeline.lines(timeline, now: .now)
        // With the Project menu beside it, the menu names the project.
        return WorkTimeline.titleWords(project: projects.count > 1 ? "" : projectName, epics: WorkTimeline.epicCount(lines, in: state.range.window(now: state.anchor)),
                                       range: state.range)
    }

    private func goToNow() {
        state.anchor = .now
        state.nowAsked += 1
    }

    // MARK: The plot

    @ViewBuilder
    private var content: some View {
        if model.timelineMachines.isEmpty {
            WorkEmptyState(symbol: WorkList.Kind.timeline.sidebarIcon, title: "No machine draws the timeline",
                           line: WorkTimeline.noMachine)
        } else if state.timeline != nil, projects.isEmpty {
            // S12: no project to draw.
            WorkEmptyState(symbol: WorkList.Kind.timeline.sidebarIcon, title: WorkTimeline.noProject,
                           line: WorkTimeline.noProjectLine)
        } else {
            // S10: before the first read, the axis and Now draw at once, and grey blocks shimmer in rows.
            let timeline = state.timeline ?? .init()
            // What runs or failed, the count and Last update are in the footer bar (#5969), not in strips; a plot
            // whose later read failed stays at full opacity (#5967's rule).
            let lines = problems(timeline)
            let drawn = state.timeline != nil
            VStack(spacing: 0) {
                if !drawn, !state.pending, !lines.isEmpty {
                    // Nothing read: what the machines said in the middle, with Try again.
                    WorkEmptyState(symbol: "exclamationmark.triangle", title: WorkTimeline.couldNotRead,
                                   line: lines.joined(separator: "\n"), action: (WorkLook.tryAgain, { state.asked += 1 }))
                        .frame(maxHeight: .infinity)
                } else {
                    // Once a minute; while a read runs, often, so the grey blocks shimmer when they draw.
                    let busy = !drawn || state.pending
                    TimelineView(.animation(minimumInterval: busy && !reduceMotion ? 1.0 / 20 : 60)) { context in
                        if listed {
                            epicList(timeline, now: context.date, drawn: drawn)
                        } else {
                            plot(timeline, now: context.date, drawn: drawn, reading: state.pending)
                        }
                    }
                }
                WorkPageBar(state: WorkLook.pageFooterState(drawn: drawn, reading: state.pending, fromDevice: state.fromDevice,
                                                            lines: lines, answered: state.answered, machines: state.asking),
                            count: drawn ? WorkLook.pageCount(epics(timeline), "epic") : nil,
                            read: drawn ? state.plotRead : nil) { state.asked += 1 }
            }
        }
    }

    /// The epics in the window, for the footer's count.
    private func epics(_ timeline: DaemonAPI.WorkTimeline) -> Int {
        WorkTimeline.epicCount(WorkTimeline.lines(timeline, now: .now), in: state.range.window(now: state.anchor))
    }

    private func problems(_ timeline: DaemonAPI.WorkTimeline) -> [String] {
        (state.problem.map { $0.components(separatedBy: "\n") } ?? []) + (timeline.errors ?? [])
    }

    private func plot(_ timeline: DaemonAPI.WorkTimeline, now: Date, drawn: Bool, reading: Bool) -> some View {
        GeometryReader { geo in
            let visible = max(geo.size.width - Self.gutterWidth, 1)
            let content = state.content
            let axis = WorkTimeline.Axis(interval: content, width: visible * content.duration / state.range.span)
            // The epics and children a session is in a turn on now (#4265): their open blocks glow.
            let working = WorkTimeline.workingNow(timeline, sessions: state.sessions, agents: model.workAgents)
            // Packed over the widest range's span, so a block keeps its row when the range changes or the plot
            // scrolls (#5506).
            let lines = WorkTimeline.layout(
                WorkTimeline.overlapping(WorkTimeline.lines(timeline, now: now, working: working), WorkTimeline.packSpan(state.anchor)))
            // S10 until a read draws, and while one runs with no epic in the window; S11 only after every read (#5925).
            let shows = WorkTimeline.shows(drawn: drawn, reading: reading,
                                           epics: WorkTimeline.overlapping(lines, state.range.window(now: state.anchor)).count)
            let loading = shows == .loading
            let rows = max(WorkTimeline.rowCount(lines), 3)
            let height = max(WorkTimeline.rowTop(rows) + WorkTimeline.rowGap, geo.size.height - Self.axisHeight - Self.footHeight)
            let drawing = Drawing(lines: lines, axis: axis, now: now, height: height, offset: offset, visible: visible,
                                  night: scheme == .dark, loading: loading, moving: !reduceMotion,
                                  bands: loading ? [] : WorkTimeline.bands(lines))
            HStack(spacing: 0) {
            gutter(drawing)
            ScrollView(.horizontal) {
                VStack(spacing: 0) {
                    // The axis at the top, the blocks under it, and Now's time at the foot (#5506).
                    Canvas { context, _ in drawing.drawAxis(&context) }
                        .frame(width: axis.width, height: Self.axisHeight)
                        .accessibilityHidden(true)
                    ScrollView(.vertical) {
                        canvas(drawing)
                            .frame(width: axis.width, height: height)
                    }
                    #if os(iOS)
                    .refreshable { state.asked += 1 }
                    #endif
                    .onScrollGeometryChange(for: Double.self) { Double($0.contentOffset.y + $0.contentInsets.top) } action: { _, y in
                        top = y
                    }
                    Canvas { context, _ in drawing.drawNow(&context) }
                        .frame(width: axis.width, height: Self.footHeight)
                        .accessibilityHidden(true)
                }
                .frame(width: axis.width, height: geo.size.height)
            }
            .scrollPosition($position)
            // Not under the sidebar: the plot is the pane's.
            .clipped()
            // The offset is under the sidebar's inset: the plot's left edge is past it.
            .onScrollGeometryChange(for: Double.self) { Double($0.contentOffset.x + $0.contentInsets.leading) } action: { _, x in
                offset = x
            }
            // A person's scroll past an edge moves the window, not the first layout's.
            .onScrollPhaseChange { old, new in
                if new == .idle, old == .interacting || old == .decelerating { shift(x: offset, visible: visible) }
            }
            // The window starts a screen in: a range's width is before it.
            .onAppear { scroll(to: visible) }
            .onChange(of: state.nowAsked) { scroll(to: visible) }
            .gesture(MagnifyGesture().onEnded { value in
                // A pinch steps the range: apart is closer in, together further out.
                if value.magnification > 1.25 || value.magnification < 0.8 {
                    state.range = state.range.zoomed(in: value.magnification > 1)
                }
            })
            .overlay {
                // S11: no line in the window itself; the margin on each side is only for the scroll.
                if shows == .empty {
                    emptyCard(timeline, now: now)
                }
            }
            }
        }
    }

    // MARK: The list of epics

    /// A phone in portrait (#6044): the epics in the window, in the order they close, under *Now · 2:40 PM*, as
    /// the web page draws them under 700 px (note 36). A tap opens the epic.
    private func epicList(_ timeline: DaemonAPI.WorkTimeline, now: Date, drawn: Bool) -> some View {
        let working = WorkTimeline.workingNow(timeline, sessions: state.sessions, agents: model.workAgents)
        let lines = WorkTimeline.lines(timeline, now: now, working: working)
        let window = state.range.window(now: state.anchor)
        let rows = WorkTimeline.epicRows(lines, window: window, now: now)
        let shows = WorkTimeline.shows(drawn: drawn, reading: state.pending, epics: rows.count)
        let night = scheme == .dark
        return List {
            Text(WorkTimeline.nowWords(now)).appText(.fine).fontWeight(.semibold).foregroundStyle(.secondary)
                .listRowSeparator(.hidden)
            switch shows {
            case .loading:
                WorkPlaceholderRows()
            case .empty:
                emptyCard(timeline, now: now)
                    .frame(maxWidth: .infinity)
                    .listRowSeparator(.hidden)
            case .epics:
                ForEach(rows) { row in
                    Button { open(row.line) } label: {
                        epicRow(row, colour: Drawing.colour(row.line, night: night), working: row.line.working > 0)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(WorkTimeline.spoken([row.line], now: now).first?.words ?? row.line.title)
                    .accessibilityHint("Opens the epic")
                    .workWindowMenu(place(row.line))
                }
            }
        }
        .listStyle(.plain)
        #if os(iOS)
        .refreshable { state.asked += 1 }
        #endif
    }

    /// A row of the list: the epic's colour at the leading edge, its title and chips, line 2, and *11 of 16*
    /// with a bar. ◐ beside the title while a session is in a turn on it (#4265), still with Reduce Motion.
    private func epicRow(_ row: WorkTimeline.EpicRow, colour: Color, working: Bool) -> some View {
        let line = row.line
        let waits = if case .waits = line.how { true } else { false }
        return HStack(alignment: .center, spacing: 10) {
            RoundedRectangle(cornerRadius: 2).fill(colour).frame(width: 4).frame(maxHeight: .infinity)
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(line.title).appText(.supporting).fontWeight(.semibold).lineLimit(2)
                    WorkPriorityChip(priority: line.priority)
                    if working { WorkTimelineHalfRing(colour: colour, moving: !reduceMotion) }
                }
                Text(row.line2).appText(.fine)
                    .foregroundStyle(waits ? StateTint.attention.style(or: .secondary) : AnyShapeStyle(.secondary))
                    .lineLimit(2)
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 3) {
                ProgressView(value: Double(line.done), total: Double(max(line.total, 1)))
                    .tint(colour).frame(width: 56)
                Text("\(line.done) of \(line.total)").appText(.fine).foregroundStyle(.secondary).monospacedDigit()
            }
            .accessibilityHidden(true)
        }
        .padding(.vertical, 4)
        .contentShape(Rectangle())
    }

    /// Each band's name, on its first row, with a rule above every band but the first (#6089). It
    /// stays at the leading edge while the plot scrolls in time, and moves with the rows.
    private func gutter(_ drawing: Drawing) -> some View {
        VStack(spacing: 0) {
            Color.clear.frame(height: Self.axisHeight)
            ZStack(alignment: .topLeading) {
                ForEach(drawing.bands, id: \.band) { band in
                    if band.row > 0 {
                        Rectangle().fill(Paper.rule).frame(height: 1)
                            .offset(y: WorkTimeline.bandRuleY(band.row) - top)
                    }
                    Text(band.words).appText(.fine).fontWeight(.semibold)
                        .foregroundStyle(Drawing.bandTint(band.band).style(or: .secondary))
                        .lineLimit(1)
                        .padding(.leading, 12)
                        .frame(height: WorkTimeline.blockHeight)
                        .offset(y: WorkTimeline.rowTop(band.row) - top)
                        .accessibilityHidden(true)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .clipped()
            Color.clear.frame(height: Self.footHeight)
        }
        .frame(width: Self.gutterWidth)
        .background(drawing.plot)
        .overlay(alignment: .trailing) { Rectangle().fill(Paper.rule).frame(width: 1) }
    }

    /// S11: no epic in the window. The axis and Now stay; the card says when the last one
    /// joined, when the read knows, and offers the week.
    private func emptyCard(_ timeline: DaemonAPI.WorkTimeline, now: Date) -> some View {
        let window = state.range.window(now: state.anchor)
        let joined = WorkTimeline.lastJoined(timeline, before: window.start)
        return VStack(spacing: 6) {
            Text(WorkTimeline.noEpicsWords(state.range)).appText(.supporting).fontWeight(.semibold)
            if let joined {
                Text(WorkTimeline.lastJoinedWords(joined, now: now)).appText(.fine).foregroundStyle(.secondary)
            }
            if state.range != .week {
                Button(WorkTimeline.showTheWeek) { state.range = .week }
                    .buttonStyle(.paper)
                    .padding(.top, 2)
            }
        }
        .padding(12)
        .background(RoundedRectangle(cornerRadius: Paper.Radius.control).fill(Paper.raised))
        .overlay(RoundedRectangle(cornerRadius: Paper.Radius.control).strokeBorder(Paper.rule))
    }

    /// A scroll the page makes: the offset is known at once, as a scroll geometry change does
    /// not come for it.
    private func scroll(to x: Double) {
        position.scrollTo(x: x)
        offset = x
    }

    /// Past an edge of what the plot holds, the window moves by a range, and the plot with it.
    private func shift(x: Double, visible: Double) {
        let total = visible * state.content.duration / state.range.span
        if x < visible * 0.05 {
            state.anchor = state.anchor.addingTimeInterval(-state.range.span)
            scroll(to: x + visible)
        } else if x > total - visible * 1.05, total > visible * 2 {
            state.anchor = state.anchor.addingTimeInterval(state.range.span)
            scroll(to: x - visible)
        }
    }

    private func canvas(_ drawing: Drawing) -> some View {
        Canvas { context, _ in drawing.draw(&context) }
            .overlay { working(drawing) }
            .overlay(alignment: .topLeading) {
                if let hover {
                    card(hover.line, drawing: drawing)
                        .offset(x: cardX(hover.at.x, drawing: drawing), y: hover.at.y + 14)
                        .allowsHitTesting(false)
                }
            }
            #if os(macOS)
            .onContinuousHover { phase in
                switch phase {
                case .active(let p):
                    hover = WorkTimeline.hit((Double(p.x), Double(p.y)), lines: drawing.lines, axis: drawing.axis).map { Hover(line: $0, at: p) }
                case .ended:
                    hover = nil
                }
            }
            // The block under the pointer has Open in New Window (#5475).
            .workWindowMenu(hover.map { place($0.line) })
            #endif
            .onTapGesture { p in
                let hit = WorkTimeline.hit((Double(p.x), Double(p.y)), lines: drawing.lines, axis: drawing.axis)
                #if os(macOS)
                if let hit { open(hit) }
                #else
                // The Remote (#6044): a tap shows the card a pointer's hover shows; a second tap on it opens it.
                guard let hit else { hover = nil; return }
                if hover?.line.id == hit.id { hover = nil; open(hit) } else { hover = Hover(line: hit, at: p) }
                #endif
            }
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Timeline")
            .accessibilityChildren {
                // A list in time order (section 5), each opening its epic's Plan.
                ForEach(WorkTimeline.spoken(drawing.lines, now: drawing.now), id: \.line.id) { row in
                    Button(row.words) { open(row.line) }
                }
            }
    }

    /// An epic a session is in a turn on now (#4265): a soft glow round its block breathes and its ◐
    /// at Now turns; with Reduce Motion, a still glow and a still ◐. One clock for every working
    /// block and ring (#1653).
    @ViewBuilder
    private func working(_ drawing: Drawing) -> some View {
        if !reduceMotion, drawing.lines.contains(where: drawing.isWorking) {
            TimelineView(.animation(minimumInterval: 1.0 / 30)) { context in
                Canvas { gc, _ in drawing.drawWorking(&gc, at: context.date) }
            }
            .allowsHitTesting(false)
        }
    }

    /// The card's leading edge: after the pointer; on the Remote, before the tap when it would pass the
    /// screen's trailing edge (#6044).
    private func cardX(_ x: Double, drawing: Drawing) -> Double {
        #if os(iOS)
        let width = min(360, drawing.visible - 24)
        return min(x + 14, max(drawing.offset + drawing.visible - width - 8, x - width - 14))
        #else
        x + 14
        #endif
    }

    private func open(_ line: WorkTimeline.Line) {
        model.follow(place(line), window: openWorkWindow)
    }

    private func place(_ line: WorkTimeline.Line) -> WorkPlace { WorkPlace(kind: .issue, ref: line.epic.ref) }

    // MARK: The hover card

    /// A mini row (milestones.md note 33) on three lines (Alex, 2026-11-10): the state glyph and the
    /// title; the label chips where the wire has them (#4428); then the prediction. No cost (Alex,
    /// 2026-11-14).
    private func card(_ line: WorkTimeline.Line, drawing: Drawing) -> some View {
        lineRow(line, drawing: drawing)
            .frame(maxWidth: 360, alignment: .leading)
            .padding(10)
            .background(RoundedRectangle(cornerRadius: Paper.Radius.control).fill(Paper.raised))
            .overlay(RoundedRectangle(cornerRadius: Paper.Radius.control).strokeBorder(Paper.rule))
            .fixedSize(horizontal: false, vertical: true)
    }

    private func lineRow(_ line: WorkTimeline.Line, drawing: Drawing) -> some View {
        let epic = state.timeline?.epics.first { $0.item.node.ref == line.epic.ref }
        let glyph = AnyView(WorkStateIcon(kind: "issue", state: epic?.item.state ?? (line.open ? "open" : "closed"),
                                          stateReason: epic?.stateReason))
        return miniRow(glyph: glyph, title: line.title, labels: epic?.item.labels ?? [],
                       line: WorkTimeline.lineLine(line, now: drawing.now),
                       priority: epic?.item.priority)
    }

    private func miniRow(glyph: AnyView, title: String, labels: [DaemonAPI.ForgeLabel],
                         line: String, priority: String? = nil) -> some View {
        let chips = WorkLook.shownLabels(labels, limit: WorkLook.rowChips)
        return VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                glyph
                Text(title).appText(.supporting).fontWeight(.semibold).lineLimit(1).truncationMode(.tail)
                // The priority chip, as an Issues row has it (#6089).
                WorkPriorityChip(priority: priority)
            }
            if !chips.shown.isEmpty {
                HStack(spacing: 4) {
                    ForEach(chips.shown, id: \.name) { WorkLabelChip(label: $0) }
                    WorkMoreChips(count: chips.more)
                }
                .padding(.leading, 22)
            }
            Text(line).appText(.fine).foregroundStyle(.secondary).lineLimit(1).padding(.leading, 22)
        }
    }

    #if canImport(AppKit)
    /// The words' step, as the canvas resolves it: bold, so a block's semibold title is never wider.
    nonisolated static var labelFont: NSFont { NSFontManager.shared.convert(TextStep.fine.nsFont, toHaveTrait: .boldFontMask) }
    #else
    /// The words' step, as the canvas resolves it: bold, so a block's semibold title is never wider.
    nonisolated static var labelFont: UIFont {
        let font = TextStep.fine.uiFont
        return font.fontDescriptor.withSymbolicTraits(.traitBold).map { UIFont(descriptor: $0, size: font.pointSize) } ?? font
    }
    #endif

    /// A title's width, as the canvas draws it.
    nonisolated static func measure(_ text: String) -> Double {
        Double((text as NSString).size(withAttributes: [.font: labelFont]).width)
    }
}

/// What one frame of the plot draws, and how.
private struct Drawing {
    var lines: [WorkTimeline.Line]
    var axis: WorkTimeline.Axis
    var now: Date
    var height: Double
    var offset: Double
    var visible: Double
    var night: Bool
    /// Before the first read (S10): grey blocks that shimmer with the clock.
    var loading = false
    /// Not Reduce Motion: the working blocks' ◐ turns in the overlay, on the glow's clock.
    var moving = true
    /// The priority bands in use (#6089): a rule above each but the first.
    var bands: [WorkTimeline.Band] = []

    /// A band's name and a chip's words take the priority chip's tint (#6089): High the failure's
    /// red, Medium the warning's orange, Low and none plain.
    static func bandTint(_ band: Int) -> StateTint {
        switch band {
        case 0: .failure
        case 1: .warning
        default: .none
        }
    }

    /// One breath of a working block's glow and one turn of its ◐, in seconds (#4265).
    static let pulsePeriod = 1.6

    /// The system colours for the features; the last, grey, for an epic with none.
    static let palette: [Color] = [.blue, .purple, .pink, Color.orange, Color.green, .teal, .indigo, .mint, .brown, .cyan, Color.red, .gray]
    /// Each colour, as the light appearance draws it, darkened until white text on it has 4.5:1 (#5506).
    static let fills: [Color] = palette.map { colour in
        var rgb: [Double] = [0, 0, 0]
        #if canImport(AppKit)
        NSAppearance(named: .aqua)?.performAsCurrentDrawingAppearance {
            if let c = NSColor(colour).usingColorSpace(.sRGB) {
                rgb = [Double(c.redComponent), Double(c.greenComponent), Double(c.blueComponent)]
            }
        }
        #else
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        if UIColor(colour).resolvedColor(with: UITraitCollection(userInterfaceStyle: .light)).getRed(&r, green: &g, blue: &b, alpha: &a) {
            rgb = [Double(r), Double(g), Double(b)].map { min(max($0, 0), 1) }
        }
        #endif
        let dark = WorkTimeline.forWhiteText(rgb)
        return Color(red: dark[0], green: dark[1], blue: dark[2])
    }
    /// Now: a grey line with its time in a grey pill (Alex, 2026-11-10).
    static let nowGrey = Color(white: 0.55)
    /// Where a block waits for the person: the colour of what waits on them.
    static let waits = StateTint.attention.color ?? .primary
    static let navy = Color(red: 0.05, green: 0.08, blue: 0.16)

    /// One background before and after Now (Alex, 2026-11-10).
    var plot: Color { night ? Self.navy : .white }
    var faint: Color { night ? Color(white: 0.6) : Color(white: 0.55) }

    /// A block's fill: its feature's colour, darkened until white has 4.5:1; grey when closed as not
    /// planned.
    func colour(_ line: WorkTimeline.Line) -> Color { Self.colour(line) }

    static func colour(_ line: WorkTimeline.Line) -> Color {
        fills[line.how == .dropped ? fills.count - 1 : WorkTimeline.colour(line.feature, of: fills.count)]
    }

    /// The list of epics' edge (#6044): the block's fill, lighter at night as the words after Now are.
    static func colour(_ line: WorkTimeline.Line, night: Bool) -> Color {
        night ? colour(line).mix(with: .white, by: 0.45) : colour(line)
    }

    /// The words and marks after Now, on the lighter tint: the colour itself by day, lighter at night
    /// to read on the navy.
    func later(_ line: WorkTimeline.Line) -> Color {
        night ? colour(line).mix(with: .white, by: 0.45) : colour(line)
    }

    /// What is drawn: the window, and a screen on each side of it.
    var seen: ClosedRange<Double> { (offset - visible)...(offset + 2 * visible) }

    func block(_ line: WorkTimeline.Line) -> CGRect {
        // An epic can be months old: no further than just past the plot's edges, so no layer is that wide.
        let sx = max(axis.x(line.from), -20), ex = min(axis.x(line.end), axis.width + 20)
        return CGRect(x: sx, y: WorkTimeline.rowTop(line.row), width: max(ex - sx, 2), height: WorkTimeline.blockHeight)
    }

    /// The solid span, full height: from when the work started to when it stopped (#6043); empty while no
    /// child has started. Clip to it.
    func solid(_ line: WorkTimeline.Line) -> CGRect {
        let from = axis.x(line.work?.start ?? line.from), to = axis.x(line.work?.end ?? line.from)
        return CGRect(x: from, y: -100, width: max(to - from, 0), height: height + 200)
    }

    /// The dashed ghost's span, full height: from when the work stopped (Now, when none started) to the
    /// predicted finish (#7058); empty with no forecast. Clip to it.
    func ahead(_ line: WorkTimeline.Line) -> CGRect {
        let from = axis.x(line.ghost?.start ?? line.end), to = axis.x(line.ghost?.end ?? line.end)
        return CGRect(x: from, y: -100, width: max(to - from + (line.ghost == nil ? 0 : 2), 0), height: height + 200)
    }

    /// The ghost's tint: the lighter one (#6043).
    func ghost(_ line: WorkTimeline.Line) -> Color { colour(line).opacity(night ? 0.32 : 0.22) }

    /// The wait pill's words: 👤 and the number, no words (Alex, 2026-11-14).
    func waitPill(_ count: Int) -> Text { Text("\(Image(icon: "person.fill")) \(count)") }
    func pillWidth(_ count: Int) -> Double { WorkTimelinePane.measure("👤 \(count)") + 12 }

    func draw(_ gc: inout GraphicsContext) {
        let nowX = axis.x(now)
        gc.fill(Path(CGRect(x: 0, y: 0, width: axis.width, height: height)), with: .color(plot))
        let shown = lines.filter { axis.x($0.from) <= seen.upperBound && axis.x(WorkTimeline.reach($0)) >= seen.lowerBound }
        let left = offset, right = offset + visible

        // A rule above each priority band but the first (#6089).
        for band in bands where band.row > 0 {
            let y = WorkTimeline.bandRuleY(band.row)
            gc.fill(Path(CGRect(x: 0, y: y - 0.5, width: axis.width, height: 1)), with: .color(Paper.rule))
        }

        for line in shown {
            let c = colour(line), rect = block(line)
            let shape = Path(roundedRect: rect, cornerRadius: Paper.Radius.code)
            let mid = rect.midY
            // An epic a session is in a turn on now (#4265): with Reduce Motion a still glow; with motion it
            // breathes in the overlay.
            if isWorking(line), !moving {
                gc.drawLayer { layer in
                    layer.addFilter(.blur(radius: 3))
                    layer.stroke(shape, with: .color(c.opacity(0.35)), lineWidth: 6)
                }
            }
            // Solid where the work happened, from started to stopped (#6043).
            let solidRect = solid(line), work = Path(solidRect)
            let idle = Path(CGRect(x: -100, y: -100, width: max(solidRect.minX + 100, 0), height: height + 200))
                .union(Path(CGRect(x: solidRect.maxX, y: -100, width: max(axis.width - solidRect.maxX + 100, 0), height: height + 200)))
            gc.drawLayer { layer in
                layer.clip(to: work)
                layer.fill(shape, with: .color(c))
            }
            // Then an open epic's ghost to its predicted finish, with a dashed edge (#7058).
            if line.ghost != nil {
                gc.drawLayer { layer in
                    layer.clip(to: Path(ahead(line)))
                    layer.fill(shape, with: .color(ghost(line)))
                    layer.stroke(shape, with: .color(later(line)), style: StrokeStyle(lineWidth: 1, dash: [4, 3]))
                }
            }
            // 8 times in 10: a thin bar after the forecast.
            if let band = line.band, axis.x(band.end) > rect.maxX {
                let bar = CGRect(x: rect.maxX, y: mid - 2, width: axis.x(band.end) - rect.maxX, height: 4)
                gc.fill(Path(roundedRect: bar, cornerRadius: 2), with: .color(later(line).opacity(0.35)))
            }
            // The words: the title in the block, white on the solid span and the colour on the ghost; or after the block.
            var trailing = 0.0
            if case .waits(let n) = line.how {
                trailing = pillWidth(n)
                let words = waitPill(n)
                pill(&gc, words, at: CGPoint(x: axis.x(WorkTimeline.reach(line)) + 6 + trailing / 2, y: mid), fill: Self.waits,
                     text: night ? Self.navy : .white)
            }
            let words = WorkTimeline.blockWords(line, axis: axis, measure: WorkTimelinePane.measure,
                                                next: WorkTimeline.nextInRow(lines, after: line), left: left + 2, trailing: trailing)
            if let inside = words.inside {
                let at = CGPoint(x: words.insideX, y: WorkTimeline.titleY(line.row))
                gc.drawLayer { layer in
                    layer.clip(to: work)
                    layer.draw(Text(inside).stepped(.fine).fontWeight(.semibold).foregroundStyle(.white), at: at, anchor: .leading)
                }
                gc.drawLayer { layer in
                    layer.clip(to: idle)
                    layer.draw(Text(inside).stepped(.fine).fontWeight(.semibold).foregroundStyle(later(line)), at: at, anchor: .leading)
                }
            }
            if let chip = words.chip {
                drawChip(&gc, chip, line: line, x: words.chipX, y: words.chipInside ? WorkTimeline.titleY(line.row) : mid)
            }
            if let outside = words.outside {
                let text = line.how == .dropped
                    ? Text(outside).stepped(.fine).foregroundStyle(faint)
                    : Text(outside).stepped(.fine).fontWeight(.semibold).foregroundStyle(later(line))
                gc.draw(text, at: CGPoint(x: words.outsideX, y: mid), anchor: .leading)
            }
            // With motion the ◐ turns in the overlay; with Reduce Motion it is still, here.
            if isWorking(line), !moving { drawHalfRing(&gc, line, x: nowX, turn: 0) }
            // → for a block that goes on past the right edge.
            if axis.x(WorkTimeline.reach(line)) > right {
                gc.draw(Text("→").stepped(.fine).fontWeight(.bold).foregroundStyle(right - 10 > solidRect.maxX || right - 10 < solidRect.minX ? later(line) : .white),
                        at: CGPoint(x: right - 10, y: mid))
            }
        }

        if loading { drawPlaceholders(&gc) }

        // Now: a grey line.
        var nowLine = Path()
        nowLine.move(to: CGPoint(x: nowX, y: 0))
        nowLine.addLine(to: CGPoint(x: nowX, y: height))
        gc.stroke(nowLine, with: .color(Self.nowGrey), lineWidth: 1.5)
    }

    /// The priority chip after a block's title (#6089): `WorkPriorityChip`'s look, drawn in the canvas,
    /// on the plot's own background so it reads on any block.
    func drawChip(_ gc: inout GraphicsContext, _ words: String, line: WorkTimeline.Line, x: Double, y: Double) {
        let width = WorkTimeline.chipWidth(words, measure: WorkTimelinePane.measure)
        let rect = CGRect(x: x, y: y - 8, width: width, height: 16)
        let shape = Path(roundedRect: rect, cornerRadius: 8)
        let tint = Self.bandTint(line.priorityBand)
        let edge: Color = switch tint {
        case .failure: Paper.lineFailure
        case .warning: Paper.lineWarning
        default: faint
        }
        gc.fill(shape, with: .color(plot))
        gc.stroke(shape, with: .color(edge), lineWidth: 1)
        gc.draw(Text(words).stepped(.fine).fontWeight(.medium).foregroundStyle(tint.color ?? faint),
                at: CGPoint(x: rect.midX, y: y))
    }

    /// S10: grey blocks in rows, in the window, their grey breathing with the clock.
    func drawPlaceholders(_ gc: inout GraphicsContext) {
        let phase = 0.5 + 0.5 * sin(now.timeIntervalSinceReferenceDate * .pi / 1.1)
        let grey = Paper.wash.opacity(0.55 + 0.45 * phase)
        for (row, from, width) in [(0, 0.04, 0.4), (1, 0.16, 0.45), (2, 0.07, 0.2), (3, 0.24, 0.36)] {
            let rect = CGRect(x: offset + visible * from, y: WorkTimeline.rowTop(row), width: visible * width, height: WorkTimeline.blockHeight)
            gc.fill(Path(roundedRect: rect, cornerRadius: Paper.Radius.code), with: .color(grey))
        }
    }

    func pill(_ gc: inout GraphicsContext, _ words: String, at: CGPoint, fill: Color, text: Color) {
        pill(&gc, Text(words), at: at, fill: fill, text: text)
    }

    func pill(_ gc: inout GraphicsContext, _ words: Text, at: CGPoint, fill: Color, text: Color) {
        let resolved = gc.resolve(words.stepped(.fine).fontWeight(.semibold).foregroundStyle(text))
        let size = resolved.measure(in: CGSize(width: 400, height: 20))
        let rect = CGRect(x: at.x - size.width / 2 - 6, y: at.y - size.height / 2 - 2, width: size.width + 12, height: size.height + 4)
        gc.fill(Path(roundedRect: rect, cornerRadius: rect.height / 2), with: .color(fill))
        gc.draw(resolved, at: at)
    }

    /// The hour axis at the top (#5506).
    func drawAxis(_ gc: inout GraphicsContext) {
        let h = WorkTimelinePane.axisHeight
        gc.fill(Path(CGRect(x: 0, y: 0, width: axis.width, height: h)), with: .color(plot))
        let calendar = Calendar.current
        for tick in axis.ticks(seen: visible * axis.interval.duration / max(axis.width, 1)) {
            let x = axis.x(tick)
            guard seen.contains(x) else { continue }
            let midnight = calendar.component(.hour, from: tick) == 0
            let words = midnight ? tick.formatted(.dateTime.weekday(.abbreviated).day()) : tick.formatted(.dateTime.hour())
            var mark = Path()
            mark.move(to: CGPoint(x: x, y: h - (midnight ? 7 : 4)))
            mark.addLine(to: CGPoint(x: x, y: h))
            gc.stroke(mark, with: .color(faint), lineWidth: 1)
            gc.draw(Text(words).stepped(.fine).fontWeight(midnight ? .semibold : .regular).foregroundStyle(faint),
                    at: CGPoint(x: x, y: 11))
        }
        var rule = Path()
        rule.move(to: CGPoint(x: 0, y: h - 0.5))
        rule.addLine(to: CGPoint(x: axis.width, y: h - 0.5))
        gc.stroke(rule, with: .color(Paper.rule), lineWidth: 1)
    }

    /// Now's time in a grey pill, under the plot where the Now line ends.
    func drawNow(_ gc: inout GraphicsContext) {
        gc.fill(Path(CGRect(x: 0, y: 0, width: axis.width, height: WorkTimelinePane.footHeight)), with: .color(plot))
        pill(&gc, now.formatted(date: .omitted, time: .shortened), at: CGPoint(x: axis.x(now), y: 12), fill: Self.nowGrey, text: .white)
    }

    /// The epic or a child of it is worked on now, and the block is open at Now.
    func isWorking(_ line: WorkTimeline.Line) -> Bool {
        line.working > 0 && line.from <= now && line.end >= now
    }

    /// ◐: a ring at Now, half of it filled; `turn` of a full turn on from the left half, clockwise.
    func drawHalfRing(_ gc: inout GraphicsContext, _ line: WorkTimeline.Line, x: Double, turn: Double) {
        let c = later(line)
        let rect = CGRect(x: x - 5, y: block(line).midY - 5, width: 10, height: 10)
        gc.fill(Path(ellipseIn: rect), with: .color(plot))
        var half = Path()
        let from = 90 + 360 * turn
        half.addArc(center: CGPoint(x: rect.midX, y: rect.midY), radius: 5, startAngle: .degrees(from),
                    endAngle: .degrees(from + 180), clockwise: false)
        gc.fill(half, with: .color(c))
        gc.stroke(Path(ellipseIn: rect), with: .color(c), lineWidth: 1.5)
    }

    /// The working blocks' glow, a soft ring round the block that breathes, and their ◐ turning: one
    /// breath and one turn in `pulsePeriod`, read off the wall clock (`SpinnerClock`), so all are in step.
    func drawWorking(_ gc: inout GraphicsContext, at time: Date) {
        let turn = SpinnerClock.fraction(at: time, period: Self.pulsePeriod)
        let phase = 0.5 - 0.5 * cos(2 * .pi * turn)
        let nowX = axis.x(now)
        for line in lines where isWorking(line) && axis.x(line.from) <= seen.upperBound && axis.x(line.end) >= seen.lowerBound {
            let shape = Path(roundedRect: block(line), cornerRadius: Paper.Radius.code)
            gc.drawLayer { layer in
                layer.addFilter(.blur(radius: 2))
                layer.stroke(shape, with: .color(colour(line).opacity(0.15 + 0.3 * phase)), lineWidth: 4 + 6 * phase)
            }
            drawHalfRing(&gc, line, x: nowX, turn: turn)
        }
    }
}

/// ◐ beside a working epic's title in the list of epics (#4265, #6044): it turns on the plot's clock;
/// still with Reduce Motion.
private struct WorkTimelineHalfRing: View {
    let colour: Color
    let moving: Bool

    var body: some View {
        if moving {
            TimelineView(.animation(minimumInterval: 1.0 / 30)) { context in
                ring(turn: SpinnerClock.fraction(at: context.date, period: Drawing.pulsePeriod))
            }
        } else {
            ring(turn: 0)
        }
    }

    private func ring(turn: Double) -> some View {
        Canvas { gc, size in
            let rect = CGRect(origin: .zero, size: size).insetBy(dx: 1, dy: 1)
            var half = Path()
            let from = 90 + 360 * turn
            half.addArc(center: CGPoint(x: rect.midX, y: rect.midY), radius: rect.width / 2, startAngle: .degrees(from),
                        endAngle: .degrees(from + 180), clockwise: false)
            gc.fill(half, with: .color(colour))
            gc.stroke(Path(ellipseIn: rect), with: .color(colour), lineWidth: 1.5)
        }
        .frame(width: 11, height: 11)
        .accessibilityHidden(true)
    }
}
