import Foundation

// MARK: - The wire (#4172)

public extension DaemonAPI {
    /// `work/timeline` (#4172): the epics of a project (its id) that overlap `from` to `to`.
    struct WorkTimelineRequest: Codable, Sendable, Hashable {
        public var project: String
        public var from: Date
        public var to: Date

        public init(project: String, from: Date, to: Date) {
            self.project = project
            self.from = from
            self.to = to
        }
    }

    /// The epics of the window, each with its children, its lines and its forecast (M10), and the
    /// releases published in it. `timeZone` is the working clock's; `backtest` how past forecasts did.
    struct WorkTimeline: Codable, Sendable, Hashable {
        public var epics: [WorkTimelineEpic]
        public var releases: [WorkTimelineRelease]
        public var timeZone: String?
        public var backtest: WorkTimelineBacktest?
        public var errors: [String]?

        public init(epics: [WorkTimelineEpic] = [], releases: [WorkTimelineRelease] = [], timeZone: String? = nil,
                    backtest: WorkTimelineBacktest? = nil, errors: [String]? = nil) {
            self.epics = epics
            self.releases = releases
            self.timeZone = timeZone
            self.backtest = backtest
            self.errors = errors
        }
    }

    /// How the forecast did on the epics of the last 30 days: `error` is the median factor it was
    /// off by (2.2 says ×2.2); `width` already widens each band.
    struct WorkTimelineBacktest: Codable, Sendable, Hashable {
        public var error: Double
        public var width: Double
        public var cuts: Int
        public var holds: Int
        public var epics: Int
        public var at: Date

        public init(error: Double, width: Double, cuts: Int, holds: Int, epics: Int, at: Date) {
            self.error = error
            self.width = width
            self.cuts = cuts
            self.holds = holds
            self.epics = epics
            self.at = at
        }
    }

    /// A child of an epic: when it started (absent while nothing started it), closed, or is
    /// forecast to close; `waitsForPerson` when it is labelled for the person, so not forecast.
    struct WorkTimelineChild: Codable, Sendable, Hashable {
        public var item: ForgeItem
        public var stateReason: String?
        public var createdAt: Date
        public var startedAt: Date?
        public var closedAt: Date?
        public var waitsForPerson: Bool?
        public var forecastAt: Date?

        public init(item: ForgeItem, stateReason: String? = nil, createdAt: Date, startedAt: Date? = nil,
                    closedAt: Date? = nil, waitsForPerson: Bool? = nil, forecastAt: Date? = nil) {
            self.item = item
            self.stateReason = stateReason
            self.createdAt = createdAt
            self.startedAt = startedAt
            self.closedAt = closedAt
            self.waitsForPerson = waitsForPerson
            self.forecastAt = forecastAt
        }
    }

    /// An epic: its children, its lines (the first first) and, while one is open, its forecast.
    struct WorkTimelineEpic: Codable, Sendable, Hashable {
        public var item: ForgeItem
        public var stateReason: String?
        public var createdAt: Date
        public var closedAt: Date?
        public var children: [WorkTimelineChild]
        public var segments: [WorkTimelineSegment]
        public var forecast: WorkTimelineForecast?

        public init(item: ForgeItem, stateReason: String? = nil, createdAt: Date, closedAt: Date? = nil,
                    children: [WorkTimelineChild] = [], segments: [WorkTimelineSegment] = [],
                    forecast: WorkTimelineForecast? = nil) {
            self.item = item
            self.stateReason = stateReason
            self.createdAt = createdAt
            self.closedAt = closedAt
            self.children = children
            self.segments = segments
            self.forecast = forecast
        }
    }

    /// An open epic's close: `mid` the P50, `low` to `high` the band that holds it 8 times in 10;
    /// `waits` when children wait for the person, so the line does not join.
    struct WorkTimelineForecast: Codable, Sendable, Hashable {
        public var low: Date
        public var mid: Date
        public var high: Date
        public var waits: Bool?

        public init(low: Date, mid: Date, high: Date, waits: Bool? = nil) {
            self.low = low
            self.mid = mid
            self.high = high
            self.waits = waits
        }
    }

    struct WorkTimelineRelease: Codable, Sendable, Hashable {
        public var tag: String
        public var at: Date
        public var repo: String
        public var url: String?

        public init(tag: String, at: Date, repo: String, url: String? = nil) {
            self.tag = tag
            self.at = at
            self.repo = repo
            self.url = url
        }
    }

    /// A line of an epic: from its first child's start to its last child's close.
    struct WorkTimelineSegment: Codable, Sendable, Hashable {
        public var startedAt: Date?
        public var closedAt: Date?

        public init(startedAt: Date? = nil, closedAt: Date? = nil) {
            self.startedAt = startedAt
            self.closedAt = closedAt
        }
    }
}

public extension DaemonAPI.Method {
    /// `WorkTimelineRequest` → `WorkTimeline` (#4172).
    static let workTimeline = "work/timeline"
}

// MARK: - The layout (#4173, #5506)

/// Work ▸ Timeline (#4173): each epic one block in its own row on a linear time axis (#5506, #7058). The
/// block is solid from when its first child started to when the work stopped (#6043). An open epic then
/// has a ghost (the lighter tint with a dashed edge) to its predicted finish; one that has not started has
/// only that ghost, from Now. No individual issue and no epic on hold is drawn (Alex, 2026-11-24). This is the part
/// both clients share (the web page has a TypeScript copy, `Web/src/model/workTimeline.ts`): the
/// range, the blocks, their rows, the marks and the words. The Mac draws it with a `Canvas`.
public enum WorkTimeline {
    /// The toolbar's range. 1 Day and 2 Days keep Now in the centre; Week puts it at three quarters,
    /// as the forecast reaches only about 1.5 days ahead (Alex, 2026-11-09, #4173).
    public enum Range: String, CaseIterable, Identifiable, Sendable, Hashable {
        case day, twoDays, week

        public var id: Self { self }

        public var title: String {
            switch self {
            case .day: "1 Day"
            case .twoDays: "2 Days"
            case .week: "Week"
            }
        }

        public var span: TimeInterval {
            switch self {
            case .day: 86_400
            case .twoDays: 2 * 86_400
            case .week: 7 * 86_400
            }
        }

        /// The range in words, after "in": "6 epics in these 2 days".
        public var these: String {
            switch self {
            case .day: "these 24 hours"
            case .twoDays: "these 2 days"
            case .week: "these 7 days"
            }
        }

        /// Where Now is, from the left edge, as a share of the width.
        public var nowAt: Double { self == .week ? 0.75 : 0.5 }

        /// The window seen when the page opens or Now is clicked.
        public func window(now: Date) -> DateInterval {
            let from = now.addingTimeInterval(-span * nowAt)
            return DateInterval(start: from, duration: span)
        }

        /// A pinch steps the range: apart is a shorter one, together a longer one.
        public func zoomed(in zoomIn: Bool) -> Range {
            let all = Self.allCases
            let i = all.firstIndex(of: self)! + (zoomIn ? -1 : 1)
            return all[min(max(i, 0), all.count - 1)]
        }
    }

    /// A time axis: `interval` across `width` points.
    public struct Axis: Sendable, Hashable {
        public var interval: DateInterval
        public var width: Double

        public init(interval: DateInterval, width: Double) {
            self.interval = interval
            self.width = width
        }

        public func x(_ date: Date) -> Double {
            interval.duration <= 0 ? 0 : date.timeIntervalSince(interval.start) / interval.duration * width
        }

        public func date(_ x: Double) -> Date {
            interval.start.addingTimeInterval(width <= 0 ? 0 : x / width * interval.duration)
        }

        /// The hour ticks for a range seen across the width: every 3 hours at 1 Day, 6 at 2 Days,
        /// 24 at Week, on the hour.
        public func ticks(seen span: TimeInterval, calendar: Calendar = .current) -> [Date] {
            let hours = span <= 86_400 * 1.5 ? 3 : span <= 86_400 * 3 ? 6 : 24
            // Each hour on the clock, so a tick stays on the hour across a change of summer time.
            var hour = calendar.dateInterval(of: .hour, for: interval.start)?.start ?? interval.start
            var out: [Date] = []
            while hour <= interval.end {
                if hour >= interval.start, calendar.component(.hour, from: hour) % hours == 0 { out.append(hour) }
                hour = calendar.date(byAdding: .hour, value: 1, to: hour) ?? interval.end.addingTimeInterval(1)
            }
            return out
        }
    }

    /// How an epic's block ends (#5506).
    public enum End: Sendable, Hashable {
        /// The epic closed: the block ends where its work stopped.
        case joined
        /// Closed as not planned: a grey block, then "✕ closed as not planned".
        case dropped
        /// Forecast: a lighter tint with a dashed edge after Now.
        case forecast
        /// Children wait for the person: a pill after it, 👤 and the number.
        case waits(Int)
        /// Open, with nothing to forecast: it ends where its work stopped.
        case open
    }

    /// One line: an epic's block (#7058).
    public struct Line: Sendable, Hashable, Identifiable {
        public var epic: DaemonAPI.LineageNode
        public var title: String
        public var number: String
        public var feature: String?
        /// The epic's children closed, and all of them.
        public var done: Int
        public var total: Int
        /// The solid span (#6043): from when its first child started to when the work stopped (its last
        /// child's close, or Now while a session is in a turn on it); nil while no child has started.
        public var work: DateInterval?
        /// An open epic's dashed ghost (#7058): from when its work stopped (Now, while no child has
        /// started) to its predicted finish; nil with no forecast.
        public var ghost: DateInterval?
        /// Where the block starts and ends: its solid span and its ghost.
        public var from: Date
        public var end: Date
        /// When the epic closed.
        public var closedAt: Date?
        public var how: End
        public var band: DateInterval?
        /// The epic and its open children that a session is in a turn on now (#4265): the line
        /// pulses and a ring turns at Now while it is above 0.
        public var working: Int
        /// The epic's priority (`ForgeItem.priority`: high, medium or low; nil for none): it gives its band.
        public var priority: String?
        /// The row, 0 at the top (#5506).
        public var row: Int = 0

        /// Its band (#6089): High 0, Medium 1, none 2, Low 3.
        public var priorityBand: Int { WorkTimeline.priorityBand(priority) }

        /// Its chip's words, after its title (#6089): *High*, *Medium* or *Low*; nil for none.
        public var chip: String? { WorkPlan.priorityWords(priority) }

        public var id: String { epic.ref }

        /// When the work stopped, on an open block where that was before Now (a minute's slack): the hover card says so.
        public func stopped(now: Date) -> Date? {
            guard open, let stopped = work?.end, stopped < now.addingTimeInterval(-60) else { return nil }
            return stopped
        }

        /// The hover card's figures, `#n · 6 of 8`. A block shows its title only (Alex, 2026-11-14).
        public var figures: String { "\(number) · \(done) of \(total)" }

        /// Whether the line runs past `date` (it has not ended by then).
        public var open: Bool {
            switch how {
            case .forecast, .waits, .open: true
            case .joined, .dropped: false
            }
        }
    }

    /// The lines of a timeline, past and forecast: one per epic (#7058). Not drawn: an epic with no
    /// children, one on hold, and one that has not started and has no forecast. `forecast` off draws the
    /// past only. `working` is the refs a session is in a turn on now (`workingNow`): only an open epic works.
    public static func lines(_ timeline: DaemonAPI.WorkTimeline, now: Date, forecast: Bool = true,
                             working: Set<String> = []) -> [Line] {
        timeline.epics.compactMap { epic -> Line? in
            let children = epic.children
            guard !children.isEmpty, epic.item.onHold != true else { return nil }
            let isOpen = epic.closedAt == nil
            let working = !isOpen ? 0
                : children.filter { $0.closedAt == nil && working.contains($0.item.node.ref) }.count
                    + (working.contains(epic.item.node.ref) ? 1 : 0)
            // The work stopped at its last child's close (or the last start after it); while a session is
            // in a turn on it, it did not stop (#6043).
            let starts = children.compactMap(\.startedAt)
            let work = starts.min().map { first -> DateInterval in
                let start = min(first, now)
                let last = working > 0 ? now : (starts + children.compactMap(\.closedAt)).max() ?? start
                return DateInterval(start: start, end: max(min(last, now), start))
            }
            let predicted = isOpen && forecast ? epic.forecast : nil
            let ghost = predicted.map { DateInterval(start: work?.end ?? now, end: max($0.mid, now)) }
            guard let from = work?.start ?? ghost?.start, let end = ghost?.end ?? work?.end else { return nil }
            var how = End.open
            var band: DateInterval?
            let waiting = children.filter { $0.closedAt == nil && $0.waitsForPerson == true }.count
            if !isOpen {
                let dropped = epic.stateReason == "not_planned" || children.allSatisfy { $0.stateReason == "not_planned" }
                how = dropped ? .dropped : .joined
            } else if waiting > 0 {
                how = .waits(waiting)
            } else if let predicted {
                how = .forecast
                band = DateInterval(start: min(predicted.low, predicted.high), end: max(predicted.low, predicted.high))
            }
            return Line(epic: epic.item.node, title: epic.item.title, number: WorkList.shortRef(epic.item.node.ref),
                        feature: epic.item.feature, done: children.filter { $0.closedAt != nil }.count,
                        total: children.count, work: work, ghost: ghost, from: from, end: end, closedAt: epic.closedAt,
                        how: how, band: band, working: working, priority: epic.item.priority)
        }
    }

    /// Where a block's drawing ends: its end or its band's, whichever is later.
    public static func reach(_ line: Line) -> Date { max(line.end, line.band?.end ?? line.end) }

    /// Only the lines that overlap the window (Alex, 2026-11-08). A line that waits for the person
    /// has no end, so it is drawn while it is open.
    public static func overlapping(_ lines: [Line], _ window: DateInterval) -> [Line] {
        lines.filter { line in
            let end: Date
            if case .waits = line.how { end = .distantFuture } else { end = reach(line) }
            return line.from <= window.end && end >= window.start
        }
    }

    /// The widest range: the rows are packed over its span, so they are the same in every range (#5506).
    static let widest = Range.week

    /// The gap after a block in its row, in time: 2 % of the widest range (#5506).
    public static let packGap: TimeInterval = widest.span / 50

    /// What a read covers and the rows are packed over: the widest range's window around `anchor`,
    /// and its width on each side to scroll through. It holds every narrower range's window around
    /// the same anchor, so a change of range packs the same epics the same way (#5506).
    public static func packSpan(_ anchor: Date) -> DateInterval {
        let window = widest.window(now: anchor)
        return DateInterval(start: window.start.addingTimeInterval(-widest.span), end: window.end.addingTimeInterval(widest.span))
    }

    /// A priority's band (#6089), the top first: High 0, Medium 1, none 2, Low 3.
    public static func priorityBand(_ priority: String?) -> Int {
        switch priority?.lowercased() {
        case "high": return 0
        case "medium": return 1
        case "low": return 3
        default: return 2
        }
    }

    /// A band's name at the plot's leading edge (#6089).
    public static func bandWords(_ band: Int) -> String {
        switch band {
        case 0: "High"
        case 1: "Medium"
        case 3: "Low"
        default: "None"
        }
    }

    /// Each priority is a band of rows, High at the top, then Medium, none and Low
    /// (#6089, Alex, 2026-11-17). In a band, each block goes in the first row that is free at its
    /// start, sorted by it, then by id (#5506), so a block never moves into a
    /// higher band's row.
    /// It packs in time, not in points: a row is busy until the block's reach and `gap` after it, so
    /// the rows do not change with the range. A block that waits for the person has no end: it holds
    /// its row while it is open. A label outside a block takes no room. A band with no block takes no row.
    public static func layout(_ lines: [Line], gap: TimeInterval = packGap) -> [Line] {
        var out: [Line] = []
        var first = 0
        for band in 0...3 {
            var busy: [Date] = []
            let mine = lines.filter { $0.priorityBand == band }.sorted { ($0.from, $0.id) < ($1.from, $1.id) }
            for line in mine {
                var placed = line
                var row = 0
                while row < busy.count, busy[row] > line.from { row += 1 }
                let until: Date
                if case .waits = line.how { until = .distantFuture } else { until = reach(line).addingTimeInterval(gap) }
                if row == busy.count { busy.append(until) } else { busy[row] = until }
                placed.row = first + row
                out.append(placed)
            }
            first += busy.count
        }
        return out
    }

    /// The bands in use, each with its first row and its rows, the top first (#6089).
    public struct Band: Sendable, Hashable {
        public var band: Int
        public var words: String
        public var row: Int
        public var rows: Int
    }

    public static func bands(_ lines: [Line]) -> [Band] {
        Dictionary(grouping: lines, by: \.priorityBand).map { band, mine in
            let rows = mine.map(\.row)
            return Band(band: band, words: bandWords(band), row: rows.min() ?? 0, rows: (rows.max() ?? 0) - (rows.min() ?? 0) + 1)
        }.sorted { $0.row < $1.row }
    }

    /// Where the rule above a band's first row goes: in the gap between the rows.
    public static func bandRuleY(_ row: Int) -> Double { rowTop(row) - rowGap / 2 }

    /// The rows in use.
    public static func rowCount(_ lines: [Line]) -> Int { (lines.map(\.row).max() ?? -1) + 1 }

    /// A block's geometry, in points (#5506): 28 high, 8 between rows, the first row 12 from the top.
    public static let blockHeight: Double = 28
    public static let rowGap: Double = 8
    public static let plotTop: Double = 12
    /// A row's top.
    public static func rowTop(_ row: Int) -> Double { plotTop + Double(row) * (blockHeight + rowGap) }
    /// Where the title goes: in the middle of the block.
    public static func titleY(_ row: Int) -> Double { rowTop(row) + blockHeight / 2 }
    /// The title's inset from the block's leading edge.
    public static let titleInset: Double = 6

    /// The next block's start in the same row, after `line`'s: a label outside `line` stops before it.
    public static func nextInRow(_ lines: [Line], after line: Line) -> Date? {
        lines.filter { $0.row == line.row && $0.from > line.from }.map(\.from).min()
    }

    /// `text` whole when it fits `room`, else cut short with "…", else nil.
    public static func cut(_ text: String, room: Double, measure: (String) -> Double) -> String? {
        if measure(text) <= room { return text }
        let chars = Array(text)
        var best: String?
        var low = 1, high = chars.count - 1
        while low <= high {
            let mid = (low + high) / 2
            let c = String(chars[..<mid]).trimmingCharacters(in: .whitespaces)
            if !c.isEmpty, measure("\(c)…") <= room {
                best = c + "…"
                low = mid + 1
            } else {
                high = mid - 1
            }
        }
        return best
    }

    /// What a closed-as-not-planned block says after it.
    public static let droppedWords = "✕ closed as not planned"

    /// A block's words (#5506). `inside`: its title in it, from `insideX` (held at the visible left
    /// edge, with ←, when the block starts before it). `outside`: what goes after the block, from
    /// `outsideX`: the title when the block is too narrow for it, and "✕ closed as not planned"; cut
    /// with "…" before the next block in the row.
    public struct BlockWords: Sendable, Hashable {
        public var inside: String?
        public var insideX: Double
        public var arrow: Bool
        public var outside: String?
        public var outsideX: Double
        /// The priority chip (#6089): after the title in the block when both fit, else after the
        /// words outside it, where there is room before the next block; nil when there is none.
        public var chip: String?
        public var chipX: Double = 0
        public var chipInside = false
    }

    /// A chip's padding on each side of its words, and the gap before it (#6089).
    public static let chipPadding: Double = 5
    public static let chipGap: Double = 6
    /// A chip's width, in points.
    public static func chipWidth(_ words: String, measure: (String) -> Double) -> Double { measure(words) + 2 * chipPadding }

    /// `trailing` is the width of what is drawn after the block first (the wait pill).
    public static func blockWords(_ line: Line, axis: Axis, measure: (String) -> Double, next: Date? = nil,
                                  left: Double = -.infinity, trailing: Double = 0) -> BlockWords {
        let sx = axis.x(line.from), ex = max(axis.x(line.end), sx)
        let fits = measure(line.title) + 2 * titleInset <= ex - sx
        let arrow = fits && sx < left
        let insideX = (arrow ? left : sx) + titleInset
        let inside = fits ? cut(arrow ? "← \(line.title)" : line.title, room: ex - insideX - titleInset, measure: measure) : nil
        let outsideX = axis.x(reach(line)) + 6 + (trailing > 0 ? trailing + 6 : 0)
        let words = [fits ? "" : line.title, line.how == .dropped ? droppedWords : ""].filter { !$0.isEmpty }.joined(separator: "  ")
        let room = (next.map { axis.x($0) - 6 } ?? .infinity) - outsideX
        var out = BlockWords(inside: inside, insideX: insideX, arrow: arrow,
                             outside: words.isEmpty ? nil : cut(words, room: room, measure: measure), outsideX: outsideX)
        guard let chip = line.chip else { return out }
        let width = chipWidth(chip, measure: measure)
        // In the block, after the whole title.
        if let inside, !inside.hasSuffix("…") {
            let x = insideX + measure(inside) + chipGap
            if x + width <= ex - titleInset {
                out.chip = chip
                out.chipX = x
                out.chipInside = true
                return out
            }
        }
        // After the block's words, with the words cut to leave it room.
        let wordsRoom = room - width - (words.isEmpty ? 0 : chipGap)
        guard wordsRoom >= 0 else { return out }
        if !words.isEmpty {
            guard let shown = cut(words, room: wordsRoom, measure: measure) else { return out }
            out.outside = shown
            out.chipX = outsideX + measure(shown) + chipGap
        } else {
            out.chipX = outsideX
        }
        out.chip = chip
        return out
    }

    // MARK: - Colours

    /// A feature's colour, as an index into the client's palette of system colours, the same
    /// every time for the same name. No feature takes the last one (grey).
    public static func colour(_ feature: String?, of count: Int) -> Int {
        guard let feature, !feature.isEmpty, count > 1 else { return max(count - 1, 0) }
        var hash: UInt32 = 2_166_136_261
        for byte in feature.lowercased().utf8 { hash = (hash ^ UInt32(byte)) &* 16_777_619 }
        return Int(hash % UInt32(count - 1))
    }

    /// The WCAG contrast of two sRGB colours, each 0–1.
    public static func contrast(_ a: [Double], _ b: [Double]) -> Double {
        func lum(_ c: [Double]) -> Double {
            let l = c.map { $0 <= 0.04045 ? $0 / 12.92 : pow(($0 + 0.055) / 1.055, 2.4) }
            return 0.2126 * l[0] + 0.7152 * l[1] + 0.0722 * l[2]
        }
        let x = lum(a), y = lum(b)
        return (max(x, y) + 0.05) / (min(x, y) + 0.05)
    }

    /// A feature's colour darkened, a step towards black at a time, until white text on it has 4.5:1
    /// (#5506): orange becomes a burnt orange, teal a deep teal; a colour that has it already stays.
    public static func forWhiteText(_ rgb: [Double]) -> [Double] {
        var c = rgb
        var k = 0
        while k < 40, contrast(c, [1, 1, 1]) < 4.5 {
            c = c.map { $0 * 0.97 }
            k += 1
        }
        return c
    }

    // MARK: - Hit testing

    /// The block the pointer is on (with its band, a few points round it).
    public static func hit(_ point: (x: Double, y: Double), lines: [Line], axis: Axis) -> Line? {
        lines.first { line in
            let top = rowTop(line.row)
            return point.y >= top && point.y <= top + blockHeight
                && point.x >= axis.x(line.from) - 2 && point.x <= axis.x(reach(line)) + 2
        }
    }

    // MARK: - Words

    static func time(_ date: Date, now: Date, calendar: Calendar) -> String {
        let style = Date.FormatStyle(timeZone: calendar.timeZone).hour().minute()
        if calendar.isDate(date, inSameDayAs: now) { return date.formatted(style) }
        return date.formatted(Date.FormatStyle(timeZone: calendar.timeZone).weekday(.abbreviated).hour().minute())
    }

    /// "3 wait for you": the hover card and VoiceOver, where a block waits for the person; its pill
    /// says 👤 and the number only (Alex, 2026-11-14).
    public static func waitWords(_ count: Int) -> String { "\(count) wait\(count == 1 ? "s" : "") for you" }

    /// "Past forecasts were off by ×2.2".
    public static func errorWords(_ backtest: DaemonAPI.WorkTimelineBacktest) -> String {
        "Past forecasts were off by ×\(backtest.error.formatted(.number.precision(.fractionLength(1))))"
    }

    /// The hover card is a mini row (Work look, milestones.md note 33): a glyph, the title, and this second
    /// line: "#4250 · 11 of 16 · closes about 5:00 PM · 3:00 PM–9:00 PM, 8 in 10".
    public static func lineLine(_ line: Line, now: Date, calendar: Calendar = .current) -> String {
        var out = [line.figures]
        if let stopped = line.stopped(now: now) { out.append(stoppedWords(stopped, now: now)) }
        switch line.how {
        case .joined: out.append("closed \(agoWords(line.closedAt ?? line.end, now: now))")
        case .dropped: out.append("closed as not planned")
        case .forecast:
            out.append("closes about \(time(line.end, now: now, calendar: calendar))")
            if let band = line.band {
                out.append("\(time(band.start, now: now, calendar: calendar))–\(time(band.end, now: now, calendar: calendar)), 8 in 10")
            }
        case .waits(let n): out.append(waitWords(n))
        case .open: out.append("open")
        }
        return out.joined(separator: " · ")
    }

    /// "stopped 3 h ago": an open block whose work stopped before Now (#6043).
    public static func stoppedWords(_ stopped: Date, now: Date) -> String { "stopped \(agoWords(stopped, now: now))" }

    /// "2 h ago", or "just now".
    static func agoWords(_ date: Date, now: Date) -> String {
        let age = WorkLook.ageWords(now.timeIntervalSince(date))
        return age == "now" ? "just now" : "\(age) ago"
    }

    /// The title line above the plot (note 32): "Agents · 6 epics in these 2 days".
    public static func titleWords(project: String, epics: Int, range: Range) -> String {
        let count = "\(epics) epic\(epics == 1 ? "" : "s") in \(range.these)"
        return project.isEmpty ? count : "\(project) · \(count)"
    }

    /// The epics in `window`.
    public static func epicCount(_ lines: [Line], in window: DateInterval) -> Int {
        overlapping(lines, window).count
    }

    /// When the last epic before `date` closed, for the empty card (S11); nil when the read has none.
    public static func lastJoined(_ timeline: DaemonAPI.WorkTimeline, before date: Date) -> Date? {
        timeline.epics.filter { !$0.children.isEmpty && $0.item.onHold != true }.compactMap(\.closedAt).filter { $0 < date }.max()
    }

    /// What the plot shows (#5925): grey blocks that shimmer (S10) while no read has drawn yet, or while
    /// any read of the window runs and no epic is in it; the empty card (S11) only when every read
    /// ended and the window has none; else the epics. A device's copy with no epic is not "no epic"
    /// while the machines are read.
    public enum Shows: Sendable, Equatable { case loading, empty, epics }

    public static func shows(drawn: Bool, reading: Bool, epics: Int) -> Shows {
        if epics > 0 { return .epics }
        return !drawn || reading ? .loading : .empty
    }

    /// The empty card (S11): "No epic in these 2 days", and "The last one joined 4 d ago." when known.
    public static func noEpicsWords(_ range: Range) -> String { "No epic in \(range.these)" }

    public static func lastJoinedWords(_ date: Date, now: Date) -> String {
        "The last one joined \(agoWords(date, now: now))."
    }

    /// What VoiceOver reads, a row per line in time order (section 5): "Azure DevOps, 3 of 7 done,
    /// closes about 6:00 PM".
    public static func spoken(_ lines: [Line], now: Date, calendar: Calendar = .current) -> [(line: Line, words: String)] {
        // In priority order, then in time (#6089): High first.
        lines.sorted { ($0.priorityBand, $0.from, $0.id) < ($1.priorityBand, $1.from, $1.id) }.map { line in
            let end = switch line.how {
            case .joined: "closed \(time(line.closedAt ?? line.end, now: now, calendar: calendar))"
            case .dropped: "closed as not planned"
            case .forecast: "closes about \(time(line.end, now: now, calendar: calendar))"
            case .waits(let n): waitWords(n)
            case .open: "open"
            }
            let chip = line.chip.map { ", priority \($0.lowercased())" } ?? ""
            let stopped = line.stopped(now: now).map { ", \(stoppedWords($0, now: now))" } ?? ""
            return (line, "\(line.title)\(chip), \(line.done) of \(line.total) done, \(end)\(stopped)")
        }
    }

    /// A row of the list of epics (note 36 of `specs/4328-work-look/milestones.md`): the Timeline on a phone in
    /// portrait (#6044), as on the web page under 700 px. `line2`: "closes about 5:00 PM · 3:00 PM–9:00 PM".
    public struct EpicRow: Sendable, Hashable, Identifiable {
        public var line: Line
        public var line2: String
        public var id: String { line.id }
    }

    /// The list of epics in the window, as the web page's `epicRows`: in priority order (High first), then in the order they close: the forecast first (the soonest first), then those that wait for
    /// the person, then those still open, then those that joined (the latest first).
    public static func epicRows(_ lines: [Line], window: DateInterval, now: Date, calendar: Calendar = .current) -> [EpicRow] {
        func rank(_ line: Line) -> Int {
            switch line.how {
            case .forecast: 0
            case .waits: 1
            case .open: 2
            case .joined, .dropped: 3
            }
        }
        func line2(_ line: Line) -> String {
            switch line.how {
            case .forecast:
                let about = "closes about \(time(line.end, now: now, calendar: calendar))"
                guard let band = line.band else { return about }
                return "\(about) · \(time(band.start, now: now, calendar: calendar))–\(time(band.end, now: now, calendar: calendar))"
            case .waits(let n): return waitWords(n)
            case .open: return "open"
            case .joined: return "joined \(time(line.closedAt ?? line.end, now: now, calendar: calendar))"
            case .dropped: return "closed as not planned"
            }
        }
        return overlapping(lines, window).sorted { a, b in
            if a.priorityBand != b.priorityBand { return a.priorityBand < b.priorityBand }
            if rank(a) != rank(b) { return rank(a) < rank(b) }
            let x = a.closedAt ?? a.end, y = b.closedAt ?? b.end
            if x != y { return rank(a) == 3 ? x > y : x < y }
            return a.title < b.title
        }
        .map { line in
            EpicRow(line: line, line2: line.stopped(now: now).map { "\(line2(line)) · \(stoppedWords($0, now: now))" } ?? line2(line))
        }
    }

    /// "Now · 2:40 PM": the list of epics' first line.
    public static func nowWords(_ now: Date, calendar: Calendar = .current) -> String {
        "Now · \(time(now, now: now, calendar: calendar))"
    }

    /// Several machines' timelines of one project as one: each epic and release once.
    public static func merge(_ timelines: [DaemonAPI.WorkTimeline]) -> DaemonAPI.WorkTimeline {
        var out = DaemonAPI.WorkTimeline()
        var epics = Set<String>(), releases = Set<String>()
        for t in timelines {
            out.epics += t.epics.filter { epics.insert($0.item.node.ref).inserted }
            out.releases += t.releases.filter { releases.insert("\($0.repo)@\($0.tag)").inserted }
            out.timeZone = out.timeZone ?? t.timeZone
            out.backtest = out.backtest ?? t.backtest
            if let e = t.errors, !e.isEmpty { out.errors = (out.errors ?? []) + e.filter { !(out.errors ?? []).contains($0) } }
        }
        out.releases.sort { $0.at < $1.at }
        return out
    }

    /// The children's starts with every machine's sessions on them (#4276, `lineage/sessionsOn`),
    /// so a child a sleeping machine's session started still starts then: a child starts at the
    /// earliest of its own start, its close and its first session's link.
    public static func started(_ timeline: DaemonAPI.WorkTimeline, by sessions: [DaemonAPI.LineageSessionOn]) -> DaemonAPI.WorkTimeline {
        var first: [String: Date] = [:]
        for session in sessions {
            for node in session.on { first[node.ref] = min(first[node.ref] ?? session.at, session.at) }
        }
        guard !first.isEmpty else { return timeline }
        var out = timeline
        for e in out.epics.indices {
            for c in out.epics[e].children.indices {
                let child = out.epics[e].children[c]
                guard let at = first[child.item.node.ref] else { continue }
                out.epics[e].children[c].startedAt = min(child.startedAt ?? at, child.closedAt ?? at, at)
            }
        }
        return out
    }

    /// The epics and children a session is in a turn on now (#4265), by ref: every machine's
    /// sessions from the control plane's copy whose state is working, and this app's own sessions
    /// that are running with a `#<n>` label of one. A session that waits (on an event, on CI, on
    /// the person) is not in a turn, so it does not count. A session this app knows goes by its own
    /// state, which is newer than the control plane's copy.
    public static func workingNow(_ timeline: DaemonAPI.WorkTimeline, sessions: [DaemonAPI.LineageSessionOn],
                                  agents: [Agent]) -> Set<String> {
        // By the session's id: its ref's topic is its project's, which this app does not know (#4800).
        let known = Set(agents.map { $0.id.uuidString.uppercased() })
        var out = Set(sessions.filter {
            $0.state?.working == true && !known.contains(WireRef.sessionID(fromRef: $0.node.ref)?.uppercased() ?? $0.node.ref)
        }
            .flatMap { $0.on.map(\.ref) })
        let turning = agents.filter { $0.state == .running || $0.state == .starting }
        guard !turning.isEmpty else { return out }
        let labels = Set(turning.flatMap { $0.labels.map(\.value) })
        for epic in timeline.epics {
            for node in [epic.item.node] + epic.children.map(\.item.node) where labels.contains(WorkList.shortRef(node.ref)) {
                out.insert(node.ref)
            }
        }
        return out
    }

    /// The nodes a timeline asks `lineage/sessionsOn` for: its epics and their children, each once,
    /// at most 500.
    public static func sessionNodes(_ timeline: DaemonAPI.WorkTimeline) -> [DaemonAPI.LineageNode] {
        var seen = Set<String>()
        let all = timeline.epics.flatMap { [$0.item.node] + $0.children.map(\.item.node) }
        return Array(all.filter { seen.insert($0.ref).inserted }.prefix(500))
    }

    public static let showTheWeek = "Show the week"
    /// S12: no project to draw.
    public static let noProject = "No project to draw"
    /// The middle of the page when no machine's read worked and nothing was read before (#5969).
    public static let couldNotRead = "Could not read the timeline"
    public static let noProjectLine = "Timeline shows a project’s epics. Add a folder to a project to see them."
    public static let noMachine = "No machine draws the timeline yet. Update Agents on a machine to see it."
}
