import Foundation

/// The trail of a Work page (#5452): where the page is, as links back to the places above it.
/// *Issues › Epic #5281 › #5291* for an issue in a feature's epic, *Issues › #12* for one in
/// none, *Pull requests › #45* for a pull request. Each part but the last opens its place: the
/// list, or the epic's page. The web page's
/// `workTrail` (`Web/src/views/workTrail.ts`) says the same.
public enum WorkTrail {
    /// Where a part opens.
    public enum Target: Hashable, Sendable {
        /// A Work list: Issues, Pull requests or Releases.
        case list(WorkList.Kind)
        /// An item's page: the epic an issue is a sub-issue of.
        case item(WorkList.Kind, ref: String)
    }

    public struct Part: Hashable, Sendable {
        public var words: String
        /// Nil for the last part: the page itself, which opens nothing.
        public var target: Target?

        public init(words: String, target: Target? = nil) {
            self.words = words
            self.target = target
        }
    }

    /// The trail of an item's page. `feature` and `parent` are the item's, from its read (nil
    /// before it is read): a page starts at its kind's list; an issue with a parent has it before
    /// itself. An issue's parent is an epic when the issue
    /// has a feature, as an epic's children take its `feature:` label (#3271).
    public static func parts(kind: WorkList.Kind, ref: String, feature: String? = nil,
                             parent: DaemonAPI.LineageNode? = nil) -> [Part] {
        var parts: [Part] = []
        let feature = feature.flatMap { $0.isEmpty ? nil : $0 }
        parts.append(Part(words: kind.title, target: .list(kind)))
        if kind == .issue, let parent, !parent.ref.isEmpty, parent.ref != ref {
            let short = WorkList.shortRef(parent.ref)
            parts.append(Part(words: feature == nil ? short : "Epic \(short)", target: .item(.issue, ref: parent.ref)))
        }
        parts.append(Part(words: WorkList.shortRef(ref)))
        return parts
    }

    /// The parts a narrow page shows (the Remote on a phone, #5452): the page itself and the one
    /// part above it, as the bar's ‹ back does; the rest are a tap further up.
    public static func short(_ parts: [Part]) -> [Part] { Array(parts.suffix(2)) }

    /// The trail as one line for VoiceOver: "Issues, Epic #5281, #5291".
    public static func spoken(_ parts: [Part]) -> String { parts.map(\.words).joined(separator: ", ") }
}
