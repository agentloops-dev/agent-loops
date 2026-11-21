import Foundation
#if canImport(Glibc)
import Glibc
#elseif canImport(Musl)
import Musl
#endif

/// Where everything lives. Injectable, so tests use a temporary directory and never go
/// near the real one.
///
/// The root is also the daemon's identity. Everything that makes one daemon one daemon
/// is in here — the lock it holds, the socket it answers on, the agents it owns — so
/// two processes pointed at two roots are two daemons that know nothing of each other,
/// and a window, the daemon it starts, and the MCP helper that daemon hands out all
/// find each other by agreeing on this one path.
///
/// That is what makes a second copy possible: a branch build can be run beside the
/// ordinary one without either touching the other's agents.
public struct StoreLocations: Sendable {
    public var root: URL
    /// Where this root's caches go (#1030): everything rebuilt or downloaded again when it
    /// is missing, so deleting the folder and starting again is always safe. The platform's
    /// cache folder, so a person can reset it the ordinary way. Settable, like
    /// `personalHome`, so a test hands one in.
    public var cacheRoot: URL
    /// Where this root's logs go (#1030): the platform's log folder.
    public var logRoot: URL

    /// The home folder whose `~/.agents` every agent shares (054), or nil for none.
    ///
    /// Only the ordinary daemon lays out the person's real home. A scratch copy or a
    /// test gets one only when `AGENTS_PERSONAL_HOME` names it, so a walk on a branch
    /// build never rearranges somebody's `~/.claude` (research R4). Settable, so a test
    /// hands one in directly rather than through the environment.
    public var personalHome: URL?

    public init(root: URL) {
        self.root = root
        let environment = ProcessInfo.processInfo.environment
        self.personalHome = Self.personalHome(root: root, environment: environment)
        (self.cacheRoot, self.logRoot) = Self.platformFolders(root: root, environment: environment)
    }

    /// The cache and log folders for `root` (#1030). Pure, so the rule is testable.
    ///
    /// - The ordinary root: `~/Library/Caches/com.alexecollins.agents` and
    ///   `~/Library/Logs/Agents` on a Mac; `$XDG_CACHE_HOME/agents` (`~/.cache/agents`) and
    ///   `$XDG_STATE_HOME/agents/logs` (`~/.local/state/agents/logs`) on Linux.
    /// - Any other root: a folder of its own under each, `roots/<name>-<hash>`, so a second
    ///   copy never shares a cache with the first.
    /// - A root in the temporary folder (a test, a walk's `/tmp/run-<slug>`): `cache` and
    ///   `logs` inside it, so that removing the root removes everything it made, and
    ///   thousands of test roots leave nothing behind in the person's folders.
    public static func platformFolders(root: URL, environment: [String: String]) -> (cache: URL, logs: URL) {
        if isTemporary(root) {
            return (root.appendingPathComponent("cache", isDirectory: true),
                    root.appendingPathComponent("logs", isDirectory: true))
        }
        #if canImport(Darwin)
        let library = FileManager.default.urls(for: .libraryDirectory, in: .userDomainMask)[0]
        var cache = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("com.alexecollins.agents", isDirectory: true)
        var logs = library.appendingPathComponent("Logs/Agents", isDirectory: true)
        #else
        let home = URL(fileURLWithPath: NSHomeDirectory(), isDirectory: true)
        func xdg(_ variable: String, _ fallback: String) -> URL {
            if let value = environment[variable], value.hasPrefix("/") { return URL(fileURLWithPath: value, isDirectory: true) }
            return home.appendingPathComponent(fallback, isDirectory: true)
        }
        var cache = xdg("XDG_CACHE_HOME", ".cache").appendingPathComponent("agents", isDirectory: true)
        var logs = xdg("XDG_STATE_HOME", ".local/state").appendingPathComponent("agents/logs", isDirectory: true)
        #endif
        if root.standardizedFileURL.path != standardRoot.standardizedFileURL.path {
            let own = "roots/\(root.lastPathComponent)-\(pathHash(root.standardizedFileURL.path))"
            cache.appendPathComponent(own, isDirectory: true)
            logs.appendPathComponent(own, isDirectory: true)
        }
        return (cache, logs)
    }

    /// Whether `url` is under the system's temporary folder or `/tmp`, through any link.
    static func isTemporary(_ url: URL) -> Bool {
        let path = url.resolvingSymlinksInPath().standardizedFileURL.path
        let temporaries = [NSTemporaryDirectory(), "/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp"]
            .map { URL(fileURLWithPath: $0, isDirectory: true).resolvingSymlinksInPath().standardizedFileURL.path }
        return temporaries.contains { path == $0 || path.hasPrefix($0.hasSuffix("/") ? $0 : $0 + "/") }
    }

    /// Eight hex digits of FNV-1a, the hash Agents Host labels a scratch set-up's jobs with.
    static func pathHash(_ path: String) -> String {
        let hash = path.utf8.reduce(UInt64(1469598103934665603)) { ($0 ^ UInt64($1)) &* 1099511628211 }
        return String(String(hash, radix: 16).suffix(8))
    }

    /// The one every process uses unless it was told otherwise.
    ///
    /// `--root <path>` first, because that is what survives `open`: macOS will pass
    /// arguments to a second copy of an app bundle and will not pass environment.
    /// `AGENTS_ROOT` second, for a daemon or a helper started from a shell, and
    /// because that is what the app hands its own daemon. Neither, and it is the
    /// ordinary place, which is what nearly every run is.
    public static var `default`: StoreLocations {
        chosen(arguments: CommandLine.arguments, environment: ProcessInfo.processInfo.environment)
    }

    /// The ordinary place: one daemon, one set of agents, no arguments.
    public static var standard: StoreLocations { StoreLocations(root: standardRoot) }

    /// The ordinary root on its own, so deciding a root's personal home does not build a
    /// whole `StoreLocations` (which would decide its own, and so on).
    static var standardRoot: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return base.appendingPathComponent("Agents", isDirectory: true)
    }

    public static let rootVariable = "AGENTS_ROOT"
    public static let personalHomeVariable = "AGENTS_PERSONAL_HOME"

    /// Pure, like `chosen`: the named home first, then the real one for the ordinary
    /// root, and none for anything else.
    public static func personalHome(root: URL, environment: [String: String]) -> URL? {
        if let named = environment[personalHomeVariable], !named.isEmpty { return expand(named) }
        guard root.standardizedFileURL.path == standardRoot.standardizedFileURL.path else { return nil }
        return URL(fileURLWithPath: NSHomeDirectory(), isDirectory: true)  // iOS has no homeDirectoryForCurrentUser
    }
    public static let rootArgument = "--root"

    /// The person's settings (#1033), `<home>/.agents/settings`: they move with the person
    /// rather than with this machine.
    public static func settings(inHome home: URL) -> URL {
        home.appendingPathComponent(".agents/settings", isDirectory: true)
    }

    /// Pure, so the rule is testable without a process to run it in.
    public static func chosen(arguments: [String], environment: [String: String]) -> StoreLocations {
        if let named = path(inArguments: arguments) { return StoreLocations(root: expand(named)) }
        if let named = environment[rootVariable], !named.isEmpty {
            return StoreLocations(root: expand(named))
        }
        return standard
    }

    /// `--root /some/where` or `--root=/some/where`. An empty one is not a root and is
    /// ignored rather than turning into the current directory.
    private static func path(inArguments arguments: [String]) -> String? {
        for (index, argument) in arguments.enumerated() {
            if argument == rootArgument, index + 1 < arguments.count {
                let next = arguments[index + 1]
                return next.isEmpty ? nil : next
            }
            if argument.hasPrefix(rootArgument + "=") {
                let value = String(argument.dropFirst(rootArgument.count + 1))
                return value.isEmpty ? nil : value
            }
        }
        return nil
    }

    private static func expand(_ path: String) -> URL {
        URL(filePath: (path as NSString).expandingTildeInPath).standardizedFileURL
    }

    /// Whether this is the ordinary daemon rather than one somebody asked for.
    public var isStandard: Bool {
        root.standardizedFileURL.path == Self.standard.root.standardizedFileURL.path
    }

    /// What to call this one when it is not the ordinary one, so two windows on screen
    /// at once can be told apart.
    public var name: String { root.lastPathComponent }

    public var socket: URL { root.appendingPathComponent("daemon.sock") }
    /// The daemon's log, rolled to `daemon.previous.log` beside it (#1030: in `logRoot`).
    public var log: URL { logRoot.appendingPathComponent("daemon.log") }
    /// What the app installs for this daemon (048): `tools/<runtime>/<id>/`, with
    /// `current` pointing at the one in use. Per root, so a scratch copy installs its own.
    /// A cache (#1030): a toolset that is missing is downloaded again.
    public var tools: URL { cacheRoot.appendingPathComponent("tools", isDirectory: true) }
    /// A host of a control plane elsewhere (058): what enrolling told it, and its own key.
    /// Written by the daemon given `--control-code`, or by the window's Run a Host Here.
    public var controlHostMembership: URL { root.appendingPathComponent("control-host.json") }
    /// The invite this host joins the lock with (#3491, #4248), Go's `controllink.LockInviteFile`.
    public var lockInvite: URL { root.appendingPathComponent("lock-invite.json") }
    /// A host code left for this daemon by Agents Host (058, T055): its launch agent's
    /// arguments are fixed, so the one-time code comes this way. Read on every try, and
    /// removed once the join has saved a membership (#113).
    public var controlJoinCode: URL { root.appendingPathComponent("control-join-code") }
    /// How this host's joining its control plane stands (#113): `HostJoinFile`, written by
    /// the daemon, read by Agents Host and by the control plane on this Mac.
    public var controlJoinStatus: URL { root.appendingPathComponent(HostJoinFile.name) }
}

public enum StoreCoding {
    /// ISO 8601 with fractional seconds: the record stays readable with `cat`, and a
    /// timestamp survives a round trip to the millisecond. Anything finer than that
    /// does not, which matters nowhere and is worth knowing anyway.
    ///
    /// Written and read with `Date.ISO8601FormatStyle`, which produces the same text
    /// `ISO8601DateFormatter` did and reads it in a sixth of the time. The formatter is
    /// what every record was written with until now, and it stays as the last resort
    /// on the way in: a stamp the style will not take is still read the old way
    /// rather than costing somebody a record.
    private static let style = Date.ISO8601FormatStyle(includingFractionalSeconds: true)
    private static let wholeSecondStyle = Date.ISO8601FormatStyle(includingFractionalSeconds: false)
    nonisolated(unsafe) private static let dateFormatter: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    public static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .custom { date, encoder in
            var c = encoder.singleValueContainer()
            // The style cuts the fraction off at the millisecond where the formatter
            // rounded it, so half a millisecond is added first: what is written is
            // then the same text for the same date as every record before it.
            try c.encode(style.format(date.addingTimeInterval(0.0005)))
        }
        e.outputFormatting = [.withoutEscapingSlashes, .sortedKeys]
        return e
    }()

    public static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            if let date = try? style.parse(text) { return date }
            if let date = try? wholeSecondStyle.parse(text) { return date }
            guard let date = dateFormatter.date(from: text) else {
                throw DecodingError.dataCorruptedError(
                    in: try decoder.singleValueContainer(), debugDescription: "not an ISO 8601 date: \(text)")
            }
            return date
        }
        return d
    }()
}

extension StoreCoding {
    /// How many set-aside copies of one file are kept; the oldest go first (#205).
    public static let asidesKept = 3

    /// Move a file that exists and cannot be read out of the way, keeping it as
    /// `<name>.corrupt-<time>` (#171): beside the original, or in `folder` when given
    /// (#205: a folder's file is never set aside inside the person's tree). The stores
    /// that read such a file as empty go on to write that empty value back, and without
    /// this the only copy of what somebody set is gone. Answers where it went, or nil
    /// when it could not be moved (then it is still where it was, and a store must not
    /// write over it). With `copy`, the original stays where it is.
    @discardableResult
    public static func setAside(_ url: URL, in folder: URL? = nil, copy: Bool = false) -> URL? {
        guard FileManager.default.fileExists(atPath: url.path) else { return nil }
        do {
            if let folder { try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true) }
            let aside = asideName(for: url, in: folder)
            if copy {
                try FileManager.default.copyItem(at: url, to: aside)
            } else {
                try FileManager.default.moveItem(at: url, to: aside)
            }
            pruneAsides(of: url, in: folder)
            return aside
        } catch {
            return nil
        }
    }

    /// Where a file outside the daemon's root is set aside: a folder of its own under
    /// `root/set-aside`, named for the folder it came from, so two folders' `pins.json`
    /// never meet.
    public static func asideFolder(for url: URL, under root: URL) -> URL {
        let parent = url.deletingLastPathComponent().standardizedFileURL.path
        var hash: UInt64 = 0xcbf2_9ce4_8422_2325
        for byte in parent.utf8 { hash = (hash ^ UInt64(byte)) &* 0x100_0000_01b3 }
        let name = url.deletingLastPathComponent().lastPathComponent
        return root.appendingPathComponent("set-aside", isDirectory: true)
            .appendingPathComponent("\(name)-\(String(hash, radix: 16))", isDirectory: true)
    }

    /// `<name>.corrupt-<time>` beside `url` (or in `folder`), one not yet taken.
    public static func asideName(for url: URL, in folder: URL? = nil, at date: Date = Date()) -> URL {
        let stamp = date.formatted(.iso8601.year().month().day().dateSeparator(.dash)
            .time(includingFractionalSeconds: false).timeSeparator(.omitted))
        let folder = folder ?? url.deletingLastPathComponent()
        let base = "\(url.lastPathComponent).corrupt-\(stamp)"
        // After any copy of this second, even one pruned since, so the names still sort
        // oldest first.
        let taken = ((try? FileManager.default.contentsOfDirectory(atPath: folder.path)) ?? []).compactMap { name -> Int? in
            if name == base { return 1 }
            guard name.hasPrefix(base + "-") else { return nil }
            return Int(name.dropFirst(base.count + 1))
        }
        guard let last = taken.max() else { return folder.appendingPathComponent(base) }
        var n = last + 1
        while FileManager.default.fileExists(atPath: folder.appendingPathComponent("\(base)-\(n)").path) { n += 1 }
        return folder.appendingPathComponent("\(base)-\(n)")
    }

    /// Every set-aside copy of `url` in `folder` (beside it by default), oldest first.
    public static func asides(of url: URL, in folder: URL? = nil) -> [URL] {
        let folder = folder ?? url.deletingLastPathComponent()
        let prefix = url.lastPathComponent + ".corrupt-"
        let names = ((try? FileManager.default.contentsOfDirectory(atPath: folder.path)) ?? [])
            .filter { $0.hasPrefix(prefix) }
            .sorted { $0.localizedStandardCompare($1) == .orderedAscending }
        return names.map { folder.appendingPathComponent($0) }
    }

    /// Keep only the newest `asidesKept` copies of `url`.
    public static func pruneAsides(of url: URL, in folder: URL? = nil) {
        for old in asides(of: url, in: folder).dropLast(asidesKept) {
            try? FileManager.default.removeItem(at: old)
        }
    }

    /// Write `data` to `url` whole: to a temporary file beside it, synced to the disk,
    /// then renamed into place (#171). A crash at any moment leaves the old file or the
    /// new one, never a torn one; `.atomic` alone renames without the sync, so a power
    /// cut could still leave the new name pointing at nothing written.
    public static func writeAtomically(_ data: Data, to url: URL, permissions: Int? = nil) throws {
        let folder = url.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let temporary = folder.appendingPathComponent(".\(url.lastPathComponent).\(UUID().uuidString).tmp")
        func failure(_ code: Int32) -> CocoaError { writeError(code, at: url) }
        let fd = open(temporary.path, O_CREAT | O_EXCL | O_WRONLY, mode_t(permissions ?? 0o644))
        guard fd >= 0 else { throw failure(errno) }
        var code: Int32 = 0
        data.withUnsafeBytes { buffer in
            var offset = 0
            while offset < buffer.count {
                let n = write(fd, buffer.baseAddress! + offset, buffer.count - offset)
                if n < 0 { if errno == EINTR { continue }; code = errno; return }
                offset += n
            }
        }
        if code == 0, fsync(fd) != 0 { code = errno }
        close(fd)
        if code == 0, rename(temporary.path, url.path) != 0 { code = errno }
        guard code == 0 else {
            try? FileManager.default.removeItem(at: temporary)
            throw failure(code)
        }
    }

    /// What Foundation's own write throws, with the errno underneath, so a refusal
    /// still says why (`WriteFailure`: full, not allowed, read-only).
    public static func writeError(_ code: Int32, at url: URL) -> CocoaError {
        let kind: CocoaError.Code = switch code {
        case ENOSPC, EDQUOT: .fileWriteOutOfSpace
        case EACCES, EPERM: .fileWriteNoPermission
        case EROFS: .fileWriteVolumeReadOnly
        default: .fileWriteUnknown
        }
        return CocoaError(kind, userInfo: [NSFilePathErrorKey: url.path,
                                           NSUnderlyingErrorKey: NSError(domain: NSPOSIXErrorDomain, code: Int(code))])
    }

    /// A file opened to add to, made if it is not there. One call, so a disk that cannot
    /// make it says why (#212): `createFile` then `FileHandle(forWritingTo:)` turned a full
    /// disk into "doesn't exist", which no alert recognised. Appending puts every write
    /// at the end; otherwise it is opened to read too, at the start.
    public static func openForAdding(_ url: URL, appending: Bool = true) throws -> FileHandle {
        let flags = (appending ? O_WRONLY | O_APPEND : O_RDWR) | O_CREAT | O_CLOEXEC
        let fd = open(url.path, flags, 0o644)
        guard fd >= 0 else { throw writeError(errno, at: url) }
        return FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    }
}
