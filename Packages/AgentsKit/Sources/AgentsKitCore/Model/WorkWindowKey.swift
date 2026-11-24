import Foundation

/// The page a Work window shows (#5475, epic #5480): its kind and its ref. A window is opened, found
/// again and restored by it, so a page has one window at most. The kind is a word, not `WorkList.Kind`,
/// so the pages that come after issues, pull requests and releases are more words.
public struct WorkWindowKey: Codable, Hashable, Sendable {
    public var kind: String
    public var ref: String

    public init(kind: String, ref: String) {
        self.kind = kind
        self.ref = ref
    }

    /// An item's page, or its Files changed page, which is in the item's window. Nil for a list.
    public init?(_ place: WorkPlace) {
        guard let ref = place.ref, !ref.isEmpty, [.issue, .pr, .release].contains(place.kind) else { return nil }
        self.init(kind: place.kind.rawValue, ref: ref)
    }

    /// A conversation of Messages (#5482), by its key in the list (`MessagesList.key`): the same on every machine.
    public init(conversation key: String) { self.init(kind: Self.messages, ref: key) }

    /// The conversation's key in the list; nil for a different page.
    public var conversation: String? { kind == Self.messages && !ref.isEmpty ? ref : nil }

    private static let messages = "messages"

    /// The item's page; nil for a kind this build does not know. It is from no list, so it has no
    /// Previous and Next.
    public var place: WorkPlace? {
        guard let kind = WorkList.Kind(rawValue: kind), [.issue, .pr, .release].contains(kind), !ref.isEmpty else { return nil }
        return WorkPlace(kind: kind, ref: ref)
    }

    /// The window's title: "#5243 Deploys: Deployed on a release's page…"; the ref alone before the
    /// title is read, or when the title says it.
    public static func title(_ title: String?, ref: String) -> String {
        guard let title, !title.isEmpty else { return WorkList.shortRef(ref) }
        return WorkList.titleSaysRef(title, ref) ? title : "\(WorkList.shortRef(ref)) \(title)"
    }
}
