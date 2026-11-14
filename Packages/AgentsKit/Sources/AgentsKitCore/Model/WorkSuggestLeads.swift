import Foundation

// MARK: - The wire (#5458, #5459)

public extension DaemonAPI {
    /// `work/suggest-leads`: the epics of every folder on this machine, or of one folder's repo.
    struct WorkSuggestLeadsRequest: Codable, Sendable, Hashable {
        public var folder: String?

        public init(folder: String? = nil) { self.folder = folder }
    }

    /// New Session's Suggested section: at most 3 epics with no lead, ranked, and how many more there are.
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

    /// An open epic with no lead. `start` is the form Start lead fills; it starts nothing.
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

        public var id: String { "\(node.kind):\(node.ref)" }

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

    /// `work/dismiss-lead`: hide an epic from the suggestions until it changes or a lead of it has ended.
    struct WorkDismissLeadRequest: Codable, Sendable, Hashable {
        public var node: LineageNode

        public init(node: LineageNode) { self.node = node }
    }
}

public extension DaemonAPI.Method {
    /// `WorkSuggestLeadsRequest` → `WorkSuggestLeads` (#5458).
    static let workSuggestLeads = "work/suggest-leads"
    /// `WorkDismissLeadRequest` → `ForgeDone` (#5458).
    static let workDismissLead = "work/dismiss-lead"
}

public extension DaemonAPI.WorkLeadSuggestion {
    /// The words under the title: "3 of 7 done", the milestone and its due date.
    var detail: String {
        var parts: [String] = []
        if standing == true { parts.append("Standing") } else if total > 0 { parts.append("\(done) of \(total) done") }
        if let milestone { parts.append(due.map { "\(milestone), due \($0)" } ?? milestone) }
        return parts.joined(separator: " · ")
    }
}
