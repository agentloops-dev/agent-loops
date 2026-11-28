import Foundation
import Testing
@testable import AgentsKitCore

/// What meeting mode's screens say (#7585). Made-up names only.
@Suite("Meeting lines")
struct MeetingLinesTests {
    static let zoom = DaemonAPI.MeetingSource.call("us.zoom.xos")
    static let start = Date(timeIntervalSinceReferenceDate: 812_345_678)

    static func meeting(state: DaemonAPI.MeetingState = .listening, speakers: [DaemonAPI.MeetingSpeaker] = [],
                        updatedAt: TimeInterval = 0, endedAt: TimeInterval? = nil) -> DaemonAPI.Meeting {
        DaemonAPI.Meeting(id: UUID(), agentID: UUID(), folder: URL(filePath: "/tmp/f/"), title: "Weekly", state: state,
                          startedAt: start, endedAt: endedAt.map { start.addingTimeInterval($0) },
                          sources: [DaemonAPI.MeetingSource.mic, zoom], speakers: speakers,
                          updatedAt: start.addingTimeInterval(updatedAt))
    }

    static func segment(_ seq: Int64, _ source: String, _ index: Int?, at: Double, _ text: String) -> DaemonAPI.MeetingSegment {
        DaemonAPI.MeetingSegment(seq: seq, source: source, audioStart: at, audioEnd: at + 2, text: text,
                                 speakerIndex: index, speakerSettled: true)
    }

    @Test func speakersAreNumberedInTheOrderHeardAcrossStreams() {
        let m = Self.meeting(speakers: [.init(source: "mic", index: 0, name: "Priya"),
                                        .init(source: Self.zoom, index: 0),
                                        .init(source: Self.zoom, index: 1, name: "  ")])
        #expect(MeetingLines.speaker(m, source: "mic", index: 0).label == "Priya")
        #expect(MeetingLines.speaker(m, source: "mic", index: 0).isNamed)
        #expect(MeetingLines.speaker(m, source: Self.zoom, index: 0).label == "Speaker 2")
        // A blank name is no name.
        #expect(MeetingLines.speaker(m, source: Self.zoom, index: 1).label == "Speaker 3")
        #expect(!MeetingLines.speaker(m, source: Self.zoom, index: 1).isNamed)
        #expect(MeetingLines.speaker(m, source: Self.zoom, index: 7).label == MeetingLines.unknownSpeaker)
        #expect(MeetingLines.speaker(m, source: "mic", index: nil).label == MeetingLines.unknownSpeaker)
        #expect(MeetingLines.namingTitle(MeetingLines.speaker(m, source: Self.zoom, index: 0)) == "Name Speaker 2")
        #expect(MeetingLines.namingTitle(MeetingLines.speaker(m, source: "mic", index: 0)) == "Rename Priya")
    }

    @Test func clockShowsHoursOnlyPastTheHour() {
        #expect(MeetingLines.clock(0) == "0:00")
        #expect(MeetingLines.clock(247.9) == "4:07")
        #expect(MeetingLines.clock(2034) == "33:54")
        #expect(MeetingLines.clock(3750) == "1:02:30")
        #expect(MeetingLines.clock(-3) == "0:00")
    }

    @Test func rowLineFollowsTheState() {
        let now = Self.start.addingTimeInterval(2034)
        #expect(MeetingLines.rowLine(Self.meeting(), now: now) == "Listening · 33:54")
        #expect(MeetingLines.rowLine(Self.meeting(state: .paused, updatedAt: 2052), now: now) == "Paused · 34:12")
        #expect(MeetingLines.rowLine(Self.meeting(state: .ended, endedAt: 3520), now: now) == "Ended · 58 min")
        #expect(MeetingLines.rowLine(Self.meeting(state: .ended, endedAt: 4330), now: now) == "Ended · 1 h 12 min")
        #expect(MeetingLines.rowLine(Self.meeting(state: .ended, endedAt: 20), now: now) == "Ended · less than a minute")
    }

    @Test func rowsNameASpeakerOnlyWhereItChanges() throws {
        let m = Self.meeting(speakers: [.init(source: "mic", index: 0, name: "Priya"), .init(source: Self.zoom, index: 0)])
        let segments = [
            Self.segment(1, "mic", 0, at: 2, "Okay, it's listening."),
            Self.segment(2, "mic", 0, at: 9, "Sam is on the call."),
            Self.segment(3, Self.zoom, 0, at: 17, "Hi."),
            Self.segment(4, Self.zoom, nil, at: 20, "Not settled."),
            Self.segment(5, Self.zoom, nil, at: 22, "Nor this."),
        ]
        let gap = MeetingLines.Gap(from: 1, through: 2, at: 11)
        let rows = MeetingLines.rows(segments, meeting: m, gaps: [gap])
        #expect(rows.map { $0.speaker?.label } == ["Priya", nil, "Speaker 2", MeetingLines.unknownSpeaker, MeetingLines.unknownSpeaker])
        #expect(rows.map(\.time) == ["0:02", "0:09", "0:17", "0:20", "0:22"])
        try #require(rows.count == 5)
        #expect(rows[1].gapAfter == gap)
        #expect(rows[1].voice.label == "Priya")
        #expect(rows.compactMap(\.gapAfter).count == 1)
    }

    @Test func gapLines() {
        #expect(MeetingLines.Gap(from: 260, through: 271, at: 1907).line == "Sent to the agent at 31:47 · lines 260–271")
        #expect(MeetingLines.Gap(from: 1, through: 40, at: nil).line == "Sent to the agent · lines 1–40")
        #expect(MeetingLines.Gap(from: 9, through: 9, at: 60).line == "Sent to the agent at 1:00 · line 9")
        #expect(MeetingLines.gap(movedFrom: 4, to: 4, at: 1) == nil)
        #expect(MeetingLines.gap(movedFrom: 4, to: 9, at: 1) == MeetingLines.Gap(from: 5, through: 9, at: 1))
    }

    @Test func sourcesAndCounts() {
        let line = MeetingLines.sourcesLine(["mic", Self.zoom]) { $0 == "us.zoom.xos" ? "Zoom" : $0 }
        #expect(line == "Mic · Zoom")
        #expect(MeetingLines.linesCount(1) == "1 line")
        #expect(MeetingLines.linesCount(712) == "712 lines")
        #expect(MeetingLines.earlierLine(0) == nil)
        #expect(MeetingLines.earlierLine(259) == "259 earlier lines")
    }

    @Test func calendarHint() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try #require(TimeZone(identifier: "UTC"))
        let start = try #require(calendar.date(from: DateComponents(year: 2026, month: 11, day: 27, hour: 11)))
        let hint = MeetingLines.calendarHint(start: start, end: start.addingTimeInterval(3600), attendees: ["Priya", "Sam"],
                                             calendar: calendar, locale: Locale(identifier: "en_GB"))
        #expect(hint == "From your calendar: 11:00–12:00, Priya, Sam")
    }
}
