import Foundation

public extension DaemonAPI.Method {
    /// `AutoMachineRequest` → `AutoMachinePick` (#5374, #5375): which machine Auto would pick
    /// for a project and runtime, or why none fits. Asked of the control plane.
    static let sessionsPreviewStart = "sessions/previewStart"
}

public extension DaemonAPI {
    /// sessions/previewStart's question (#5374, specs/5373-auto-machine).
    struct AutoMachineRequest: Codable, Hashable, Sendable {
        public var project: String
        public var runtimeID: String

        public init(project: String, runtimeID: String) {
            self.project = project
            self.runtimeID = runtimeID
        }
    }

    /// Why no machine fits.
    enum AutoMachineNone: String, Codable, Hashable, Sendable {
        case noFolder, noRuntime, offline
    }

    /// Why Auto chose the machine it chose.
    enum AutoMachineReason: String, Codable, Hashable, Sendable {
        case lastUsed, leastLoaded
    }

    /// sessions/previewStart's answer (#5374): the pick, or why none fits. The words in
    /// `text` are the control plane's; a client draws them as they are.
    struct AutoMachinePick: Codable, Hashable, Sendable {
        public var machine: MachineRef?
        public var machineName: String?
        public var tier: CapacityLevel?
        public var reason: AutoMachineReason?
        public var loadPerCore: Double?
        public var commit: String?
        public var text: String?
        public var none: AutoMachineNone?
        public var offline: [String]?
        public var single: Bool?

        public init(machine: MachineRef? = nil, machineName: String? = nil, tier: CapacityLevel? = nil,
                    reason: AutoMachineReason? = nil, loadPerCore: Double? = nil, commit: String? = nil,
                    text: String? = nil, none: AutoMachineNone? = nil, offline: [String]? = nil, single: Bool? = nil) {
            self.machine = machine
            self.machineName = machineName
            self.tier = tier
            self.reason = reason
            self.loadPerCore = loadPerCore
            self.commit = commit
            self.text = text
            self.none = none
            self.offline = offline
            self.single = single
        }
    }
}

/// New Session's Auto machine (#5375, specs/5373-auto-machine): Auto is the first row of On
/// and is selected at every start; a machine named by hand starts there and is not
/// remembered. The control plane makes the pick and its words; this only keeps what the
/// form needs of them. The web page's is `Web/src/model/autoMachine.ts`.
public enum AutoMachine {
    public typealias Pick = DaemonAPI.AutoMachinePick

    /// Whether the row is drawn: a project with one machine has no Auto row and no choice.
    public static func offered(_ pick: Pick?) -> Bool {
        guard let pick else { return false }
        return pick.single != true
    }

    /// Whether Auto can be chosen: greyed when no machine fits.
    public static func usable(_ pick: Pick?) -> Bool {
        guard let pick, offered(pick) else { return false }
        return pick.none == nil && pick.machine != nil
    }

    /// Whether Auto is the choice for the form on `folder`: it is, unless that folder's machine
    /// was named by hand (`named`).
    public static func chosen(_ folder: FolderKey, named: FolderKey?, pick: Pick?) -> Bool {
        named != folder && usable(pick)
    }

    /// The Auto row's words: `Auto · Mac mini`, or `Auto`.
    public static func rowWords(_ pick: Pick?) -> String {
        if usable(pick), let name = pick?.machineName, !name.isEmpty { return "Auto · \(name)" }
        return "Auto"
    }

    /// Why Auto is greyed, in the control plane's words; nil when it is not.
    public static func offWords(_ pick: Pick?) -> String? {
        guard let pick, offered(pick), pick.none != nil else { return nil }
        return pick.text
    }

    /// Where a start with Auto chosen goes (#5375).
    public struct Target: Equatable, Sendable {
        /// The folder the session starts in.
        public var folder: FolderKey
        /// The form is on another machine: its draft and its account are that machine's, so
        /// the start leaves them out.
        public var moved: Bool
        /// `StartRequest.autoChose`: the line the session's chat says first.
        public var autoChose: String?
    }

    /// The folder of `project` on the machine `pick` names, with the start's line. Nil when
    /// the pick names no machine with a folder of the project: the start goes where the form is.
    /// `same` says whether two ids name one machine: the window and the Remote call this Mac
    /// `mac`, and the control plane calls it by its own id.
    public static func target(from folder: FolderKey, project: [FolderKey], pick: Pick?,
                              same: (MachineRef, MachineRef) -> Bool = { $0 == $1 }) -> Target? {
        guard let pick, let machine = pick.machine, pick.none == nil else { return nil }
        let text = pick.text.flatMap { $0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? nil : $0 }
        if same(machine, folder.host) { return Target(folder: folder, moved: false, autoChose: text) }
        guard let there = project.first(where: { same(machine, $0.host) }) else { return nil }
        return Target(folder: there, moved: true, autoChose: text)
    }
}
