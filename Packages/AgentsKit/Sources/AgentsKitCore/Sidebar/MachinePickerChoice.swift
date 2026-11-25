import Foundation

/// The one machine picker (#5621, the Mac's half of the web's #5287, epic #5281): a search, then the
/// pinned and the recently used machines, then the rest grouped by label, each with a state dot. It
/// asks `machines/list` (#5283) with the typed words, so it holds only a page, never every machine.
/// The web page's `Web/src/model/machinePicker.ts`, in Swift, so the Remote (#5622) can draw it too.
public enum MachinePickerChoice {
    public static let limit = 30
    public static let recentKey = "machinePicker.recent"
    static let recentCap = 5

    /// A row's state dot: green online, amber needing an update, grey stopped, red anything else that does not answer.
    public enum Dot: String, Sendable {
        case online, stopped, offline, attention
    }

    public static func dot(_ machine: DaemonAPI.ControlMachine) -> Dot {
        switch machine.state {
        case "needsUpdate": .attention
        case "stopped", "starting", "stopping": .stopped
        case "online": .online
        default: .offline
        }
    }

    public struct Section: Equatable, Sendable, Identifiable {
        public var title: String
        public var machines: [DaemonAPI.ControlMachine]
        public var id: String { title }
    }

    /// The query for the typed words: a page of machines whose name, id or label matches, by name.
    public static func query(_ search: String) -> DaemonAPI.MachineListQuery {
        let words = search.trimmingCharacters(in: .whitespacesAndNewlines)
        return DaemonAPI.MachineListQuery(query: words.isEmpty ? nil : words, sort: .name, limit: limit)
    }

    /// A control plane before `machines/2` answers every machine: the ones the words match, by name, as it would.
    public static func filtered(_ all: [DaemonAPI.ControlMachine], search: String) -> [DaemonAPI.ControlMachine] {
        let words = search.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return all.filter { machine in
            words.isEmpty || machine.name.lowercased().contains(words) || machine.id.rawValue.lowercased().contains(words)
                || (machine.labels ?? []).contains { $0.lowercased().contains(words) }
        }.sorted(by: byName)
    }

    /// A label's group: `key` for `key=value`, the label for a plain one.
    public static func group(of label: String) -> String {
        label.firstIndex(of: "=").map { String(label[..<$0]) } ?? label
    }

    private static func byName(_ a: DaemonAPI.ControlMachine, _ b: DaemonAPI.ControlMachine) -> Bool {
        a.name.localizedStandardCompare(b.name) == .orderedAscending
    }

    /// The sections of a page: Pinned, Recent (in the order last used), then one section for each label group by name, each
    /// machine under the first of its person-set labels (the provider's own ones group nothing), and last the ones with no
    /// label. A machine appears once, in the first section that takes it. `keep` says which machines may be picked.
    public static func sections(_ page: [DaemonAPI.ControlMachine], pinned: [String], recent: [String],
                                keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }) -> [Section] {
        let rows = page.filter(keep)
        var taken = Set<MachineRef>()
        func take(_ ids: [String], _ title: String) -> [Section] {
            let found = ids.compactMap { id in rows.first { $0.id.rawValue == id } }.filter { taken.insert($0.id).inserted }
            return found.isEmpty ? [] : [Section(title: title, machines: found)]
        }
        var out = take(pinned, "Pinned") + take(recent, "Recent")
        var groups: [String: [DaemonAPI.ControlMachine]] = [:]
        var none: [DaemonAPI.ControlMachine] = []
        for machine in rows.filter({ !taken.contains($0.id) }).sorted(by: byName) {
            let auto = Set(machine.autoLabels ?? [])
            guard let own = (machine.labels ?? []).first(where: { !auto.contains($0) }) else { none.append(machine); continue }
            groups[group(of: own), default: []].append(machine)
        }
        for key in groups.keys.sorted() { out.append(Section(title: key, machines: groups[key] ?? [])) }
        if !none.isEmpty { out.append(Section(title: out.isEmpty ? "Machines" : "Other machines", machines: none)) }
        return out
    }

    // MARK: Pinned and recent, kept in this app's defaults as the web page keeps them in localStorage

    /// The machines pinned in the sidebar too: `MachinePins` keeps both (#6797).
    public static func pinned(_ store: UserDefaults) -> [String] {
        MachinePins.pinned(MachinePins.read(store)).map(\.rawValue)
    }
    public static func recent(_ store: UserDefaults) -> [String] {
        Array((store.stringArray(forKey: recentKey) ?? []).filter { $0.utf8.count <= MachinePins.idMost }.prefix(recentCap))
    }

    /// A machine pinned, or let go when it is. Returns the new list; nil when the pin is refused.
    @discardableResult
    public static func togglePin(_ store: UserDefaults, _ id: String) -> [String]? {
        guard let next = MachinePins.toggled(MachineRef(rawValue: id), in: MachinePins.read(store)) else { return nil }
        MachinePins.put(next, in: store)
        return pinned(store)
    }

    /// The picker's pin button: the list it shows after, or what to say when the pin is refused.
    public static func togglePin(_ store: UserDefaults, _ id: MachineRef, pinned: inout [String], problem: inout String?) {
        guard let next = togglePin(store, id.rawValue) else { return problem = MachinePins.fullWords }
        pinned = next
        if problem == MachinePins.fullWords { problem = nil }
    }

    /// A machine picked goes first in Recent; the list keeps the last five.
    @discardableResult
    public static func remember(_ store: UserDefaults, _ id: String) -> [String] {
        guard id.utf8.count <= MachinePins.idMost else { return recent(store) }
        let next = Array(([id] + recent(store).filter { $0 != id }).prefix(recentCap))
        // store-bounds: kind=key-value; count=5; age=until-deleted; action=evict; record=64B; applied=on write
        store.putEntry(next, forKey: recentKey, most: recentCap * MachinePins.idMost)
        return next
    }

    // MARK: Words, the web page's `pickerWords`

    public static let searchWords = "Search machines by name or label"
    public static let noneWords = "No machine matches. Clear the search."
    public static func pinWords(_ name: String) -> String { "Pin \(name)" }
    public static func unpinWords(_ name: String) -> String { "Unpin \(name)" }
    public static func moreWords(shown: Int, total: Int) -> String { "Showing \(shown) of \(total): type to narrow" }
}
