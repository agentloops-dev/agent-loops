import Foundation

/// Make This the Control Plane… (#6614, epic #6608): the types of `control/eligible`,
/// `control/move`, `control/moves/list` and `control/moveChanged` (Packages/Wire's
/// machines.schema.json), and the words the Mac window draws them with. The web page says the
/// same in `Web/src/model/controlMove.ts`.
public extension DaemonAPI {
    /// Why a machine cannot take the control role (#6611).
    enum ControlIneligibility: String, Codable, Sendable, Hashable {
        case notReachable, macSleeps, stopsWhenIdle, belowFloor, bothRoles, olderVersion
    }

    /// One reason, with its words for the person.
    struct ControlEligibleReason: Codable, Sendable, Hashable {
        public var reason: ControlIneligibility
        public var text: String

        public init(reason: ControlIneligibility, text: String) {
            self.reason = reason
            self.text = text
        }
    }

    /// `control/eligible`'s params.
    struct ControlEligibleRequest: Codable, Sendable, Hashable {
        public var machine: MachineRef
        public var allowSleep: Bool?

        public init(machine: MachineRef, allowSleep: Bool? = nil) {
            self.machine = machine
            self.allowSleep = allowSleep
        }
    }

    /// `control/eligible`'s answer: ok when there are no reasons.
    struct ControlEligible: Codable, Sendable, Hashable {
        public var ok: Bool
        public var reasons: [ControlEligibleReason]
        public var address: String?
        public var memoryBytes: Int64?
        public var diskFreeBytes: Int64?

        public init(ok: Bool, reasons: [ControlEligibleReason], address: String? = nil,
                    memoryBytes: Int64? = nil, diskFreeBytes: Int64? = nil) {
            self.ok = ok
            self.reasons = reasons
            self.address = address
            self.memoryBytes = memoryBytes
            self.diskFreeBytes = diskFreeBytes
        }
    }

    /// Where a move stands (#6612).
    enum ControlMovePhase: String, Codable, Sendable, Hashable {
        case checking, copying, receiving, frozen, forwarding, done, failed
    }

    /// One move of the control plane to another machine (#6612).
    struct ControlMove: Codable, Sendable, Hashable, Identifiable {
        public var id: String
        public var from: String
        public var to: MachineRef
        public var toName: String
        public var phase: ControlMovePhase
        public var failedPhase: ControlMovePhase?
        public var address: String?
        public var forwardingUntil: Date?
        public var startedAt: Date
        public var updatedAt: Date
        public var error: String?
        public var by: String

        public init(id: String, from: String, to: MachineRef, toName: String, phase: ControlMovePhase,
                    failedPhase: ControlMovePhase? = nil, address: String? = nil, forwardingUntil: Date? = nil,
                    startedAt: Date, updatedAt: Date, error: String? = nil, by: String) {
            self.id = id
            self.from = from
            self.to = to
            self.toName = toName
            self.phase = phase
            self.failedPhase = failedPhase
            self.address = address
            self.forwardingUntil = forwardingUntil
            self.startedAt = startedAt
            self.updatedAt = updatedAt
            self.error = error
            self.by = by
        }
    }

    /// `control/move`'s params.
    struct ControlMoveRequest: Codable, Sendable, Hashable {
        public var machine: MachineRef

        public init(machine: MachineRef) {
            self.machine = machine
        }
    }

    /// `control/move`'s answer: the move, started.
    struct ControlMoveResult: Codable, Sendable, Hashable {
        public var move: ControlMove
    }

    /// `control/moves/list`'s answer: the newest first, at most 20.
    struct ControlMoves: Codable, Sendable, Hashable {
        public var moves: [ControlMove]
    }
}

public extension DaemonAPI.Method {
    /// `ControlEligibleRequest` → `ControlEligible` (#6611). Admins only.
    static let controlEligible = "control/eligible"
    /// `ControlMoveRequest` → `ControlMoveResult` (#6612). Admins only.
    static let controlMove = "control/move"
    /// `Empty` → `ControlMoves` (#6612). Admins only.
    static let controlMovesList = "control/moves/list"
}

public extension DaemonAPI.Notification {
    /// To the admins: a move changed its step (#6612): `ControlMove`.
    static let controlMoveChanged = "control/moveChanged"
}

/// The words and steps of a move (#6614), as the approved wireframes (gate 2 of #6608) say them.
public enum ControlMoveWords {
    public static let make = "Make This the Control Plane…"
    public static let confirm = "Make Control Plane"
    public static let tryAgain = "Try Again…"
    public static let checking = "Checking this machine…"
    public static let section = "Control plane"
    public static let anotherRuns = "Another move of the control plane is running. Wait for it to end."
    /// How long a move that ended stays on the strip and the page.
    public static let shownFor: TimeInterval = 24 * 60 * 60

    /// A step of the strip, in the order the move runs them (the #6612 lane's note): the
    /// wireframe's words. "Seal key" is part of Receive: the move seals the key as it asks the
    /// machine to receive.
    public struct Step: Hashable, Sendable {
        public let phase: DaemonAPI.ControlMovePhase
        public let title: String
    }

    public enum StepState: String, Sendable {
        case todo, current, done, failed
    }

    public static let steps: [Step] = [
        Step(phase: .checking, title: "Check"),
        Step(phase: .receiving, title: "Receive"),
        Step(phase: .frozen, title: "Freeze"),
        Step(phase: .copying, title: "Copy store"),
        Step(phase: .forwarding, title: "Forward"),
    ]

    /// Each step's state for this move.
    public static func states(_ move: DaemonAPI.ControlMove) -> [StepState] {
        let index: (DaemonAPI.ControlMovePhase?) -> Int? = { phase in steps.firstIndex { $0.phase == phase } }
        switch move.phase {
        case .done, .forwarding:
            return steps.map { _ in .done }
        case .failed:
            let at = index(move.failedPhase) ?? 0
            return steps.indices.map { $0 < at ? .done : $0 == at ? .failed : .todo }
        default:
            let at = index(move.phase) ?? 0
            return steps.indices.map { $0 < at ? .done : $0 == at ? .current : .todo }
        }
    }

    /// The move is under way.
    public static func running(_ move: DaemonAPI.ControlMove) -> Bool {
        switch move.phase {
        case .done, .forwarding, .failed: false
        default: true
        }
    }

    /// The move is under way, or ended in the last day: the strip and the page say it.
    public static func shown(_ move: DaemonAPI.ControlMove, now: Date = Date()) -> Bool {
        running(move) || now.timeIntervalSince(move.updatedAt) < shownFor
    }

    /// What the step is doing, in the strip's sentence.
    static func doing(_ phase: DaemonAPI.ControlMovePhase) -> String {
        switch phase {
        case .checking: "checking the machine"
        case .receiving: "sealing the control key to it and starting its copy"
        case .frozen: "holding every change"
        case .copying: "copying the store"
        default: "finishing"
        }
    }

    /// The step's title, as the strip shows it; "Check" for none.
    static func title(_ phase: DaemonAPI.ControlMovePhase?) -> String {
        steps.first { $0.phase == phase }?.title ?? steps[0].title
    }

    /// The strip's sentence.
    public static func line(_ move: DaemonAPI.ControlMove) -> String {
        switch move.phase {
        case .failed:
            let why = move.error.map { ": \(trimmed($0))" } ?? ""
            return "The move to \(move.toName) stopped at “\(title(move.failedPhase))”\(why). The control plane still serves where it was."
        case .done, .forwarding:
            return "\(move.toName) is the control plane."
        default:
            return "Moving the control plane to \(move.toName): \(doing(move.phase))."
        }
    }

    /// Under a move that ended: until when the old address forwards, when it does.
    public static func forwarding(_ move: DaemonAPI.ControlMove) -> String? {
        guard move.phase == .forwarding, let until = move.forwardingUntil else { return nil }
        return "The old address forwards to it until \(until.formatted(date: .abbreviated, time: .omitted))."
    }

    /// "It meets the checks: reachable at ams3-1.example.dev, always on, 2 GB memory, 40 GB free disk."
    public static func meets(_ eligible: DaemonAPI.ControlEligible) -> String {
        var parts: [String] = []
        if let address = eligible.address { parts.append("reachable at \(address)") }
        parts.append("always on")
        if let memory = eligible.memoryBytes { parts.append("\(bytes(memory)) memory") }
        if let disk = eligible.diskFreeBytes { parts.append("\(bytes(disk)) free disk") }
        return "It meets the checks: " + parts.joined(separator: ", ") + "."
    }

    /// The confirmation's title.
    public static func question(_ name: String) -> String { "Make \(name) the control plane?" }

    /// What the confirmation says happens. `now` is where clients reach the control plane today.
    public static func happens(name: String, address: String?, now: String?) -> [String] {
        var lines = [
            "The store and the control key move to \(name). The key is sealed to that machine.",
            "Every window, phone, browser and machine reconnects by itself. Nobody pairs again.",
        ]
        if let address, address != now {
            lines.append("The address changes to \(address). The old address forwards to it for up to 30 days: change your DNS before then.")
        } else if let address {
            lines.append("The address stays \(address).")
        }
        lines.append("Agents keep working. For about a minute, screens can’t reach machines.")
        return lines
    }

    static func trimmed(_ error: String) -> String {
        var words = error.trimmingCharacters(in: .whitespacesAndNewlines)
        while words.hasSuffix(".") { words.removeLast() }
        return words
    }

    static func bytes(_ count: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: count, countStyle: .memory)
    }
}
