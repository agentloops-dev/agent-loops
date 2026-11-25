import AgentsKitCore
import Foundation

/// The words and the symbol for an agent's `archive_session` that waits for the person to
/// approve archiving (#584, #649), once for both apps. The web page says the same in
/// `archiveWords.ts`.
///
/// Whether a session carries the mark is `Agent.asksToArchive`'s to decide; this only
/// says what the mark and its buttons are called.
enum ArchiveRequestWords {
    static let symbol = "archivebox"

    /// The mark on the row.
    static let mark = "Archiving needs approval"

    /// The card's head at the end of the chat (#827).
    static let question = "The agent asks to archive this session"

    /// Under the head when the agent said nothing of its own.
    static let noReport = "The agent says its work is done."

    /// Under the head (#6067): why the agent asks; what its turn said for a request with no
    /// reason; `noReport` with neither.
    static func body(_ agent: Agent) -> String {
        agent.archiveRequest?.reason ?? agent.report?.message ?? noReport
    }

    /// The one click that approves it, and archives the session.
    static let archive = "Approve"

    /// The tooltip and the accessibility hint for Approve beside the mark.
    static let archiveHelp = "The agent is done with this session: approve archiving it"

    /// Deny (#773): the request goes, and the session stays where it is.
    static let keep = "Keep Session"

    /// The mark and why: "Archiving needs approval: PR #12 merged", the mark's tooltip (#6067).
    static func detail(_ agent: Agent) -> String {
        guard let reason = agent.archiveRequest?.reason else { return mark }
        return "\(mark): \(reason)"
    }

    /// "Archiving needs approval", or nil for a session that does not.
    static func line(_ agent: Agent?) -> String? {
        agent?.asksToArchive == true ? mark : nil
    }
}
