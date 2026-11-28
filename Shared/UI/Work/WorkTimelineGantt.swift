#if os(macOS)
import AgentsKitCore
import SwiftUI

/// Work ▸ Timeline's forecast as a Gantt (#7526, epic #7067), behind the flag `timeline-forecast`; the Mac only until
/// the port (#7527). The wireframe Alex approved at gate 2: a row for each open epic of the schedule (#7525), its name
/// and a line under it at the leading edge; in the track the days, Today, the work so far solid, the predicted rest
/// dashed, a planned epic's span light, an arrow from each blocker, and why an epic has no date.
/// Work's bar (Project, Board, On hold, Today, Refresh) and its footer (Last update) stay the pane's.
struct WorkTimelineGantt: View {
    let timeline: DaemonAPI.WorkTimeline?
    let held: Bool
    let reading: Bool
    let nowAsked: Int
    let open: (String) -> Void

    @State private var position = ScrollPosition(edge: .leading)
    @State private var offset: Double = 0
    @State private var hover: Hover?

    struct Hover: Equatable {
        var row: Int
        var at: CGPoint
    }

    static let nameWidth: Double = 250
    static let headerHeight: Double = 34
    static let rowHeight: Double = 52
    static let barHeight: Double = 16

    var body: some View {
        TimelineView(.periodic(from: .now, by: 60)) { context in
            let now = context.date
            if let timeline, let schedule = timeline.schedule {
                let rows = WorkGantt.rows(timeline, now: now, held: held)
                VStack(spacing: 0) {
                    controlRow(schedule)
                    Divider()
                    if rows.isEmpty {
                        WorkEmptyState(symbol: WorkList.Kind.timeline.sidebarIcon, title: WorkGantt.noRows,
                                       line: held ? "" : WorkTimeline.heldToggleHelp)
                            .frame(maxHeight: .infinity)
                    } else {
                        grid(rows, now: now)
                    }
                    Divider()
                    Text(WorkGantt.legend).appText(.fine).foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 16).padding(.vertical, 6)
                }
            } else if timeline == nil || reading {
                // Before the first read, or a first answer with no schedule yet: placeholder rows.
                VStack(alignment: .leading, spacing: 0) {
                    WorkPlaceholderRows()
                    Spacer(minLength: 0)
                }
                .padding(16)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            } else {
                WorkEmptyState(symbol: WorkList.Kind.timeline.sidebarIcon, title: WorkGantt.noSchedule,
                               line: WorkGantt.noScheduleLine)
                    .frame(maxHeight: .infinity)
            }
        }
    }

    /// FORECAST, then the places and when the schedule was made.
    private func controlRow(_ schedule: DaemonAPI.WorkTimelineSchedule) -> some View {
        HStack(spacing: 10) {
            Text(WorkGantt.badge).appText(.fine).fontWeight(.semibold).foregroundStyle(.secondary)
                .padding(.horizontal, 6).padding(.vertical, 2)
                .background(RoundedRectangle(cornerRadius: Paper.Radius.code).fill(Paper.wash))
            if schedule.incomplete == true {
                Text(WorkGantt.reasonWords("incomplete")).appText(.fine)
                    .foregroundStyle(StateTint.attention.style(or: .secondary))
            }
            Spacer(minLength: 0)
            Text(WorkGantt.capacityWords(schedule)).appText(.fine).foregroundStyle(.secondary).monospacedDigit()
        }
        .padding(.horizontal, 16).padding(.vertical, 7)
    }

    // MARK: The grid

    private func grid(_ rows: [WorkGantt.Row], now: Date) -> some View {
        let interval = WorkGantt.days(rows, now: now)
        let ticks = WorkGantt.ticks(interval)
        return GeometryReader { geo in
            let visible = max(geo.size.width - Self.nameWidth, 1)
            let dayWidth = WorkGantt.dayWidth(days: ticks.count, width: visible)
            let width = dayWidth * Double(ticks.count)
            let height = Self.headerHeight + Self.rowHeight * Double(rows.count)
            let chart = Chart(rows: rows, interval: interval, ticks: ticks, width: width, height: height, now: now,
                              offset: offset, visible: visible)
            ScrollView(.vertical) {
                HStack(alignment: .top, spacing: 0) {
                    names(rows)
                    ScrollView(.horizontal) {
                        Canvas { gc, _ in chart.draw(&gc) }
                            .frame(width: width, height: height)
                            .overlay(alignment: .topLeading) {
                                if let hover, rows.indices.contains(hover.row) {
                                    card(rows[hover.row])
                                        .offset(x: min(hover.at.x + 14, offset + visible - 300), y: hover.at.y + 14)
                                        .allowsHitTesting(false)
                                }
                            }
                            .onContinuousHover { phase in
                                switch phase {
                                case .active(let p):
                                    hover = WorkGantt.row(at: p.y - Self.headerHeight, rowHeight: Self.rowHeight, count: rows.count)
                                        .map { Hover(row: $0, at: p) }
                                case .ended:
                                    hover = nil
                                }
                            }
                            .onTapGesture { p in
                                if let n = WorkGantt.row(at: p.y - Self.headerHeight, rowHeight: Self.rowHeight, count: rows.count) {
                                    open(rows[n].ref)
                                }
                            }
                            .accessibilityHidden(true)
                    }
                    .scrollPosition($position)
                    .onScrollGeometryChange(for: Double.self) { Double($0.contentOffset.x) } action: { _, x in offset = x }
                    .onAppear { scrollToToday(interval: interval, width: width, visible: visible) }
                    .onChange(of: nowAsked) { scrollToToday(interval: interval, width: width, visible: visible) }
                }
            }
            .background(Paper.raised)
        }
    }

    /// Today a day and a half in from the leading edge, or the start when it all fits.
    private func scrollToToday(interval: DateInterval, width: Double, visible: Double) {
        guard width > visible else { return }
        let x = WorkGantt.x(.now, in: interval, width: width) - visible * 0.25
        position.scrollTo(x: min(max(x, 0), width - visible))
    }

    /// The names at the leading edge: the epic's title and its line, a click opens it.
    private func names(_ rows: [WorkGantt.Row]) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(WorkGantt.nameHeader.uppercased()).appText(.fine).fontWeight(.semibold).foregroundStyle(.secondary)
                .padding(.horizontal, 16)
                .frame(width: Self.nameWidth, height: Self.headerHeight, alignment: .leading)
                .background(Paper.well)
                .overlay(alignment: .bottom) { Rectangle().fill(Paper.rule).frame(height: 1) }
            ForEach(rows) { row in
                Button { open(row.ref) } label: {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(row.title).appText(.supporting).fontWeight(.semibold)
                            .lineLimit(1).truncationMode(.tail)
                        Text(row.line).appText(.fine).foregroundStyle(.secondary)
                            .lineLimit(1).truncationMode(.tail)
                    }
                    .padding(.horizontal, 16)
                    .frame(width: Self.nameWidth, height: Self.rowHeight, alignment: .leading)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .opacity(row.kind == .held ? 0.7 : 1)
                .help("\(row.title)\n\(row.help)")
                .accessibilityLabel("\(row.title). \(row.line). \(row.reason ?? row.help)")
                .accessibilityHint("Opens the epic")
                .workWindowMenu(WorkPlace(kind: .issue, ref: row.ref))
                .overlay(alignment: .bottom) { Rectangle().fill(Paper.rule.opacity(0.5)).frame(height: 1) }
            }
        }
        .frame(width: Self.nameWidth)
        .overlay(alignment: .trailing) { Rectangle().fill(Paper.rule).frame(width: 1) }
    }

    private func card(_ row: WorkGantt.Row) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(row.title).appText(.supporting).fontWeight(.semibold).lineLimit(2)
            Text(row.help).appText(.fine).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .frame(width: 280, alignment: .leading)
        .padding(10)
        .background(RoundedRectangle(cornerRadius: Paper.Radius.control).fill(Paper.raised))
        .overlay(RoundedRectangle(cornerRadius: Paper.Radius.control).strokeBorder(Paper.rule))
    }
}

/// One frame of the track: the days, Today, the bars, the arrows and the reasons.
private struct Chart {
    var rows: [WorkGantt.Row]
    var interval: DateInterval
    var ticks: [Date]
    var width: Double
    var height: Double
    var now: Date
    var offset: Double
    var visible: Double

    static let solid = StateTint.vouched.color ?? Paper.ink
    static let ahead = Paper.accent
    static let plannedFill = Paper.lineAccent.opacity(0.45)
    static let heldFill = Paper.forgeWaitingWash
    static let heldEdge = Paper.forgeWaitingRule
    static let heldTag = Paper.forgeWaiting
    static let today = StateTint.warning.color ?? Paper.ink

    func x(_ date: Date) -> Double { WorkGantt.x(date, in: interval, width: width) }
    func mid(_ row: Int) -> Double { WorkTimelineGantt.headerHeight + WorkTimelineGantt.rowHeight * (Double(row) + 0.5) }
    func bar(_ span: DateInterval, row: Int) -> CGRect {
        let from = max(x(span.start), -4), to = min(x(span.end), width + 4)
        return CGRect(x: from, y: mid(row) - WorkTimelineGantt.barHeight / 2, width: max(to - from, 4), height: WorkTimelineGantt.barHeight)
    }

    func draw(_ gc: inout GraphicsContext) {
        let header = WorkTimelineGantt.headerHeight
        gc.fill(Path(CGRect(x: 0, y: 0, width: width, height: header)), with: .color(Paper.well))
        let calendar = Calendar.current
        let today = calendar.startOfDay(for: now)
        // The days: a rule at each, its name centred in the header.
        for (n, tick) in ticks.enumerated() {
            let left = x(tick), right = n + 1 < ticks.count ? x(ticks[n + 1]) : width
            if n > 0 { gc.fill(Path(CGRect(x: left - 0.5, y: 0, width: 1, height: height)), with: .color(Paper.rule.opacity(0.5))) }
            let isToday = tick == today
            let words = isToday ? WorkGantt.today : WorkGantt.day(tick)
            gc.draw(Text(words.uppercased()).stepped(.fine).fontWeight(isToday ? .bold : .medium)
                .foregroundStyle(isToday ? Self.today : Paper.secondary),
                    at: CGPoint(x: (left + right) / 2, y: header / 2))
        }
        gc.fill(Path(CGRect(x: 0, y: header - 1, width: width, height: 1)), with: .color(Paper.rule))
        // A rule under each row.
        for n in rows.indices {
            let y = header + WorkTimelineGantt.rowHeight * Double(n + 1)
            gc.fill(Path(CGRect(x: 0, y: y - 0.5, width: width, height: 1)), with: .color(Paper.rule.opacity(0.5)))
        }
        arrows(&gc)
        for (n, row) in rows.enumerated() { draw(&gc, row, n) }
        // Today: a line through the rows.
        let nowX = x(now)
        gc.fill(Path(CGRect(x: nowX - 0.75, y: header, width: 1.5, height: height - header)), with: .color(Self.today))
    }

    private func draw(_ gc: inout GraphicsContext, _ row: WorkGantt.Row, _ n: Int) {
        let radius = Paper.Radius.code
        // The words that stay at the track's leading edge while it scrolls in time.
        let edge = offset + 12
        switch row.kind {
        case .started:
            if let s = row.solid {
                let r = bar(s, row: n)
                gc.fill(Path(roundedRect: r, cornerRadii: .init(topLeading: radius, bottomLeading: radius)), with: .color(Self.solid))
            }
            if let a = row.ahead { dashed(&gc, bar(a, row: n), open: true) }
        case .planned:
            if let a = row.ahead {
                let r = bar(a, row: n)
                gc.fill(Path(roundedRect: r, cornerRadius: radius), with: .color(Self.plannedFill))
                dashed(&gc, r, open: false)
            }
        case .held:
            var tagX = edge
            if let s = row.solid {
                let r = bar(s, row: n)
                let shape = Path(roundedRect: r, cornerRadius: radius)
                gc.fill(shape, with: .color(Self.heldFill))
                gc.stroke(shape, with: .color(Self.heldEdge), lineWidth: 1)
                tagX = max(r.maxX + 8, edge)
            }
            gc.draw(Text(WorkGantt.onHoldTag).stepped(.fine).fontWeight(.semibold).foregroundStyle(Self.heldTag),
                    at: CGPoint(x: tagX, y: mid(n)), anchor: .leading)
        case .undated:
            var textX = edge
            if let s = row.solid {
                let r = bar(s, row: n)
                gc.fill(Path(roundedRect: r, cornerRadius: radius), with: .color(Self.solid.opacity(0.5)))
                textX = max(r.maxX + 8, edge)
            }
            let dot = CGRect(x: textX, y: mid(n) - 3, width: 6, height: 6)
            gc.fill(Path(ellipseIn: dot), with: .color(Paper.tertiary))
            gc.draw(Text(row.reason ?? "").stepped(.fine).foregroundStyle(Paper.secondary),
                    at: CGPoint(x: dot.maxX + 6, y: mid(n)), anchor: .leading)
        }
    }

    /// The dashed span: the predicted rest of a started epic (open at its start, where the solid meets it) or the
    /// edge of a planned one.
    private func dashed(_ gc: inout GraphicsContext, _ r: CGRect, open: Bool) {
        let style = StrokeStyle(lineWidth: 1.5, dash: [4, 3])
        let inset = r.insetBy(dx: 0.75, dy: 0.75)
        if open {
            var p = Path()
            let radius = Paper.Radius.code
            p.move(to: CGPoint(x: inset.minX, y: inset.minY))
            p.addLine(to: CGPoint(x: inset.maxX - radius, y: inset.minY))
            p.addQuadCurve(to: CGPoint(x: inset.maxX, y: inset.minY + radius), control: CGPoint(x: inset.maxX, y: inset.minY))
            p.addLine(to: CGPoint(x: inset.maxX, y: inset.maxY - radius))
            p.addQuadCurve(to: CGPoint(x: inset.maxX - radius, y: inset.maxY), control: CGPoint(x: inset.maxX, y: inset.maxY))
            p.addLine(to: CGPoint(x: inset.minX, y: inset.maxY))
            gc.stroke(p, with: .color(Self.ahead), style: style)
        } else {
            gc.stroke(Path(roundedRect: inset, cornerRadius: Paper.Radius.code), with: .color(Self.ahead), style: style)
        }
    }

    /// An arrow from each blocker's end to the start of the epic it blocks: across, down, then across to it.
    private func arrows(_ gc: inout GraphicsContext) {
        let index = Dictionary(rows.enumerated().map { ($1.ref, $0) }, uniquingKeysWith: { a, _ in a })
        for (n, row) in rows.enumerated() {
            guard let start = (row.ahead ?? row.solid)?.start else { continue }
            let toX = x(start)
            for blocker in row.blockers {
                guard let b = index[blocker], let end = (rows[b].ahead ?? rows[b].solid)?.end else { continue }
                let fromX = x(end), fromY = mid(b), toY = mid(n)
                // A planned epic starts no earlier than its blocker's finish, so the turn is between them.
                let turn = toX - fromX > 16 ? fromX + 8 : fromX + max(toX - fromX, 0) / 2
                var p = Path()
                p.move(to: CGPoint(x: fromX, y: fromY))
                p.addLine(to: CGPoint(x: turn, y: fromY))
                p.addLine(to: CGPoint(x: turn, y: toY))
                p.addLine(to: CGPoint(x: toX - 1, y: toY))
                gc.stroke(p, with: .color(Paper.edge), lineWidth: 1)
                var head = Path()
                head.move(to: CGPoint(x: toX - 6, y: toY - 4))
                head.addLine(to: CGPoint(x: toX - 1, y: toY))
                head.addLine(to: CGPoint(x: toX - 6, y: toY + 4))
                gc.stroke(head, with: .color(Paper.edge), lineWidth: 1)
            }
        }
    }
}
#endif
