import Foundation

/// One finished speaker turn of one stream, on the meeting's audio clock: seconds from the
/// meeting's start, as the segments are (#7584). The diarizer tells voices apart; the slot
/// has no name.
public struct SpeakerTurn: Sendable, Equatable, Codable {
    public let speakerIndex: Int
    public let start: TimeInterval
    public let end: TimeInterval

    public init(speakerIndex: Int, start: TimeInterval, end: TimeInterval) {
        self.speakerIndex = speakerIndex
        self.start = start
        self.end = end
    }
}

/// The turns of one stream's diarizer so far, and the rule for which voice a stretch of words
/// is (as Actionable's `SpeakerTimeline.swift`, the reference): the voice that covers most of
/// it, by at least `minimumOverlap`. The diarizer runs behind the transcriber, so a segment's
/// label can change until `hasSettled(through:)`.
public struct SpeakerTimeline: Sendable {
    /// Slots above it are dropped: a stray extra "speaker" is nearly always one voice split,
    /// and a wrong label is worse than none.
    public var maximumSpeakers = 8
    /// A turn covers at least this much of a segment to claim it, so a cough at the edge does
    /// not win the line.
    public var minimumOverlap: TimeInterval = 0.2

    /// By `start`; turns come in audio order, so the insert is nearly always an append.
    public private(set) var turns: [SpeakerTurn] = []
    /// The audio the diarizer has finished with: through a turn's end, or through silence.
    public private(set) var heardThrough: TimeInterval = 0
    private var longestTurn: TimeInterval = 0

    public init() {}

    public mutating func add(_ turn: SpeakerTurn) {
        guard turn.speakerIndex >= 0, turn.speakerIndex < maximumSpeakers, turn.end > turn.start else { return }
        let index = turns.lastIndex { $0.start <= turn.start }.map { $0 + 1 } ?? 0
        turns.insert(turn, at: index)
        heardThrough = max(heardThrough, turn.end)
        longestTurn = max(longestTurn, turn.end - turn.start)
    }

    /// The diarizer finished the audio through `time`, with or without a turn in it.
    public mutating func heard(through time: TimeInterval) {
        heardThrough = max(heardThrough, time)
    }

    /// The voice of most of `start` to `end`; nil when no turn covers enough of it.
    public func speaker(from start: TimeInterval, to end: TimeInterval) -> Int? {
        var overlapBySpeaker: [Int: TimeInterval] = [:]
        for turn in turns.reversed() {
            if turn.start < start - longestTurn { break }
            let overlap = min(end, turn.end) - max(start, turn.start)
            if overlap > 0 { overlapBySpeaker[turn.speakerIndex, default: 0] += overlap }
        }
        // A tie goes to the lower slot, so the answer is the same each time.
        let best = overlapBySpeaker.max { a, b in a.value < b.value || (a.value == b.value && a.key > b.key) }
        guard let best, best.value >= minimumOverlap else { return nil }
        return best.key
    }

    /// How much of `start` to `end` any turn covers, overlaps counted once.
    public func speech(from start: TimeInterval, to end: TimeInterval) -> TimeInterval {
        var covered: TimeInterval = 0
        var reached = start
        for turn in turns where turn.end > start && turn.start < end {
            let from = max(reached, turn.start)
            let to = min(end, turn.end)
            if to > from {
                covered += to - from
                reached = to
            }
        }
        return covered
    }

    /// True once the diarizer is past `end`, so `speaker(from:to:)` will not change there.
    public func hasSettled(through end: TimeInterval) -> Bool { heardThrough >= end }
}

/// One stream's audio clock (#7584): the seconds of audio a stream has fed its transcriber
/// and its diarizer, to seconds from the meeting's start. A pause stops the feed and the
/// meeting's clock goes on, so each run (a start or a resume) begins at its own time.
public struct MeetingAudioClock: Sendable, Equatable {
    private struct Run: Sendable, Equatable {
        var fed: TimeInterval
        var meeting: TimeInterval
    }

    private var runs: [Run] = []

    public init() {}

    /// A run starts: the audio fed from `fed` on was heard from `meeting` on.
    public mutating func startRun(fed: TimeInterval, meeting: TimeInterval) {
        if let last = runs.last, last.fed == fed {
            runs[runs.count - 1].meeting = meeting
        } else {
            runs.append(Run(fed: fed, meeting: meeting))
        }
    }

    /// Seconds from the meeting's start of a time on the fed audio's clock.
    public func meetingTime(_ fed: TimeInterval) -> TimeInterval {
        guard let run = runs.last(where: { $0.fed <= fed }) ?? runs.first else { return fed }
        return run.meeting + (fed - run.fed)
    }
}
