import Foundation

/// The sidebar's Machines group (#5286, epic #5281), on the Mac window and the Remote, as the
/// web page's `machinesGroup.ts`: at most seven rows, in this order: This Mac, the pinned
/// machines, the machines with something going on (it needs something, or sessions work or wait
/// there), the machines being made, then the rest. With more than seven, one row
/// **All Machines (N)** comes after them. A person with seven or fewer sees every one, and no
/// such row. The limit is always seven, also for 2,000 machines, so the rule is the same for all.
public enum MachinesGroup {
    /// The most rows the group draws before All Machines (N).
    public static let limit = 7

    /// One machine of the group, as the client knows it now.
    public struct Machine: Equatable, Sendable {
        public var host: MachineRef
        public var pinned: Bool
        /// It needs something (a problem, an update, a sign-in), it is starting, or sessions
        /// work or wait for the person there.
        public var active: Bool

        public init(host: MachineRef, pinned: Bool = false, active: Bool = false) {
            self.host = host
            self.pinned = pinned
            self.active = active
        }
    }

    /// What the group draws.
    public struct Rows: Equatable, Sendable {
        /// The machines' rows, in the order drawn.
        public var machines: [MachineRef]
        /// How many of the machines being made get a row, after the machines'.
        public var making: Int
        /// Every machine, with the ones being made: the N of All Machines (N).
        public var total: Int
        /// Whether the group stops short: All Machines (N) is drawn.
        public var bounded: Bool { machines.count + making < total }
    }

    /// Whether `rows` ranks the machines, so it reads each one's `active`: only past the limit.
    /// With seven or fewer, every machine is drawn in the client's order, and a client asks
    /// nothing about their sessions (P20261117-1).
    public static func ranks(_ machines: Int, making: Int, all: Bool = false) -> Bool {
        !all && machines + making > limit
    }

    /// The rows of the group. `machines` is in the order the client lists them (This Mac, then
    /// by name); `making` is how many machines New Machine is making or that never joined.
    /// `all` draws every one (the Remote, until its Machines page: #5568).
    public static func rows(_ machines: [Machine], making: Int, all: Bool = false) -> Rows {
        let total = machines.count + making
        guard ranks(machines.count, making: making, all: all) else {
            return Rows(machines: machines.map(\.host), making: making, total: total)
        }
        let rank = { (m: Machine) -> Int in
            if m.host == .mac { return 0 }
            if m.pinned { return 1 }
            return m.active ? 2 : 4
        }
        // A stable order: within a rank, the order the client gave.
        let ranked = machines.enumerated().sorted { (rank($0.element), $0.offset) < (rank($1.element), $1.offset) }.map(\.element)
        let first = ranked.filter { rank($0) < 4 }.prefix(limit)
        let madeRows = min(making, limit - first.count)
        let rest = ranked.filter { rank($0) == 4 }.prefix(limit - first.count - madeRows)
        return Rows(machines: (first + rest).map(\.host), making: madeRows, total: total)
    }

    /// The row's words: "All Machines (124)".
    public static func allTitle(_ total: Int) -> String { "All Machines (\(total))" }
    /// The words of the row that folds the group again, while it draws every machine.
    public static let fewerTitle = "Fewer Machines"
}

/// The machines a person pinned on this device (#5286, #5287): **Pin to Sidebar** in a machine's
/// menu. They come after This Mac in the Machines group and first in the machine picker. Kept in
/// defaults as one string, a machine on each line, so `@AppStorage` keeps a view in step, as the
/// web page keeps them in `localStorage` (`agents.machinePicker.pinned`).
///
/// Bounded (#6797): `most` pins of `idMost` bytes; one past that is refused, as a pin is the
/// person's choice. No age, on purpose: a pin stays until it is let go, or until the control
/// plane no longer lists its machine (`keep`).
public enum MachinePins {
    public static let key = "machinePicker.pinned"
    public static let most = 100
    /// A machine's id, as `ControlWire.isHostID` bounds it.
    static let idMost = 64

    /// The pinned machines in `stored`, the defaults' string.
    public static func pinned(_ stored: String) -> [MachineRef] {
        stored.split(separator: "\n").map { MachineRef(rawValue: String($0)) }
    }

    /// `stored` with the machine pinned, or let go when it is pinned. Nil when it is refused: there is no room.
    public static func toggled(_ host: MachineRef, in stored: String) -> String? {
        var now = pinned(stored)
        if let at = now.firstIndex(of: host) {
            now.remove(at: at)
        } else {
            guard now.count < most, host.rawValue.utf8.count <= idMost else { return nil }
            now.append(host)
        }
        return joined(now)
    }

    /// The pins kept in `store`. The picker once kept them as an array, under the same key.
    public static func read(_ store: UserDefaults) -> String {
        store.string(forKey: key) ?? (store.stringArray(forKey: key) ?? []).joined(separator: "\n")
    }

    /// The control plane listed its machines: the pin of one it does not list goes. An offline
    /// machine is listed, so its pin stays; so does this Mac's, which goes by a name of its own.
    public static func keep(_ store: UserDefaults, listed: some Sequence<MachineRef>) {
        let there = Set(listed).union([.mac])
        let left = joined(pinned(read(store)).filter(there.contains))
        guard store.object(forKey: key) != nil, store.string(forKey: key) != left else { return }
        put(left, in: store)
    }

    /// Keeps the pins, the defaults' string, which `toggled` and `keep` bound.
    static func put(_ stored: String, in store: UserDefaults) {
        // store-bounds: kind=key-value; count=100; age=until-deleted; action=refuse; record=64B; applied=on write, and when the machines are listed
        store.putEntry(stored, forKey: key, most: most * (idMost + 1))
    }

    private static func joined(_ pins: [MachineRef]) -> String { pins.map(\.rawValue).joined(separator: "\n") }

    /// Said when a pin is refused.
    public static let fullWords = "At most \(most) machines can be pinned. Unpin one first."

    /// The menu item's words.
    public static func menuTitle(pinned: Bool) -> String { pinned ? "Unpin from Sidebar" : "Pin to Sidebar" }
}
