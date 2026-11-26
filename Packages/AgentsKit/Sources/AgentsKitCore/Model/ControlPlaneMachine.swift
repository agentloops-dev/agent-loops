import Foundation

/// The control plane as a machine in Machines (#6591, epic #6593), for an admin. Its row is its
/// machine record (#6609): roles `["control"]`, or both roles on a machine that also runs agents.
/// Its level and figures come from `control/capacity` (#6590), from the copy that names that
/// machine record. The web page's model says the same in #6592.
public enum ControlPlaneMachine {
    /// What the row and the page call a machine that only runs the control plane.
    public static let title = "Control plane"

    /// What tells one control-only machine from the others (#7362): nil when it is the only one;
    /// else its name when no other control-only machine has the same name, else its id.
    public static func label(_ row: DaemonAPI.ControlMachine?, among rows: [DaemonAPI.ControlMachine]) -> String? {
        guard let row, controlOnly(row) else { return nil }
        let others = rows.filter { controlOnly($0) && $0.id != row.id }
        guard !others.isEmpty else { return nil }
        let name = row.name.trimmingCharacters(in: .whitespacesAndNewlines)
        if !name.isEmpty, name != title, !others.contains(where: { $0.name.trimmingCharacters(in: .whitespacesAndNewlines) == name }) {
            return name
        }
        return row.id.rawValue
    }

    /// The row's and the page's title: "Control plane" for the only control-only machine, else
    /// "Control plane · <label>" (#7362).
    public static func title(_ row: DaemonAPI.ControlMachine?, among rows: [DaemonAPI.ControlMachine]) -> String {
        label(row, among: rows).map { "\(title) \u{00B7} \($0)" } ?? title
    }

    /// How often the control plane looks at itself, and so how often its page reads again.
    public static let lookSeconds = 15

    /// The machine has the control role: it runs the control plane, or can take it (#6609). A team
    /// moves the control plane only to a control-only machine, which runs no copy until then.
    public static func controlRole(_ row: DaemonAPI.ControlMachine?) -> Bool {
        row?.roles?.contains("control") == true
    }

    /// The machine runs the control plane now: a copy names it (#6609, #7393), not its role. A copy
    /// from before #6591 names no machine: when it is the only copy, it runs on the only machine
    /// with the control role. Nil copies (not read yet) fall back to the role.
    public static func runsControl(_ row: DaemonAPI.ControlMachine?, copies: [DaemonAPI.ControlCapacityCopy]?,
                                   controlMachines: Int) -> Bool {
        guard let row else { return false }
        guard let copies else { return controlRole(row) }
        if copies.contains(where: { $0.machine == row.id.rawValue }) { return true }
        return controlRole(row) && controlMachines == 1 && copies.count == 1 && copies[0].machine == nil
    }

    /// The machine runs the control plane and no agents: nothing dials in with it, so it has no
    /// sessions, folders, start or stop.
    public static func controlOnly(_ row: DaemonAPI.ControlMachine?) -> Bool {
        guard let roles = row?.roles else { return false }
        return roles.contains("control") && !roles.contains("agents")
    }

    /// The control plane's machines first, then the others in the order they came (#6591).
    public static func first(_ rows: [DaemonAPI.ControlMachine]) -> [DaemonAPI.ControlMachine] {
        rows.filter { controlRole($0) } + rows.filter { !controlRole($0) }
    }

    /// The latest look of the copy that runs on this machine. A copy from before #6591 names no
    /// machine: when it is the only copy and this is the only control machine, it is this one's.
    public static func look(for machine: MachineRef, in copies: [DaemonAPI.ControlCapacityCopy],
                            controlMachines: Int) -> DaemonAPI.ControlCapacityCopy? {
        let named = copies.filter { $0.machine == machine.rawValue }
        if let latest = named.max(by: { $0.at < $1.at }) { return latest }
        let unnamed = copies.filter { $0.machine == nil }
        return controlMachines == 1 && copies.count == 1 ? unnamed.first : nil
    }

    /// The highest level of the copies: the one the window's strip says.
    public static func worst(_ copies: [DaemonAPI.ControlCapacityCopy]) -> DaemonAPI.ControlCapacityCopy? {
        copies.max { rank($0.level) < rank($1.level) || (rank($0.level) == rank($1.level) && $0.at < $1.at) }
    }

    /// The copy the window's strip names: the highest level, near or full only. None while the
    /// window cannot reach the control plane (#7363): the level is then the last one known, not
    /// now, and the strip "Can't reach the control plane" says how old the window's data is.
    public static func warning(_ copies: [DaemonAPI.ControlCapacityCopy], controlAway: Bool) -> DaemonAPI.ControlCapacityCopy? {
        guard !controlAway else { return nil }
        return worst(copies).flatMap { $0.level == .ok ? nil : $0 }
    }

    static func rank(_ level: DaemonAPI.CapacityLevel) -> Int {
        switch level {
        case .ok: 0
        case .near: 1
        case .full: 2
        }
    }

    // MARK: Words

    /// The level in a row or a summary: "Near capacity", "At capacity", "OK", or "No reading"
    /// before the first look.
    public static func level(_ look: DaemonAPI.ControlCapacityCopy?) -> String {
        guard let look else { return "No reading" }
        return CapacityWords.level(look.level) ?? "OK"
    }

    /// The row's line under the name: "Near capacity · I/O pressure 16.7% · Swap 330 MB"; at OK
    /// the load, and before the first look, "No capacity reading yet".
    public static func rowLine(_ look: DaemonAPI.ControlCapacityCopy?) -> String {
        guard let look else { return noReading }
        var parts = [level(look)]
        switch look.level {
        case .ok:
            parts.append(CapacityWords.load(look.loadPerCore))
        case .near, .full:
            // Only the pressures that are there: 0.0% says nothing.
            if look.pressureIOFullAvg60 >= 0.05 { parts.append("I/O pressure \(percent(look.pressureIOFullAvg60))") }
            if look.pressureMemoryFullAvg60 >= 0.05 { parts.append("Memory pressure \(percent(look.pressureMemoryFullAvg60))") }
            parts.append(CapacityWords.load(look.loadPerCore))
        }
        return parts.joined(separator: " \u{00B7} ")
    }

    /// Under a machine that also runs agents, while its control plane is near or full: "Control
    /// plane near capacity". Nil at OK and before the first look.
    public static func agentsRowLine(_ look: DaemonAPI.ControlCapacityCopy?) -> String? {
        CapacityWords.level(look?.level).map { "Control plane " + $0.lowercased() }
    }

    /// The page's and the strip's sentence: "The control plane is near capacity: its disk is
    /// busy." The control plane's own reason, which says why, with a capital and a full stop.
    /// With a label (several control-only machines, #7362), it names the machine: "The control
    /// plane on box-2 is near capacity: its disk is busy.", or "box-2: …" for a reason in other words.
    public static func sentence(_ look: DaemonAPI.ControlCapacityCopy, on label: String?) -> String {
        let words = sentence(look)
        guard let label, !label.isEmpty else { return words }
        let lead = "The control plane"
        if words.hasPrefix(lead) { return lead + " on " + label + words.dropFirst(lead.count) }
        return label + ": " + words
    }

    public static func sentence(_ look: DaemonAPI.ControlCapacityCopy) -> String {
        let reason = look.reason.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let first = reason.first else {
            return look.level == .ok ? "The control plane is within capacity." : "The control plane is \(level(look).lowercased())."
        }
        let words = first.uppercased() + reason.dropFirst()
        return words.hasSuffix(".") ? words : words + "."
    }

    /// What the strip offers: the guide that says how to give the control plane more room.
    public static let guide = URL(string: "https://docs.agentloops.dev/how-to/run-the-control-plane-in-the-cloud/#metrics-and-logs")!

    public static let noReading = "No capacity reading yet"
    public static let noReadingDetail = "The first reading will appear after the control plane checks itself."
    /// The line when no reading will come by waiting (#7364): the read failed, or the answers stay empty.
    public static let noReadingNow = "No capacity reading"
    public static let readFailedDetail = "The capacity read failed. There is no reading to show."
    public static let emptyDetail = "The control plane gives no capacity reading. It may need an update."

    /// Why there is no reading (#7364), from the state of the reads: before the first read and
    /// after an empty answer, that one will come; after a first read that failed, that it
    /// failed; when the answers stay empty for one look or more, that the control plane gives
    /// none. `emptyFor` is the time from the first empty answer to the last one.
    public static func noReadingWords(failed: Bool, emptyFor: TimeInterval?) -> (line: String, detail: String) {
        if failed { return (noReadingNow, readFailedDetail) }
        if let emptyFor, emptyFor >= Double(lookSeconds) { return (noReadingNow, emptyDetail) }
        return (noReading, noReadingDetail)
    }
    public static let foot = "Reading every \(lookSeconds) seconds. The control plane keeps serving requests at every level."
    public static let refreshFailed = "Capacity could not refresh. Showing the last reading."

    /// The level in one word, as the Capacity figure says it: "OK", "Near" or "Full".
    public static func short(_ level: DaemonAPI.CapacityLevel) -> String {
        switch level {
        case .ok: "OK"
        case .near: "Near"
        case .full: "Full"
        }
    }

    /// "16.7%": a kernel pressure figure, the share of the last 60 s that work waited.
    public static func percent(_ value: Double) -> String {
        String(format: "%.1f%%", value)
    }

    /// "1.42": the load per core over a minute.
    public static func load(_ value: Double) -> String {
        String(format: "%.2f", value)
    }

    /// "184 MB", as Finder says it; "0 bytes", not Foundation's "Zero KB".
    public static func bytes(_ value: Double) -> String {
        let format = ByteCountFormatter()
        format.countStyle = .memory
        format.allowsNonnumericFormatting = false
        return format.string(fromByteCount: Int64(max(0, value)))
    }

    /// "1,284".
    public static func count(_ value: Int64) -> String {
        value.formatted(.number.grouping(.automatic))
    }

    /// The page's figures, in the order the wireframe has them (gate 2 of #6593).
    public struct Figure: Hashable, Sendable {
        public let label: String
        public let value: String
        public let detail: String?
    }

    /// The four figures across the top: the level, load per core, memory and I/O pressure.
    public static func summary(_ look: DaemonAPI.ControlCapacityCopy) -> [Figure] {
        [Figure(label: "Capacity", value: short(look.level), detail: nil),
         Figure(label: "Load per core", value: load(look.loadPerCore), detail: "\(load(look.loadPerCore5)) over 5 min"),
         Figure(label: "Memory pressure", value: percent(look.pressureMemoryFullAvg60), detail: "Full \u{00B7} avg 60 s"),
         Figure(label: "I/O pressure", value: percent(look.pressureIOFullAvg60), detail: "Full \u{00B7} avg 60 s")]
    }

    /// The sections under them: Memory, Process and Store, each a line of figures.
    public static func sections(_ look: DaemonAPI.ControlCapacityCopy) -> [(title: String, figures: [Figure])] {
        [("Memory", [Figure(label: "Available", value: bytes(look.memoryAvailableBytes), detail: nil),
                     Figure(label: "Swap used", value: "\(Int(look.swapUsedPercent.rounded()))%", detail: nil),
                     Figure(label: "Go heap", value: bytes(look.heapInuseBytes), detail: nil)]),
         ("Process", [Figure(label: "Goroutines", value: count(look.goroutines), detail: nil),
                      Figure(label: "Open files", value: count(look.openFiles), detail: nil)]),
         ("Store", [Figure(label: "Files", value: count(look.storeFiles), detail: nil),
                    Figure(label: "Size", value: bytes(Double(look.storeBytes)), detail: nil)])]
    }
}
