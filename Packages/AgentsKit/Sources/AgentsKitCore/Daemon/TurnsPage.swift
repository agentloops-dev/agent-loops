import Foundation

/// Some of a conversation's finished turns, oldest first, and where the turn still in
/// progress starts in the transcript.
public struct TurnsPage: Codable, Hashable, Sendable {
    public var turns: [TurnSummary]
    /// The position of the first of `turns` among all the finished ones.
    public var firstTurn: Int
    /// The transcript's index of the first entry of the turn after the last finished
    /// one: where `agents/transcript` is asked to start.
    public var openStart: Int
    /// How many of the person's prompts come before the first of `turns`: what the Prompts
    /// list numbers from (#1862). Nil from a host too old to say.
    public var firstPrompt: Int?
    /// When the 90-day or size trim took the start of the chat (#7405): the host's line for
    /// where the start was, the same on every client. Nil when nothing was removed, or from
    /// a host too old to say.
    public var startRemoved: String?

    public init(turns: [TurnSummary], firstTurn: Int, openStart: Int, firstPrompt: Int? = nil,
                startRemoved: String? = nil) {
        self.turns = turns
        self.firstTurn = firstTurn
        self.openStart = openStart
        self.firstPrompt = firstPrompt
        self.startRemoved = startRemoved
    }

    public var hasMoreBefore: Bool { firstTurn > 0 }
}
