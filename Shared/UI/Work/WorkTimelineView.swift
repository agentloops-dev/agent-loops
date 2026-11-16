#if os(macOS)
import AgentsKitCore
import AppKit
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
    /// The last read got nothing: the plot is the read before it, dimmed under the strip (S12).
    var stale = false
    var asked = 0
    /// Now was clicked: the plot scrolls back to it.
    var nowAsked = 0
    /// Children that closed since the last read, and when they were seen: their dots pulse once.
    var landed: [String: Date] = [:]
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
    /// that worked. A stale plot says it in the failure strip, not only by its dim.
    var plotRead: Date?

    /// Work ▸ Timeline as this device keeps it (#4930): the plot around now, and the boards.
    struct Kept: Codable, Sendable {
        var timeline: DaemonAPI.WorkTimeline
        var boards: [DaemonAPI.ProjectBoard]
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
        return model.workProjects.filter { project in
            project.folders.contains { machines.contains(String($0.prefix { $0 != "|" })) }
        }
    }

    func load(model: any WorkModel) async {
        let mine = ask
        guard read != mine || timeline == nil else { return }
        read = mine
        reading = true
        defer { if ask == mine { reading = false } }
        let projects = projects(model: model)
        guard let project = projects.first(where: { $0.id == self.project }) ?? projects.first else {
            timeline = .init()
            stale = false
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
        // The widest range's span, whatever the range: the rows are packed over it, so they stay put
        // across ranges (#5506).
        let span = WorkTimeline.packSpan(anchor)
        let board = board
        var got: [DaemonAPI.WorkTimeline] = []
        var epics: Set<String>?
        var failed: [String] = []
        func draw(all: Bool) async {
            problem = all && got.isEmpty && !failed.isEmpty ? failed.joined(separator: "\n") : nil
            // Every machine failed: the last plot stays, dimmed under the strip.
            stale = all && got.isEmpty && !failed.isEmpty && timeline != nil
            if stale { return }
            var merged = WorkTimeline.merge(got)
            if let epics { merged.epics = merged.epics.filter { epics.contains($0.item.node.ref) } }
            // A child that closed since the last read of the same window lands: its dot pulses once.
            if let before = timeline {
                let was = Set(before.epics.flatMap(\.children).filter { $0.closedAt != nil }.map(\.item.node.ref))
                let seen = Date.now
                for child in merged.epics.flatMap(\.children) where child.closedAt != nil && !was.contains(child.item.node.ref)
                    && before.epics.contains(where: { $0.children.contains { $0.item.node.ref == child.item.node.ref } }) {
                    landed[child.item.node.ref] = seen
                }
            }
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
        await withTaskGroup(of: (DaemonAPI.WorkTimeline?, [String]?, String?).self) { group in
            for machine in machines {
                group.addTask {
                    do {
                        let t = try await model.forge(DaemonAPI.Method.workTimeline,
                                                      DaemonAPI.WorkTimelineRequest(project: project.id, from: span.start,
                                                                                    to: span.end),
                                                      returning: DaemonAPI.WorkTimeline.self, on: machine.id)
                        // A board narrows to its epics: the forge says which they are.
                        var refs: [String]?
                        if !board.isEmpty {
                            let list = try? await model.forgeList(.issue, .init(state: "all", label: "epic", limit: 100, board: board),
                                                                  on: machine.id)
                            refs = list?.items.map(\.node.ref) ?? []
                        }
                        return (t, refs, nil)
                    } catch {
                        return (nil, nil, machines.count > 1 ? "\(machine.name): \(error.localizedDescription)" : error.localizedDescription)
                    }
                }
            }
            // The first machine to answer draws the plot (#5449), and each one after adds its epics: a slow or offline
            // machine does not hold it. A machine that failed says so only when no machine answered.
            var answered = 0
            for await (t, refs, problem) in group {
                answered += 1
                if let t { got.append(t) }
                if let refs { epics = (epics ?? []).union(refs) }
                if let problem { failed.append(problem) }
                if ask == mine, answered < machines.count, t != nil { await draw(all: false) }
            }
        }
        let boardsFound = await found
        guard ask == mine else { return }
        boards = boardsFound
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

/// Work ▸ Timeline (#4173): time across, each epic a filled block in its own row from when its first
/// child starts to when its last one closes, a lighter tint with a dashed edge after Now (the
/// forecast), then a thin bar to where it closes 8 times in 10 (#5506, Alex, 2026-11-14). A SwiftUI
/// `Canvas` (spike #4171): the rows and the marks come from `WorkTimeline` in AgentsKitCore.
struct WorkTimelinePane: View {
    let model: any WorkModel
    @Bindable var state: WorkTimelineState
    @Environment(\.colorScheme) private var scheme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    @State private var position = ScrollPosition(edge: .leading)
    @State private var offset: Double = 0
    @State private var hover: Hover?

    struct Hover: Equatable {
        var hit: WorkTimeline.Hit
        var at: CGPoint
    }

    nonisolated static let axisHeight: Double = 26
    nonisolated static let footHeight: Double = 26

    var body: some View {
        VStack(spacing: 0) {
            bar.padding(.horizontal, 16).padding(.vertical, 8)
            Divider()
            content
        }
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                WorkRefreshButton(running: state.reading) { state.asked += 1 }
            }
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
            VStack(spacing: 0) {
                let lines = problems(timeline)
                WorkFromDeviceStrip(read: state.fromDevice && lines.isEmpty ? state.plotRead : nil)
                    .padding(.horizontal, 16).padding(.vertical, 8)
                if !lines.isEmpty {
                    WorkErrorStrip(lines: lines, read: state.stale || state.fromDevice ? state.plotRead : nil) { state.asked += 1 }
                        .padding(.horizontal, 16).padding(.vertical, 8)
                }
                // Once a minute; while the first read runs, often, so the grey blocks shimmer.
                let loading = state.timeline == nil
                TimelineView(.animation(minimumInterval: loading && !reduceMotion ? 1.0 / 20 : 60)) { context in
                    plot(timeline, now: context.date, loading: loading)
                }
                .opacity(state.stale ? 0.5 : 1)
            }
        }
    }

    private func problems(_ timeline: DaemonAPI.WorkTimeline) -> [String] {
        (state.problem.map { $0.components(separatedBy: "\n") } ?? []) + (timeline.errors ?? [])
    }

    private func plot(_ timeline: DaemonAPI.WorkTimeline, now: Date, loading: Bool) -> some View {
        GeometryReader { geo in
            let visible = max(geo.size.width, 1)
            let content = state.content
            let axis = WorkTimeline.Axis(interval: content, width: visible * content.duration / state.range.span)
            // The epics and children a session is in a turn on now (#4265): their open blocks glow.
            let working = WorkTimeline.workingNow(timeline, sessions: state.sessions, agents: model.workAgents)
            // Packed over the widest range's span, so a block keeps its row when the range changes or the plot
            // scrolls (#5506).
            let lines = WorkTimeline.layout(
                WorkTimeline.overlapping(WorkTimeline.lines(timeline, now: now, working: working), WorkTimeline.packSpan(state.anchor)))
            let rows = max(WorkTimeline.rowCount(lines), 3)
            let height = max(WorkTimeline.rowTop(rows) + WorkTimeline.rowGap, geo.size.height - Self.axisHeight - Self.footHeight)
            let drawing = Drawing(lines: lines, axis: axis, now: now, height: height, offset: offset, visible: visible,
                                  night: scheme == .dark, loading: loading, moving: !reduceMotion)
            ScrollView(.horizontal) {
                VStack(spacing: 0) {
                    // The axis at the top, the blocks under it, and Now's time at the foot (#5506).
                    Canvas { context, _ in drawing.drawAxis(&context) }
                        .frame(width: axis.width, height: Self.axisHeight)
                        .accessibilityHidden(true)
                    ScrollView(.vertical) {
                        canvas(drawing, timeline: timeline)
                            .frame(width: axis.width, height: height)
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
                if !loading, WorkTimeline.overlapping(lines, state.range.window(now: state.anchor)).isEmpty {
                    emptyCard(timeline, now: now)
                }
            }
        }
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

    private func canvas(_ drawing: Drawing, timeline: DaemonAPI.WorkTimeline) -> some View {
        Canvas { context, _ in drawing.draw(&context) }
            .overlay { working(drawing) }
            .overlay { pulses(drawing) }
            .overlay(alignment: .topLeading) {
                if let hover {
                    card(hover.hit, drawing: drawing)
                        .offset(x: hover.at.x + 14, y: hover.at.y + 14)
                        .allowsHitTesting(false)
                }
            }
            .onContinuousHover { phase in
                switch phase {
                case .active(let p):
                    hover = WorkTimeline.hit((Double(p.x), Double(p.y)), lines: drawing.lines, axis: drawing.axis).map { Hover(hit: $0, at: p) }
                case .ended:
                    hover = nil
                }
            }
            .onTapGesture { p in
                guard let hit = WorkTimeline.hit((Double(p.x), Double(p.y)), lines: drawing.lines, axis: drawing.axis) else { return }
                open(hit)
            }
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Timeline")
            .accessibilityChildren {
                // A list in time order (section 5), each opening its epic's Plan.
                ForEach(WorkTimeline.spoken(drawing.lines, now: drawing.now), id: \.line.id) { row in
                    Button(row.words) { open(.line(row.line)) }
                }
            }
    }

    /// A dot that landed while the page is open pulses once; not with Reduce Motion.
    @ViewBuilder
    private func pulses(_ drawing: Drawing) -> some View {
        let landed = state.landed
        if !landed.isEmpty, !reduceMotion {
            TimelineView(.animation) { context in
                Canvas { gc, _ in drawing.drawPulses(&gc, landed: landed, at: context.date) }
            }
            .allowsHitTesting(false)
            .task(id: landed.count) {
                try? await Task.sleep(for: .seconds(1.2))
                let old = Date.now.addingTimeInterval(-1.1)
                state.landed = state.landed.filter { $0.value > old }
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

    private func open(_ hit: WorkTimeline.Hit) {
        switch hit {
        case .dot(_, let dot): model.showWork(WorkPlace(kind: .issue, ref: dot.ref))
        case .line(let line): model.showWork(WorkPlace(kind: .issue, ref: line.epic.ref))
        }
    }

    // MARK: The hover card

    /// A mini row (milestones.md note 33) on three lines (Alex, 2026-11-10): the state glyph and the
    /// title; the label chips where the wire has them (#4428); then the prediction. No cost (Alex,
    /// 2026-11-14).
    private func card(_ hit: WorkTimeline.Hit, drawing: Drawing) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            switch hit {
            case .dot(_, let dot):
                let child = self.child(dot.ref)
                miniRow(glyph: dotGlyph(dot), title: dot.title, labels: child?.item.labels ?? [],
                        line: WorkTimeline.dotLine(dot, now: drawing.now))
            case .line(let line):
                lineRow(line, drawing: drawing)
            }
        }
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
                       line: WorkTimeline.lineLine(line, now: drawing.now))
    }

    private func miniRow(glyph: AnyView, title: String, labels: [DaemonAPI.ForgeLabel], line: String) -> some View {
        let chips = WorkLook.shownLabels(labels, limit: WorkLook.rowChips)
        return VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                glyph
                Text(title).appText(.supporting).fontWeight(.semibold).lineLimit(1).truncationMode(.tail)
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

    /// A child's glyph: open, done, or not planned (a ring with a slash).
    private func dotGlyph(_ dot: WorkTimeline.Dot) -> AnyView {
        AnyView(WorkStateIcon(kind: "issue", state: dot.closed ? "closed" : "open", stateReason: dot.notPlanned ? "not_planned" : nil))
    }

    private func child(_ ref: String) -> DaemonAPI.WorkTimelineChild? {
        state.timeline?.epics.lazy.flatMap(\.children).first { $0.item.node.ref == ref }
    }

    /// The words' step, as the canvas resolves it: bold, so a block's semibold title is never wider.
    nonisolated static var labelFont: NSFont { NSFontManager.shared.convert(TextStep.fine.nsFont, toHaveTrait: .boldFontMask) }

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

    /// One breath of a working block's glow and one turn of its ◐, in seconds (#4265).
    static let pulsePeriod = 1.6

    /// The system colours for the features; the last, grey, for an epic with none.
    static let palette: [Color] = [.blue, .purple, .pink, Color.orange, Color.green, .teal, .indigo, .mint, .brown, .cyan, Color.red, .gray]
    /// Each colour, as the light appearance draws it, darkened until white text on it has 4.5:1 (#5506).
    static let fills: [Color] = palette.map { colour in
        var rgb: [Double] = [0, 0, 0]
        NSAppearance(named: .aqua)?.performAsCurrentDrawingAppearance {
            if let c = NSColor(colour).usingColorSpace(.sRGB) {
                rgb = [Double(c.redComponent), Double(c.greenComponent), Double(c.blueComponent)]
            }
        }
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

    /// A block's fill: its feature's colour, darkened until white has 4.5:1; grey when closed as not planned.
    func colour(_ line: WorkTimeline.Line) -> Color {
        Self.fills[line.how == .dropped ? Self.fills.count - 1 : WorkTimeline.colour(line.feature, of: Self.fills.count)]
    }

    /// The words and marks after Now, on the lighter tint: the colour itself by day, lighter at night
    /// to read on the navy.
    func later(_ line: WorkTimeline.Line) -> Color {
        night ? colour(line).mix(with: .white, by: 0.45) : colour(line)
    }

    /// What is drawn: the window, and a screen on each side of it.
    var seen: ClosedRange<Double> { (offset - visible)...(offset + 2 * visible) }

    func block(_ line: WorkTimeline.Line) -> CGRect {
        let sx = axis.x(line.start)
        return CGRect(x: sx, y: WorkTimeline.rowTop(line.row), width: max(axis.x(line.end) - sx, 2), height: WorkTimeline.blockHeight)
    }

    /// The wait pill's words: 👤 and the number, no words (Alex, 2026-11-14).
    func waitPill(_ count: Int) -> Text { Text("\(Image(icon: "person.fill")) \(count)") }
    func pillWidth(_ count: Int) -> Double { WorkTimelinePane.measure("👤 \(count)") + 12 }

    func draw(_ gc: inout GraphicsContext) {
        let nowX = axis.x(now)
        gc.fill(Path(CGRect(x: 0, y: 0, width: axis.width, height: height)), with: .color(plot))
        let shown = lines.filter { axis.x($0.start) <= seen.upperBound && axis.x(WorkTimeline.reach($0)) >= seen.lowerBound }
        let past = Path(CGRect(x: -100, y: 0, width: max(nowX + 100, 0), height: height))
        let ahead = Path(CGRect(x: nowX, y: 0, width: max(axis.width - nowX + 100, 0), height: height))
        let left = offset, right = offset + visible

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
            // Solid to Now.
            gc.drawLayer { layer in
                layer.clip(to: past)
                layer.fill(shape, with: .color(c))
            }
            // After Now, the forecast: a lighter tint with a dashed edge.
            if line.how == .forecast || { if case .waits = line.how { true } else { false } }() {
                gc.drawLayer { layer in
                    layer.clip(to: ahead)
                    layer.fill(shape, with: .color(c.opacity(night ? 0.32 : 0.22)))
                    layer.stroke(shape, with: .color(later(line)), style: StrokeStyle(lineWidth: 1, dash: [4, 3]))
                }
            }
            // 8 times in 10: a thin bar after the forecast.
            if let band = line.band, axis.x(band.end) > rect.maxX {
                let bar = CGRect(x: rect.maxX, y: mid - 2, width: axis.x(band.end) - rect.maxX, height: 4)
                gc.fill(Path(roundedRect: bar, cornerRadius: 2), with: .color(later(line).opacity(0.35)))
            }
            // The children: white dots for the closed ones, rings for those ahead.
            for dot in line.dots {
                let x = WorkTimeline.dotX(dot, on: line, axis: axis)
                let r = dot.closed ? 2.0 : 2.5
                let circle = Path(ellipseIn: CGRect(x: x - r, y: WorkTimeline.dotY(line.row) - r, width: 2 * r, height: 2 * r))
                let mark = x > nowX ? later(line) : .white
                if dot.closed { gc.fill(circle, with: .color(mark)) } else { gc.stroke(circle, with: .color(mark), lineWidth: 1.2) }
            }
            // The words: the title in the block, white to Now and the colour after it; or after the block.
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
                    layer.clip(to: past)
                    layer.draw(Text(inside).stepped(.fine).fontWeight(.semibold).foregroundStyle(.white), at: at, anchor: .leading)
                }
                gc.drawLayer { layer in
                    layer.clip(to: ahead)
                    layer.draw(Text(inside).stepped(.fine).fontWeight(.semibold).foregroundStyle(later(line)), at: at, anchor: .leading)
                }
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
                gc.draw(Text("→").stepped(.fine).fontWeight(.bold).foregroundStyle(right - 10 > nowX ? later(line) : .white),
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

    /// A child of the block is worked on now, and the block is open at Now.
    func isWorking(_ line: WorkTimeline.Line) -> Bool {
        line.working > 0 && line.start <= now && line.end >= now
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
        for line in lines where isWorking(line) && axis.x(line.start) <= seen.upperBound && axis.x(line.end) >= seen.lowerBound {
            let shape = Path(roundedRect: block(line), cornerRadius: Paper.Radius.code)
            gc.drawLayer { layer in
                layer.addFilter(.blur(radius: 2))
                layer.stroke(shape, with: .color(colour(line).opacity(0.15 + 0.3 * phase)), lineWidth: 4 + 6 * phase)
            }
            drawHalfRing(&gc, line, x: nowX, turn: turn)
        }
    }

    /// The dots that landed: a ring that grows and fades over a second.
    func drawPulses(_ gc: inout GraphicsContext, landed: [String: Date], at time: Date) {
        for line in lines {
            for dot in line.dots {
                guard let seen = landed[dot.ref] else { continue }
                let t = time.timeIntervalSince(seen)
                guard t >= 0, t < 1 else { continue }
                let r = 4 + 10 * t
                let x = WorkTimeline.dotX(dot, on: line, axis: axis)
                let ring = Path(ellipseIn: CGRect(x: x - r, y: WorkTimeline.dotY(line.row) - r, width: 2 * r, height: 2 * r))
                gc.stroke(ring, with: .color(later(line).opacity(1 - t)), lineWidth: 2)
            }
        }
    }
}
#endif
