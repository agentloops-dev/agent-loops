import Foundation

/// The words of a warm pool's card and of New Machine's Start from (#5366, epic #5353, boards 1
/// and 5), the same on every client that draws them.
public enum WarmPoolWords {
    /// The chip on a pool's card: *Healthy* when it holds what it aims at, *Filling* while it
    /// makes up to it, else its state.
    public static func chip(_ pool: DaemonAPI.WarmPool) -> String {
        switch pool.state {
        case .stopped: "Stopped"
        case .offHours: "Off hours"
        case .empty: "Min 0"
        case .keeping: (pool.ready ?? 0) >= pool.target ? "Healthy" : "Filling"
        }
    }

    /// The line under its name: *s-8vcpu-16gb · fra1 · claude, codex · projects Agents, website*.
    public static func summary(_ pool: DaemonAPI.WarmPool) -> String {
        var parts: [String] = []
        if let size = pool.size, !size.isEmpty { parts.append(size) }
        if let region = pool.region, !region.isEmpty { parts.append(region) }
        if let runtimes = pool.runtimes, !runtimes.isEmpty { parts.append(runtimes.joined(separator: ", ")) }
        let projects = (pool.projects ?? []).map(projectName)
        parts.append(projects.isEmpty ? "no projects" : "projects \(projects.joined(separator: ", "))")
        parts.append("Agents kept at the latest release")
        return parts.joined(separator: " · ")
    }

    /// A project's name from the path a pool keeps.
    public static func projectName(_ path: String) -> String {
        URL(filePath: path).lastPathComponent
    }

    /// *ready (min 2, max 4)*, beside the number ready.
    public static func readyLine(_ pool: DaemonAPI.WarmPool) -> String {
        "ready (min \(pool.minReady), max \(pool.maxSize))"
    }

    /// Why a pool makes nothing now, or nil when it can make.
    public static func stateLine(_ pool: DaemonAPI.WarmPool) -> String? {
        switch pool.state {
        case .stopped:
            "Stopped: \(pool.stoppedBecause ?? "three makes failed in a row"). Save the pool to try again."
        case .offHours:
            "Outside its hours: it holds no ready machine and makes none."
        case .empty:
            "Min ready is 0: it makes nothing."
        case .keeping:
            pool.waitingBecause.map { "Waiting to make a machine: \($0)." }
        }
    }

    /// The last seven days: *41 claims · median 22 s · 3 found it empty*.
    public static func claimsLine(_ claims: DaemonAPI.WarmPoolClaims?) -> String {
        guard let claims, claims.count > 0 else { return "No claims yet" }
        var parts = [claims.count == 1 ? "1 claim" : "\(claims.count) claims"]
        if let median = claims.medianSeconds { parts.append("median \(MachineSetupWords.duration(median))") }
        if claims.empty > 0 { parts.append("\(claims.empty) found it empty") }
        return parts.joined(separator: " · ")
    }

    /// One machine's line on the card: *pool-fra1-a · ready 2 h 38 min*, *pool-fra1-c ·
    /// installing Claude · 41 s*, *pool-fra1-d · failed: quota*.
    public static func machineLine(_ machine: DaemonAPI.WarmPoolMachine, now: Date = Date()) -> String {
        let age = MachineSetupWords.duration(now.timeIntervalSince(machine.madeAt))
        switch machine.state {
        case .ready:
            return "\(machine.name) · ready \(age)"
        case .settingUp:
            return "\(machine.name) · \(machine.detail.flatMap { $0.isEmpty ? nil : $0 } ?? "setting up") · \(age)"
        case .failed:
            return "\(machine.name) · failed\(machine.detail.map { ": \($0)" } ?? "")"
        }
    }

    /// The hourly price in a provider's `x-costs` words: *$0.05/hour* is 0.05. Nil when it
    /// names none.
    public static func hourly(_ cost: String?) -> Double? {
        guard let cost, let dollar = cost.firstIndex(of: "$") else { return nil }
        let digits = cost[cost.index(after: dollar)...].prefix { $0.isNumber || $0 == "." }
        return Double(digits)
    }

    /// *$0.38*, or *$4.6* for a larger sum.
    public static func dollars(_ amount: Double) -> String {
        amount < 1 ? String(format: "$%.2f", amount) : String(format: "$%.1f", amount)
    }

    /// What the idle machines cost now: *Idle ones cost about $0.38 an hour*; with no price,
    /// *Cost not known*.
    public static func idleCost(_ pool: DaemonAPI.WarmPool, hourly: Double?) -> String {
        guard let hourly else { return "Cost not known" }
        let idle = Double((pool.ready ?? 0) + (pool.settingUp ?? 0))
        return "Idle ones cost about \(dollars(idle * hourly)) an hour"
    }

    /// The form's cost line: *Min 2: about $0.38 an hour · at the max of 4: $0.76 an hour*.
    public static func formCost(minReady: Int, maxSize: Int, hourly: Double?) -> String {
        guard let hourly else { return "Cost not known: the provider names no price for this size." }
        return "Min \(minReady): about \(dollars(Double(minReady) * hourly)) an hour · at the max of \(maxSize): \(dollars(Double(maxSize) * hourly)) an hour"
    }

    /// What the form refuses before Save: a name, and min ready no more than max size.
    public static func formProblem(name: String, minReady: Int, maxSize: Int) -> String? {
        if name.trimmingCharacters(in: .whitespaces).isEmpty { return "A warm pool needs a name." }
        if maxSize < 1 { return "Max size is at least 1." }
        if minReady > maxSize { return "Min ready (\(minReady)) is above max size (\(maxSize))." }
        return nil
    }

    /// *Replace a ready machine after*'s choices, in seconds.
    public static let replaceChoices: [(label: String, seconds: Double)] = [
        ("6 hours", 6 * 3600), ("12 hours", 12 * 3600), ("24 hours", 24 * 3600), ("2 days", 48 * 3600), ("7 days", 7 * 24 * 3600),
    ]

    /// The pools New Machine's Start from offers for a provider and size: those of the
    /// provider and size that are making machines, the ones with a ready machine first, as
    /// a claim takes them.
    public static func matching(_ pools: [DaemonAPI.WarmPool], provider: ProviderID?, size: String?) -> [DaemonAPI.WarmPool] {
        let same = pools.filter { $0.provider == provider && $0.state != .stopped && ($0.size ?? "") == (size ?? "") }
        return same.filter { ($0.ready ?? 0) > 0 } + same.filter { ($0.ready ?? 0) == 0 }
    }

    /// Start from's line for a pool: *2 ready · about 22 s*; *none ready now · a new machine instead*.
    public static func startFromLine(_ pool: DaemonAPI.WarmPool) -> String {
        let ready = pool.ready ?? 0
        guard ready > 0 else { return "none ready now · a new machine instead" }
        let time = pool.claims?.medianSeconds.map { "about \(MachineSetupWords.duration($0))" } ?? "in seconds"
        return "\(ready) ready · \(time)"
    }

    /// Whether a machine's name is one a pool of these names gives its machines (#5366): the
    /// pool's name as a slug (lower case letters, digits and dashes, starting with a letter, at
    /// most 24), a dash and six hex digits, as the control plane's `machineName` makes it. The
    /// sidebar leaves them out: nobody owns a pool's machine until a claim, and the provider's
    /// list can name one before the pool has kept it.
    public static func isPoolMachine(_ name: String, pools: [String]) -> Bool {
        guard let dash = name.lastIndex(of: "-") else { return false }
        let tail = name[name.index(after: dash)...]
        guard tail.count == 6, tail.allSatisfy(\.isHexDigit) else { return false }
        let head = String(name[..<dash])
        return pools.contains { slug($0) == head }
    }

    /// A pool's name as its machines' names begin.
    static func slug(_ pool: String) -> String {
        var out = ""
        var dash = false
        for c in pool.lowercased() {
            if c.isASCII, c.isLetter || c.isNumber {
                if dash, !out.isEmpty { out.append("-") }
                dash = false
                out.append(c)
            } else {
                dash = true
            }
        }
        if out.first.map({ !($0.isASCII && $0.isLetter) }) ?? true { out = "pool-" + out }
        if out.count > 24 {
            out = String(out.prefix(24))
            while out.hasSuffix("-") { out.removeLast() }
        }
        return out
    }

    /// Start from's line for a new machine.
    public static let newMachineLine = "a few minutes"

    /// The footnote on a machine's page once a claim gave it: the pool makes another.
    public static func replacedNote(pool: String) -> String {
        "The pool replaced this machine the moment it was claimed: “\(pool)” makes a new one now (Settings ▸ Machine Providers)."
    }

    // MARK: A pool that waits for root.sh (#7157, gate 2 board *pool waits*)

    public static let reviewLine = "root.sh changed. The pool makes no machines until you review it."

    /// What a yes does: the pool's unclaimed machines ran the older root.sh.
    public static func reviewAfter(_ pool: DaemonAPI.WarmPool) -> String {
        let held = (pool.ready ?? 0) + (pool.settingUp ?? 0)
        switch held {
        case 0: return "After you approve, it makes machines again."
        case 1: return "After you approve, it replaces its unclaimed machine."
        default: return "After you approve, it replaces its \(held) unclaimed machines."
        }
    }

    /// The pool as it is, with the person's yes to its changed root.sh.
    public static func reviewed(_ pool: DaemonAPI.WarmPool) -> DaemonAPI.WarmPoolSet {
        DaemonAPI.WarmPoolSet(id: pool.id, name: pool.name, scope: pool.scope, provider: pool.provider, size: pool.size,
                              region: pool.region, runtimes: pool.runtimes, projects: pool.projects, minReady: pool.minReady,
                              maxSize: pool.maxSize, replaceAfter: pool.replaceAfter, hours: pool.hours, reviewed: true)
    }
}
