import Foundation
import Testing
@testable import AgentsKitCore

/// Work ▸ Timeline's forecast as a Gantt (#7526): the rows, words and days from the schedule (#7525).
@Suite("Work Gantt")
struct WorkGanttTests {
    typealias Node = DaemonAPI.LineageNode
    typealias Row = DaemonAPI.WorkTimelineScheduleRow
    static let now = Date(timeIntervalSince1970: 1_800_000_000)
    static var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    static func d(_ days: Double) -> Date { now.addingTimeInterval(days * 86400) }
    static func node(_ n: Int) -> Node { Node(kind: "issue", ref: "github:alexec/Agents#\(n)") }
    static func forecast(_ mid: Double) -> DaemonAPI.WorkTimelineForecast {
        DaemonAPI.WorkTimelineForecast(low: d(mid - 1), mid: d(mid), high: d(mid + 2))
    }

    static func epic(_ n: Int, title: String, children: Int = 0, closed: Int = 0) -> DaemonAPI.WorkTimelineEpic {
        let kids = (0..<children).map { i in
            DaemonAPI.WorkTimelineChild(item: DaemonAPI.ForgeItem(node: node(1000 + n * 10 + i), title: "c", state: i < closed ? "closed" : "open",
                                                                  updatedAt: now),
                                        createdAt: d(-5), closedAt: i < closed ? d(-1) : nil)
        }
        return DaemonAPI.WorkTimelineEpic(item: DaemonAPI.ForgeItem(node: node(n), title: title, state: "open", updatedAt: now, feature: "work"),
                                          createdAt: d(-10), children: kids)
    }

    static func timeline(_ rows: [Row], epics: [DaemonAPI.WorkTimelineEpic]) -> DaemonAPI.WorkTimeline {
        DaemonAPI.WorkTimeline(epics: epics, releases: [],
                               schedule: DaemonAPI.WorkTimelineSchedule(places: 2, readAt: now, rows: rows))
    }

    static let sample = timeline([
        Row(epic: node(1), startedAt: d(-2), startSource: "session", finish: forecast(2)),
        Row(epic: node(2), plannedStart: d(2), finish: forecast(4), blockers: [node(1)]),
        Row(epic: node(3), reason: "cycle"),
        Row(epic: node(4), reason: "somethingNew"),
        Row(epic: node(5), startedAt: d(-1), reason: "onHold"),
    ], epics: [
        epic(1, title: "Projects workspace", children: 6, closed: 3),
        epic(2, title: "Timeline forecast", children: 4),
        epic(3, title: "Channels"),
        epic(5, title: "Runtime assessments"),
    ])

    @Test func aStartedEpicIsSolidToNowThenDashedToItsFinish() {
        let rows = WorkGantt.rows(Self.sample, now: Self.now, held: false, calendar: Self.utc)
        let started = rows[0]
        #expect(started.kind == .started)
        #expect(started.solid == DateInterval(start: Self.d(-2), end: Self.now))
        #expect(started.ahead == DateInterval(start: Self.now, end: Self.d(2)))
        #expect(started.line == "Started Jan 13 · 3/6 issues · forecast Jan 17")
    }

    @Test func aPlannedEpicSpansItsPlanAndPointsFromItsBlocker() {
        let planned = WorkGantt.rows(Self.sample, now: Self.now, held: false, calendar: Self.utc)[1]
        #expect(planned.kind == .planned)
        #expect(planned.solid == nil)
        #expect(planned.ahead == DateInterval(start: Self.d(2), end: Self.d(4)))
        #expect(planned.blockers == ["github:alexec/Agents#1"])
        #expect(planned.line == "Planned Jan 17 · 4 issues · forecast Jan 19")
    }

    @Test func anUndatedEpicSaysWhyAndAnUnknownReasonSaysNoDate() throws {
        let rows = WorkGantt.rows(Self.sample, now: Self.now, held: false, calendar: Self.utc)
        try #require(rows.count == 4)
        #expect(rows[2].kind == .undated)
        #expect(rows[2].reason == "Dependency cycle")
        #expect(rows[2].line == "No forecast")
        // No epic in the read: the ref's number is the title.
        #expect(rows[3].title == "Epic #4")
        #expect(rows[3].reason == "No date")
    }

    @Test func heldEpicsShowOnlyWithTheToggle() throws {
        #expect(WorkGantt.rows(Self.sample, now: Self.now, held: false).count == 4)
        let rows = WorkGantt.rows(Self.sample, now: Self.now, held: true, calendar: Self.utc)
        try #require(rows.count == 5)
        #expect(rows[4].kind == .held)
        #expect(rows[4].solid == DateInterval(start: Self.d(-1), end: Self.now))
        #expect(rows[4].line == "On hold")
    }

    @Test func noScheduleIsNoRows() {
        #expect(WorkGantt.rows(DaemonAPI.WorkTimeline(), now: Self.now, held: true).isEmpty)
    }

    @Test func everyReasonHasWords() {
        for reason in ["onHold", "noIssues", "blockerOnHold", "blockerNotEpic", "blockerNotRead", "cycle", "blockerUndated",
                       "noForecast", "noPlace", "incomplete"] {
            #expect(WorkGantt.reasonWords(reason) != "No date", "\(reason)")
        }
        #expect(WorkGantt.reasonWords("blockerNotEpic") == "Blocker is not an epic")
    }

    @Test func theCapacityCountsStartedEpicsNotOnHold() {
        let words = WorkGantt.capacityWords(Self.sample.schedule!)
        #expect(words.hasPrefix("2 epic places · 1 occupied · read "))
    }

    @Test func theDaysRunFromBeforeTheFirstStartToAfterTheLastFinish() {
        let rows = WorkGantt.rows(Self.sample, now: Self.now, held: false, calendar: Self.utc)
        let days = WorkGantt.days(rows, now: Self.now, calendar: Self.utc)
        let today = Self.utc.startOfDay(for: Self.now)
        #expect(days.start == today.addingTimeInterval(-3 * 86400))
        #expect(days.end == today.addingTimeInterval(6 * 86400))
        #expect(WorkGantt.ticks(days, calendar: Self.utc).count == 9)
    }

    @Test func theDaysAreAtLeastAWeekAndBounded() throws {
        let today = Self.utc.startOfDay(for: Self.now)
        let none = WorkGantt.days([], now: Self.now, calendar: Self.utc)
        #expect(none.start == today.addingTimeInterval(-86400))
        #expect(none.duration == 7 * 86400)
        var far = try #require(WorkGantt.rows(Self.sample, now: Self.now, held: false, calendar: Self.utc).first)
        far.ahead = DateInterval(start: Self.now, end: Self.d(400))
        far.solid = DateInterval(start: Self.d(-400), end: Self.now)
        let bounded = WorkGantt.days([far], now: Self.now, calendar: Self.utc)
        #expect(bounded.start == today.addingTimeInterval(-15 * 86400))
        #expect(bounded.end == today.addingTimeInterval(62 * 86400))
    }

    @Test func theTrackPlacesTimesAndRows() {
        let interval = DateInterval(start: Self.now, duration: 10 * 86400)
        #expect(WorkGantt.x(Self.d(5), in: interval, width: 700) == 350)
        #expect(WorkGantt.dayWidth(days: 10, width: 400) == 64)
        #expect(WorkGantt.dayWidth(days: 10, width: 1000) == 100)
        #expect(WorkGantt.row(at: 150, rowHeight: 56, count: 4) == 2)
        #expect(WorkGantt.row(at: 300, rowHeight: 56, count: 4) == nil)
        #expect(WorkGantt.row(at: -1, rowHeight: 56, count: 4) == nil)
    }

    @Test func machinesSchedulesMergeOnceEachInTheScheduleOrder() {
        let a = DaemonAPI.WorkTimelineSchedule(places: 2, readAt: Self.now, rows: [
            Row(epic: Self.node(3), reason: "cycle"),
            Row(epic: Self.node(1), plannedStart: Self.d(1), finish: Self.forecast(2)),
        ])
        let b = DaemonAPI.WorkTimelineSchedule(places: 3, readAt: Self.d(-1), incomplete: true, rows: [
            Row(epic: Self.node(1), startedAt: Self.d(-1), finish: Self.forecast(2)),
            Row(epic: Self.node(2), startedAt: Self.d(-2), finish: Self.forecast(3)),
        ])
        let merged = DaemonAPI.WorkTimelineSchedule.merged([a, b])
        #expect(merged?.places == 3)
        #expect(merged?.readAt == Self.d(-1))
        #expect(merged?.incomplete == true)
        #expect(merged?.rows.map(\.epic.ref.last) == ["2", "1", "3"])
        #expect(DaemonAPI.WorkTimelineSchedule.merged([]) == nil)
        #expect(WorkTimeline.merge([Self.sample]).schedule == Self.sample.schedule)
    }

    @Test func aFirstAnswerKeepsTheScheduleOnScreen() {
        var first = Self.sample
        first.schedule = nil
        #expect(WorkTimeline.keepForecasts(first, shown: Self.sample).schedule == Self.sample.schedule)
    }
}
