import Foundation

/// Work ▸ Timeline's forecast as a Gantt (#7526, epic #7067; the Mac wireframes Alex approved at gate 2): a row for
/// each open epic of the schedule (#7525), with its name and a line under it, and in the track the solid span of
/// the work so far, the dashed span to the predicted finish, or the planned span; an epic with no date says why.
/// The client draws it; this works out the rows, the days and the words, so each client draws the same.
public enum WorkGantt {
    /// What a row's track draws.
    public enum Kind: Equatable, Sendable {
        /// Started: solid from the start to now, then dashed to the predicted finish.
        case started
        /// Not started: a light span from the planned start to the predicted finish.
        case planned
        /// On hold, shown by choice: a tinted span over its work so far, or only the tag.
        case held
        /// No date: the reason in the track.
        case undated
    }

    public struct Row: Equatable, Sendable, Identifiable {
        public var id: String { ref }
        public var ref: String
        public var title: String
        /// Line 2 under the name: "Started Nov 25 · 3/6 issues · forecast Nov 29".
        public var line: String
        public var kind: Kind
        public var feature: String?
        /// The solid span (started, or a held epic's work so far).
        public var solid: DateInterval?
        /// The dashed span to the predicted finish (started) or the planned span (planned).
        public var ahead: DateInterval?
        /// Why it has no date, in the track (undated).
        public var reason: String?
        /// The refs of its open blockers that are rows of the chart: an arrow from each one's end to its start.
        public var blockers: [String]
        /// The words a hover says: the range of the finish, 8 times in 10.
        public var help: String
    }

    /// The rows in the schedule's order (started by start, planned by planned start, no date, on hold), from the
    /// timeline's epics for the titles and children; the epics on hold only when `held` is on.
    public static func rows(_ timeline: DaemonAPI.WorkTimeline, now: Date, held: Bool,
                            calendar: Calendar = .current) -> [Row] {
        guard let schedule = timeline.schedule else { return [] }
        let epics = Dictionary(timeline.epics.map { ($0.item.node.ref, $0) }, uniquingKeysWith: { a, _ in a })
        let shown = schedule.rows.filter { held || $0.reasonKind != .onHold }
        let refs = Set(shown.map(\.epic.ref))
        return shown.map { row in
            let epic = epics[row.epic.ref]
            let title = epic?.item.title ?? row.epic.ref.split(separator: "#").last.map { "Epic #\($0)" } ?? row.epic.ref
            let children = epic?.children ?? []
            let done = children.filter { $0.closedAt != nil }.count
            let issues = children.isEmpty ? nil : row.startedAt != nil ? "\(done)/\(children.count) issues"
                : children.count == 1 ? "1 issue" : "\(children.count) issues"
            let finish = row.finish.map { day($0.mid, calendar: calendar) }
            var out = Row(ref: row.epic.ref, title: title, line: "", kind: .undated, feature: epic?.item.feature,
                          blockers: (row.blockers ?? []).map(\.ref).filter { refs.contains($0) && $0 != row.epic.ref },
                          help: help(row, calendar: calendar))
            if row.reasonKind == .onHold {
                out.kind = .held
                if let start = row.startedAt, start < now { out.solid = DateInterval(start: start, end: now) }
                out.line = [onHoldWords, issues].compactMap { $0 }.joined(separator: " · ")
            } else if let start = row.startedAt, let f = row.finish {
                out.kind = .started
                out.solid = DateInterval(start: min(start, now), end: now)
                out.ahead = DateInterval(start: now, end: max(f.mid, now))
                out.line = ["Started \(day(start, calendar: calendar))", issues, finish.map { "forecast \($0)" }]
                    .compactMap { $0 }.joined(separator: " · ")
            } else if let start = row.plannedStart, let f = row.finish {
                out.kind = .planned
                out.ahead = DateInterval(start: start, end: max(f.mid, start))
                out.line = ["Planned \(day(start, calendar: calendar))", issues, finish.map { "forecast \($0)" }]
                    .compactMap { $0 }.joined(separator: " · ")
            } else {
                out.kind = .undated
                out.reason = reasonWords(row.reason)
                // A started epic with no forecast still shows its work so far.
                if let start = row.startedAt, start < now { out.solid = DateInterval(start: start, end: now) }
                out.line = ["No forecast", row.startedAt.map { "started \(day($0, calendar: calendar))" }, issues]
                    .compactMap { $0 }.joined(separator: " · ")
            }
            return out
        }
    }

    /// The words for why an epic has no date (#7525's reasons); a reason this build does not know says "No date".
    public static func reasonWords(_ reason: String?) -> String {
        switch reason.flatMap(DaemonAPI.WorkTimelineScheduleReason.init(rawValue:)) {
        case .onHold: onHoldWords
        case .noIssues: "Epic has no issues"
        case .blockerOnHold: "Blocker is on hold"
        case .blockerNotEpic: "Blocker is not an epic"
        case .blockerNotRead: "Blocker could not be read"
        case .cycle: "Dependency cycle"
        case .blockerUndated: "Blocker has no date"
        case .noForecast: "Not enough history to forecast"
        case .noPlace: "No free place in the schedule"
        case .incomplete: "Too many epics to read them all"
        case nil: "No date"
        }
    }

    public static let onHoldWords = WorkPlan.onHoldWords
    /// The tag on a held row's track.
    public static let onHoldTag = "ON HOLD"
    public static let badge = "FORECAST"
    public static let nameHeader = "Epic"
    public static let today = "Today"
    public static let legend = "Solid = elapsed work · dashed = predicted remaining time · arrows show schedule order. "
        + "The forecast is an estimate, not a promise of when work starts."
    public static let noSchedule = "No forecast yet"
    public static let noScheduleLine = "The machines that read this project do not make a schedule yet. Update Agents on them to see it."
    public static let noRows = "No open epics"

    /// "3 epic places · 1 occupied · read 10:42 AM": the places, the started epics that hold one, and when the
    /// schedule was made.
    public static func capacityWords(_ schedule: DaemonAPI.WorkTimelineSchedule) -> String {
        let occupied = schedule.rows.filter { $0.startedAt != nil && $0.reasonKind != .onHold }.count
        let places = schedule.places == 1 ? "1 epic place" : "\(schedule.places) epic places"
        return "\(places) · \(occupied) occupied · read \(schedule.readAt.formatted(date: .omitted, time: .shortened))"
    }

    /// A hover's words: when it started or is planned to, and when it finishes, 8 times in 10.
    static func help(_ row: DaemonAPI.WorkTimelineScheduleRow, calendar: Calendar) -> String {
        var parts: [String] = []
        if let s = row.startedAt { parts.append("Started \(day(s, calendar: calendar))") }
        if let p = row.plannedStart { parts.append("Planned to start \(day(p, calendar: calendar))") }
        if let f = row.finish {
            parts.append("Predicted to finish \(day(f.mid, calendar: calendar)); 8 times in 10 between \(day(f.low, calendar: calendar)) and \(day(f.high, calendar: calendar))")
        } else {
            parts.append(reasonWords(row.reason))
        }
        return parts.joined(separator: ". ")
    }

    /// "Nov 25".
    public static func day(_ date: Date, calendar: Calendar = .current) -> String {
        var style = Date.FormatStyle.dateTime.month(.abbreviated).day()
        style.calendar = calendar
        style.timeZone = calendar.timeZone
        return date.formatted(style)
    }

    // MARK: The days

    /// The days across: from the day before the earliest start (no more than `before` days back) to the day after
    /// the latest predicted finish (no more than `after` days on), and at least a week from yesterday.
    public static func days(_ rows: [Row], now: Date, calendar: Calendar = .current,
                            before: Int = 14, after: Int = 60) -> DateInterval {
        let today = calendar.startOfDay(for: now)
        let earliest = calendar.date(byAdding: .day, value: -before, to: today) ?? today
        let latest = calendar.date(byAdding: .day, value: after, to: today) ?? today
        let spans = rows.flatMap { [$0.solid, $0.ahead].compactMap { $0 } }
        let from = max(min(spans.map(\.start).min() ?? today, today), earliest)
        let to = min(max(spans.map(\.end).max() ?? today, today), latest)
        var start = calendar.date(byAdding: .day, value: -1, to: calendar.startOfDay(for: from)) ?? from
        var end = calendar.date(byAdding: .day, value: 2, to: calendar.startOfDay(for: to)) ?? to
        let week = calendar.date(byAdding: .day, value: 7, to: start) ?? end
        if end < week { end = week }
        if start > now { start = today }
        return DateInterval(start: start, end: end)
    }

    /// The start of each day in `interval`.
    public static func ticks(_ interval: DateInterval, calendar: Calendar = .current) -> [Date] {
        var out: [Date] = []
        var day = calendar.startOfDay(for: interval.start)
        while day < interval.end, out.count < 400 {
            if day >= interval.start { out.append(day) }
            guard let next = calendar.date(byAdding: .day, value: 1, to: day) else { break }
            day = next
        }
        return out
    }

    /// A day's width: the days fill `width`, and are never narrower than `least`.
    public static func dayWidth(days: Int, width: Double, least: Double = 64) -> Double {
        max(width / Double(max(days, 1)), least)
    }

    /// Where a time is on a track of `width` that spans `interval`.
    public static func x(_ date: Date, in interval: DateInterval, width: Double) -> Double {
        guard interval.duration > 0 else { return 0 }
        return (date.timeIntervalSince(interval.start) / interval.duration) * width
    }

    /// The row under a point `y` of the track, or nil past the last.
    public static func row(at y: Double, rowHeight: Double, count: Int) -> Int? {
        guard y >= 0 else { return nil }
        let n = Int(y / rowHeight)
        return n < count ? n : nil
    }
}

extension DaemonAPI.WorkTimelineSchedule {
    /// Several machines' schedules of one project as one (#7526): each epic's row once, the first machine's; the most
    /// places, the oldest read, and incomplete when any was.
    public static func merged(_ schedules: [Self]) -> Self? {
        guard var out = schedules.first else { return nil }
        var seen = Set(out.rows.map(\.epic.ref))
        for s in schedules.dropFirst() {
            out.places = max(out.places, s.places)
            out.readAt = min(out.readAt, s.readAt)
            if s.incomplete == true { out.incomplete = true }
            out.rows += s.rows.filter { seen.insert($0.epic.ref).inserted }
        }
        if schedules.count > 1 {
            // The schedule's order (#7525's compareRows): started by start, planned by planned start, no date, on hold.
            func key(_ r: DaemonAPI.WorkTimelineScheduleRow) -> (Int, Date) {
                if r.reasonKind == .onHold { return (3, .distantPast) }
                if let s = r.startedAt { return (0, s) }
                if let p = r.plannedStart { return (1, p) }
                return (2, .distantPast)
            }
            out.rows = out.rows.enumerated().sorted { a, b in
                let ka = key(a.element), kb = key(b.element)
                return ka.0 != kb.0 ? ka.0 < kb.0 : ka.1 != kb.1 ? ka.1 < kb.1 : a.offset < b.offset
            }.map(\.element)
        }
        return out
    }
}
