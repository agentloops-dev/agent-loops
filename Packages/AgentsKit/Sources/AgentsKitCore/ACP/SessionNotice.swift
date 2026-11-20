import Foundation

/// Something the runtime wants the person to know that is not part of the reply: a
/// rate limit coming up, a setting it ignored, a model it fell back to.
///
/// ACP's `notice` update (unstable, RFD "Session Notices"). The protocol calls these
/// live events rather than history, and a runtime never sends them again on load, but
/// we keep the ones we are sent: the record is what happened, and a warning scrolled
/// past is still a warning someone may go back for.
public struct SessionNotice: Codable, Hashable, Sendable {
    /// `info`, `warning`, `error`, or a value this build does not know, kept as sent.
    public var severity: String
    public var title: String
    public var detail: String?

    public init(severity: String, title: String, detail: String? = nil) {
        self.severity = severity
        self.title = title
        self.detail = detail
    }

    /// Whether something has gone wrong, which is the only thing the chat gives a colour.
    public var isError: Bool { severity == "error" }
    public var isWarning: Bool { severity == "warning" }
}
