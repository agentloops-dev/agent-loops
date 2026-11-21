import Foundation

/// The Dashboard (#5511, PoC #5534): one place to see what is being worked on, on this Mac. A row
/// per work item (an issue, else a pull request, else the session itself), with its sessions
/// under it, its pull request and checks, and its epic; epic cards with their progress on top.
/// No releases and no cost (Alex, 2026-11-14). Made from what the window already has: the
/// sessions, and the forge's open issues, pull requests and epics.
public enum Dashboard {
    /// A session's or a row's mark. A row shows its worst session's: needs you, then failed, then
    /// working, then waiting, queued and paused, then done (#5512). Waiting, queued and paused are
    /// the session list's own marks (#5763), so a state looks the same in both places.
    public enum Mark: Int, Comparable, Sendable, Hashable, CaseIterable {
        case done, paused, queued, waiting, working, failed, needsYou

        public static func < (a: Mark, b: Mark) -> Bool { a.rawValue < b.rawValue }

        /// The mark as the mocks draw it, for a screen reader and the tests.
        public var words: String {
            switch self {
            case .needsYou: "Needs you"
            case .failed: "Failed"
            case .working: "Working"
            case .waiting: "Waiting"
            case .queued: "Queued"
            case .paused: "Paused"
            case .done: "Done"
            }
        }

        /// A session's mark from its group: what the sidebar's mark says of it (`SessionMark` in
        /// `AgentRow.swift`): a queued helper waits for a free place, not on anything it asked for
        /// (#630), and a stopped session is paused. A session that ended badly has failed.
        public static func of(_ agent: Agent, group: AgentGroup) -> Mark {
            switch group {
            case .needsAttention, .blocked: return .needsYou
            case .running: return .working
            case .waiting: return agent.state == .queued ? .queued : .waiting
            case .stopped, .finished:
                if let reason = agent.endedReason, !reason.isFinish, reason != .cancelled { return .failed }
                return group == .stopped ? .paused : .done
            case .archived: return .done
            }
        }
    }

    /// The section a row is listed under. Finished work is not on the board (Alex, #5695). Working
    /// and Waiting are apart (Alex, #5698): a row is Working while one of its sessions runs a turn or
    /// a tool, and Waiting when none does and none asks the person (idle, paused on an event, failed,
    /// or done with a pull request that still needs something).
    public enum Section: String, CaseIterable, Sendable, Hashable, Identifiable {
        case needsYou, working, waiting

        public var id: Self { self }

        public var title: String {
            switch self {
            case .needsYou: "Needs you"
            case .working: "Working"
            case .waiting: "Waiting"
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

        /// It waits for the person to approve it (#5534): open, ready for review, with a review
        /// required by the forge, as a review requested from the person asks (#5803); no label
        /// says so. Only a pull request the forge listed can, so a row with labels alone never asks.
        public var waitsForApproval: Bool {
            node != nil && state == "open" && !draft && review == "review_required"
        }

        /// Its checks fail on an open pull request the forge listed.
        public var failsChecks: Bool { node != nil && state == "open" && checks == "failing" }
    }

    /// What a row is about.
    public enum Subject: Sendable, Hashable {
        case issue(number: Int)
        case pullRequest(number: Int)
        /// A session with no issue or pull request: the row is the session's own.
        case session
    }

    /// A row: a work item and the sessions on it that have not finished.
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
        /// When its first session started: the board's order, which does not move while its sessions
        /// work (#5695).
        public var startedAt: Date
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
    /// neither is a row of its own.
    ///
    /// Finished work is not on the board (Alex, #5695): a session that is done, paused or archived
    /// is left out, unless it failed or has a card for the person. A row whose sessions are all
    /// left out stays only while its pull request needs something: an approval, or failing checks.
    /// Rows and the sessions on them keep their order while they work: the oldest first, by when
    /// they started, never by their state or last activity.
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
            // An archived session can still name a pull request that needs the person, but only for the day.
            if g == .archived {
                guard let at = agent.archivedAt, calendar.isDate(at, inSameDayAs: now) else { continue }
            }
            let session = Session(agent: agent, mark: g != .archived && asks(agent) ? .needsYou : Mark.of(agent, group: g))
            let shows = isLive(g) || session.mark == .needsYou || session.mark == .failed
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
                                         sessions: [], mark: .done, lastActivity: .distantPast, startedAt: .distantFuture,
                            folder: agent.folderPath)
            } else if let n = pullN {
                id = "\(repo ?? path(agent.folderPath))!\(n)"
                let forge = repo.flatMap { pullBy["\($0)#\(n)"] }
                item = items[id] ?? Item(id: id, subject: .pullRequest(number: n), node: forge?.node,
                                         title: forge?.title ?? "Pull request #\(n)", epic: nil, pullRequest: nil,
                                         sessions: [], mark: .done, lastActivity: .distantPast, startedAt: .distantFuture,
                                         folder: agent.folderPath)
            } else {
                id = "session:\(agent.id.uuidString)"
                item = Item(id: id, subject: .session, node: nil, title: sessionTitle(agent), epic: nil, pullRequest: nil,
                            sessions: [], mark: .done, lastActivity: .distantPast, startedAt: .distantFuture,
                                         folder: agent.folderPath)
            }
            if item.pullRequest == nil, let n = pullN {
                let forge = repo.flatMap { pullBy["\($0)#\(n)"] }
                item.pullRequest = PullRequest(node: forge?.node, number: n, state: forge?.state, checks: forge?.checks,
                                               draft: forge?.draft ?? false, review: forge?.review,
                                               labels: forge?.labels.map(\.name) ?? [])
            }
            if shows { item.sessions.append(session) }
            item.lastActivity = max(item.lastActivity, agent.lastActivityAt)
            item.startedAt = min(item.startedAt, agent.createdAt)
            if items[id] == nil { order.append(id) }
            items[id] = item
        }

        var rows: [Section: [Item]] = [:]
        for id in order {
            guard var item = items[id] else { continue }
            item.sessions.sort { ($0.agent.createdAt, $0.id.uuidString) < ($1.agent.createdAt, $1.id.uuidString) }
            if let mark = item.sessions.map(\.mark).max() {
                item.mark = mark
            } else if item.pullRequest?.failsChecks == true {
                // Its sessions are done, but its pull request's checks fail: someone must look.
                item.mark = .failed
            } else if item.pullRequest?.waitsForApproval != true {
                continue
            }
            // A pull request that waits for the person's approval needs them, whatever its sessions do.
            if item.pullRequest?.waitsForApproval == true { item.mark = .needsYou }
            let section: Section = item.mark == .needsYou ? .needsYou
                : item.sessions.contains { $0.mark == .working } ? .working : .waiting
            rows[section, default: []].append(item)
        }
        for key in rows.keys {
            rows[key]?.sort(by: startsFirst)
        }

        // An epic card for each epic that a row on the board is a child of, the most urgent first.
        let epicBy = Dictionary(epics.map { ($0.node.ref, $0) }, uniquingKeysWith: { a, _ in a })
        var cards: [String: Epic] = [:]
        var cardOrder: [String] = []
        for item in rows.values.joined().sorted(by: startsFirst) {
            guard let node = item.epic else { continue }
            let forge = epicBy[node.ref]
            var card = cards[node.ref] ?? Epic(node: node, title: forge?.title ?? "Epic \(WorkList.shortRef(node.ref))",
                                               progress: forge?.progress, items: 0, mark: .done)
            card.items += 1
            card.mark = max(card.mark, item.mark)
            if cards[node.ref] == nil { cardOrder.append(node.ref) }
            cards[node.ref] = card
        }
        // In the order of their oldest row, so a card keeps its place as a row's state moves.
        return Board(epics: cardOrder.compactMap { cards[$0] }, rows: rows)
    }

    /// A session that has not finished: it works, waits on something, or needs the person.
    static func isLive(_ group: AgentGroup) -> Bool {
        switch group {
        case .needsAttention, .blocked, .waiting, .running: true
        case .finished, .stopped, .archived: false
        }
    }

    /// The board's order: the row that started first, then by id, so it never moves with an update.
    static func startsFirst(_ a: Item, _ b: Item) -> Bool { (a.startedAt, a.id) < (b.startedAt, b.id) }

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
