import Foundation

/// The Commit trailers group of Agents ▸ Policy (#6373, epic #6365): one switch for each
/// trailer of an agent's commits, in two sub-groups by which way the rule is tighter. The
/// machine resolves each one as the rule `commitTrailers.<key>` (`policylayers.Trailers`);
/// this draws it. The web page's is #6374.
public enum CommitTrailerWords {
    public static let title = "Commit trailers"
    public static let intro = "Lines at the end of each commit that an agent makes."
    public static let previewTitle = "Preview"
    public static let previewScope = "the squash commit of a pull request, in a folder with no rules of its own"
    public static let tryAgain = "Try Again"
    /// This Mac's host, when it is the one that does not follow the rule yet.
    public static let thisMac = "This Mac"

    /// Which way a trailer's rule is tighter.
    public enum Kind: String, CaseIterable, Sendable {
        /// A record of agent work: on is tighter, so a folder or an organisation can require it.
        case record
        /// What the work cost: off is tighter, so a folder or an organisation can keep it out.
        case spend

        public var title: String {
            switch self {
            case .record: "Record"
            case .spend: "Spend"
            }
        }

        public var line: String {
            switch self {
            case .record: "a folder or your organisation can require these"
            case .spend: "a folder or your organisation can keep these out"
            }
        }

        /// The value that is tighter: a layer above that sets it holds the layers below to it.
        public var tight: Bool { self == .record }
    }

    public struct Trailer: Hashable, Sendable, Identifiable {
        public var key: String
        public var kind: Kind
        /// What applies when no layer sets it: every trailer on but Cost.
        public var isOnByDefault: Bool
        /// An example value, in the mono font under the name, and in the Preview: as the daemon
    /// writes it (`spendTrailers`: `$1.84`, `in=412.0k out=38.0k`, `47m`).
        public var example: String
        public var id: String { key }
        /// The rule's name in `policy/effective`.
        public var rule: String { "commitTrailers.\(key)" }

        /// The row Where this comes from draws.
        public var row: PolicyLayersWords.Row {
            PolicyLayersWords.Row(rule: rule, title: key, section: .tools, kind: .toggle, unset: isOnByDefault ? "On" : "Off")
        }
    }

    /// Every trailer, in the order of a commit's block (the daemon's `trailerBlockOrder`). There
    /// is no Co-authored-by: it is the runtime's own line, which the app never adds, changes or
    /// removes (#7174), so it has no switch.
    public static let trailers: [Trailer] = [
        Trailer(key: "Managed-by", kind: .record, isOnByDefault: true, example: "Agent Loops <noreply@agentloops.dev>"),
        Trailer(key: "Runtime", kind: .record, isOnByDefault: true, example: "claude-code"),
        Trailer(key: "Model", kind: .record, isOnByDefault: true, example: "opus-5-5"),
        Trailer(key: "Session", kind: .record, isOnByDefault: true, example: "4f2c9a1e-…"),
        Trailer(key: "Fixes", kind: .record, isOnByDefault: true, example: "#6334"),
        Trailer(key: "Epic", kind: .record, isOnByDefault: true, example: "#5627"),
        Trailer(key: "Requested-by", kind: .record, isOnByDefault: true, example: "alexec"),
        Trailer(key: "Approved-by", kind: .record, isOnByDefault: true, example: "alexec"),
        Trailer(key: "Cost", kind: .spend, isOnByDefault: false, example: "$1.84"),
        Trailer(key: "Tokens", kind: .spend, isOnByDefault: true, example: "in=412.0k out=38.0k"),
        Trailer(key: "Duration", kind: .spend, isOnByDefault: true, example: "47m"),
    ]

    public static func trailers(_ kind: Kind) -> [Trailer] { trailers.filter { $0.kind == kind } }

    /// Whether the machine says the rule at all: one older than `host.commit-trailers/1` does not.
    public static func isFollowed(_ effective: DaemonAPI.EffectivePolicy) -> Bool {
        effective.rules.contains { $0.rule.hasPrefix("commitTrailers.") }
    }

    /// The trailer's value in force: the layer that decides, else its default.
    public static func isOn(_ trailer: Trailer, _ rule: DaemonAPI.EffectiveRule?) -> Bool {
        guard let rule, rule.decides != nil, let on = rule.value?.boolValue else { return trailer.isOnByDefault }
        return on
    }

    /// Whether a layer above holds your value: it sets the tight value, so your switch can't move.
    public static func isCapped(_ trailer: Trailer, _ rule: DaemonAPI.EffectiveRule?) -> Bool {
        rule?.cap?.boolValue == trailer.kind.tight
    }

    /// Your layer's value for the trailer; nil when you do not set it.
    public static func yours(_ trailer: Trailer, in rules: DaemonAPI.PolicyRules) -> Bool? {
        rules.commitTrailers?[trailer.key].map { $0 == .on }
    }

    /// `rules` with your value for the trailer. A key this build does not know is kept.
    public static func setting(_ rules: DaemonAPI.PolicyRules, _ trailer: Trailer, on: Bool) -> DaemonAPI.PolicyRules {
        var r = rules
        var map = r.commitTrailers ?? [:]
        map[trailer.key] = on ? .on : .off
        r.commitTrailers = map
        return r
    }

    /// The block a pull request's squash commit gets (`commit_trailers` with `pr`), with each trailer
    /// that is on, and its example value in the daemon's format (#6532). A commit on a
    /// branch gets `Refs` in place of `Fixes`, and no `Approved-by`: only the squash has those.
    public static func preview(_ isOn: (Trailer) -> Bool) -> [String] {
        trailers.filter(isOn).map { "\($0.key): \($0.example)" }
    }

    /// The line above the folders whose own rule changes the block (#6534).
    public static let foldersTitle = "These folders change it:"

    /// A folder whose own `.agents/policy.json` changes the block (#6534): its name, its
    /// path (for its Project Settings ▸ Policy) and what it changes, as "Cost on, Tokens off".
    public struct FolderChange: Hashable, Sendable, Identifiable {
        public var folder: String
        public var name: String
        public var changes: String
        public var id: String { folder }

        public init(folder: String, name: String, changes: String) {
            self.folder = folder
            self.name = name
            self.changes = changes
        }
    }

    /// What a folder's rule changes: each trailer whose value in force for an agent in
    /// the folder is not its value outside any folder. Nil when it changes none.
    public static func folderChange(folder: String, name: String, inFolder: DaemonAPI.EffectivePolicy,
                                    outside: DaemonAPI.EffectivePolicy) -> FolderChange? {
        func rule(_ policy: DaemonAPI.EffectivePolicy, _ trailer: Trailer) -> DaemonAPI.EffectiveRule? {
            policy.rules.first { $0.rule == trailer.rule }
        }
        let changes = trailers.compactMap { trailer -> String? in
            let on = isOn(trailer, rule(inFolder, trailer))
            guard on != isOn(trailer, rule(outside, trailer)) else { return nil }
            return "\(trailer.key) \(on ? "on" : "off")"
        }
        guard !changes.isEmpty else { return nil }
        return FolderChange(folder: folder, name: name, changes: changes.joined(separator: ", "))
    }

    /// A failed save's line: the switch is back where it was.
    public static func failed(_ trailer: Trailer, wasOn: Bool, reason: String) -> String {
        "Not saved: \(reason) \(trailer.key) is \(wasOn ? "On" : "Off") again."
    }

    /// The machines that do not follow the rule yet: their agents' commits get no trailers.
    public static func behindLine(_ names: [String]) -> String? {
        guard !names.isEmpty else { return nil }
        let one = names.count == 1
        return "\(MachineNames.words(names.sorted())) \(one ? "does" : "do") not follow Commit trailers yet. "
            + "\(one ? "Its" : "Their") agents’ commits get no trailers until \(one ? "it updates" : "they update")."
    }
}
