import Foundation

// A folder's pinned pages (#159): Markdown documents and HTML pages pinned under the
// folder. specs/159-pinned-pages/README.md is the reference.
// And its pinned sessions (#180), kept at the top of its sessions whatever their state,
// and its pinned workflows (#432), after them.

/// A pinned view (#189): which server's `ui://` resource, and the tool call that feeds it.
/// Opening the pin makes that call. Our extension of MCP Apps (SEP-1865), which ties a view
/// only to a call the model made. Shared through git, so `arguments` never holds a secret.
public struct ViewPin: Codable, Sendable, Hashable {
    public var server: String
    public var uri: String
    public var tool: String
    public var arguments: JSONValue?

    public init(server: String, uri: String, tool: String, arguments: JSONValue? = nil) {
        self.server = server
        self.uri = uri
        self.tool = tool
        self.arguments = arguments
    }
}

public enum PinKind: String, Codable, Sendable, Hashable {
    case markdown, html
    /// A `ui://` view (#189).
    case view
}

public enum PinLimits {
    /// Pins a folder may hold (Alex, #159).
    public static let perFolder = 10
    /// Pinned sessions a folder may hold, apart from its pages (#180).
    public static let sessionsPerFolder = 10
    /// Pinned workflows a folder may hold, apart from its pages and sessions (#432).
    public static let workflowsPerFolder = 10
    /// A view pin's arguments, as JSON (#189).
    public static let viewArgumentsBytes = 2048
}

/// The rules every door holds to: which files, which paths, what a row is called.
public enum PinRules {
    public static let markdownExtensions: Set<String> = ["md", "markdown", "mdown", "mkd"]
    public static let htmlExtensions: Set<String> = ["html", "htm"]

    public static func kind(_ path: String) -> PinKind? {
        let ext = (path as NSString).pathExtension.lowercased()
        if markdownExtensions.contains(ext) { return .markdown }
        if htmlExtensions.contains(ext) { return .html }
        return nil
    }

    /// A relative path, tidied: no leading `./` or `/`, no empty parts, nothing that
    /// climbs out. Nil for anything that can't name a file in the folder.
    public static func normalize(_ path: String) -> String? {
        let parts = path.split(separator: "/", omittingEmptySubsequences: true)
            .map(String.init).filter { $0 != "." }
        guard !parts.isEmpty, !parts.contains(".."), !path.hasPrefix("/") else { return nil }
        return parts.joined(separator: "/")
    }

    /// The path of `file` inside `folder`, or nil when it is not inside.
    public static func relative(_ file: String, in folder: URL) -> String? {
        let base = folder.standardizedFileURL.path(percentEncoded: false)
        let root = base.hasSuffix("/") ? base : base + "/"
        let full = URL(filePath: file).standardizedFileURL.path(percentEncoded: false)
        guard full.hasPrefix(root) else { return nil }
        return normalize(String(full.dropFirst(root.count)))
    }

    /// The row's words without a title: the file's name without its extension, or, for a
    /// `README.md` or `index.html`, its folder's name.
    public static func defaultTitle(_ path: String) -> String {
        let name = (path as NSString).lastPathComponent
        let stem = (name as NSString).deletingPathExtension
        if ["readme", "index"].contains(stem.lowercased()) {
            let folder = ((path as NSString).deletingLastPathComponent as NSString).lastPathComponent
            if !folder.isEmpty { return folder }
        }
        return stem
    }

    /// What is wrong with a view pin's shape, or nil. Whether its server has the view is
    /// the host's to say.
    public static func problem(_ view: ViewPin) -> String? {
        if view.server.trimmingCharacters(in: .whitespaces).isEmpty { return "a view pin names its server." }
        if !view.uri.hasPrefix("ui://") || view.uri.count <= 5 { return "a view's address starts ui://." }
        if view.tool.trimmingCharacters(in: .whitespaces).isEmpty { return "a view pin names the tool that feeds it." }
        if let arguments = view.arguments {
            guard case .object = arguments else { return "`arguments` is a JSON object." }
            if ((try? JSONEncoder().encode(arguments))?.count ?? .max) > PinLimits.viewArgumentsBytes {
                return "`arguments` is at most \(PinLimits.viewArgumentsBytes / 1024) KB of JSON."
            }
        }
        return nil
    }

    /// The row's words for a view without a title: the last part of its address.
    public static func defaultTitle(_ view: ViewPin) -> String {
        let name = view.uri.split(separator: "/").last.map(String.init) ?? view.uri
        return name.replacingOccurrences(of: "-", with: " ").capitalized(with: nil)
    }
}

/// One pin as a screen draws it.
public struct PinView: Codable, Sendable, Hashable, Identifiable {
    public var id: String { path }
    public var path: String
    /// The title given, or the file's name.
    public var title: String
    public var kind: PinKind
    /// The file is not in the folder (deleted, moved, or only on a branch).
    public var missing: Bool
    public var pinnedBy: PinnerView
    /// A view pin's view and feeding call (#189); `path` is then its `ui://` address.
    public var view: ViewPin?
    /// Why a view pin is missing here, in a few words: `PinMissing`.
    public var missingReason: String?
    /// A server's list view (#2032) that the person hid from the sidebar.
    public var hidden: Bool?
    /// A list view's count, as its server last said it (#2033): open issues, open pull
    /// requests. Nil when the server says none.
    public var count: Int?
    /// How many of `count` want the person, when the server says (PRs of yours with failed checks).
    public var attention: Int?
    /// When the count was read.
    public var countAt: Date?
    /// The last poll failed: `count` is the one before.
    public var countStale: Bool?

    public init(path: String, title: String, kind: PinKind, missing: Bool, pinnedBy: PinnerView,
                view: ViewPin? = nil, missingReason: String? = nil, hidden: Bool? = nil,
                count: Int? = nil, attention: Int? = nil, countAt: Date? = nil, countStale: Bool? = nil) {
        self.path = path
        self.title = title
        self.kind = kind
        self.missing = missing
        self.pinnedBy = pinnedBy
        self.view = view
        self.missingReason = missingReason
        self.hidden = hidden
        self.count = count
        self.attention = attention
        self.countAt = countAt
        self.countStale = countStale
    }

    /// The row's tooltip for its count (#2033): when it was read, and that it may be old.
    public var countHelp: String? {
        guard count != nil, let countAt else { return nil }
        let when = countAt.formatted(date: .omitted, time: .shortened)
        return countStale == true ? "As of \(when): the server did not answer since." : "As of \(when)."
    }

    /// A server's list view, shown without a pin (#2032): Hide, not Unpin.
    public var isServerView: Bool { pinnedBy.kind == .server }
}

/// Who pinned it, named for a person.
public struct PinnerView: Codable, Sendable, Hashable {
    public var kind: PinnerKind
    /// An agent's id or a workflow's; empty for the person.
    public var id: String
    public var name: String

    public init(kind: PinnerKind, id: String, name: String) {
        self.kind = kind
        self.id = id
        self.name = name
    }
}

public enum PinnerKind: String, Codable, Sendable, Hashable {
    case person, agent, workflow
    /// A connected server shows its list views itself (#2032): `id` is its name in
    /// mcp.json, `name` its title ("GitHub").
    case server
}

/// One folder's pins, for the sidebar.
public struct FolderPins: Codable, Sendable, Hashable {
    public var folder: URL
    public var pins: [PinView]
    /// Its pinned sessions in their order (#180). Nil from a host that has none to say.
    public var sessions: [UUID]?
    /// Its pinned workflows' ids in their order (#432). Nil from a host that has none to say.
    public var workflows: [String]?
    /// Its servers' list views (#2032), the hidden ones marked. Nil from a host with none.
    public var views: [PinView]?
    /// Its pull requests waiting for the person's approval (#3330). Nil from a host with none.
    public var reviews: [ReviewPull]?
    /// What the read-only daily checks found in it (#3331), newest first. Nil from a host with none.
    public var suggestions: [CheckSuggestion]?

    public init(folder: URL, pins: [PinView], sessions: [UUID] = [], workflows: [String] = [],
                views: [PinView] = [], reviews: [ReviewPull] = [], suggestions: [CheckSuggestion] = []) {
        self.folder = folder
        self.pins = pins
        self.sessions = sessions.isEmpty ? nil : sessions
        self.workflows = workflows.isEmpty ? nil : workflows
        self.views = views.isEmpty ? nil : views
        self.reviews = reviews.isEmpty ? nil : reviews
        self.suggestions = suggestions.isEmpty ? nil : suggestions
    }
}

/// An open pull request with a review requested from the person (#5803), a UI change waiting
/// for them (#2811), listed in the sidebar's For Review (#3330) until they review it, it is
/// closed, or it is dismissed. A row opens `url`, its page with the
/// screenshots.
public struct ReviewPull: Codable, Sendable, Hashable, Identifiable {
    public var url: URL
    public var number: Int
    public var title: String
    /// owner/name.
    public var repo: String
    /// Its lineage ref, "github:pr:owner/name#12": a press opens its Work page. Nil from an older
    /// host: a press opens its forge page.
    public var ref: String?
    public var author: String?
    public var updatedAt: Date
    /// The person has opened its page (`reviews/open`, #3464): its row is drawn read, not bold.
    public var opened: Bool?

    public var id: URL { url }
    /// Not opened yet: its title is bold.
    public var isUnread: Bool { opened != true }

    public init(url: URL, number: Int, title: String, repo: String, ref: String? = nil, author: String? = nil,
                updatedAt: Date, opened: Bool? = nil) {
        self.url = url
        self.number = number
        self.title = title
        self.repo = repo
        self.ref = ref
        self.author = author
        self.updatedAt = updatedAt
        self.opened = opened
    }
}

/// A finding of the read-only daily checks in a folder (#3355): failing CI, a pull request in
/// conflict, an issue no session has. Listed in the sidebar's For Review (#3331) until a session
/// is started from it (Start, `suggestions/start`) or it is dismissed (`suggestions/dismiss`).
public struct CheckSuggestion: Codable, Sendable, Hashable, Identifiable {
    /// What Start and Dismiss name.
    public var id: String
    /// What is wrong, in a line.
    public var title: String
    /// What Start sends to the new session.
    public var prompt: String
    /// The page it is about, such as the failing run or the pull request.
    public var link: URL?
    public var urgent: Bool?
    /// When the check found it.
    public var at: Date

    public init(id: String, title: String, prompt: String, link: URL? = nil, urgent: Bool? = nil, at: Date) {
        self.id = id
        self.title = title
        self.prompt = prompt
        self.link = link
        self.urgent = urgent
        self.at = at
    }
}
