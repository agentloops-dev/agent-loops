import Foundation

/// Whether a lost host is the Mac's sleep, and not a connection that broke while it was
/// awake (#6570).
///
/// After a sleep, a host's link to the control plane is often dead, and it drops and joins
/// again: in a dark wake every 15 minutes, and after a full wake. That reconnect is quiet
/// for `quiet` after the wake, as the other reconnects are (#6227). When the host is still
/// lost after that, the cause is the sleep, and the words say so, with the time it woke.
///
/// A wake is known two ways: the wake notification (a full wake only), and the two clocks
/// (#6541): a continuous clock counts the time asleep and a suspending clock does not, so a
/// gap between them that grew is a sleep, also a dark wake that no notification tells of.
public struct AfterSleep: Equatable, Sendable {
    /// How long after a wake a reconnect says nothing.
    public static let quiet: TimeInterval = 30
    /// How near to a wake a drop must be for the sleep to be its cause.
    public static let near: TimeInterval = 120
    /// The least growth of the clocks' gap that is a sleep, not a clock's own jitter.
    public static let leastSleep: Duration = .seconds(5)

    public enum Verdict: Equatable, Sendable {
        /// Woke a moment ago: the window is reconnecting, and says nothing yet.
        case quiet
        /// The drop came with a sleep, and the window still has not reconnected.
        case wasAsleep(woke: Date)
        /// Not about a sleep.
        case awake
    }

    /// The last wake that was seen, by either way.
    public private(set) var wokeAt: Date?
    /// The time asleep since the clocks started, when they were last read.
    private var asleepSeen: Duration?

    public init() {}

    /// The wake notification came at `now`.
    public mutating func woke(at now: Date) {
        if let wokeAt, wokeAt >= now { return }
        wokeAt = now
    }

    /// The clocks say this Mac was asleep for `asleepSoFar` in all since they started. When
    /// that grew by `leastSleep` or more since the last read, the Mac slept since then, and
    /// woke at about `now`. The first read only sets where to count from.
    /// Returns whether it saw a sleep.
    @discardableResult
    public mutating func look(asleepSoFar: Duration, now: Date) -> Bool {
        defer { asleepSeen = asleepSoFar }
        guard let asleepSeen, asleepSoFar - asleepSeen >= Self.leastSleep else { return false }
        woke(at: now)
        return true
    }

    /// What a host lost since `downSince` is, at `now`.
    public func verdict(downSince: Date, now: Date) -> Verdict {
        guard let wokeAt, abs(downSince.timeIntervalSince(wokeAt)) <= Self.near else { return .awake }
        return now.timeIntervalSince(wokeAt) < Self.quiet ? .quiet : .wasAsleep(woke: wokeAt)
    }

    /// When a quiet that is running now ends, so a window can look again then.
    public func quietEnds(now: Date) -> Date? {
        guard let wokeAt else { return nil }
        let end = wokeAt.addingTimeInterval(Self.quiet)
        return end > now ? end : nil
    }
}

/// The time this process has seen the Mac asleep since `init`: a continuous clock's time
/// less a suspending clock's (#6541, #6570).
public struct SleepClocks: Sendable {
    private let continuousStart = ContinuousClock.now
    private let suspendingStart = SuspendingClock.now

    public init() {}

    public var asleepSoFar: Duration {
        (ContinuousClock.now - continuousStart) - (SuspendingClock.now - suspendingStart)
    }
}
