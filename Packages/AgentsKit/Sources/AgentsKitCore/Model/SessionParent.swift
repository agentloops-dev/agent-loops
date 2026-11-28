import Foundation

/// Where a session is put in the tree (#7538, spec 7535): under the lead `id`, or at the top
/// when `id` is absent. Absent on the record, the tree follows the starter, as before.
public struct SessionParent: Codable, Hashable, Sendable {
    public enum SetBy: String, Codable, Hashable, Sendable {
        case person, agent
    }

    /// The lead; nil puts the session at the top, its starter ignored.
    public var id: UUID?
    public var setAt: Date
    public var setBy: SetBy

    public init(id: UUID?, setAt: Date = Date(), setBy: SetBy = .person) {
        self.id = id
        self.setAt = setAt
        self.setBy = setBy
    }
}

/// What `agents/setParent` asks for: under a lead, at the top, or back under the starter.
public enum SessionPlacement: Hashable, Sendable {
    case under(UUID)
    case top
    case starter

    /// The placement a record has now.
    public init(of agent: Agent) {
        switch agent.parent {
        case nil: self = .starter
        case let parent?: self = parent.id.map(SessionPlacement.under) ?? .top
        }
    }

    /// The record's `parent` once the host has made this placement.
    public var parent: SessionParent? {
        switch self {
        case .under(let id): SessionParent(id: id)
        case .top: SessionParent(id: nil)
        case .starter: nil
        }
    }

    /// The wire's `placement`.
    public var wire: String {
        switch self {
        case .under: "under"
        case .top: "top"
        case .starter: "starter"
        }
    }

    public var leadID: UUID? {
        if case .under(let id) = self { return id }
        return nil
    }
}
