import Foundation

/// What meeting mode's screens say (#7585, epic #7327), apart from how they draw it: the
/// speakers' names, the clock, the session row's second line, and the transcript's lines with
/// the gaps between them. The Remote and the web page say the same later (#7332, #7333).
public enum MeetingLines {
    // MARK: Speakers

    /// A voice's place in the meeting, from 1, in the order the daemon heard them: "Speaker 2"
    /// here is "Speaker 2" in the agent's prompts (`meetinggaps.go`).
    public static func speakerNumber(_ meeting: DaemonAPI.Meeting, source: String, index: Int) -> Int? {
        meeting.speakers.firstIndex { $0.source == source && $0.index == index }.map { $0 + 1 }
    }

    /// A voice as the transcript names it: its name, or "Speaker 4" while it has none.
    public struct Speaker: Hashable, Sendable {
        public let source: String
        public let index: Int?
        public let label: String
        public let isNamed: Bool
        /// The name it has now, for the popover's field.
        public let name: String?
    }

    public static let unknownSpeaker = "Not known yet"

    public static func speaker(_ meeting: DaemonAPI.Meeting, source: String, index: Int?) -> Speaker {
        guard let index else {
            return Speaker(source: source, index: nil, label: unknownSpeaker, isNamed: false, name: nil)
        }
        let voice = meeting.speakers.first { $0.source == source && $0.index == index }
        if let name = voice?.name?.trimmingCharacters(in: .whitespacesAndNewlines), !name.isEmpty {
            return Speaker(source: source, index: index, label: name, isNamed: true, name: name)
        }
        let number = speakerNumber(meeting, source: source, index: index)
        return Speaker(source: source, index: index, label: number.map { "Speaker \($0)" } ?? unknownSpeaker,
                       isNamed: false, name: nil)
    }

    /// The popover's title: "Name Speaker 4", or "Rename Priya".
    public static func namingTitle(_ speaker: Speaker) -> String {
        speaker.isNamed ? "Rename \(speaker.label)" : "Name \(speaker.label)"
    }

    public static let namingHint = "The name is for the whole meeting. The agent sees it at the next pause."

    // MARK: The clock

    /// Seconds from the start, as the transcript and the capsule show them: "4:07", "1:02:30".
    public static func clock(_ seconds: TimeInterval) -> String {
        let whole = max(0, Int(seconds.rounded(.down)))
        let hours = whole / 3600, minutes = (whole % 3600) / 60, rest = whole % 60
        return hours > 0 ? String(format: "%d:%02d:%02d", hours, minutes, rest) : String(format: "%d:%02d", minutes, rest)
    }

    /// How long the meeting has run at `now`: to its end once it ended, and to its last change
    /// while paused (the pause's own change).
    public static func elapsed(_ meeting: DaemonAPI.Meeting, now: Date) -> TimeInterval {
        let end: Date = switch meeting.state {
        case .listening: now
        case .paused: meeting.updatedAt
        case .ended: meeting.endedAt ?? meeting.updatedAt
        }
        return max(0, end.timeIntervalSince(meeting.startedAt))
    }

    // MARK: The session row

    /// The row's second line, in the place of the agent's report: "Listening · 33:54",
    /// "Paused · 34:12", "Ended · 58 min". The action items to file are #7586's to add.
    public static func rowLine(_ meeting: DaemonAPI.Meeting, now: Date) -> String {
        let elapsed = elapsed(meeting, now: now)
        switch meeting.state {
        case .listening: return "Listening · \(clock(elapsed))"
        case .paused: return "Paused · \(clock(elapsed))"
        case .ended: return "Ended · \(minutes(elapsed))"
        }
    }

    /// "58 min", "1 h 12 min", "less than a minute".
    public static func minutes(_ seconds: TimeInterval) -> String {
        let whole = Int((seconds / 60).rounded(.down))
        if whole < 1 { return "less than a minute" }
        return whole < 60 ? "\(whole) min" : "\(whole / 60) h \(whole % 60) min"
    }

    // MARK: The capsule

    public enum CapsuleState: Hashable, Sendable {
        /// Made, and this Mac does not listen to it (yet).
        case notListening
        case starting
        case listening
        case paused
        case ended
    }

    public static func capsuleTitle(_ state: CapsuleState) -> String {
        switch state {
        case .notListening: "Not listening"
        case .starting: "Starting…"
        case .listening: "Listening"
        case .paused: "Paused"
        case .ended: "Ended"
        }
    }

    /// The sources, as the capsule names them: "Mic · Zoom".
    public static func sourcesLine(_ sources: [String], appName: (String) -> String) -> String {
        sources.map { source in
            DaemonAPI.MeetingSource.bundleID(of: source).map(appName) ?? "Mic"
        }.joined(separator: " · ")
    }

    public static let pausedLine = "Nothing is heard"

    /// The ended capsule's line: "712 lines".
    public static func linesCount(_ count: Int64) -> String { count == 1 ? "1 line" : "\(count) lines" }

    // MARK: The transcript

    /// What the agent was sent at one gap: the seqs `from` to `through`, at `at` seconds from
    /// the start when this window saw it go. A gap from before the window opened has no time.
    public struct Gap: Hashable, Sendable {
        public let from: Int64
        public let through: Int64
        public let at: TimeInterval?

        public init(from: Int64, through: Int64, at: TimeInterval?) {
            self.from = from
            self.through = through
            self.at = at
        }

        /// "Sent to the agent at 31:47 · lines 260–271".
        public var line: String {
            let lines = from == through ? "line \(through)" : "lines \(from)–\(through)"
            guard let at else { return "Sent to the agent · \(lines)" }
            return "Sent to the agent at \(MeetingLines.clock(at)) · \(lines)"
        }
    }

    /// The gaps after `sentThroughSeq` moved from `old` to `new` at `at`.
    public static func gap(movedFrom old: Int64, to new: Int64, at: TimeInterval?) -> Gap? {
        new > old ? Gap(from: old + 1, through: new, at: at) : nil
    }

    /// One line of the transcript: a segment, with its speaker named only where the speaker
    /// changes, and the gap that follows it, if any.
    public struct Row: Hashable, Sendable, Identifiable {
        public let seq: Int64
        public let time: String
        /// Nil when the line before has the same speaker.
        public let speaker: Speaker?
        /// The voice, named or not, for the popover; the same on every line of a voice.
        public let voice: Speaker
        public let text: String
        public let gapAfter: Gap?

        public var id: Int64 { seq }
    }

    public static func rows(_ segments: [DaemonAPI.MeetingSegment], meeting: DaemonAPI.Meeting, gaps: [Gap]) -> [Row] {
        let gapsByEnd = Dictionary(gaps.map { ($0.through, $0) }, uniquingKeysWith: { first, _ in first })
        var last: (String, Int?)?
        return segments.map { segment in
            let voice = speaker(meeting, source: segment.source, index: segment.speakerIndex)
            let key = (segment.source, segment.speakerIndex)
            let same = last.map { $0.0 == key.0 && $0.1 == key.1 && key.1 != nil } ?? false
            last = key
            return Row(seq: segment.seq, time: clock(segment.audioStart), speaker: same ? nil : voice, voice: voice,
                       text: segment.text, gapAfter: gapsByEnd[segment.seq])
        }
    }

    /// The line over the first one shown, when earlier ones are not: "↑ 259 earlier lines".
    public static func earlierLine(_ count: Int64) -> String? {
        count > 0 ? (count == 1 ? "1 earlier line" : "\(count) earlier lines") : nil
    }

    /// The transcript with nothing in it yet.
    public static let emptyTitle = "Nothing heard yet"
    public static let emptyText = "Start Listening plays a rising tone, so the room knows the Mac listens. Then the agent "
        + "hears what is said at each pause, and writes the notes and the action items."
    public static let emptyFoot = "Audio is never kept. Stop plays a falling tone."

    /// The line at the foot of a paused or ended meeting.
    public static func pausedAt(_ seconds: TimeInterval) -> String { "Paused at \(clock(seconds))" }
    public static func endedAt(_ seconds: TimeInterval) -> String {
        "Ended at \(clock(seconds)) · the agent has the last lines"
    }

    // MARK: New Meeting

    public static let sheetTitle = "New Meeting"
    public static let startButton = "Start Listening"
    public static let sheetNote = "A rising tone plays when the Mac starts to listen. Audio is never kept."
    public static let microphone = "The microphone"

    /// A call app's line in Listen to: "Zoom's sound (the others on the call)".
    public static func callAppLine(_ name: String) -> String { "\(name)'s sound (the others on the call)" }

    /// The title's hint from the calendar: "From your calendar: 11:00–12:00, Priya, Sam".
    public static func calendarHint(start: Date, end: Date, attendees: [String],
                                    calendar: Calendar = .current, locale: Locale = .current) -> String {
        var style = Date.FormatStyle(date: .omitted, time: .shortened, locale: locale, calendar: calendar)
        style.timeZone = calendar.timeZone
        let times = "\(start.formatted(style))–\(end.formatted(style))"
        let names = attendees.prefix(6).joined(separator: ", ")
        return names.isEmpty ? "From your calendar: \(times)" : "From your calendar: \(times), \(names)"
    }
}
