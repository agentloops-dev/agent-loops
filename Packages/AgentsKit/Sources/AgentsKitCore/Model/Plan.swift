import Foundation

/// What an agent says it is about to do.
///
/// 001 kept the raw value and drew "Made a plan", which threw away the clearest
/// statement an agent makes of its intentions, and the moment to stop it if it is
/// wrong.
public struct Plan: Codable, Hashable, Sendable, Identifiable {
    /// The protocol's own id, where the update carried one. The plain `plan` update
    /// has none, and there is only ever one of those at a time.
    public var planID: String?
    public var entries: [PlanEntry]
    public var state: State
    public var at: Date

    public enum State: String, Codable, Hashable, Sendable {
        case current
        /// Withdrawn by the agent. Kept on the record rather than deleted: it happened.
        case withdrawn
    }

    public var id: String { planID ?? "plan" }

    public init(planID: String? = nil, entries: [PlanEntry], state: State = .current, at: Date = Date()) {
        self.planID = planID
        self.entries = entries
        self.state = state
        self.at = at
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        planID = try c.decodeIfPresent(String.self, forKey: .planID)
        entries = try c.decodeIfPresent([PlanEntry].self, forKey: .entries) ?? []
        state = try c.decodeIfPresent(State.self, forKey: .state) ?? .current
        at = try c.decodeIfPresent(Date.self, forKey: .at) ?? Date()
    }
}

public struct PlanEntry: Codable, Hashable, Sendable, Identifiable {
    public var content: String
    public var priority: Priority
    public var status: Status

    public var id: String { content }

    public enum Priority: String, Codable, Hashable, Sendable {
        case high, medium, low
    }

    public enum Status: String, Codable, Hashable, Sendable {
        case pending
        case inProgress = "in_progress"
        case completed
    }

    public init(content: String, priority: Priority = .medium, status: Status = .pending) {
        self.content = content
        self.priority = priority
        self.status = status
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        content = try c.decodeIfPresent(String.self, forKey: .content) ?? ""
        priority = try c.decodeIfPresent(Priority.self, forKey: .priority) ?? .medium
        status = try c.decodeIfPresent(Status.self, forKey: .status) ?? .pending
    }
}
