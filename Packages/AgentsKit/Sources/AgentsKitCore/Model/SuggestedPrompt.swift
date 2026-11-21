import Foundation

/// Something the agent thinks you might want to say next.
///
/// The protocol has no way to send one: every "suggestion" in ACP is a code edit. So
/// these arrive the only way an agent can hand the app something of its own, which is
/// a tool call against an MCP server we serve. `AppService` is that server.
///
/// A suggestion is a draft, never an instruction. Tapping one fills the prompt and
/// leaves the cursor in it: what goes to the runtime is still what the user sent.
public struct SuggestedPrompt: Codable, Hashable, Sendable, Identifiable {
    public var id: UUID
    /// The few words on the chip.
    public var label: String
    /// What goes into the prompt when it is tapped, which is usually longer.
    public var prompt: String
}
