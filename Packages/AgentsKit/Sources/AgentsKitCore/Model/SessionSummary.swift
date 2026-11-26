import Foundation

/// A session's summary (#7321, epic #7318): a few sentences of what is going on, written
/// by a short call to the runtime's fastest model when a client opens the session in
/// summary mode (`agents/summarise`), not by the agent.
public struct SessionSummary: Codable, Hashable, Sendable {
    /// Two to four sentences, at most 600 characters; empty until the first is written.
    public var text: String
    /// When the text was written: the view's "Last update".
    public var at: Date?
    /// The `TurnSummary.id` of the last turn the text read, so a turn is not read twice.
    public var throughTurn: UUID?
    public var state: State
    /// The model that wrote the text.
    public var model: String?

    public enum State: String, Codable, Hashable, Sendable {
        case writing, written, failed
    }

    public init(text: String, at: Date? = nil, throughTurn: UUID? = nil, state: State, model: String? = nil) {
        self.text = text
        self.at = at
        self.throughTurn = throughTurn
        self.state = state
        self.model = model
    }
}
