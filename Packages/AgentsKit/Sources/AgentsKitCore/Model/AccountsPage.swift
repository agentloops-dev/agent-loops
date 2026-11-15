import Foundation

/// The Accounts page (#5258, `specs/5254-secrets-accounts-environment/wireframes.md` sections 3 and 6): every outside
/// account of the person's in one list, with a search, chips, a Project and a Service menu, a sort, the **Needs you**
/// group on top and the total a month at the foot. It has the Secrets page's frame (`SecretsPage`). The window and the
/// Remote draw it from these; the web page's are the same, in `Web/src/model/accountsPage.ts`.
public enum AccountsPage {
    /// A chip. The Project and Service menus are not chips.
    public enum Chip: String, Sendable, Hashable, CaseIterable {
        case all, needsYou, asks

        public var title: String {
            switch self {
            case .all: "All"
            case .needsYou: "Needs you"
            case .asks: "Asks before use"
            }
        }
    }

    public enum Sort: String, Sendable, Hashable, CaseIterable {
        case service, lastUsed, cost

        public var title: String {
            switch self {
            case .service: "Service"
            case .lastUsed: "Last used"
            case .cost: "Cost"
            }
        }
    }

    /// One account, and what the page says of it. It holds no value.
    public struct Item: Sendable, Hashable, Identifiable {
        public var account: DaemonAPI.OutsideAccount
        /// Why it needs the person, or nil: its token expires in 30 days or less, or its secret is on one machine only.
        public var needs: String?

        public var id: String { account.id }

        public init(account: DaemonAPI.OutsideAccount, needs: String? = nil) {
            self.account = account
            self.needs = needs
        }

        /// When it was last used, for the sort.
        public var lastUsed: Date? { account.lastOp?.at }
    }

    /// A group of the list, in the order it shows. The rest after Needs you has no title.
    public struct Group: Sendable, Hashable, Identifiable {
        /// `needsYou` or `accounts`.
        public var id: String
        public var title: String?
        public var items: [Item]
    }

    // MARK: Words

    public static let title = "Accounts"
    public static let searchPrompt = "Search service, address, plan, secret…"
    public static let add = "Add Account…"
    public static let sortLabel = "Sort"
    public static let projectAny = "Any project"
    public static let serviceAny = "Any service"
    public static let selectOne = "Select an account to see it."
    public static let clearSearch = "Clear Search"
    public static let edit = "Edit"
    public static let remove = "Remove…"
    public static let openSecret = "Open in Secrets"
    public static let settingsLine = "Your accounts are in the sidebar, under Status."
    public static let openAccounts = "Open Accounts"
    public static let none = "No accounts yet. Agents add one when they make it, or add one here."
    public static let removeMessage = "Only the record goes. The account itself, and its secrets, stay."

    public static func noMatch(_ search: String) -> String { "No account matches “\(search)”." }

    /// The row's and the detail's title: "GitHub · alexec", the user name, else the address, after the service.
    public static func name(_ a: DaemonAPI.OutsideAccount) -> String {
        guard let who = text(a.username) ?? text(a.address) else { return a.service }
        return "\(a.service) · \(who)"
    }

    /// The row's cost: "$4/mo", "free" for 0, else none.
    public static func shortCost(_ a: DaemonAPI.OutsideAccount) -> String? {
        guard let usd = a.monthlyCostUsd else { return nil }
        return usd == 0 ? "free" : "\(AccountsWords.money(usd))/mo"
    }

    /// The line under the row's name: the warning for one in Needs you, else the description.
    public static func subtitle(_ item: Item) -> String {
        item.needs ?? AccountsWords.about(item.account)
    }

    /// "Team · $4 a month", or nil.
    public static func planLine(_ a: DaemonAPI.OutsideAccount) -> String? {
        let parts = [text(a.plan), a.monthlyCostUsd.map { "\(AccountsWords.money($0)) a month" }].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// The foot of the list: "23 accounts · $61 a month", or "4 of 23 · $16 a month" when a filter hides some.
    public static func foot(shown: [Item], of all: Int) -> String {
        let total = shown.compactMap(\.account.monthlyCostUsd).reduce(0, +)
        let count = shown.count == all ? "\(all) \(all == 1 ? "account" : "accounts")" : "\(shown.count) of \(all)"
        return "\(count) · \(AccountsWords.money(total)) a month"
    }

    // MARK: Items

    /// The items from the person's accounts and secrets. `machineNames` gives each machine's name by its id, for the
    /// not-backed-up line, which is left out when every secret is on one machine (the page says that once instead).
    public static func items(_ accounts: [DaemonAPI.OutsideAccount], secrets: [DaemonAPI.UserSecretRow],
                             machineNames: [String: String] = [:], now: Date = Date()) -> [Item] {
        let oneMachine = SecretInfoWords.onlyMachine(secrets) != nil
        return accounts.map { a in
            let soon = AccountsWords.expiresSoon(a, now: now)
            let kept = oneMachine ? nil : AccountsWords.notBackedUp(a, rows: secrets, names: machineNames)
            return Item(account: a, needs: soon ?? kept)
        }
    }

    // MARK: Search, chips, menus, sort

    /// Whether the search matches: the service, the address, the user name, the plan, the description, the projects and
    /// the names of the secret and the card. Never a value: an account holds none. Every word must match (AND).
    public static func matches(_ item: Item, search: String) -> Bool {
        let words = search.lowercased().split(whereSeparator: \.isWhitespace)
        guard !words.isEmpty else { return true }
        let a = item.account
        let fields = ([a.service, a.address, a.username, a.plan, a.description, a.secret, a.card].compactMap { $0 } + (a.projects ?? []))
            .map { $0.lowercased() }
        return words.allSatisfy { word in fields.contains { $0.contains(word) } }
    }

    public static func inChip(_ item: Item, _ chip: Chip) -> Bool {
        switch chip {
        case .all: true
        case .needsYou: item.needs != nil
        case .asks: AccountsWords.asks(item.account)
        }
    }

    /// An account that names no project is for every project, as agents read it (`AccountsWords.of`).
    public static func inProject(_ item: Item, _ project: String?) -> Bool {
        guard let project else { return true }
        let named = item.account.projects ?? []
        return named.isEmpty || named.contains { $0.caseInsensitiveCompare(project) == .orderedSame }
    }

    public static func inService(_ item: Item, _ service: String?) -> Bool {
        guard let service else { return true }
        return item.account.service.caseInsensitiveCompare(service) == .orderedSame
    }

    /// Every project an account names, for the menu.
    public static func projects(_ items: [Item]) -> [String] { AccountsWords.projects(items.map(\.account)) }

    /// Every service, once, for the menu.
    public static func services(_ items: [Item]) -> [String] {
        var seen = Set<String>()
        return items.map(\.account.service).filter { seen.insert($0.lowercased()).inserted }
            .sorted { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }
    }

    /// The items the search and the menus let through, before a chip.
    public static func found(_ items: [Item], search: String, project: String?, service: String?) -> [Item] {
        items.filter { matches($0, search: search) && inProject($0, project) && inService($0, service) }
    }

    /// Each chip's count, after the search and the menus (they add together).
    public static func counts(_ items: [Item], search: String, project: String? = nil, service: String? = nil) -> [Chip: Int] {
        let found = found(items, search: search, project: project, service: service)
        return Dictionary(uniqueKeysWithValues: Chip.allCases.map { chip in (chip, found.filter { inChip($0, chip) }.count) })
    }

    public static func sorted(_ items: [Item], by sort: Sort) -> [Item] {
        func byService(_ a: Item, _ b: Item) -> Bool {
            let order = a.account.service.localizedStandardCompare(b.account.service)
            if order != .orderedSame { return order == .orderedAscending }
            let x = name(a.account), y = name(b.account)
            return x == y ? a.id < b.id : x.localizedStandardCompare(y) == .orderedAscending
        }
        return items.sorted { a, b in
            switch sort {
            case .service:
                return byService(a, b)
            case .lastUsed:
                let x = a.lastUsed ?? .distantPast, y = b.lastUsed ?? .distantPast
                return x == y ? byService(a, b) : x > y
            case .cost:
                let x = a.account.monthlyCostUsd ?? -1, y = b.account.monthlyCostUsd ?? -1
                return x == y ? byService(a, b) : x > y
            }
        }
    }

    /// The list: Needs you on top whatever the sort, while it has items, then the rest. An item in Needs you is not in the
    /// rest as well.
    public static func groups(_ items: [Item], search: String, chip: Chip, project: String?, service: String?, sort: Sort) -> [Group] {
        let shown = sorted(found(items, search: search, project: project, service: service).filter { inChip($0, chip) }, by: sort)
        var groups: [Group] = []
        let needs = shown.filter { $0.needs != nil }
        if !needs.isEmpty { groups.append(Group(id: "needsYou", title: "Needs you", items: needs)) }
        let rest = shown.filter { $0.needs == nil }
        if !rest.isEmpty { groups.append(Group(id: "accounts", title: nil, items: rest)) }
        return groups
    }

    private static func text(_ s: String?) -> String? {
        let t = (s ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }
}
