import Foundation

/// The Dashboard (#5511, PoC #5534): one place to see what is being worked on, on this Mac. A row
/// per work item (an issue, else a pull request, else the session itself), with its sessions
/// under it, its pull request and checks, and its epic; epic cards with their progress on top.
/// No releases and no cost (Alex, 2026-11-14). Made from what the window already has: the
/// sessions, and the forge's open issues, pull requests and epics.
public enum Dashboard {
    /// A session's or a row's mark. A row shows its worst session's: needs you, then failed, then
    /// working, then idle, then done (#5512).
    public enum Mark: Int, Comparable, Sendable, Hashable, CaseIterable {
        case done, idle, working, failed, needsYou

        public static func < (a: Mark, b: Mark) -> Bool { a.rawValue < b.rawValue }

        /// The mark as the mocks draw it, for a screen reader and the tests.
        public var words: String {
            switch self {
            case .needsYou: "Needs you"
            case .failed: "Failed"
            case .working: "Working"
            case .idle: "Idle"
            case .done: "Done"
            }
        }

        /// A session's mark from its group: what the sidebar says of it, in the Dashboard's five.
        public static func of(_ agent: Agent, group: AgentGroup) -> Mark {
            switch group {
            case .needsAttention, .blocked: return .needsYou
            case .running: return .working
            case .waiting: return .idle
            case .stopped, .finished:
                if let reason = agent.endedReason, !reason.isFinish, reason != .cancelled { return .failed }
                return group == .stopped ? .idle : .done
            case .archived: return .done
            }
        }
    }

    /// The section a row is listed under.
    public enum Section: String, CaseIterable, Sendable, Hashable, Identifiable {
        case needsYou, working, finished

        public var id: Self { self }

        public var title: String {
            switch self {
            case .needsYou: "Needs you"
            case .working: "Working"
            case .finished: "Finished today"
            }
        }
    }

    /// One session under a row.
    public struct Session: Sendable, Hashable, Identifiable {
        public var agent: Agent
        public var mark: Mark
        public var id: UUID { agent.id }

        public init(agent: Agent, mark: Mark) {
            self.agent = agent
            self.mark = mark
        }
    }

    /// A pull request on a row: its number, state (open, merged or closed), checks (passing,
    /// failing, running or none), review (approved, changes_requested or review_required) and
    /// labels, as the forge says them.
    public struct PullRequest: Sendable, Hashable {
        public var node: DaemonAPI.LineageNode?
        public var number: Int
        public var state: String?
        public var checks: String?
        public var draft: Bool
        public var review: String?
        public var labels: [String]

        public init(node: DaemonAPI.LineageNode?, number: Int, state: String? = nil, checks: String? = nil, draft: Bool = false,
                    review: String? = nil, labels: [String] = []) {
            self.node = node
            self.number = number
            self.state = state
            self.checks = checks
            self.draft = draft
            self.review = review
            self.labels = labels
        }

        /// The label a pull request waits for a person's approval under (#2811): this repo's, and
        /// any folder's that uses the same word.
        public static let approvalLabel = "needs-approval"

        /// It waits for the person to approve it (#5534): open, ready for review, not approved yet,
        /// and labelled `needs-approval` or with a review required by the forge. Only a pull request
        /// the forge listed can, so a row with labels alone never asks.
        public var waitsForApproval: Bool {
            guard node != nil, state == "open", !draft, review != "approved" else { return false }
            return labels.contains(Self.approvalLabel) || review == "review_required"
        }
    }

    /// What a row is about.
    public enum Subject: Sendable, Hashable {
        case issue(number: Int)
        case pullRequest(number: Int)
        /// A session with no issue or pull request: the row is the session's own.
        case session
    }

    /// A row: a work item and its sessions.
    public struct Item: Sendable, Hashable, Identifiable {
        public var id: String
        public var subject: Subject
        /// The issue's or the pull request's lineage node, where the forge listed it: what its Work page opens.
        public var node: DaemonAPI.LineageNode?
        public var title: String
        public var epic: DaemonAPI.LineageNode?
        public var pullRequest: PullRequest?
        public var sessions: [Session]
        public var mark: Mark
        public var lastActivity: Date
        /// The folder of its first session, for a row with no issue.
        public var folder: URL

        /// "#5534" or "PR #5560"; nil for a session's own row.
        public var refText: String? {
            switch subject {
            case .issue(let n): "#\(n)"
            case .pullRequest(let n): "PR #\(n)"
            case .session: nil
            }
        }
    }

    /// An epic card: its title and progress, and the rows on the board that are its children.
    public struct Epic: Sendable, Hashable, Identifiable {
        public var node: DaemonAPI.LineageNode
        public var title: String
        public var progress: DaemonAPI.ForgeProgress?
        public var items: Int
        public var mark: Mark
        public var id: String { node.ref }

        public var number: Int { WorkList.number(node.ref) }
    }

    public struct Board: Sendable, Hashable {
        public var epics: [Epic]
        public var sections: [(section: Section, items: [Item])] {
            Section.allCases.compactMap { s in
                let items = rows[s] ?? []
                return items.isEmpty ? nil : (s, items)
            }
        }
        public var rows: [Section: [Item]]

        public func count(_ section: Section) -> Int { rows[section]?.count ?? 0 }
        public var isEmpty: Bool { rows.values.allSatisfy(\.isEmpty) }
    }

    /// The board. `agents` are this Mac's sessions; `issues`, `pulls` and `epics` the forge's
    /// open ones as read (empty when not read yet: rows then show their labels alone). A
    /// session's first `#n` label names its issue, its first `P#n` its pull request; one with
    /// neither is a row of its own. Archived sessions count only when archived today, so a row
    /// whose work ended today is under Finished today; older done rows are left out.
    ///
    /// `asks` says a session has a card for the person that its state does not show, such as a
    /// change to the folder's own files it made (#531): it needs the person all the same.
    public static func board(agents: [Agent], group: (Agent) -> AgentGroup, asks: (Agent) -> Bool = { _ in false },
                             issues: [DaemonAPI.ForgeItem] = [], pulls: [DaemonAPI.ForgeItem] = [],
                             epics: [DaemonAPI.ForgeItem] = [],
                             now: Date = Date(), calendar: Calendar = .current) -> Board {
        let folders = repos(of: issues + pulls + epics)
        let issueBy = index(issues)
        let pullBy = index(pulls)
        var items: [String: Item] = [:]
        var order: [String] = []

        for agent in agents {
            let g = group(agent)
            if g == .archived {
                guard let at = agent.archivedAt, calendar.isDate(at, inSameDayAs: now) else { continue }
            }
            // The daily summary has its own page; it is no work item.
            if agent.writesDailySummary { continue }
            let session = Session(agent: agent, mark: g != .archived && asks(agent) ? .needsYou : Mark.of(agent, group: g))
            let repo = folders[path(agent.folderPath)]
            let issueN = agent.labels.lazy.filter { $0.kind == .issue }.compactMap { number($0.value) }.first
            let pullN = agent.labels.lazy.filter { $0.kind == .pullRequest }.compactMap { number($0.value) }.first
            let id: String
            var item: Item
            if let n = issueN {
                id = "\(repo ?? path(agent.folderPath))#\(n)"
                let forge = repo.flatMap { issueBy["\($0)#\(n)"] }
                item = items[id] ?? Item(id: id, subject: .issue(number: n), node: forge?.node,
                                         title: forge?.title ?? "Issue #\(n)", epic: forge?.parent, pullRequest: nil,
                                         sessions: [], mark: .done, lastActivity: .distantPast, folder: agent.folderPath)
            } else if let n = pullN {
                id = "\(repo ?? path(agent.folderPath))!\(n)"
                let forge = repo.flatMap { pullBy["\($0)#\(n)"] }
                item = items[id] ?? Item(id: id, subject: .pullRequest(number: n), node: forge?.node,
                                         title: forge?.title ?? "Pull request #\(n)", epic: nil, pullRequest: nil,
                                         sessions: [], mark: .done, lastActivity: .distantPast, folder: agent.folderPath)
            } else {
                id = "session:\(agent.id.uuidString)"
                item = Item(id: id, subject: .session, node: nil, title: sessionTitle(agent), epic: nil, pullRequest: nil,
                            sessions: [], mark: .done, lastActivity: .distantPast, folder: agent.folderPath)
            }
            if item.pullRequest == nil, let n = pullN {
                let forge = repo.flatMap { pullBy["\($0)#\(n)"] }
                item.pullRequest = PullRequest(node: forge?.node, number: n, state: forge?.state, checks: forge?.checks,
                                               draft: forge?.draft ?? false, review: forge?.review,
                                               labels: forge?.labels.map(\.name) ?? [])
            }
            item.sessions.append(session)
            item.lastActivity = max(item.lastActivity, agent.lastActivityAt)
            if items[id] == nil { order.append(id) }
            items[id] = item
        }

        var rows: [Section: [Item]] = [:]
        for id in order {
            guard var item = items[id] else { continue }
            item.sessions.sort { ($0.mark, $0.agent.lastActivityAt) > ($1.mark, $1.agent.lastActivityAt) }
            item.mark = item.sessions.map(\.mark).max() ?? .done
            // A pull request that waits for the person's approval needs them, whatever its sessions do.
            if item.pullRequest?.waitsForApproval == true { item.mark = .needsYou }
            let section: Section
            switch item.mark {
            case .needsYou: section = .needsYou
            case .working, .failed, .idle: section = .working
            case .done:
                guard calendar.isDate(item.lastActivity, inSameDayAs: now) else { continue }
                section = .finished
            }
            rows[section, default: []].append(item)
        }
        for key in rows.keys {
            rows[key]?.sort { ($0.mark, $0.lastActivity) > ($1.mark, $1.lastActivity) }
        }

        // An epic card for each epic that a row on the board is a child of, the most urgent first.
        let epicBy = Dictionary(epics.map { ($0.node.ref, $0) }, uniquingKeysWith: { a, _ in a })
        var cards: [String: Epic] = [:]
        for item in rows.values.joined() {
            guard let node = item.epic else { continue }
            let forge = epicBy[node.ref]
            var card = cards[node.ref] ?? Epic(node: node, title: forge?.title ?? "Epic \(WorkList.shortRef(node.ref))",
                                               progress: forge?.progress, items: 0, mark: .done)
            card.items += 1
            card.mark = max(card.mark, item.mark)
            cards[node.ref] = card
        }
        let epicCards = cards.values.sorted { ($0.mark, $0.items, $1.number) > ($1.mark, $1.items, $0.number) }
        return Board(epics: epicCards, rows: rows)
    }

    /// A session's own title, or what started it.
    static func sessionTitle(_ agent: Agent) -> String {
        let title = agent.title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return title.isEmpty ? "Untitled session" : title
    }

    /// "#12" or "P#12" → 12.
    static func number(_ label: String) -> Int? {
        guard let hash = label.lastIndex(of: "#") else { return nil }
        return Int(label[label.index(after: hash)...].trimmingCharacters(in: .whitespaces))
    }

    static func path(_ url: URL) -> String { Folder.standardize(url).path }

    /// Each folder's repo, from the items the forge listed with a folder.
    static func repos(of items: [DaemonAPI.ForgeItem]) -> [String: String] {
        var out: [String: String] = [:]
        for item in items {
            guard let folder = item.folder, !folder.isEmpty else { continue }
            let key = path(URL(fileURLWithPath: folder))
            if out[key] == nil { out[key] = WorkList.repo(of: item.node.ref) }
        }
        return out
    }

    /// Items by "repo#number", whichever mark the forge puts before the number.
    static func index(_ items: [DaemonAPI.ForgeItem]) -> [String: DaemonAPI.ForgeItem] {
        var out: [String: DaemonAPI.ForgeItem] = [:]
        for item in items {
            let key = "\(WorkList.repo(of: item.node.ref))#\(WorkList.number(item.node.ref))"
            if out[key] == nil { out[key] = item }
        }
        return out
    }
}
