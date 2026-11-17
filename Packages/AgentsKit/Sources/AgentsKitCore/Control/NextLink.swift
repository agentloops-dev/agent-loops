import Foundation

/// **Next ↗** on Work's Issues list (#5691, epic #5687): Work ▸ Next on the web page, the work
/// that is ready in one order, with the reason for each place. A Web + Mac link (#4937, Lead role,
/// #5290): the Mac draws this one link and nothing more, and the Remote, which sets no
/// `openWebPlace`, has none.
public enum NextLink {
    public static let title = "Next"

    public static let help = "What goes next, in order, and why: on the web page"

    public static let cannotOpen = "Agents can’t open the web page"

    /// The page's place, after its `#`: `/work/next`, with `?folder=<path>` for the project's
    /// folder, so the page opens on that project. The web page finds the project by the folder's
    /// path on any machine, as its host id may not be the one the Mac knows. No project, or one
    /// with no folder: the page's own choice.
    public static func place(project: WorkList.Project?) -> String {
        guard let key = project?.folders.sorted().first, let bar = key.firstIndex(of: "|") else { return "/work/next" }
        let path = String(key[key.index(after: bar)...])
        guard !path.isEmpty, let escaped = path.addingPercentEncoding(withAllowedCharacters: component) else { return "/work/next" }
        return "/work/next?folder=\(escaped)"
    }

    /// What `encodeURIComponent` leaves as it is.
    private static let component = CharacterSet(charactersIn:
        "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()")
}
