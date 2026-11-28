import Foundation

/// `autopilot/resume` (#5905): drops a paused item's row, so Autopilot may start it again. The
/// web page's Work ▸ Next sends it; the other Autopilot types are in DaemonAPI.swift (#5898, #5904).
public extension DaemonAPI {
    /// The folder and the paused issue or epic, as `autopilot/status` names it.
    struct AutopilotResumeRequest: Codable, Sendable, Hashable {
        public var folder: URL
        public var ref: String

        public init(folder: URL, ref: String) {
            self.folder = folder
            self.ref = ref
        }
    }
}

public extension DaemonAPI.Method {
    /// `AutopilotResumeRequest` → `AutopilotStatus` (#5905). Not in `ConnectionRole.agentMethods`: only the person resumes.
    static let autopilotResume = "autopilot/resume"
}
