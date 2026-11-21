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
        /// Whether the daemon writes a line for it now: Attested has no value since #6438.
        public var isWritten: Bool { key != "Attested" }
        /// The rule's name in `policy/effective`.
        public var rule: String { "commitTrailers.\(key)" }

        /// The row Where this comes from draws.
        public var row: PolicyLayersWords.Row {
            PolicyLayersWords.Row(rule: rule, title: key, section: .tools, kind: .toggle, unset: isOnByDefault ? "On" : "Off")
        }
    }

    /// Every trailer, in the order of a commit's block (the daemon's `trailerBlockOrder`).
    public static let trailers: [Trailer] = [
        Trailer(key: "Made-by", kind: .record, isOnByDefault: true, example: "claude-code:claude-opus-5-5"),
        Trailer(key: "Session", kind: .record, isOnByDefault: true, example: "4f2c9a1e-…"),
        Trailer(key: "Fixes", kind: .record, isOnByDefault: true, example: "#6334"),
        Trailer(key: "Epic", kind: .record, isOnByDefault: true, example: "#5627"),
        Trailer(key: "Requested-by", kind: .record, isOnByDefault: true, example: "alexec"),
        Trailer(key: "Approved-by", kind: .record, isOnByDefault: true, example: "alexec"),
        Trailer(key: "Attested", kind: .record, isOnByDefault: true, example: "no value now, so left out"),
        Trailer(key: "Cost", kind: .spend, isOnByDefault: false, example: "$1.84"),
        Trailer(key: "Tokens", kind: .spend, isOnByDefault: true, example: "in=412.0k out=38.0k"),
        Trailer(key: "Duration", kind: .spend, isOnByDefault: true, example: "47m"),
        Trailer(key: "Co-authored-by", kind: .record, isOnByDefault: true, example: "Claude Opus 5.5 <noreply@anthropic.com>"),
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
    /// that is on and written, and its example value in the daemon's format (#6532). A commit on a
    /// branch gets `Refs` in place of `Fixes`, and no `Approved-by`: only the squash has those.
    public static func preview(_ isOn: (Trailer) -> Bool) -> [String] {
        trailers.filter { $0.isWritten && isOn($0) }.map { "\($0.key): \($0.example)" }
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
