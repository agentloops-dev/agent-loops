import Foundation

// MARK: - The wire (#5458, epic #5457)

public extension DaemonAPI {
    /// `work/suggest-leads` (#5458): the epics of every folder on this machine, or of one folder's repo.
    struct WorkSuggestLeadsRequest: Codable, Sendable, Hashable {
        public var folder: String?

        public init(folder: String? = nil) {
            self.folder = folder
        }
    }

    /// New Session's Suggested section: at most 3 open epics with no lead, ranked by the daemon;
    /// `more` is how many more there are, for "N more in Work"; `errors` the forges it could not read.
    struct WorkSuggestLeads: Codable, Sendable, Hashable {
        public var suggestions: [WorkLeadSuggestion]
        public var more: Int
        public var errors: [ForgeListError]?

        public init(suggestions: [WorkLeadSuggestion] = [], more: Int = 0, errors: [ForgeListError]? = nil) {
            self.suggestions = suggestions
            self.more = more
            self.errors = errors
        }
    }

    /// An open epic with no lead. `done` and `total` are its children closed and all (0 and 0 for a
    /// standing epic); `due` is its milestone's due date, YYYY-MM-DD; `start` is Start lead's form:
    /// the prompt, the labels `#<n>` and `lead`, and no worktree.
    struct WorkLeadSuggestion: Codable, Sendable, Hashable, Identifiable {
        public var node: LineageNode
        public var title: String
        public var url: String?
        public var folder: String?
        public var milestone: String?
        public var due: String?
        public var done: Int
        public var total: Int
        public var high: Bool?
        public var ui: Bool?
        public var standing: Bool?
        public var start: ForgeStart

        public var id: String { node.ref }

        public init(node: LineageNode, title: String, url: String? = nil, folder: String? = nil,
                    milestone: String? = nil, due: String? = nil, done: Int = 0, total: Int = 0,
                    high: Bool? = nil, ui: Bool? = nil, standing: Bool? = nil, start: ForgeStart) {
            self.node = node
            self.title = title
            self.url = url
            self.folder = folder
            self.milestone = milestone
            self.due = due
            self.done = done
            self.total = total
            self.high = high
            self.ui = ui
            self.standing = standing
            self.start = start
        }
    }

    /// `work/dismiss-lead` (#5458): hide an epic from the suggestions until it changes or a lead of it ends.
    struct WorkDismissLeadRequest: Codable, Sendable, Hashable {
        public var node: LineageNode

        public init(node: LineageNode) {
            self.node = node
        }
    }
}

public extension DaemonAPI.Method {
    /// `WorkSuggestLeadsRequest` → `WorkSuggestLeads` (#5458).
    static let workSuggestLeads = "work/suggest-leads"
    /// `WorkDismissLeadRequest` → `ForgeDone` (#5458).
    static let workDismissLead = "work/dismiss-lead"
}

// MARK: - The words (#5457)

/// The Suggested section's words, the same on every client (epic #5457): a row is
/// "Lead #4487 Screens foundation", with "0/8 · due 12-04" under it.
public enum SuggestedLeads {
    public static let heading = "Suggested"

    /// "Lead #4487 Screens foundation"; "Lead Screens foundation" for an epic with no number.
    public static func title(_ s: DaemonAPI.WorkLeadSuggestion) -> String {
        guard let n = number(s) else { return "Lead \(s.title)" }
        return "Lead #\(n) \(s.title)"
    }

    /// "0/8 · due 12-04": its children closed of all, and its milestone's due date. A standing
    /// epic's plan is a query, so it says "Standing" in place of the count.
    public static func detail(_ s: DaemonAPI.WorkLeadSuggestion) -> String {
        var parts: [String] = []
        if s.standing == true {
            parts.append("Standing")
        } else if s.total > 0 {
            parts.append("\(s.done)/\(s.total)")
        }
        if let due = s.due.flatMap(shortDue) { parts.append("due \(due)") }
        return parts.joined(separator: " · ")
    }

    /// What VoiceOver says for a row's detail: "0 of 8 done, due 12-04".
    public static func spokenDetail(_ s: DaemonAPI.WorkLeadSuggestion) -> String {
        var parts: [String] = []
        if s.standing == true {
            parts.append("Standing epic")
        } else if s.total > 0 {
            parts.append("\(s.done) of \(s.total) done")
        }
        if let due = s.due.flatMap(shortDue) { parts.append("due \(due)") }
        return parts.joined(separator: ", ")
    }

    /// The line Hide leaves, beside Show (#5790).
    public static let hiddenLine = "Suggested is hidden."

    /// Whether New Session asks the folder's host (#5790): with no control plane (`hosts` nil),
    /// yes; with one, only once its list has the host and the host says `work.suggest-leads/1`
    /// (or says no list). A list that has not come in yet is a no, and the view asks again when it does.
    public static func canAsk(_ host: MachineRef, in hosts: [DaemonAPI.ControlMachine]?) -> Bool {
        guard let hosts else { return true }
        return hosts.first { $0.id == host }?.has(Capability.workSuggestLeads1) ?? false
    }

    /// The one line for a read that failed (#5790), or nil when there is nothing to say: the
    /// read was cancelled, or the host does not know the method and so draws no section.
    public static func failure(_ error: Error) -> String? {
        if error is CancellationError { return nil }
        if let rpc = error as? JSONRPCError {
            if rpc.isMethodNotFound || rpc.isCancelled { return nil }
            return "Could not read: \(rpc.message)"
        }
        return "Could not read: \(error.localizedDescription)"
    }

    /// The one line for an answer with no rows because a forge could not be read (#5790): the
    /// first forge's words, or nil when it has rows or every forge was read.
    public static func unread(_ answer: DaemonAPI.WorkSuggestLeads) -> String? {
        guard answer.suggestions.isEmpty, let first = answer.errors?.first else { return nil }
        return "Could not read: \(first.message)"
    }

    /// "1 more in Work", "4 more in Work"; nil with none more.
    public static func more(_ n: Int) -> String? {
        n > 0 ? "\(n) more in Work" : nil
    }

    /// The issue number of an epic on a repo ("owner/name#4487"), or nil.
    public static func number(_ s: DaemonAPI.WorkLeadSuggestion) -> Int? {
        if let label = s.start.labels.first(where: { $0.hasPrefix("#") }), let n = Int(label.dropFirst()) { return n }
        guard let hash = s.node.ref.lastIndex(of: "#") else { return nil }
        return Int(s.node.ref[s.node.ref.index(after: hash)...])
    }

    /// "2026-12-04" → "12-04"; nil for a date in no form it knows.
    static func shortDue(_ due: String) -> String? {
        let parts = due.split(separator: "-")
        guard parts.count == 3, parts.allSatisfy({ Int($0) != nil }) else { return nil }
        return "\(parts[1])-\(parts[2])"
    }
}
