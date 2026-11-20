import Foundation

/// How much of a plan window a runtime says is used, for Agent Runtimes to show.
///
/// Standard ACP has no way to ask this: `usage_update` is the session's context and
/// cost, not the account's allowance. Two runtimes say it in extensions of their own,
/// and each is read here. A reading is only ever shown. Whether a chat may run is
/// `AllowanceState.status`, which a stale percentage must never decide.
public struct AllowanceReading: Codable, Hashable, Sendable {
    /// The runtime's name for the window: Claude's `five_hour`, `seven_day`,
    /// `seven_day_opus`; Grok's `weekly`, `monthly`. Kept as sent.
    public var window: String?
    /// How much is used, from 0 to 1. Nil when the runtime said when it resets but not
    /// how much is gone, which Claude often does.
    public var used: Double?
    public var resetsAt: Date?
    /// The runtime refused for this window: none left, whatever `used` says.
    public var spent: Bool
    /// The runtime warned that it is nearly used up.
    public var nearlySpent: Bool
    /// When it was measured.
    public var at: Date

    public init(window: String? = nil, used: Double? = nil, resetsAt: Date? = nil,
                spent: Bool = false, nearlySpent: Bool = false, at: Date) {
        self.window = window
        self.used = used.map { min(max($0, 0), 1) }
        self.resetsAt = resetsAt
        self.spent = spent
        self.nearlySpent = nearlySpent
        self.at = at
    }

    /// What is left, from 0 to 1, where it is known.
    public var left: Double? {
        if spent { return 0 }
        return used.map { 1 - $0 }
    }

    /// How much of the window is used, from 0 to 1, where it is known: what the
    /// Runtimes page's bar fills to (#1097). A spent window is full whatever it said.
    public var usedFraction: Double? {
        if spent { return 1 }
        return used
    }

    /// A window that has since reset says nothing about the new one.
    public func isCurrent(now: Date) -> Bool {
        guard let resetsAt else { return true }
        return resetsAt > now
    }
}
