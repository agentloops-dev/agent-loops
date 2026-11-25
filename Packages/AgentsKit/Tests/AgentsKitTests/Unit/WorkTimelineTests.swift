import Foundation
import Testing
@testable import AgentsKitCore

/// Work ▸ Timeline's layout (#4173; blocks in rows, #5506).
@Suite("Work timeline")
struct WorkTimelineTests {
    typealias Node = DaemonAPI.LineageNode
    static let now = Date(timeIntervalSince1970: 1_800_000_000)

    static func h(_ hours: Double) -> Date { now.addingTimeInterval(hours * 3600) }

    static func item(_ n: Int, feature: String? = nil, state: String = "open") -> DaemonAPI.ForgeItem {
        DaemonAPI.ForgeItem(node: Node(kind: "issue", ref: "github:alexec/Agents#\(n)"), title: "Issue \(n)",
                            state: state, updatedAt: now, feature: feature)
    }

    /// `created` is when it was filed: by default when it started (or closed), so the ghost adds nothing.
    static func child(_ n: Int, created: Double? = nil, started: Double? = nil, closed: Double? = nil,
                      waits: Bool = false, reason: String? = nil) -> DaemonAPI.WorkTimelineChild {
        DaemonAPI.WorkTimelineChild(item: item(n, state: closed == nil ? "open" : "closed"), stateReason: reason,
                                    createdAt: h(created ?? started ?? closed ?? -100), startedAt: started.map(h), closedAt: closed.map(h),
                                    waitsForPerson: waits ? true : nil)
    }

    /// By default created with its first child, and closed with its last once all are.
    static func epic(_ n: Int, _ children: [DaemonAPI.WorkTimelineChild], created: Double? = nil, closed: Double? = nil,
                     forecast: DaemonAPI.WorkTimelineForecast? = nil, feature: String? = "work",
                     reason: String? = nil) -> DaemonAPI.WorkTimelineEpic {
        let closes = children.compactMap(\.closedAt)
        let last = closes.count == children.count ? closes.max() : nil
        let closedAt = closed.map(h) ?? last
        return DaemonAPI.WorkTimelineEpic(item: item(n, feature: feature, state: closedAt == nil ? "open" : "closed"), stateReason: reason,
                                          createdAt: created.map(h) ?? children.map(\.createdAt).min() ?? h(-100), closedAt: closedAt,
                                          children: children, forecast: forecast)
    }

    static let axis = WorkTimeline.Axis(interval: DateInterval(start: h(-24), end: h(24)), width: 960)

    @Test func theRangesPutNowInTheCentreAndWeekAtThreeQuarters() {
        #expect(WorkTimeline.Range.allCases.map(\.title) == ["1 Day", "2 Days", "Week"])
        let two = WorkTimeline.Range.twoDays.window(now: Self.now)
        #expect(two.start == Self.h(-24) && two.end == Self.h(24))
        let day = WorkTimeline.Range.day.window(now: Self.now)
        #expect(day.start == Self.h(-12) && day.end == Self.h(12))
        // Alex, 2026-11-09: in Week, Now sits at three quarters of the width.
        let week = WorkTimeline.Range.week.window(now: Self.now)
        #expect(week.start == Self.h(-126) && week.end == Self.h(42))
        #expect(WorkTimeline.Range.twoDays.zoomed(in: true) == .day)
        #expect(WorkTimeline.Range.day.zoomed(in: true) == .day)
        #expect(WorkTimeline.Range.twoDays.zoomed(in: false) == .week)
    }

    @Test func theAxisIsLinearTime() {
        #expect(Self.axis.x(Self.h(-24)) == 0)
        #expect(Self.axis.x(Self.now) == 480)
        #expect(Self.axis.date(960) == Self.h(24))
        let ticks = Self.axis.ticks(seen: 2 * 86_400, calendar: Calendar(identifier: .gregorian))
        #expect(ticks.count >= 8 && ticks.allSatisfy { Self.axis.interval.contains($0) })
        // A wider plot keeps the seen range's step: still every 6 hours.
        let wide = WorkTimeline.Axis(interval: DateInterval(start: Self.h(-72), end: Self.h(72)), width: 2880)
        #expect(wide.ticks(seen: 2 * 86_400, calendar: Calendar(identifier: .gregorian)).count >= 24)
        // Across the end of summer time, a day's tick is still midnight.
        var pacific = Calendar(identifier: .gregorian)
        pacific.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        let fall = DateInterval(start: pacific.date(from: DateComponents(year: 2026, month: 10, day: 28))!,
                                end: pacific.date(from: DateComponents(year: 2026, month: 11, day: 5))!)
        let days = WorkTimeline.Axis(interval: fall, width: 800).ticks(seen: 7 * 86_400, calendar: pacific)
        #expect(days.count == 9 && days.allSatisfy { pacific.component(.hour, from: $0) == 0 })
    }

    @Test func anEpicWithNoChildrenIsNotDrawnAndAnEpicIsOneBlock() throws {
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(1, []),
            // A pause in the work (#7058): one block, solid over the pause.
            Self.epic(2, [Self.child(10, started: -20, closed: -18), Self.child(11, started: -6, closed: -5)]),
        ])
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        try #require(lines.count == 1)
        #expect(lines[0].id == "github:alexec/Agents#2" && lines[0].how == .joined)
        #expect(lines[0].work == DateInterval(start: Self.h(-20), end: Self.h(-5)))
        #expect(lines[0].figures == "#2 · 2 of 2")
    }

    /// #7058: a block is solid from start to finish; only an open epic has a ghost, to its predicted finish.
    @Test func onlyAnOpenEpicHasAGhostToItsPredictedFinish() throws {
        let forecast = DaemonAPI.WorkTimelineForecast(low: Self.h(2), mid: Self.h(5), high: Self.h(11))
        let timeline = DaemonAPI.WorkTimeline(epics: [
            // Created before its children and closed after them: its dates draw nothing.
            Self.epic(1, [Self.child(10, created: -20, started: -18, closed: -10)], created: -30, closed: -4),
            Self.epic(2, [Self.child(11, started: -8, closed: -5), Self.child(12)], created: -9, forecast: forecast),
            // Not started: the ghost only, from Now.
            Self.epic(3, [Self.child(13), Self.child(14)], created: -15, forecast: forecast),
            // Not started, and nothing to forecast: nothing to draw.
            Self.epic(4, [Self.child(15)], created: -15),
            Self.epic(5, [Self.child(16, started: -12, closed: -7)], created: -14, closed: -6, reason: "not_planned"),
            // On hold: never shown, open or closed.
            Self.with(Self.epic(6, [Self.child(17, started: -3)], forecast: forecast), onHold: true),
            Self.with(Self.epic(7, [Self.child(18, started: -3, closed: -2)]), onHold: true),
        ])
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        try #require(lines.map(\.number) == ["#1", "#2", "#3", "#5"])
        func span(_ from: Double, _ to: Double) -> DateInterval { DateInterval(start: Self.h(from), end: Self.h(to)) }
        #expect(lines.map(\.work) == [span(-18, -10), span(-8, -5), nil, span(-12, -7)])
        #expect(lines.map(\.ghost) == [nil, span(-5, 5), span(0, 5), nil])
        #expect(lines.map(\.from) == [-18, -8, 0, -12].map { Self.h($0) })
        #expect(lines.map(\.end) == [-10, 5, 5, -7].map { Self.h($0) })
        #expect(lines.map(\.how) == [.joined, .forecast, .forecast, .dropped])
        // The words say when the epic closed, not when its work stopped.
        #expect(WorkTimeline.lineLine(lines[0], now: Self.now) == "#1 · 1 of 1 · closed 4 h ago")
        // Nothing started, so nothing stopped.
        #expect(lines[2].stopped(now: Self.now) == nil)
        #expect(WorkTimeline.lastJoined(timeline, before: Self.h(-1)) == Self.h(-4))
        // A session in a turn: solid to Now, the ghost from there. One on an epic not started: still a ghost.
        let working = WorkTimeline.lines(timeline, now: Self.now, working: Set([3, 5, 12].map { "github:alexec/Agents#\($0)" }))
        try #require(working.count == 4)
        #expect(working.map(\.working) == [0, 1, 1, 0])
        #expect(working[1].work == span(-8, 0) && working[1].ghost == span(0, 5) && working[2].work == nil)
        // Forecast off: the past only.
        #expect(WorkTimeline.lines(timeline, now: Self.now, forecast: false).map(\.number) == ["#1", "#2", "#5"])
    }

    @Test func anOpenLineIsForecastWaitsOrStopsAtNow() throws {
        let forecast = DaemonAPI.WorkTimelineForecast(low: Self.h(2), mid: Self.h(5), high: Self.h(11))
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(4, [Self.child(20, started: -8, closed: -3), Self.child(21, started: -1),
                          Self.child(22)], forecast: forecast),
            Self.epic(5, [Self.child(23, started: -4), Self.child(24, waits: true)],
                      forecast: .init(low: Self.h(1), mid: Self.h(2), high: Self.h(3), waits: true)),
            Self.epic(6, [Self.child(25, started: -4)]),
            // Not started, but forecast: a ghost from Now to the forecast.
            Self.epic(7, [Self.child(26)], created: -3,
                      forecast: .init(low: Self.h(5), mid: Self.h(6), high: Self.h(8))),
        ])
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        try #require(lines.count == 4)
        #expect(lines.map(\.how) == [.forecast, .waits(1), .open, .forecast])
        #expect(lines[0].end == Self.h(5))
        #expect(lines[0].band == DateInterval(start: Self.h(2), end: Self.h(11)))
        // A start is history: no session in a turn, no line works (#4265).
        #expect(lines.allSatisfy { $0.working == 0 })
        #expect(lines[2].end == Self.h(-4))
        #expect(lines[3].from == Self.now && lines[3].end == Self.h(6) && lines[3].work == nil)
        // Forecast off: the past only.
        let past = WorkTimeline.lines(timeline, now: Self.now, forecast: false)
        #expect(past.map(\.how) == [.open, .waits(1), .open])
        #expect(past.allSatisfy { $0.band == nil && $0.ghost == nil && $0.end == $0.work?.end })
    }

    /// #6043: solid from started to stopped.
    @Test func aBlockIsSolidFromStartedToStopped() throws {
        let timeline = DaemonAPI.WorkTimeline(epics: [
            // Open, nobody on it, its last close 3 h before Now: solid to there.
            Self.epic(1, [Self.child(10, created: -12, started: -10, closed: -3), Self.child(11, created: -9)]),
            Self.epic(2, [Self.child(12, created: -30, started: -20), Self.child(13, started: -6)]),
        ])
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        try #require(lines.count == 2)
        let stopped = lines[0], filed = lines[1]
        #expect(stopped.from == Self.h(-10) && stopped.end == Self.h(-3) && stopped.ghost == nil)
        #expect(stopped.stopped(now: Self.now) == Self.h(-3))
        #expect(WorkTimeline.lineLine(stopped, now: Self.now) == "#1 · 1 of 2 · stopped 3 h ago · open")
        #expect(WorkTimeline.spoken([stopped], now: Self.now).first?.words == "Issue 1, 1 of 2 done, open, stopped 3 h ago")
        #expect(filed.work == DateInterval(start: Self.h(-20), end: Self.h(-6)))
        // A session in a turn on it: the work did not stop, solid to Now.
        let working = try #require(WorkTimeline.lines(timeline, now: Self.now, working: ["github:alexec/Agents#11"]).first)
        #expect(working.work?.end == Self.now && working.stopped(now: Self.now) == nil)
        #expect(WorkTimeline.lineLine(working, now: Self.now) == "#1 · 1 of 2 · open")
        // The block, its words and its hit start where the work started.
        let words = WorkTimeline.blockWords(filed, axis: Self.axis, measure: { Double($0.count) * 6 })
        #expect(words.insideX == Self.axis.x(Self.h(-20)) + 6)
        let y = WorkTimeline.rowTop(filed.row) + 4
        #expect(WorkTimeline.hit((x: Self.axis.x(Self.h(-10)), y: y), lines: [filed], axis: Self.axis) == filed)
    }

    @Test func aDroppedEpicEndsInACross() {
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(8, [Self.child(30, started: -5, closed: -4)], reason: "not_planned"),
        ])
        #expect(WorkTimeline.lines(timeline, now: Self.now).map(\.how) == [.dropped])
    }

    @Test func onlyTheLinesThatOverlapTheWindowAreDrawn() {
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(9, [Self.child(31, started: -60, closed: -50)]),
            Self.epic(10, [Self.child(32, started: -30, closed: -20)]),
            Self.epic(11, [Self.child(33, started: -90), Self.child(34, waits: true)],
                      forecast: .init(low: Self.h(-80), mid: Self.h(-80), high: Self.h(-80), waits: true)),
        ])
        let lines = WorkTimeline.overlapping(WorkTimeline.lines(timeline, now: Self.now), Self.axis.interval)
        // A line that waits for the person is drawn while it is open.
        #expect(lines.map(\.number) == ["#10", "#11"])
    }

    @Test func eachBlockTakesTheFirstRowFreeAtItsStartPackedInTime() {
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(12, [Self.child(40, started: -20, closed: -10)]),
            Self.epic(13, [Self.child(41, started: -18, closed: -12)]),
            Self.epic(14, [Self.child(42, started: -11, closed: -9)]),
            // Starts 5 h after #12 ends, more than the gap: row 0 is free again.
            Self.epic(15, [Self.child(43, started: -5, closed: -1)]),
        ])
        let lines = WorkTimeline.layout(WorkTimeline.lines(timeline, now: Self.now))
        #expect(lines.map(\.row) == [0, 1, 2, 0])
        #expect(WorkTimeline.rowCount(lines) == 3)
        #expect(WorkTimeline.rowCount([]) == 0)
        // The gap is 2 % of the widest range: #14 starts 1 h after #12 ends, so it does not take row 0.
        #expect(WorkTimeline.packGap == 7 * 86_400 / 50)
        // A block that waits for the person holds its row while it is open.
        let waits = WorkTimeline.layout(WorkTimeline.lines(DaemonAPI.WorkTimeline(epics: [
            Self.epic(16, [Self.child(44, started: -30, waits: true)]),
            Self.epic(17, [Self.child(45, started: -1, closed: -0.5)]),
        ]), now: Self.now))
        #expect(waits.map(\.row) == [0, 1])
    }

    static func with(_ epic: DaemonAPI.WorkTimelineEpic, priority: String? = nil, onHold: Bool = false) -> DaemonAPI.WorkTimelineEpic {
        var epic = epic
        epic.item.priority = priority
        epic.item.onHold = onHold ? true : nil
        return epic
    }

    @Test func eachPriorityIsABandOfRowsHighAtTheTop() {
        let forecast = DaemonAPI.WorkTimelineForecast(low: Self.h(1), mid: Self.h(2), high: Self.h(4))
        let timeline = DaemonAPI.WorkTimeline(epics: [
            // No priority, and it starts first.
            Self.epic(30, [Self.child(70, started: -20, closed: -10)]),
            // High, and it starts later: it still goes above the one with none (#6089).
            Self.with(Self.epic(31, [Self.child(71, started: -8, closed: -2)]), priority: "high"),
            Self.with(Self.epic(32, [Self.child(72, started: -15, closed: -12)]), priority: "HIGH"),
            Self.with(Self.epic(33, [Self.child(73, started: -6, closed: -3)]), priority: "low"),
            // On hold: not shown (#7058).
            Self.with(Self.epic(34, [Self.child(74, started: -4), Self.child(75)],
                                forecast: forecast), priority: "high", onHold: true),
            Self.with(Self.epic(35, [Self.child(76, started: -30, closed: -25)]), priority: "medium"),
        ])
        let lines = WorkTimeline.layout(WorkTimeline.lines(timeline, now: Self.now))
        let rows = Dictionary(uniqueKeysWithValues: lines.map { ($0.number, $0.row) })
        // In a band, the blocks still pack in time: #32 and #31 share High's row.
        #expect(rows == ["#32": 0, "#31": 0, "#35": 1, "#30": 2, "#33": 3])
        #expect(lines.map(\.chip) == ["High", "High", "Medium", nil, "Low"])
        // Each band in use has its name on its first row; a band with no block takes no room.
        #expect(WorkTimeline.bands(lines).map { "\($0.words)@\($0.row)" } == ["High@0", "Medium@1", "None@2", "Low@3"])
        let some = WorkTimeline.layout(lines.filter { $0.number == "#31" || $0.number == "#33" })
        #expect(some.map(\.row) == [0, 1] && WorkTimeline.bands(some).map(\.words) == ["High", "Low"])
        // A band of two rows: its next band starts below both.
        let two = WorkTimeline.layout(WorkTimeline.lines(DaemonAPI.WorkTimeline(epics: [
            Self.with(Self.epic(36, [Self.child(77, started: -10, closed: -2)]), priority: "high"),
            Self.with(Self.epic(37, [Self.child(78, started: -9, closed: -3)]), priority: "high"),
            Self.epic(38, [Self.child(79, started: -20, closed: -15)]),
        ]), now: Self.now))
        #expect(two.map(\.row) == [0, 1, 2])
        #expect(WorkTimeline.bands(two).map { "\($0.words)@\($0.row)x\($0.rows)" } == ["High@0x2", "None@2x1"])
        // The list: High first, then in time.
        let spoken = WorkTimeline.spoken(lines, now: Self.now).map(\.line.number)
        #expect(spoken == ["#32", "#31", "#35", "#30", "#33"])
        #expect(WorkTimeline.spoken(lines, now: Self.now).first?.words.hasPrefix("Issue 32, priority high, 1 of 1 done") == true)
    }

    @Test func thePriorityChipIsAfterTheTitleInTheBlockOrAfterIt() throws {
        let measure: (String) -> Double = { Double($0.count) * 6 }
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.with(Self.epic(19, [Self.child(60, started: -9, closed: -1)]), priority: "high"),
            Self.with(Self.epic(20, [Self.child(61, started: -24, closed: -23.5)]), priority: "medium"),
            Self.with(Self.epic(23, [Self.child(63, started: -6, closed: -4)]), priority: "low"),
            Self.epic(24, [Self.child(64, started: -9, closed: -1)]),
        ])
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        try #require(lines.count == 4)
        let (high, medium, low, none) = (lines[0], lines[1], lines[2], lines[3])
        // 160 pt: "Issue 19" (48) and "High" (24 + 10) fit in it, 6 pt after the title.
        let inside = WorkTimeline.blockWords(high, axis: Self.axis, measure: measure)
        #expect(inside.chip == "High" && inside.chipInside && inside.chipX == inside.insideX + 48 + 6)
        // 10 pt: the title and the chip after the block.
        let after = WorkTimeline.blockWords(medium, axis: Self.axis, measure: measure)
        #expect(after.outside == "Issue 20" && after.chip == "Medium" && !after.chipInside && after.chipX == after.outsideX + 48 + 6)
        // No room before the next block for the chip: the words keep it, cut.
        let cut = WorkTimeline.blockWords(medium, axis: Self.axis, measure: measure, next: Self.h(-21.6))
        #expect(cut.chip == nil && cut.outside == "Iss…")
        // 40 pt: neither fits in it; both go after the block.
        let tight = WorkTimeline.blockWords(low, axis: Self.axis, measure: measure)
        #expect(tight.inside == nil && tight.outside == "Issue 23" && tight.chip == "Low" && !tight.chipInside)
        // 160 pt with a title of 140: the title fits, the chip does not, so it goes after the block.
        var long = none
        long.title = String(repeating: "x", count: 23)
        long.priority = "low"
        let squeezed = WorkTimeline.blockWords(long, axis: Self.axis, measure: measure)
        #expect(squeezed.inside == long.title && squeezed.outside == nil && squeezed.chip == "Low"
            && !squeezed.chipInside && squeezed.chipX == squeezed.outsideX)
        // None: no chip.
        #expect(WorkTimeline.blockWords(none, axis: Self.axis, measure: measure).chip == nil)
    }

    @Test func theRowsDoNotChangeBetweenRangesForTheSameRead() {
        let forecast = DaemonAPI.WorkTimelineForecast(low: Self.h(4), mid: Self.h(6), high: Self.h(10))
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(1, [Self.child(10, started: -150, closed: -100)]),
            Self.epic(2, [Self.child(11, started: -120, closed: -2)]),
            Self.epic(3, [Self.child(12, started: -90, closed: -60)]),
            Self.epic(4, [Self.child(13, started: -20), Self.child(14)], forecast: forecast),
            Self.epic(5, [Self.child(15, started: -5, closed: -1)]),
        ])
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        // Every range's window, and its scroll on each side, is inside the span the rows are packed over.
        let span = WorkTimeline.packSpan(Self.now)
        for range in WorkTimeline.Range.allCases {
            let window = range.window(now: Self.now)
            #expect(window.start.addingTimeInterval(-range.span) >= span.start && window.end.addingTimeInterval(range.span) <= span.end)
        }
        func rows() -> [String: Int] {
            Dictionary(uniqueKeysWithValues: WorkTimeline.layout(WorkTimeline.overlapping(lines, span)).map { ($0.number, $0.row) })
        }
        // The same in 1 Day, 2 Days and Week: the packing has no range and no points in it.
        for _ in WorkTimeline.Range.allCases { #expect(rows() == ["#1": 0, "#2": 1, "#3": 0, "#4": 0, "#5": 2]) }
    }

    @Test func aBlocksTitleIsInItOrAfterItCutBeforeTheNextBlock() throws {
        let measure: (String) -> Double = { Double($0.count) * 6 }
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(19, [Self.child(60, started: -9, closed: -1)]),
            Self.epic(20, [Self.child(61, started: -24, closed: -23.5)]),
            Self.epic(21, [Self.child(62, started: -20, closed: -19)], reason: "not_planned"),
        ])
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        let wide = lines[0], narrow = lines[1], dropped = lines[2]
        // 8 h is 160 pt: "Issue 19" fits, 6 pt in.
        let inside = WorkTimeline.blockWords(wide, axis: Self.axis, measure: measure)
        #expect(inside.inside == "Issue 19" && inside.insideX == Self.axis.x(wide.from) + 6 && inside.outside == nil)
        // Half an hour is 10 pt: the title goes after the block, cut before the next block in the row.
        let after = WorkTimeline.blockWords(narrow, axis: Self.axis, measure: measure)
        #expect(after.inside == nil && after.outside == "Issue 20" && after.outsideX == Self.axis.x(narrow.end) + 6)
        #expect(WorkTimeline.blockWords(narrow, axis: Self.axis, measure: measure, next: Self.h(-21.6)).outside == "Iss…")
        #expect(WorkTimeline.blockWords(narrow, axis: Self.axis, measure: measure, next: Self.h(-23.4)).outside == nil)
        // Closed as not planned: "✕ closed as not planned" after the block.
        #expect(WorkTimeline.blockWords(dropped, axis: Self.axis, measure: measure).outside == "Issue 21  ✕ closed as not planned")
        // A block that starts before the left edge keeps its title at the edge, after ←, cut to what is left of it.
        let edge = WorkTimeline.blockWords(wide, axis: Self.axis, measure: measure, left: Self.axis.x(Self.h(-4)))
        #expect(edge.arrow && edge.insideX == Self.axis.x(Self.h(-4)) + 6 && edge.inside == "← Issue…")
        // The wait pill goes first: the words after it.
        #expect(WorkTimeline.blockWords(narrow, axis: Self.axis, measure: measure, trailing: 30).outsideX == Self.axis.x(narrow.end) + 42)
        #expect(WorkTimeline.cut("Issue 19", room: 0, measure: measure) == nil)
    }

    @Test func whiteTextHasFourAndAHalfToOneOnEveryFeaturesColour() {
        let colours: [[Double]] = [[0, 122, 255], [255, 149, 0], [52, 199, 89], [48, 176, 199], [0, 199, 190], [142, 142, 147]]
        for rgb in colours.map({ $0.map { $0 / 255 } }) {
            let dark = WorkTimeline.forWhiteText(rgb)
            #expect(WorkTimeline.contrast(dark, [1, 1, 1]) >= 4.5)
            #expect(zip(dark, rgb).allSatisfy { $0 <= $1 })
        }
        // A colour that has it already stays as it is.
        let indigo = [88.0, 86, 214].map { $0 / 255 }
        #expect(WorkTimeline.forWhiteText(indigo) == indigo)
    }

    @Test func aFeatureKeepsItsColour() {
        #expect(WorkTimeline.colour("work", of: 12) == WorkTimeline.colour("Work", of: 12))
        #expect(WorkTimeline.colour(nil, of: 12) == 11)
        #expect((0..<11).contains(WorkTimeline.colour("autonomy", of: 12)))
    }

    @Test func theHitIsTheBlock() {
        let timeline = DaemonAPI.WorkTimeline(epics: [Self.epic(21, [Self.child(80, started: -10, closed: -2)])])
        let lines = WorkTimeline.layout(WorkTimeline.lines(timeline, now: Self.now))
        func hit(_ x: Double, _ y: Double) -> String? { WorkTimeline.hit((x, y), lines: lines, axis: Self.axis)?.number }
        #expect(hit(Self.axis.x(Self.h(-4)), WorkTimeline.rowTop(0) + 4) == "#21")
        #expect(hit(Self.axis.x(Self.h(-4)), WorkTimeline.rowTop(1) + 4) == nil)
        #expect(hit(Self.axis.x(Self.now), WorkTimeline.rowTop(0) + 4) == nil)
        // The title is in the middle of the block (#7058: no dots under it).
        #expect(WorkTimeline.titleY(0) == WorkTimeline.rowTop(0) + WorkTimeline.blockHeight / 2)
    }

    @Test func theWordsSayAbout() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        let backtest = DaemonAPI.WorkTimelineBacktest(error: 2.16, width: 1.3, cuts: 69, holds: 55, epics: 21, at: Self.now)
        let forecast = DaemonAPI.WorkTimelineForecast(low: Self.h(1), mid: Self.h(2), high: Self.h(4))
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(22, [Self.child(90, started: -3, closed: -1), Self.child(91)],
                      forecast: forecast),
        ])
        let line = WorkTimeline.lines(timeline, now: Self.now)[0]
        let words = WorkTimeline.lineLine(line, now: Self.now, calendar: calendar)
        #expect(words.hasPrefix("#22 · 1 of 2 · stopped 1 h ago · closes about "))
        #expect(words.hasSuffix(", 8 in 10"))
        #expect(WorkTimeline.errorWords(backtest) == "Past forecasts were off by ×2.2")
        let spoken = WorkTimeline.spoken([line], now: Self.now, calendar: calendar)
        #expect(spoken.first?.words.hasPrefix("Issue 22, 1 of 2 done, closes about ") == true)
        #expect(WorkTimeline.waitWords(3) == "3 wait for you")
        #expect(WorkTimeline.waitWords(1) == "1 waits for you")
    }

    @Test func theHoverCardIsAMiniRow() throws {
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(30, [Self.child(31, started: -5, closed: -2), Self.child(32, started: -5, closed: -1, reason: "not_planned")]),
            Self.epic(33, [Self.child(34, started: -3), Self.child(35, waits: true)]),
        ])
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        let joined = try #require(lines.first { $0.number == "#30" })
        #expect(WorkTimeline.lineLine(joined, now: Self.now) == "#30 · 2 of 2 · closed 1 h ago")
        let waits = try #require(lines.first { $0.number == "#33" })
        #expect(WorkTimeline.lineLine(waits, now: Self.now) == "#33 · 0 of 2 · stopped 3 h ago · 1 waits for you")
    }

    @Test func theTitleLineAndTheEmptyCardSayTheRange() {
        #expect(WorkTimeline.titleWords(project: "Agents", epics: 6, range: .twoDays) == "Agents · 6 epics in these 2 days")
        #expect(WorkTimeline.titleWords(project: "", epics: 1, range: .day) == "1 epic in these 24 hours")
        #expect(WorkTimeline.noEpicsWords(.week) == "No epic in these 7 days")
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(40, [Self.child(41, closed: -100)]),
            Self.epic(42, [Self.child(43, closed: -96)]),
            Self.epic(44, [Self.child(45, started: -2)]),
        ])
        let joined = WorkTimeline.lastJoined(timeline, before: Self.h(-24))
        #expect(joined == Self.h(-96))
        #expect(WorkTimeline.lastJoinedWords(Self.h(-96), now: Self.now) == "The last one joined 4 d ago.")
        #expect(WorkTimeline.lastJoined(timeline, before: Self.h(-200)) == nil)
        let lines = WorkTimeline.lines(timeline, now: Self.now)
        #expect(WorkTimeline.epicCount(lines, in: WorkTimeline.Range.twoDays.window(now: Self.now)) == 1)
    }

    /// #6044: the phone in portrait draws the list of epics, in the order the web page's `epicRows` gives.
    @Test func theListOfEpicsIsInTheOrderTheyClose() throws {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(50, [Self.child(51, started: -10, closed: -6)]),
            Self.epic(52, [Self.child(53, started: -3, closed: -2)]),
            Self.epic(54, [Self.child(55, started: -4)]),
            Self.epic(56, [Self.child(57, started: -1)],
                      forecast: .init(low: Self.h(5), mid: Self.h(6), high: Self.h(9))),
            Self.epic(58, [Self.child(59, started: -1)],
                      forecast: .init(low: Self.h(2), mid: Self.h(2), high: Self.h(2))),
            Self.epic(60, [Self.child(61, started: -2), Self.child(62, waits: true)],
                      forecast: .init(low: Self.h(1), mid: Self.h(1), high: Self.h(1), waits: true)),
            Self.with(Self.epic(63, [Self.child(64, started: -5, closed: -4)]), priority: "high"),
        ])
        let rows = WorkTimeline.epicRows(WorkTimeline.lines(timeline, now: Self.now), window: Self.axis.interval, now: Self.now,
                                         calendar: utc)
        // High first; then the forecasts, the soonest first; the wait; the open one; the joined, the latest first.
        #expect(rows.map(\.line.number) == ["#63", "#58", "#56", "#60", "#54", "#52", "#50"])
        try #require(rows.count == 7)
        let six = Self.h(6).formatted(Date.FormatStyle(timeZone: utc.timeZone).hour().minute())
        let nine = Self.h(9).formatted(Date.FormatStyle(timeZone: utc.timeZone).hour().minute())
        let five = Self.h(5).formatted(Date.FormatStyle(timeZone: utc.timeZone).hour().minute())
        // Now is 8:00 UTC, so 5:00 PM is the same day: the time alone. No session is in a turn: each open one stopped.
        #expect(rows[2].line2 == "closes about \(six) · \(five)–\(nine) · stopped 1 h ago")
        #expect(rows[3].line2 == "1 waits for you · stopped 2 h ago")
        #expect(rows[4].line2 == "open · stopped 4 h ago")
        #expect(rows[5].line2.hasPrefix("joined "))
        #expect(WorkTimeline.nowWords(Self.now, calendar: utc).hasPrefix("Now · "))
    }

    /// #5925: two reads, the first with no epic and the second with one; S11 never shows between them.
    @Test func theWindowIsLoadingNotEmptyUntilEveryReadEnded() {
        let window = WorkTimeline.Range.twoDays.window(now: Self.now)
        func count(_ t: DaemonAPI.WorkTimeline) -> Int { WorkTimeline.epicCount(WorkTimeline.lines(t, now: Self.now), in: window) }
        let first = DaemonAPI.WorkTimeline(epics: [], releases: [])
        let both = WorkTimeline.merge([first, DaemonAPI.WorkTimeline(
            epics: [Self.epic(44, [Self.child(45, started: -2)])], releases: [])])
        #expect(WorkTimeline.shows(drawn: false, reading: true, epics: 0) == .loading)
        #expect(WorkTimeline.shows(drawn: true, reading: true, epics: count(first)) == .loading)
        #expect(WorkTimeline.shows(drawn: true, reading: true, epics: count(both)) == .epics)
        #expect(WorkTimeline.shows(drawn: true, reading: false, epics: count(both)) == .epics)
        // Only once every read ended does an empty window say so (S11).
        #expect(WorkTimeline.shows(drawn: true, reading: false, epics: count(first)) == .empty)
    }

    @Test func machinesMergeEachEpicAndReleaseOnce() {
        let a = DaemonAPI.WorkTimeline(epics: [Self.epic(23, [Self.child(1, closed: -1)])],
                                       releases: [.init(tag: "v2", at: Self.h(-1), repo: "a/b")], errors: ["x"])
        let b = DaemonAPI.WorkTimeline(epics: [Self.epic(23, [Self.child(1, closed: -1)])],
                                       releases: [.init(tag: "v1", at: Self.h(-3), repo: "a/b"), .init(tag: "v2", at: Self.h(-1), repo: "a/b")],
                                       errors: ["x", "y"])
        let merged = WorkTimeline.merge([a, b])
        #expect(merged.epics.count == 1)
        #expect(merged.releases.map(\.tag) == ["v1", "v2"])
        #expect(merged.errors == ["x", "y"])
    }

    /// #4276: a session on another machine, from the control plane's copy, starts a child that
    /// this machine had no start for, and an earlier one moves a start and its block's solid span back.
    @Test func sessionsOnEveryMachineStartTheChildren() throws {
        let open = Self.child(2)
        let closed = Self.child(3, started: -10, closed: -5)
        let later = Self.child(4, started: -3)
        let timeline = DaemonAPI.WorkTimeline(epics: [Self.epic(1, [closed, open, later])])
        func on(_ n: Int, _ hours: Double) -> DaemonAPI.LineageSessionOn {
            .init(node: Node(kind: "session", ref: "session:\(n)-\(hours)"), host: "BEAS-MAC",
                  on: [Node(kind: "issue", ref: "github:alexec/Agents#\(n)")], at: Self.h(hours))
        }
        let got = WorkTimeline.started(timeline, by: [on(2, -4), on(3, -12), on(4, -1), on(9, -50)])
        let epic = try #require(got.epics.first)
        let kids = epic.children
        // #3 started earlier on Bea's machine; #2 started there; #4 started here first.
        #expect(kids.map(\.startedAt) == [Self.h(-12), Self.h(-4), Self.h(-3)])
        #expect(WorkTimeline.lines(got, now: Self.now).first?.work?.start == Self.h(-12))
        #expect(WorkTimeline.started(timeline, by: []) == timeline)
        #expect(WorkTimeline.sessionNodes(got).map(\.ref) == [1, 3, 2, 4].map { "github:alexec/Agents#\($0)" })
    }

    /// #4265: a line works only while a session is in a turn on its epic or an open child; a session that waits does not count, and this app's own state is the newest.
    @Test func aLineWorksOnlyWhileASessionIsInATurnOnIt() throws {
        let timeline = DaemonAPI.WorkTimeline(epics: [
            Self.epic(1, [Self.child(2, started: -10, closed: -6), Self.child(3, started: -2)]),
            Self.epic(4, [Self.child(5, started: -2)]),
            Self.epic(6, [Self.child(7, started: -2)]),
        ])
        func ref(_ n: Int) -> String { "github:alexec/Agents#\(n)" }
        func on(_ n: Int, _ state: String, id: UUID = UUID()) -> DaemonAPI.LineageSessionOn {
            .init(node: LineageList.node(ofSession: id), host: "BEAS-MAC", on: [Node(kind: "issue", ref: ref(n))],
                  at: Self.h(-1), state: .init(state: state, at: Self.h(-1)))
        }
        func agent(_ label: String, _ state: AgentState) -> Agent {
            var a = Agent(runtimeID: "claude", cwd: URL(fileURLWithPath: "/tmp/work"))
            a.labels = [SessionLabel(value: label, owner: .agent)]
            a.state = state
            return a
        }
        // Here: a lead on #4 between turns, and a lane on #7 in one. There: a lane on #3 in a turn,
        // one on #5 waiting, and one that is #4's lead, whose old copy still says working.
        let lead = agent("#4", .finished)
        let working = WorkTimeline.workingNow(
            timeline, sessions: [on(3, "working"), on(5, "waiting"), on(4, "working", id: lead.id)],
            agents: [lead, agent("#7", .running), agent("#9", .running)])
        #expect(working == [ref(3), ref(7)])
        let lines = WorkTimeline.lines(timeline, now: Self.now, working: working)
        #expect(lines.map(\.working) == [1, 0, 1])
        // The epic's own session counts too: a lead in a turn.
        let leading = WorkTimeline.lines(timeline, now: Self.now, working: [ref(4)])
        #expect(leading.map(\.working) == [0, 1, 0])
        #expect(WorkTimeline.workingNow(timeline, sessions: [], agents: []).isEmpty)
    }

    @Test func timelineIsAWorkListWithNoLineagePage() {
        #expect(WorkList.Kind.allCases.last == .timeline)
        #expect(WorkList.kind(of: Node(kind: "timeline", ref: "x")) == nil)
    }
}
