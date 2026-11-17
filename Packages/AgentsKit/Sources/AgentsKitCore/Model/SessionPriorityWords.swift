import Foundation

/// The words for a session's priority on its row and in its menu (#5849), the same on the
/// Mac, the Remote and the web page (`Web/src/model/sessionPriority.ts`).
///
/// The row has a chip for high and low only, with the look of the Work rows' (#5690):
/// medium and none are the usual, and a chip on every row would say nothing.
public enum SessionPriorityWords {
    /// The menu's title: Priority ▸ High, Medium, Low, From issue (or None).
    public static let menu = "Priority"

    /// *High* or *Low* for the row's chip; nil for medium and none.
    public static func chip(_ agent: Agent) -> String? {
        switch agent.priority {
        case .high: "High"
        case .low: "Low"
        case .medium, nil: nil
        }
    }

    /// The chip's tooltip and spoken label: "High, from #5849", "Low, from the session
    /// that started it", or "High, set on this session".
    public static func help(_ agent: Agent) -> String? {
        guard let words = chip(agent) else { return nil }
        switch agent.prioritySource {
        case .issue: return "\(words), from \(issueLabel(agent) ?? "its issue")"
        case .starter: return "\(words), from the session that started it"
        case .set: return "\(words), set on this session"
        case .some(.none), nil: return words
        }
    }

    /// The first label that links the session to an issue or a pull request: #5849, ENG-4, P#5895.
    public static func issueLabel(_ agent: Agent) -> String? {
        agent.labels.first { $0.kind == .issue }?.value
            ?? agent.labels.first { $0.kind == .pullRequest }?.value
    }

    /// Whether the session has an issue to follow: a label that links one, or a priority
    /// the daemon read from one.
    public static func hasIssue(_ agent: Agent) -> Bool {
        agent.prioritySource == .issue || issueLabel(agent) != nil
    }

    /// One item of the menu. `follow` clears the set priority: the session goes by its
    /// issue again, or by none.
    public enum Choice: Hashable, Sendable, CaseIterable, Identifiable {
        case high, medium, low, follow

        public var id: Self { self }

        /// The priority `agents/setPriority` sends; nil clears it.
        public var priority: SessionPriority? {
            switch self {
            case .high: .high
            case .medium: .medium
            case .low: .low
            case .follow: nil
            }
        }
    }

    /// The item's title: High, Medium, Low, then "From issue", or "None" with no issue.
    /// "From issue" says the issue's level when the session goes by it: "From issue (High)".
    public static func title(_ choice: Choice, for agent: Agent) -> String {
        guard choice == .follow else { return title(choice) }
        guard hasIssue(agent) else { return "None" }
        if agent.prioritySet == nil, agent.prioritySource == .issue, let p = agent.priority {
            return "From issue (\(level(p)))"
        }
        return "From issue"
    }

    /// The item's title with no session to word it for: "From issue" for `follow`.
    public static func title(_ choice: Choice) -> String {
        choice.priority.map(level) ?? "From issue"
    }

    /// The item that has the check: the priority set on the session, else the one that
    /// follows its issue (or none).
    public static func checked(_ agent: Agent) -> Choice {
        switch agent.prioritySet {
        case .high: .high
        case .medium: .medium
        case .low: .low
        case nil: .follow
        }
    }

    /// High, Medium or Low.
    public static func level(_ priority: SessionPriority) -> String {
        switch priority {
        case .high: "High"
        case .medium: "Medium"
        case .low: "Low"
        }
    }
}
