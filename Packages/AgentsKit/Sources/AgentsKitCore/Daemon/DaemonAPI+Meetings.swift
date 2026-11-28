import Foundation

/// Meeting mode's host methods (#7581, epic #7327), behind the flag `meeting-mode`. The Mac
/// listens and sends each final segment (#7584); the daemon keeps them and decides each gap.
/// The types are `Packages/Wire/schema/meetings.schema.json`, written by hand here.
extension DaemonAPI.Method {
    public static let meetingsStart = "meetings/start"
    public static let meetingsSetState = "meetings/setState"
    public static let meetingsAppend = "meetings/append"
    public static let meetingsSegments = "meetings/segments"
}

extension DaemonAPI.Notification {
    public static let meetingsChanged = "meetings/changed"
}

extension DaemonAPI {
    public enum MeetingState: String, Codable, Hashable, Sendable, CaseIterable {
        case listening, paused, ended
    }

    /// What a meeting listens to: `mic`, or `call:<bundle id>` for a call app.
    public enum MeetingSource {
        public static let mic = "mic"
        public static let callPrefix = "call:"

        public static func call(_ bundleID: String) -> String { callPrefix + bundleID }

        /// The call app's bundle id of a `call:` source; nil for the microphone.
        public static func bundleID(of source: String) -> String? {
            guard source.hasPrefix(callPrefix) else { return nil }
            let id = String(source.dropFirst(callPrefix.count))
            return id.isEmpty ? nil : id
        }
    }

    /// A voice of the meeting: its stream and its slot there (#7580: one diarizer per stream).
    public struct MeetingSpeaker: Codable, Hashable, Sendable {
        public var source: String
        public var index: Int
        public var name: String?

        public init(source: String, index: Int, name: String? = nil) {
            self.source = source
            self.index = index
            self.name = name
        }
    }

    public struct Meeting: Codable, Hashable, Sendable, Identifiable {
        public var id: UUID
        public var agentID: UUID
        public var folder: URL
        public var title: String
        public var state: MeetingState
        public var startedAt: Date
        public var endedAt: Date?
        public var sources: [String]
        public var speakers: [MeetingSpeaker]
        public var lastSeq: Int64
        public var sentThroughSeq: Int64
        public var segmentsRemoved: Bool?
        public var updatedAt: Date

        public init(id: UUID, agentID: UUID, folder: URL, title: String, state: MeetingState, startedAt: Date,
                    endedAt: Date? = nil, sources: [String], speakers: [MeetingSpeaker] = [], lastSeq: Int64 = 0,
                    sentThroughSeq: Int64 = 0, segmentsRemoved: Bool? = nil, updatedAt: Date) {
            self.id = id
            self.agentID = agentID
            self.folder = folder
            self.title = title
            self.state = state
            self.startedAt = startedAt
            self.endedAt = endedAt
            self.sources = sources
            self.speakers = speakers
            self.lastSeq = lastSeq
            self.sentThroughSeq = sentThroughSeq
            self.segmentsRemoved = segmentsRemoved
            self.updatedAt = updatedAt
        }
    }

    /// A final piece of a meeting's words, from one stream. Audio is never kept.
    public struct MeetingSegment: Codable, Hashable, Sendable {
        /// 1 for the first segment of the meeting; each next one adds 1.
        public var seq: Int64
        public var source: String
        /// Seconds from the meeting's start.
        public var audioStart: Double
        public var audioEnd: Double
        public var text: String
        /// The diarizer's slot in this segment's own stream.
        public var speakerIndex: Int?
        /// The diarizer will not change the speaker any more.
        public var speakerSettled: Bool

        /// The most text a segment holds.
        public static let textLimit = 1_500

        public init(seq: Int64, source: String, audioStart: Double, audioEnd: Double, text: String,
                    speakerIndex: Int? = nil, speakerSettled: Bool) {
            self.seq = seq
            self.source = source
            self.audioStart = audioStart
            self.audioEnd = audioEnd
            self.text = text
            self.speakerIndex = speakerIndex
            self.speakerSettled = speakerSettled
        }
    }

    public struct MeetingStartRequest: Codable, Hashable, Sendable {
        public var folder: URL
        public var title: String?
        public var sources: [String]
        public var runtimeID: String?

        public init(folder: URL, title: String? = nil, sources: [String], runtimeID: String? = nil) {
            self.folder = folder
            self.title = title
            self.sources = sources
            self.runtimeID = runtimeID
        }
    }

    public struct MeetingStarted: Codable, Hashable, Sendable {
        public var meeting: Meeting
        public var agentID: UUID
    }

    public struct MeetingSetStateRequest: Codable, Hashable, Sendable {
        public var meetingID: UUID
        public var state: MeetingState

        public init(meetingID: UUID, state: MeetingState) {
            self.meetingID = meetingID
            self.state = state
        }
    }

    /// New segments, each following the last with no gap, or a changed speaker of an earlier
    /// seq (its text is not changed). At most 200.
    public struct MeetingAppendRequest: Codable, Hashable, Sendable {
        public var meetingID: UUID
        public var segments: [MeetingSegment]

        public static let segmentLimit = 200

        public init(meetingID: UUID, segments: [MeetingSegment]) {
            self.meetingID = meetingID
            self.segments = segments
        }
    }

    public struct MeetingAppended: Codable, Hashable, Sendable {
        public var lastSeq: Int64
        public var sentThroughSeq: Int64
    }

    public struct MeetingSegmentsRequest: Codable, Hashable, Sendable {
        public var meetingID: UUID
        public var afterSeq: Int64
        public var limit: Int?

        public init(meetingID: UUID, afterSeq: Int64, limit: Int? = nil) {
            self.meetingID = meetingID
            self.afterSeq = afterSeq
            self.limit = limit
        }
    }

    public struct MeetingSegmentsPage: Codable, Hashable, Sendable {
        public var segments: [MeetingSegment]
        public var more: Bool
    }
}
