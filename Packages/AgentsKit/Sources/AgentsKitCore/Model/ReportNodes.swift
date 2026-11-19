import Foundation

/// `reports/nodes` (#3118): the lineage nodes of one kind in a scope (issues, PRs or
/// releases), with their cost, sessions and lead time, filtered, sorted and in pages of at
/// most 100. The data behind Work ▸ Issues, Pull requests and Releases (#3100).
public extension DaemonAPI {
    /// How `reports/nodes` sorts: the last change first (the default), the most cost or waste
    /// first, or the longest lead time first (nodes with none last).
    enum ReportNodesSort: String, Codable, Sendable, Hashable, CaseIterable {
        case newest, cost, waste, leadTime
    }

    /// `reports/nodes`' question. `kind` is issue, pr or release. `state` is open,
    /// closed or merged; `repo` a ref prefix (`github:alexec/Agents`); `since` is on the node's
    /// last change. `limit` is 1 to 100, 50 when nil; `cursor` is the last page's.
    struct ReportNodesQuery: Codable, Sendable, Hashable {
        public var kind: String
        public var state: String?
        public var repo: String?
        public var label: String?
        public var since: Date?
        public var sort: ReportNodesSort?
        public var cursor: String?
        public var limit: Int?
        /// Me, a group or the organisation (#1804), as `reports/value`'s.
        public var scope: ReportScope?

        public init(kind: String, state: String? = nil, repo: String? = nil, label: String? = nil, since: Date? = nil,
                    sort: ReportNodesSort? = nil, cursor: String? = nil, limit: Int? = nil, scope: ReportScope? = nil) {
            self.kind = kind
            self.state = state
            self.repo = repo
            self.label = label
            self.since = since
            self.sort = sort
            self.cursor = cursor
            self.limit = limit
            self.scope = scope
        }
    }

    /// A `reports/nodes` row's cost: its own, with its parts, and its parts' waste.
    struct ReportNodeCost: Codable, Sendable, Hashable {
        public var own: LineageCost
        public var total: LineageCost
        public var waste: LineageCost

        public init(own: LineageCost = .init(), total: LineageCost = .init(), waste: LineageCost = .init()) {
            self.own = own
            self.total = total
            self.waste = waste
        }
    }

    /// One `reports/nodes` row: its cost rolled up from every machine in the scope, the
    /// sessions that worked on it and its parts, and the title, state and labels a machine sent.
    struct ReportNode: Codable, Sendable, Hashable, Identifiable {
        public var node: LineageNode
        public var repo: String?
        public var resolved: LineageResolved?
        public var cost: ReportNodeCost
        public var sessions: Int
        /// From the first work on it to its last PR merged; nil before a merge.
        public var leadTimeSeconds: Double?
        /// A PR's end: merged, closed or reverted; nil while open.
        public var outcome: String?
        public var lastAt: Date

        public var id: String { node.ref }

        public init(node: LineageNode, repo: String? = nil, resolved: LineageResolved? = nil,
                    cost: ReportNodeCost = .init(), sessions: Int = 0, leadTimeSeconds: Double? = nil,
                    outcome: String? = nil, lastAt: Date) {
            self.node = node
            self.repo = repo
            self.resolved = resolved
            self.cost = cost
            self.sessions = sessions
            self.leadTimeSeconds = leadTimeSeconds
            self.outcome = outcome
            self.lastAt = lastAt
        }
    }

    /// `reports/nodes`' answer: a page. `cursor` asks for the next; nil on the last. `unresolved`
    /// is how many nodes no machine sent a title, state or labels for.
    struct ReportNodes: Codable, Sendable, Hashable {
        public var nodes: [ReportNode]
        public var cursor: String?
        public var limited: Bool
        public var unresolved: Int
    }
}

public extension DaemonAPI {
    /// `reports/changes`' question (#6350): the rows of `kind` whose figures changed since
    /// `since`, the last answer's `cursor` (nil asks for every row). `kind`, `repo` and `scope`
    /// are as `ReportNodesQuery`'s. `pageCursor` is the last page's `nextPageCursor`; `limit`
    /// is 1 to 100, 100 when nil.
    struct ReportChangesQuery: Codable, Sendable, Hashable {
        public var kind: String
        public var repo: String?
        public var scope: ReportScope?
        public var since: String?
        public var pageCursor: String?
        public var limit: Int?

        public init(kind: String, repo: String? = nil, scope: ReportScope? = nil, since: String? = nil,
                    pageCursor: String? = nil, limit: Int? = nil) {
            self.kind = kind
            self.repo = repo
            self.scope = scope
            self.since = since
            self.pageCursor = pageCursor
            self.limit = limit
        }
    }

    /// `reports/changes`' answer: a page of rows by ref. `nextPageCursor` asks for the next
    /// page; `cursor` is on the last page only, to send as `since` next time. `reset` says the
    /// pages hold every row of the kind, so the cache replaces the figures it holds of it.
    struct ReportChanges: Codable, Sendable, Hashable {
        public var nodes: [ReportNode]
        public var nextPageCursor: String?
        public var cursor: String?
        public var reset: Bool

        public init(nodes: [ReportNode] = [], nextPageCursor: String? = nil, cursor: String? = nil, reset: Bool = false) {
            self.nodes = nodes
            self.nextPageCursor = nextPageCursor
            self.cursor = cursor
            self.reset = reset
        }
    }
}

public extension DaemonAPI.Method {
    /// `ReportNodesQuery` → `ReportNodes` (#3118).
    static let reportsNodes = "reports/nodes"
    /// `ReportChangesQuery` → `ReportChanges` (#6350).
    static let reportsChanges = "reports/changes"
}
