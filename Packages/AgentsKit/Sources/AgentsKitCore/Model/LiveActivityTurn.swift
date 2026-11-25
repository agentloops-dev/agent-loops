import Foundation

/// A turn that the Remote's Live Activity speaks for (#1657): when it began, and the runtime and folder it runs in.
public struct LiveActivityTurn: Codable, Hashable, Sendable {
    /// The most turns kept, and the most bytes of a turn's place (#7252). Past it the oldest goes: a turn is the host's, and
    /// a launch without it only starts no Live Activity for it.
    public static let most = 100
    public static let placeMost = 256
    /// The most the kept turns take.
    public static let bytes = 128 * 1024

    public var startedAt: Date
    public var place: String

    public init(startedAt: Date, place: String) {
        self.startedAt = startedAt
        self.place = place
    }

    /// The turns that are kept: the newest `most`, none past 90 days, each with its place cut.
    public static func kept(_ turns: [UUID: LiveActivityTurn], now: Date = Date()) -> [UUID: LiveActivityTurn] {
        let live = turns.filter { now.timeIntervalSince($0.value.startedAt) <= cacheAgeLimit }
            .sorted { $0.value.startedAt > $1.value.startedAt }.prefix(most)
        return Dictionary(uniqueKeysWithValues: live.map { id, turn in
            (id, LiveActivityTurn(startedAt: turn.startedAt, place: WorkCache.prefix(turn.place, bytes: placeMost)))
        })
    }
}
