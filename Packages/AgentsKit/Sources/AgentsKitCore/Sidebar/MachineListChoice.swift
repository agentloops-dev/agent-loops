import Foundation

/// The Machines page's list (#5567, the Mac's half of the web's #5285, epic #5281): a search, filter
/// chips with counts, a sort and a table that loads by page from `machines/list` (#5283), then Start,
/// Stop and Delete on the selected rows. The web page's `Web/src/model/machineList.ts`, in Swift. A
/// control plane before `machines/2` answers the plain array, so the page filters and sorts it itself.
public struct MachineListChoice: Equatable, Sendable {
    public enum Filter: String, CaseIterable, Sendable {
        case state, provider, os, version, labels

        public var title: String {
            switch self {
            case .state: "State"
            case .provider: "Provider"
            case .os: "OS"
            case .version: "Version"
            case .labels: "Label"
            }
        }
    }

    public typealias Sort = DaemonAPI.MachineListQuery.Sort

    public static let pageSize = 50

    public var search = ""
    public var state: String?
    public var provider: String?
    public var os: String?
    public var version: String?
    /// A machine must have all of them.
    public var labels: [String] = []
    public var sort: Sort = .name

    public init(search: String = "", state: String? = nil, provider: String? = nil, os: String? = nil,
                version: String? = nil, labels: [String] = [], sort: Sort = .name) {
        self.search = search
        self.state = state
        self.provider = provider
        self.os = os
        self.version = version
        self.labels = labels
        self.sort = sort
    }

    private var words: String { search.trimmingCharacters(in: .whitespacesAndNewlines) }

    /// Whether a search or a filter narrows the list.
    public var narrowed: Bool {
        !words.isEmpty || state != nil || provider != nil || os != nil || version != nil || !labels.isEmpty
    }

    /// The same sort, with no search and no filter: Clear.
    public var cleared: MachineListChoice { MachineListChoice(sort: sort) }

    /// The query of one page; `cursor` is the previous page's `next`.
    public func query(cursor: String? = nil) -> DaemonAPI.MachineListQuery {
        DaemonAPI.MachineListQuery(query: words.isEmpty ? nil : words, state: state, labels: labels.isEmpty ? nil : labels,
                                   provider: provider, os: os, version: version, sort: sort,
                                   limit: Self.pageSize, cursor: cursor)
    }

    public func value(of filter: Filter) -> String? {
        switch filter {
        case .state: state
        case .provider: provider
        case .os: os
        case .version: version
        case .labels: nil
        }
    }

    public func isChosen(_ filter: Filter, _ value: String) -> Bool {
        filter == .labels ? labels.contains(value) : self.value(of: filter) == value
    }

    /// A chip's value picked, or let go when it already is the one.
    public func toggled(_ filter: Filter, _ value: String) -> MachineListChoice {
        var next = self
        let off = isChosen(filter, value)
        switch filter {
        case .state: next.state = off ? nil : value
        case .provider: next.provider = off ? nil : value
        case .os: next.os = off ? nil : value
        case .version: next.version = off ? nil : value
        case .labels: next.labels = off ? labels.filter { $0 != value } : labels + [value]
        }
        return next
    }

    public struct Chip: Equatable, Sendable, Identifiable {
        public var filter: Filter
        public var value: String
        public var count: Int
        public var on: Bool
        public var id: String { "\(filter.rawValue):\(value)" }
        /// What it says: `online 84`, or `# gpu 3` for a label.
        public var words: String { filter == .labels ? "# \(value) \(count)" : "\(value) \(count)" }
    }

    /// The chips of each filter the counts have values for (and the one chosen, even at 0), most machines first.
    public func chips(_ counts: DaemonAPI.MachineCounts?) -> [Chip] {
        guard let counts else { return [] }
        return Filter.allCases.flatMap { filter -> [Chip] in
            let seen = Self.counts(counts, filter)
            var values = Set(seen.keys)
            if filter == .labels { values.formUnion(labels) } else if let value = value(of: filter) { values.insert(value) }
            return values.map { Chip(filter: filter, value: $0, count: seen[$0] ?? 0, on: isChosen(filter, $0)) }
                .sorted { $0.count != $1.count ? $0.count > $1.count : $0.value.localizedStandardCompare($1.value) == .orderedAscending }
        }
    }

    private static func counts(_ counts: DaemonAPI.MachineCounts, _ filter: Filter) -> [String: Int] {
        switch filter {
        case .state: counts.state ?? [:]
        case .provider: counts.provider ?? [:]
        case .os: counts.os ?? [:]
        case .version: counts.version ?? [:]
        case .labels: counts.labels ?? [:]
        }
    }

    /// The counts of a plain array, when the control plane has no pages: the shape `machines/list` would say.
    public static func counts(of all: [DaemonAPI.ControlMachine]) -> DaemonAPI.MachineCounts {
        var counts = DaemonAPI.MachineCounts(state: [:], labels: [:], provider: [:], os: [:], version: [:])
        for machine in all {
            counts.state?[machine.state, default: 0] += 1
            if let provider = machine.workspace?.providerName { counts.provider?[provider, default: 0] += 1 }
            counts.os?[machine.platform, default: 0] += 1
            counts.version?[machine.version, default: 0] += 1
        }
        return counts
    }

    /// A plain array filtered and sorted as `machines/list` would, for a control plane before `machines/2`.
    public func filtered(_ all: [DaemonAPI.ControlMachine]) -> [DaemonAPI.ControlMachine] {
        let words = words.lowercased()
        let rows = all.filter { machine in
            (words.isEmpty || machine.name.lowercased().contains(words) || machine.id.rawValue.lowercased().contains(words))
                && (state == nil || machine.state == state) && (provider == nil || machine.workspace?.providerName == provider)
                && (os == nil || machine.platform == os) && (version == nil || machine.version == version)
        }
        func key(_ machine: DaemonAPI.ControlMachine) -> String {
            switch sort {
            case .state: machine.state
            case .version: machine.version
            case .name, .lastSeen: machine.name
            }
        }
        return rows.sorted {
            let order = key($0).localizedStandardCompare(key($1))
            return order != .orderedSame ? order == .orderedAscending
                : $0.name.localizedStandardCompare($1.name) == .orderedAscending
        }
    }

    /// The sort's name in its menu.
    public static func title(_ sort: Sort) -> String {
        switch sort {
        case .name: "Name"
        case .state: "State"
        case .lastSeen: "Last seen"
        case .version: "Version"
        }
    }

    /// A machine's state as a row says it: "Needs an update" for `needsUpdate`.
    public static func stateWords(_ machine: DaemonAPI.ControlMachine) -> String {
        if machine.state == "needsUpdate" { return "Needs an update" }
        // A delete's backup, before its workspace goes (#1815).
        if machine.state == "stopping", machine.workspace?.phase == "backing up" { return "Backing up to delete" }
        return machine.state.prefix(1).uppercased() + machine.state.dropFirst()
    }

    /// "Agent Loops 0.9 (Go)" for a machine whose host is written in Go (#1134).
    public static func versionWords(_ machine: DaemonAPI.ControlMachine) -> String {
        machine.implementation == "go" ? "Agent Loops \(machine.version) (Go)" : "Agent Loops \(machine.version)"
    }

    public static func emptyWords(narrowed: Bool) -> String {
        narrowed ? "No machine matches. Clear the search or a filter." : "No machines yet. Add one under Machines in the sidebar."
    }

    /// "3 of 120 machines", or "1 machine" when all are shown.
    public static func countWords(shown: Int, total: Int) -> String {
        shown < total ? "\(shown) of \(total) machines" : "\(total) \(total == 1 ? "machine" : "machines")"
    }

    public static func showMoreWords(shown: Int, total: Int) -> String { "Show More (\(shown) of \(total))" }
}

/// Which of the selected machines each action reaches (#5567, the web's `bulkActions`).
public struct MachineBulkActions: Equatable, Sendable {
    public var start: [DaemonAPI.ControlMachine]
    public var stop: [DaemonAPI.ControlMachine]
    public var delete: [DaemonAPI.ControlMachine]

    public init(_ selected: [DaemonAPI.ControlMachine]) {
        func of(_ pick: KeyPath<MachineRowActions, Bool>) -> [DaemonAPI.ControlMachine] {
            selected.filter { MachineRowActions.of($0, isThisMac: $0.id == .mac)[keyPath: pick] }
        }
        start = of(\.start)
        stop = of(\.stop)
        delete = of(\.delete)
    }

    public static func selectedWords(_ count: Int) -> String { "\(count) selected" }

    /// An action's button: `Stop 3` says how many of the selected ones it reaches; it is off when none.
    public static func label(_ verb: String, reached: Int, selected: Int) -> String {
        reached == selected || reached == 0 ? verb : "\(verb) \(reached)"
    }

    /// Delete's question for many: it always asks, and names the unpushed work of each machine that has some.
    public static func deleteQuestion(_ names: [String], unpushed: [String]) -> String {
        let list = names.count <= 5 ? names.joined(separator: ", ")
            : "\(names.prefix(5).joined(separator: ", ")) and \(names.count - 5) more"
        let work = unpushed.isEmpty ? ""
            : " Work not pushed on \(unpushed.count == 1 ? "it" : "them"): \(unpushed.joined(separator: "; "))."
        let what = names.count == 1 ? list : "\(names.count) machines (\(list))"
        return "Delete \(what)? Their providers delete them and the sessions on them stop.\(work)"
    }

    /// The question's title, which a confirmation dialog shows over the message.
    public static func deleteTitle(_ count: Int) -> String {
        count == 1 ? "Delete 1 machine?" : "Delete \(count) machines?"
    }

    /// The unpushed work of each machine, as `deleteQuestion` names it: "devbox: api" for each worktree.
    public static func unpushed(_ previews: [(name: String, preview: DaemonAPI.StartedMachinePreview?)]) -> [String] {
        previews.flatMap { name, preview -> [String] in
            if let unknown = preview?.unknown { return ["\(name): \(unknown)"] }
            return (preview?.unpushed ?? []).map { "\(name): \($0.folder.lastPathComponent)" }
        }
    }
}
