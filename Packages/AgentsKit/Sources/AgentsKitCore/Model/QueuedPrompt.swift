import Foundation

/// Something typed while the agent was still working, waiting its turn.
///
/// The queue is the daemon's, and deliberately not the runtime's. ACP has no way to
/// say "take this when you are done", and a `session/prompt` sent while a turn is in
/// flight is answered differently by each of them: one holds it, one refuses it, one
/// starts a second turn over the top of the first. Holding it here makes all three
/// behave the same way, and it is the only version that is still true after the
/// window closes: the queue is on the agent's record, so it survives the daemon
/// going idle and coming back.
public struct QueuedPrompt: Codable, Hashable, Sendable, Identifiable {
    public var id: UUID
    public var text: String
    public var attachments: [Attachment]
    public var queuedAt: Date
    /// Whose prompt this is. The app queues one of its own after a turn that ended
    /// without saying how it went; everything else here is the person's.
    public var from: PromptOrigin
    /// Words for the runtime only, sent before `text` and never recorded as said: what
    /// the app owes the agent about this prompt, such as the wait it just cancelled
    /// (042 FR-013). The person's bubble stays their own words.
    public var preface: String?
    /// The agent that sent it, when `from` is `.agent` (#560).
    public var sender: MessageSender?

    /// What goes to the runtime when its turn comes: the words, then what was
    /// attached. The same shape a prompt sent straight away takes.
    public var blocks: [ContentBlock] {
        [.text(text)] + attachments.map(\.block)
    }

    /// Said when an edit came too late (#1552): the prompt went, or was taken back, first.
    /// The same words on the page (`sentBeforeEdit` in `Web/src/views/Chat.tsx`).
    public static let sentBeforeEdit = "That prompt was sent before your change. Your words are in the prompt bar."
}
