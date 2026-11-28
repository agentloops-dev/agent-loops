import Foundation

/// A session under a lead (#7539, spec 7535): the checks a client makes from its cache before
/// it calls `agents/setParent`, the leads the pickers list, and the words, the same on each
/// client (the wireframes' part 7). The daemon makes the same checks again and is the one that
/// is true (`Go/internal/agentsd/parents.go`).
public enum SessionParents {
    /// The deepest a tree goes: 6 levels.
    public static let levelsMost = 6
    /// The most live sessions under one lead.
    public static let childrenMost = 100
    /// The label a parent has.
    public static let leadLabel = "lead"

    /// A live session with the label `lead`.
    public static func isLead(_ agent: Agent) -> Bool {
        agent.state != .archived && agent.labels.contains { $0.value.caseInsensitiveCompare(leadLabel) == .orderedSame }
    }

    /// The session this one is under in the tree: its parent when one is set (none when the
    /// parent says the top), else its starter.
    public static func effectiveParent(of agent: Agent) -> UUID? {
        if let parent = agent.parent { return parent.id }
        return agent.startedByAgent
    }

    /// Why a move is refused before it is sent, with the words a client shows.
    public enum Refusal: Equatable, Sendable {
        /// Onto itself.
        case itself
        /// Onto a lead in its own subtree: `lead` is under `session`.
        case cycle(lead: String, session: String)
        /// The move makes `levels` levels.
        case tooDeep(levels: Int)
        /// The lead has `childrenMost` live sessions under it.
        case full(lead: String)
        /// Not a live lead.
        case notLead(String)

        public var words: String {
            switch self {
            case .itself: "A session cannot go under itself."
            case .cycle(let lead, let session):
                "\(lead) is under \(session). A session cannot go under its own subtree."
            case .tooDeep(let levels):
                "Too deep. Sessions can go \(SessionParents.levelsMost) levels down; this move makes \(levels)."
            case .full(let lead): "\(lead) has \(SessionParents.childrenMost) sessions under it, the most it can have."
            case .notLead(let target): "\(target) is not a lead."
            }
        }

        /// The short reason after a dimmed lead in a list.
        public var short: String {
            switch self {
            case .itself: "This session"
            case .cycle: "Under this session"
            case .tooDeep: "Too deep: \(SessionParents.levelsMost) levels"
            case .full: "Full: \(SessionParents.childrenMost) sessions under it"
            case .notLead: "Not a lead"
            }
        }
    }

    /// The held sessions of one host, each under its effective parent.
    public struct Index: Sendable {
        public let byID: [UUID: Agent]
        private let under: [UUID: [UUID]]

        public init(_ agents: [Agent]) {
            byID = Dictionary(agents.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
            var under: [UUID: [UUID]] = [:]
            for agent in agents {
                if let parent = SessionParents.effectiveParent(of: agent) { under[parent, default: []].append(agent.id) }
            }
            self.under = under
        }

        public func parent(of id: UUID) -> UUID? { byID[id].flatMap(SessionParents.effectiveParent) }

        /// The sessions under this one, at every level, any state; each once.
        public func descendants(of id: UUID) -> [Agent] {
            var found: [Agent] = []
            var seen: Set<UUID> = [id]
            var next = under[id] ?? []
            while let child = next.popLast() {
                guard seen.insert(child).inserted else { continue }
                if let agent = byID[child] { found.append(agent) }
                next.append(contentsOf: under[child] ?? [])
            }
            return found.sorted { $0.createdAt < $1.createdAt }
        }

        /// The live sessions right under this one.
        public func liveChildren(of id: UUID) -> Int {
            (under[id] ?? []).count { byID[$0].map { $0.state != .archived } ?? false }
        }

        /// The levels from this session up to the top, itself the first: 1 at the top.
        public func levels(of id: UUID) -> Int {
            var levels = 1
            var seen: Set<UUID> = [id]
            var next = parent(of: id)
            while let at = next, byID[at] != nil, seen.insert(at).inserted, levels <= SessionParents.levelsMost {
                levels += 1
                next = parent(of: at)
            }
            return levels
        }

        /// How many levels hang under this session: 0 with none.
        public func height(of id: UUID) -> Int {
            func height(_ id: UUID, _ seen: inout Set<UUID>, _ most: Int) -> Int {
                guard most > 0, seen.insert(id).inserted else { return 0 }
                return (under[id] ?? []).map { 1 + height($0, &seen, most - 1) }.max() ?? 0
            }
            var seen: Set<UUID> = []
            return height(id, &seen, SessionParents.levelsMost)
        }

        /// Whether `id` is `root` or under it.
        public func isInSubtree(_ id: UUID, of root: UUID) -> Bool {
            var seen: Set<UUID> = []
            var next: UUID? = id
            while let at = next, seen.insert(at).inserted {
                if at == root { return true }
                next = parent(of: at)
            }
            return false
        }

        /// Why `child` (nil for a session not started yet) cannot go under `lead`, or nil.
        public func refusal(moving child: Agent?, under lead: Agent) -> Refusal? {
            guard SessionParents.isLead(lead) else { return .notLead(SessionParents.title(lead)) }
            if let child {
                if child.id == lead.id { return .itself }
                if isInSubtree(lead.id, of: child.id) {
                    return .cycle(lead: SessionParents.title(lead), session: SessionParents.title(child))
                }
            }
            let levels = levels(of: lead.id) + 1 + (child.map { height(of: $0.id) } ?? 0)
            if levels > SessionParents.levelsMost { return .tooDeep(levels: levels) }
            var children = liveChildren(of: lead.id)
            if let child, parent(of: child.id) == lead.id, child.state != .archived { children -= 1 }
            if children >= SessionParents.childrenMost { return .full(lead: SessionParents.title(lead)) }
            return nil
        }
    }

    /// One lead in a picker: its title, its folder, how many are under it, and why it cannot
    /// be chosen, when it cannot.
    public struct Choice: Identifiable, Sendable {
        public let lead: Agent
        public let under: Int
        /// A lead is under it.
        public let isDirector: Bool
        public let refusal: Refusal?
        public var id: UUID { lead.id }
        public var title: String { SessionParents.title(lead) }

        /// "shop-demo · 3 under it · director", or the reason it is dimmed.
        public var description: String {
            var parts = [lead.folderPath.lastPathComponent]
            if let refusal { parts.append(refusal.short) } else {
                parts.append("\(under) under it")
                if isDirector { parts.append("director") }
            }
            return parts.joined(separator: " · ")
        }
    }

    /// The live leads of `agents` (one host's), for `child` (nil in New Session): those of
    /// `folder` first, then by title. The child itself is left out.
    public static func choices(for child: Agent?, folder: URL?, in agents: [Agent]) -> [Choice] {
        let index = Index(agents)
        let leads = agents.filter { isLead($0) && $0.id != child?.id }
        return leads.map { lead in
            let kids = index.descendants(of: lead.id).filter { index.parent(of: $0.id) == lead.id && $0.state != .archived }
            return Choice(lead: lead, under: kids.count, isDirector: kids.contains(where: isLead),
                          refusal: index.refusal(moving: child, under: lead))
        }
        .sorted { first, second in
            let a = first.lead.folderPath == folder, b = second.lead.folderPath == folder
            if a != b { return a }
            return first.title.localizedStandardCompare(second.title) == .orderedAscending
        }
    }

    /// A session's title as the words name it.
    public static func title(_ agent: Agent) -> String {
        let trimmed = agent.title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? "Untitled" : trimmed
    }

    // MARK: Words (the wireframes' part 7)

    public static let noLead = "No lead"
    public static let noLeadDescription = "Under the session that starts it, or at the top"
    public static let chipName = "Lead"
    public static let searchPrompt = "Search leads"
    public static let leadsHeading = "Leads on this Mac"
    public static let moveUnderLead = "Move Under Lead…"
    public static let moveToTop = "Move to Top"
    public static let underNoSession = "Under no session"
    public static let underItsStarter = "Under its starter"
    /// The search field shows past this many leads.
    public static let searchPast = 8

    /// The chip, and the said label after the start: "Under Billing page".
    public static func under(_ lead: String) -> String { "Under \(lead)" }
    public static func atTopOf(_ folder: String) -> String { "At the top of \(folder)" }

    /// The label on a drop target.
    public static func dropUnder(_ lead: String) -> String { "Move under \(lead)" }
    public static func dropTop(_ folder: String) -> String { "Move to the top of \(folder)" }
    public static func dropSubtree(_ moved: String, count: Int, under lead: String) -> String {
        "Move \(moved) and the \(count == 1 ? "session" : "\(count) sessions") under it under \(lead)"
    }

    /// The Move sheet's title and its line under it.
    public static func sheetTitle(_ session: String) -> String { "Move \(session)" }
    public static func sheetLine(lead: String?, starter: String?) -> String {
        let now = lead.map { "Now under \($0)." } ?? "Now under no lead."
        return [now, starter.map { "Started by \($0)." }].compactMap { $0 }.joined(separator: " ")
    }

    /// What is done, for the toast and VoiceOver.
    public static func moved(_ placement: SessionPlacement, lead: String?) -> String {
        switch placement {
        case .under: "Moved under \(lead ?? "its lead")"
        case .top: "Moved to the top"
        case .starter: "Moved under its starter"
        }
    }

    /// The daemon refused after the drop: "Could not move X. <why>".
    public static func couldNotMove(_ session: String, _ why: String) -> String {
        let why = why.trimmingCharacters(in: .whitespacesAndNewlines)
        return why.isEmpty ? "Could not move \(session)." : "Could not move \(session). \(why)"
    }

    /// VoiceOver on a row: ", under Billing page, level 3".
    public static func accessibilityPlace(lead: String?, level: Int) -> String {
        [lead.map { "under \($0)" }, "level \(level)"].compactMap { $0 }.joined(separator: ", ")
    }

    /// A row's help when its parent is not its starter.
    public static func help(lead: String?, starter: String?) -> String? {
        guard let lead else { return nil }
        return [under(lead), starter.map { "started by \($0)" }].compactMap { $0 }.joined(separator: " · ")
    }
}
