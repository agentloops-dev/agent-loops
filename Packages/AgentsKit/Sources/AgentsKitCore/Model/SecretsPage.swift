import Foundation

/// The Secrets page (#5257, `specs/5254-secrets-accounts-environment/wireframes.md` sections 2 and 6):
/// every secret of the person's in one list (theirs, those for one session and those on one machine),
/// with a search, chips, a sort, and the **Needs you** group on top. The window and the Remote draw it
/// from these; the web page's are the same, in `Web/src/model/secretsPage.ts`.
public enum SecretsPage {
    /// Where a secret is kept: a chip and a group each.
    public enum Place: String, Sendable, Hashable, CaseIterable {
        case yours, session, machine
    }

    /// A chip. `all` and `needsYou` are not places.
    public enum Chip: String, Sendable, Hashable, CaseIterable {
        case all, yours, session, machine, needsYou

        public var title: String {
            switch self {
            case .all: "All"
            case .yours: "Yours"
            case .session: "For a session"
            case .machine: "On a machine"
            case .needsYou: "Needs you"
            }
        }
    }

    public enum Sort: String, Sendable, Hashable, CaseIterable {
        case lastUsed, name, added

        public var title: String {
            switch self {
            case .lastUsed: "Last used"
            case .name: "Name"
            case .added: "Added"
            }
        }
    }

    /// One secret, whatever its place. It holds no value.
    public struct Item: Sendable, Hashable, Identifiable {
        /// `yours/NAME`, `session/<agent id>/NAME` or `machine/NAME`: a name can be in more than one place.
        public var id: String
        public var name: String
        public var place: Place
        public var description: String?
        /// The value's last 4, or nil.
        public var hint: String?
        public var isCard: Bool
        public var asksBeforeUse: Bool
        public var added: Date?
        public var updated: Date?
        public var lastOp: DaemonAPI.SecretLastOp?
        /// For a secret for one session: its session.
        public var agentID: UUID?
        public var sessionTitle: String?
        /// The names of the machines it is on (Yours: those with a copy; On a machine: the one).
        public var machines: [String]
        /// The services of the accounts that name it (#3967), for the search and Used by.
        public var services: [String]
        /// The projects of those accounts: a secret has none of its own.
        public var projects: [String]
        /// Why it needs the person, or nil. The first that applies.
        public var needs: String?

        public init(id: String, name: String, place: Place, description: String? = nil, hint: String? = nil, isCard: Bool = false,
                    asksBeforeUse: Bool = false, added: Date? = nil, updated: Date? = nil, lastOp: DaemonAPI.SecretLastOp? = nil,
                    agentID: UUID? = nil, sessionTitle: String? = nil, machines: [String] = [], services: [String] = [],
                    projects: [String] = [], needs: String? = nil) {
            self.id = id
            self.name = name
            self.place = place
            self.description = description
            self.hint = hint
            self.isCard = isCard
            self.asksBeforeUse = asksBeforeUse
            self.added = added
            self.updated = updated
            self.lastOp = lastOp
            self.agentID = agentID
            self.sessionTitle = sessionTitle
            self.machines = machines
            self.services = services
            self.projects = projects
            self.needs = needs
        }

        /// When it was last read, for the sort and the row's age: a read or a denied use only.
        public var lastUsed: Date? {
            guard let lastOp, lastOp.what == "read" || lastOp.what == "denied" else { return nil }
            return lastOp.at
        }
    }

    /// A group of the list, in the order it shows.
    public struct Group: Sendable, Hashable, Identifiable {
        /// `needsYou` or a place's raw value.
        public var id: String
        public var title: String
        public var items: [Item]
    }

    /// Not used for this long: it goes in Needs you.
    public static let unusedDays = 90

    // MARK: Words

    public static let title = "Secrets"
    public static let searchPrompt = "Search names, descriptions, sessions, machines…"
    public static let add = "Add"
    public static let addSecret = "Add a Secret…"
    public static let addCard = "Add a Card…"
    public static let addForSession = "Add for a Session…"
    public static let sortLabel = "Sort"
    public static let projectAny = "Any project"
    public static let selectOne = "Select a secret to see it."
    public static let clearSearch = "Clear Search"
    public static let replaceValue = "Replace Value…"
    public static let edit = "Edit"
    public static let remove = "Remove…"
    public static let recentUse = "Recent use"
    public static let usedBy = "Used by"
    public static let noUse = "Not used yet."
    public static let editNote = "Ask before use and the description are sealed with the value, so type the value again to change them."
    public static let settingsLine = "Your secrets are in the sidebar, under Status."
    public static let openSecrets = "Open Secrets"
    public static let none = "No secrets yet. Agents ask you for one when they need it, or add one here."

    public static func noMatch(_ search: String) -> String { "No secret matches “\(search)”." }

    public static func groupTitle(_ place: Place) -> String {
        switch place {
        case .yours: "Yours"
        case .session: "For a session"
        case .machine: "On a machine"
        }
    }

    /// "Yours, on 3 machines", "For “Fix the sign-in”, on this Mac", "On devbox only".
    public static func whereLine(_ item: Item) -> String {
        switch item.place {
        case .yours:
            let count = item.machines.count
            let on = count == 0 ? "on no machine yet" : count == 1 ? "on 1 machine" : "on \(count) machines"
            return "Yours, \(on)"
        case .session:
            return "For “\(item.sessionTitle ?? "a session")”, on \(item.machines.first ?? "one machine") only"
        case .machine:
            return "On \(item.machines.first ?? "one machine") only"
        }
    }

    public static func kind(_ item: Item) -> String { item.isCard ? "Card" : "Secret" }

    /// The row's end: `••••ab12`, or `Card`.
    public static func rowHint(_ item: Item) -> String { item.isCard ? "Card" : SecretInfoWords.hint(item.hint) }

    /// The row's age: of the last use, else of the last change. "2 h", "12 d", "20 min".
    public static func shortAge(_ item: Item, now: Date = Date()) -> String? {
        guard let at = item.lastUsed ?? item.updated ?? item.added else { return nil }
        let seconds = max(0, now.timeIntervalSince(at))
        switch seconds {
        case ..<60: return "now"
        case ..<3600: return "\(Int(seconds / 60)) min"
        case ..<86_400: return "\(Int(seconds / 3600)) h"
        default: return "\(Int(seconds / 86_400)) d"
        }
    }

    /// The line under the row's name: the warning for one in Needs you, else the description, else
    /// its session and machine.
    public static func subtitle(_ item: Item) -> String {
        if let needs = item.needs { return needs }
        if let text = item.description?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty { return text }
        switch item.place {
        case .session: return [item.sessionTitle, item.machines.first].compactMap { $0 }.joined(separator: " · ")
        case .machine: return "Saved by an agent on \(item.machines.first ?? "one machine")"
        case .yours: return UserScopeWords.noDescription
        }
    }

    /// The age line of the detail: "Added 27 d ago · Replaced 4 d ago · Used 2 h ago".
    public static func ageLine(_ item: Item, now: Date = Date(), timeZone: TimeZone = .current) -> [SecretInfoWords.Part] {
        var parts: [SecretInfoWords.Part] = []
        if let set = SecretInfoWords.set(created: item.added, updated: item.updated, now: now, timeZone: timeZone) { parts.append(set) }
        parts.append(SecretInfoWords.used(item.lastOp, now: now, timeZone: timeZone))
        return parts
    }

    /// Recent use: the newest operation the machines keep (#4158), in words. "2 h ago: read by “Fix the
    /// shop sign-in”". Nil when there is none.
    public static func recentLine(_ item: Item, now: Date = Date(), agentTitle: (UUID) -> String? = { _ in nil }) -> String? {
        guard let last = item.lastOp else { return nil }
        let verb = switch last.what {
        case "read": "read"
        case "denied": "use denied"
        case "added": "added"
        case "replaced": "replaced"
        case "removed": "removed"
        default: last.what
        }
        let who = last.agent.flatMap(agentTitle).map { " by “\($0)”" } ?? (last.agent == nil && last.what != "read" ? " by you" : "")
        return "\(SecretInfoWords.age(last.at, now: now)): \(verb)\(who)"
    }

    // MARK: Items

    /// The items from what the client read: the person's secrets, the secrets for one session and those on one machine
    /// of the host it reaches (`hostName`), and the accounts, for Used by and the projects. `noCopy` names the person's
    /// secrets this client has no copy of yet (#5115), `didNotOpen` those whose copy did not open.
    public static func items(yours: [DaemonAPI.UserSecretRow], sessions: [SessionSecret] = [], local: [LocalSecret] = [],
                             accounts: [DaemonAPI.OutsideAccount] = [], machineNames: [String: String] = [:],
                             hostName: String = "this Mac", noCopy: Set<String> = [], didNotOpen: Set<String> = [],
                             clientName: String = "this Mac", now: Date = Date()) -> [Item] {
        func uses(_ name: String) -> [DaemonAPI.OutsideAccount] {
            accounts.filter { $0.secret == name || $0.card == name }
        }
        func projects(_ name: String) -> [String] {
            Array(Set(uses(name).flatMap { $0.projects ?? [] })).sorted()
        }
        let oneMachine = SecretInfoWords.onlyMachine(yours) != nil
        var items: [Item] = yours.map { row in
            let machines = row.machines.map { machineNames[$0] ?? $0 }
            var item = Item(id: "yours/\(row.name)", name: row.name, place: .yours, description: row.description, hint: row.hint,
                            isCard: row.isCard, asksBeforeUse: row.asksBeforeUse, added: SecretInfoWords.date(row.created),
                            updated: SecretInfoWords.date(row.updated), lastOp: row.lastOp, machines: machines,
                            services: uses(row.name).map(\.service), projects: projects(row.name))
            item.needs = needs(row, item: item, names: machineNames, oneMachine: oneMachine, noCopy: noCopy.contains(row.name),
                               didNotOpen: didNotOpen.contains(row.name), clientName: clientName, now: now)
            return item
        }
        items += sessions.map { secret in
            var item = Item(id: "session/\(secret.agentID.uuidString)/\(secret.name)", name: secret.name, place: .session,
                            description: secret.description, hint: secret.hint, asksBeforeUse: secret.asksBeforeUse,
                            added: secret.created ?? secret.setAt, updated: secret.setAt, lastOp: secret.lastOp,
                            agentID: secret.agentID, sessionTitle: secret.title, machines: [hostName],
                            services: uses(secret.name).map(\.service), projects: projects(secret.name))
            item.needs = unused(item, now: now)
            return item
        }
        items += local.map { secret in
            var item = Item(id: "machine/\(secret.name)", name: secret.name, place: .machine, description: secret.description,
                            asksBeforeUse: secret.asksBeforeUse, added: secret.setAt, updated: secret.setAt, lastOp: secret.lastOp,
                            machines: [hostName], services: uses(secret.name).map(\.service), projects: projects(secret.name))
            item.needs = unused(item, now: now)
            return item
        }
        return items
    }

    /// Why one of the person's secrets needs them: not backed up, no copy for this client yet, its copy did not open,
    /// a machine waits for a copy, it was on a removed machine, or it is not used in 90 days.
    static func needs(_ row: DaemonAPI.UserSecretRow, item: Item, names: [String: String], oneMachine: Bool, noCopy: Bool,
                      didNotOpen: Bool, clientName: String, now: Date) -> String? {
        if let left = row.leftOn, !left.isEmpty { return "It was on \(MachineNames.words(left.map(\.name))), which was removed." }
        if didNotOpen { return "This \(clientName == "this Mac" ? "Mac" : "browser")’s copy did not open." }
        if noCopy { return "No copy for \(clientName) yet." }
        if !oneMachine, row.backedUp == false, row.waiting?.isEmpty ?? true, let id = row.machines.first {
            return "Only on \(names[id] ?? "one machine"). Not backed up."
        }
        if let waiting = row.waiting, !waiting.isEmpty {
            return waiting.count == 1 ? "1 machine waits for a copy." : "\(waiting.count) machines wait for a copy."
        }
        return unused(item, now: now)
    }

    /// "Not used in 90 days." for one added more than 90 days ago and not read since.
    static func unused(_ item: Item, now: Date) -> String? {
        let limit = TimeInterval(unusedDays) * 86_400
        guard let since = item.lastUsed ?? item.updated ?? item.added, now.timeIntervalSince(since) > limit else { return nil }
        return "Not used in \(unusedDays) days."
    }

    // MARK: Search, chips, sort

    /// Whether the search matches: the name, the description, the session, the machines, the projects and the services of
    /// the accounts that use it. Never a value: the item has none. Every word must match (AND), in any field.
    public static func matches(_ item: Item, search: String) -> Bool {
        let words = search.lowercased().split(whereSeparator: \.isWhitespace)
        guard !words.isEmpty else { return true }
        let fields = ([item.name, item.description ?? "", item.sessionTitle ?? ""] + item.machines + item.projects + item.services)
            .map { $0.lowercased() }
        return words.allSatisfy { word in fields.contains { $0.contains(word) } }
    }

    public static func inChip(_ item: Item, _ chip: Chip) -> Bool {
        switch chip {
        case .all: true
        case .yours: item.place == .yours
        case .session: item.place == .session
        case .machine: item.place == .machine
        case .needsYou: item.needs != nil
        }
    }

    public static func inProject(_ item: Item, _ project: String?) -> Bool {
        guard let project else { return true }
        return item.projects.contains(project)
    }

    /// Every project an item has, for the menu.
    public static func projects(_ items: [Item]) -> [String] {
        Array(Set(items.flatMap(\.projects))).sorted { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }
    }

    /// Each chip's count, after the search and the project (a chip and the search add together).
    public static func counts(_ items: [Item], search: String, project: String? = nil) -> [Chip: Int] {
        let found = items.filter { matches($0, search: search) && inProject($0, project) }
        return Dictionary(uniqueKeysWithValues: Chip.allCases.map { chip in (chip, found.filter { inChip($0, chip) }.count) })
    }

    public static func sorted(_ items: [Item], by sort: Sort) -> [Item] {
        func byName(_ a: Item, _ b: Item) -> Bool {
            let order = a.name.localizedStandardCompare(b.name)
            return order == .orderedSame ? a.id < b.id : order == .orderedAscending
        }
        return items.sorted { a, b in
            switch sort {
            case .name:
                return byName(a, b)
            case .lastUsed:
                let x = a.lastUsed ?? .distantPast, y = b.lastUsed ?? .distantPast
                return x == y ? byName(a, b) : x > y
            case .added:
                let x = a.added ?? a.updated ?? .distantPast, y = b.added ?? b.updated ?? .distantPast
                return x == y ? byName(a, b) : x > y
            }
        }
    }

    /// The list: Needs you on top whatever the sort, while it has items, then a group per place. With a place's chip,
    /// only that place. An item in Needs you is not in its place's group as well.
    public static func groups(_ items: [Item], search: String, chip: Chip, project: String?, sort: Sort) -> [Group] {
        let found = sorted(items.filter { matches($0, search: search) && inChip($0, chip) && inProject($0, project) }, by: sort)
        var groups: [Group] = []
        let needs = found.filter { $0.needs != nil }
        if !needs.isEmpty { groups.append(Group(id: "needsYou", title: "Needs you", items: needs)) }
        for place in Place.allCases {
            let these = found.filter { $0.place == place && $0.needs == nil }
            if !these.isEmpty { groups.append(Group(id: place.rawValue, title: groupTitle(place), items: these)) }
        }
        return groups
    }
}
