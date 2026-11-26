import Foundation

/// What Autopilot did in a folder and why each item waits (#5898, #5905): `autopilot/status`, and
/// `autopilot/resume` for a paused item. The web page's Work ▸ Next reads them; the Mac draws only a link.
public extension DaemonAPI {
    /// `autopilot/status`'s question: the folder.
    struct AutopilotRequest: Codable, Sendable, Hashable {
        public var folder: String

        public init(folder: String) { self.folder = folder }
    }

    /// `autopilot/resume`'s question: the folder and the paused issue or epic, as the status names it.
    struct AutopilotResumeRequest: Codable, Sendable, Hashable {
        public var folder: String
        public var ref: String

        public init(folder: String, ref: String) {
            self.folder = folder
            self.ref = ref
        }
    }

    enum AutopilotMode: String, Codable, Sendable, Hashable, CaseIterable {
        case off, high, on
    }

    /// A lead is for an epic; a lane is for an issue.
    enum AutopilotRowKind: String, Codable, Sendable, Hashable, CaseIterable {
        case lead, lane
    }

    enum AutopilotRowState: String, Codable, Sendable, Hashable, CaseIterable {
        case started, waiting, paused
    }

    /// One issue or epic: what Autopilot did with it, or why it waits (`reason`, empty once started).
    struct AutopilotRow: Codable, Sendable, Hashable {
        public var ref: String
        public var kind: AutopilotRowKind
        public var state: AutopilotRowState
        public var reason: String
        public var session: String?
        public var since: Date

        public init(ref: String, kind: AutopilotRowKind, state: AutopilotRowState, reason: String = "",
                    session: String? = nil, since: Date) {
            self.ref = ref
            self.kind = kind
            self.state = state
            self.reason = reason
            self.session = session
            self.since = since
        }
    }

    /// The mode, when Autopilot last looked, and at most 100 rows, the newest first.
    struct AutopilotStatus: Codable, Sendable, Hashable {
        public var mode: AutopilotMode
        public var lastRunAt: Date?
        public var rows: [AutopilotRow]

        public init(mode: AutopilotMode, lastRunAt: Date? = nil, rows: [AutopilotRow] = []) {
            self.mode = mode
            self.lastRunAt = lastRunAt
            self.rows = rows
        }
    }
}

public extension DaemonAPI.Method {
    /// `AutopilotRequest` → `AutopilotStatus` (#5898).
    static let autopilotStatus = "autopilot/status"
    /// `AutopilotResumeRequest` → `AutopilotStatus` (#5905): drops a paused row.
    static let autopilotResume = "autopilot/resume"
}
