import Foundation

/// The words a stream's transcriber finished, on the meeting's audio clock.
public struct HeardWords: Sendable, Equatable, Codable {
    public var source: String
    public var start: TimeInterval
    public var end: TimeInterval
    public var text: String

    public init(source: String, start: TimeInterval, end: TimeInterval, text: String) {
        self.source = source
        self.start = start
        self.end = end
        self.text = text
    }
}

/// What a stream's diarizer said: a finished turn, or that it finished the audio through a time.
public enum DiarizerNews: Sendable, Equatable, Codable {
    case turn(SpeakerTurn)
    case through(TimeInterval)
}

/// The listening Mac's segments (#7584), with no audio in it, so a test can give it what a
/// recorded made-up call gave the transcribers and the diarizers.
///
/// - Each stream has its own diarizer (#7580), so a speaker is (source, slot).
/// - A final transcript piece becomes the next seq at once, with the speaker the timeline
///   gives so far. When the stream's diarizer is past its end, the seq is sent again with
///   the settled speaker (`meetings/append` keeps the words and changes only the speaker).
/// - The fall-back gate: when the microphone has no voice processing, the call's sound from
///   the speakers reaches it. A mic piece then waits until the call's diarizer is past it,
///   and is dropped when the call stream has speech for at least half of it: those words
///   are the call's, heard twice.
/// - A stream with no diarizer (its model would not load) has no speakers; its pieces are
///   settled at once.
public struct MeetingClerk: Sendable {
    public struct Options: Sendable {
        /// The streams that have a diarizer.
        public var diarized: Set<String>
        /// The microphone has voice processing, so it does not hear the call.
        public var micCancelsEcho: Bool
        /// The part of a mic piece the call's speech covers for the gate to drop it.
        public var gateShare: Double

        public init(diarized: Set<String>, micCancelsEcho: Bool, gateShare: Double = 0.5) {
            self.diarized = diarized
            self.micCancelsEcho = micCancelsEcho
            self.gateShare = gateShare
        }
    }

    public var options: Options
    public private(set) var timelines: [String: SpeakerTimeline] = [:]
    /// The last seq given out.
    public private(set) var lastSeq: Int64
    /// Sent, with a speaker that can still change.
    public private(set) var unsettled: [DaemonAPI.MeetingSegment] = []
    /// Mic pieces held by the gate, with no seq yet.
    public private(set) var gated: [HeardWords] = []
    /// Pieces the gate dropped, for the log.
    public private(set) var dropped = 0

    public init(options: Options, lastSeq: Int64 = 0) {
        self.options = options
        self.lastSeq = lastSeq
    }

    /// The call streams: every diarized source but the mic.
    private var callSources: [String] {
        options.diarized.filter { $0 != DaemonAPI.MeetingSource.mic }.sorted()
    }

    private var gates: Bool {
        !options.micCancelsEcho && options.diarized.contains(DaemonAPI.MeetingSource.mic) && !callSources.isEmpty
    }

    /// A transcriber's final words: what to send now.
    public mutating func heard(_ words: HeardWords) -> [DaemonAPI.MeetingSegment] {
        let pieces = Self.split(words)
        guard !pieces.isEmpty else { return [] }
        if gates, words.source == DaemonAPI.MeetingSource.mic {
            gated += pieces
            return releaseGated()
        }
        var out: [DaemonAPI.MeetingSegment] = []
        for piece in pieces { out.append(numbered(piece, settledAnyway: false)) }
        return out
    }

    /// A diarizer's news: the speakers it settled, and the mic pieces the gate lets go.
    public mutating func diarizer(_ news: DiarizerNews, of source: String) -> [DaemonAPI.MeetingSegment] {
        switch news {
        case .turn(let turn): timelines[source, default: SpeakerTimeline()].add(turn)
        case .through(let time): timelines[source, default: SpeakerTimeline()].heard(through: time)
        }
        var out = settle(source)
        if gates { out += releaseGated() }
        return out
    }

    /// The meeting stops or pauses: every held piece goes, and every speaker is settled as
    /// it is now.
    public mutating func finish() -> [DaemonAPI.MeetingSegment] {
        var out: [DaemonAPI.MeetingSegment] = []
        let held = gated
        gated = []
        for words in held where !callHasSpeech(over: words) {
            out.append(numbered(words, settledAnyway: true))
        }
        dropped += held.count - out.count
        for var segment in unsettled {
            segment.speakerIndex = speaker(of: segment.source, segment.audioStart, segment.audioEnd)
            segment.speakerSettled = true
            out.append(segment)
        }
        unsettled = []
        return out
    }

    // MARK: -

    private func speaker(of source: String, _ start: TimeInterval, _ end: TimeInterval) -> Int? {
        timelines[source]?.speaker(from: start, to: end)
    }

    private func settled(_ source: String, through end: TimeInterval) -> Bool {
        guard options.diarized.contains(source) else { return true }
        return timelines[source]?.hasSettled(through: end) ?? false
    }

    private mutating func numbered(_ words: HeardWords, settledAnyway: Bool) -> DaemonAPI.MeetingSegment {
        lastSeq += 1
        let settled = settledAnyway || settled(words.source, through: words.end)
        let segment = DaemonAPI.MeetingSegment(
            seq: lastSeq, source: words.source, audioStart: words.start, audioEnd: words.end, text: words.text,
            speakerIndex: speaker(of: words.source, words.start, words.end), speakerSettled: settled)
        if !settled { unsettled.append(segment) }
        return segment
    }

    /// The unsettled segments of `source` the diarizer is now past, sent again settled.
    private mutating func settle(_ source: String) -> [DaemonAPI.MeetingSegment] {
        var out: [DaemonAPI.MeetingSegment] = []
        var waiting: [DaemonAPI.MeetingSegment] = []
        for segment in unsettled {
            guard segment.source == source, settled(source, through: segment.audioEnd) else {
                waiting.append(segment)
                continue
            }
            var changed = segment
            changed.speakerIndex = speaker(of: source, segment.audioStart, segment.audioEnd)
            changed.speakerSettled = true
            out.append(changed)
        }
        unsettled = waiting
        return out
    }

    /// The gated mic pieces every call diarizer is past, in order: dropped when the call
    /// has speech over them, else numbered.
    private mutating func releaseGated() -> [DaemonAPI.MeetingSegment] {
        var out: [DaemonAPI.MeetingSegment] = []
        while let words = gated.first, callSources.allSatisfy({ settled($0, through: words.end) }) {
            gated.removeFirst()
            if callHasSpeech(over: words) {
                dropped += 1
            } else {
                out.append(numbered(words, settledAnyway: false))
            }
        }
        return out
    }

    private func callHasSpeech(over words: HeardWords) -> Bool {
        let length = words.end - words.start
        guard length > 0 else { return false }
        let speech = callSources.map { timelines[$0]?.speech(from: words.start, to: words.end) ?? 0 }.max() ?? 0
        return speech >= length * options.gateShare
    }

    /// The words cut to segments of at most `textLimit` characters, at a space where there
    /// is one, each with its share of the time. Nothing when there are no words.
    static func split(_ words: HeardWords) -> [HeardWords] {
        let text = words.text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return [] }
        let limit = DaemonAPI.MeetingSegment.textLimit
        guard text.count > limit else {
            var one = words
            one.text = text
            return [one]
        }
        var parts: [String] = []
        var rest = Substring(text)
        while rest.count > limit {
            let window = rest.prefix(limit)
            let cut = window.lastIndex(of: " ") ?? window.endIndex
            parts.append(String(rest[..<cut]).trimmingCharacters(in: .whitespaces))
            rest = rest[cut...].drop { $0 == " " }
        }
        if !rest.isEmpty { parts.append(String(rest)) }
        let total = Double(parts.reduce(0) { $0 + $1.count })
        var at = words.start
        return parts.map { part in
            let length = (words.end - words.start) * Double(part.count) / total
            defer { at += length }
            return HeardWords(source: words.source, start: at, end: at + length, text: part)
        }
    }
}
