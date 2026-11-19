import Foundation

/// One count of a machine's sessions (#5488, specs/machine-sessions), as `machineSessions.ts`:
/// the sidebar line ("N running") and the machine's page ("N sessions · M running", the groups)
/// both read it, so the two never disagree. A stopped or offline machine's list is the last one
/// it sent, so it counts as last known.
public struct MachineSessions: Sendable {
    public enum Group: String, CaseIterable, Sendable {
        case working = "Working", waiting = "Waiting", done = "Done"

        public init?(_ group: AgentGroup) {
            switch group {
            case .running: self = .working
            case .needsAttention, .blocked, .waiting: self = .waiting
            case .finished, .stopped: self = .done
            case .archived: return nil
            }
        }
    }

    /// Every non-archived session, newest activity first, by group.
    public let groups: [Group: [Agent]]

    public init(_ agents: [Agent]) {
        var groups: [Group: [Agent]] = [:]
        for agent in agents {
            if let group = Group(agent.group()) { groups[group, default: []].append(agent) }
        }
        self.groups = groups.mapValues { $0.sorted { $0.lastActivityAt > $1.lastActivityAt } }
    }

    public func agents(_ group: Group) -> [Agent] { groups[group] ?? [] }
    public var total: Int { groups.values.reduce(0) { $0 + $1.count } }
    /// Sessions with status Working.
    public var running: Int { agents(.working).count }

    /// The sidebar's line under an online machine: "3 running", or "Idle".
    public var runningLine: String { MachineSessionCounts.line(running) }
    /// The page's header: "7 sessions · 2 running".
    public var header: String { "\(total) \(total == 1 ? "session" : "sessions") · \(running) running" }
}

/// How many of a machine's sessions work and wait (P20261117-1): all that the sidebar's
/// Machines group, its rows and the Machines page's Working column read. `AgentsModel` keeps
/// one per machine as agents are filed, so a reader of it draws again only when a count moves,
/// not on every change to every agent held (#165). Grouped as `MachineSessions` groups them.
public struct MachineSessionCounts: Equatable, Sendable {
    /// Sessions with status Working: `MachineSessions.running`.
    public var working = 0
    /// Sessions waiting for the person (needs attention, blocked, waiting).
    public var waiting = 0

    public init(working: Int = 0, waiting: Int = 0) {
        self.working = working
        self.waiting = waiting
    }

    /// Sessions work or wait for the person there: what makes the machine active in the group.
    public var busy: Bool { working > 0 || waiting > 0 }
    /// The sidebar's line under an online machine, as `MachineSessions.runningLine`.
    public var runningLine: String { Self.line(working) }

    static func line(_ running: Int) -> String { running > 0 ? "\(running) running" : "Idle" }

    /// One agent of `group` more (`by` 1) or less (`by` -1). False when the group is not
    /// counted here, and nothing changed.
    @discardableResult
    public mutating func add(_ group: AgentGroup, by: Int = 1) -> Bool {
        switch MachineSessions.Group(group) {
        case .working: working += by
        case .waiting: waiting += by
        case .done, nil: return false
        }
        return true
    }
}
