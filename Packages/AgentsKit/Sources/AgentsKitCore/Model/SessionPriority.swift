import Foundation

/// A session's priority (#5846): the same three levels as an issue's, in the order high,
/// medium, none, low. The daemon works out the one a session goes by (`Agent.priority`).
public enum SessionPriority: String, Codable, Hashable, Sendable, CaseIterable {
    case high, medium, low
}

/// Where a session's priority comes from (#5846): set on it, its issue's, its starter's, or none.
public enum SessionPrioritySource: String, Codable, Hashable, Sendable, CaseIterable {
    case set, issue, starter, none
}
