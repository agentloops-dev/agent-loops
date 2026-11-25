import Foundation

/// When each site of the Mac's browser pane was last opened, and which sites the store's bounds let go (#6796).
///
/// WebKit's records have no date and no size, and it can remove only what is *newer* than a date, so the age is kept
/// here. A site is named as WebKit names its record (the registrable domain); a host that was opened since the last
/// sweep waits under its own name until `fold` gives its date to its record.
public struct BrowserSites: Codable, Equatable, Sendable {
    /// What the store keeps at most (docs/explanation/data-stores.md). Past a bound, the site opened longest ago goes.
    public enum Bounds {
        public static let bytes = 10 * 1024 * 1024
        public static let sites = 100
        public static let age = cacheAgeLimit
        /// Past this, a sweep runs soon instead of at the next hour.
        public static let highWater = 2 * bytes
        /// The hosts kept between two sweeps. One over that gets its date at the next sweep.
        static let hosts = 1_000
        static let hostLength = 253
    }

    /// The date each site, or host not folded yet, was last opened.
    public private(set) var opened: [String: Date] = [:]

    public init() {}

    /// The host a page counts under: its own, or an empty one for a local file. Nil for a page with no site.
    public static func host(of url: URL?) -> String? {
        guard let url else { return nil }
        if url.isFileURL { return "" }
        guard let host = url.host()?.lowercased(), !host.isEmpty, host.utf8.count <= Bounds.hostLength else { return nil }
        return host
    }

    /// Whether `host` is one of the site's. WebKit names local files' record in words, which no host is.
    public static func isSite(_ site: String, of host: String) -> Bool {
        if host.isEmpty { return site.contains(" ") }
        let site = site.lowercased()
        return host == site || host.hasSuffix("." + site)
    }

    /// A page of `host` was opened. True when that changed what is kept: a date moves an hour at a time, so a page
    /// with many loads does not write each one.
    public mutating func open(_ host: String, at now: Date) -> Bool {
        if let last = opened[host] {
            guard abs(now.timeIntervalSince(last)) >= 3600 else { return false }
        } else if opened.count >= Bounds.hosts {
            return false
        }
        opened[host] = now
        return true
    }

    /// Gives each record of the store its date: the last time one of its hosts was opened, now for one in an open pane
    /// and for one not seen before. What has no record goes.
    public mutating func fold(records: [String], open: Set<String>, now: Date) {
        var folded: [String: Date] = [:]
        for site in records {
            if open.contains(where: { Self.isSite(site, of: $0) }) {
                folded[site] = now
                continue
            }
            let last = opened.filter { $0.key == site || Self.isSite(site, of: $0.key) }.map(\.value).max()
            folded[site] = min(last ?? now, now)
        }
        opened = folded
    }

    /// The sites that are too old, or over the count, with none that an open pane shows.
    public func expired(open: Set<String>, now: Date) -> Set<String> {
        let idle = idle(open: open)
        let old = idle.filter { now.timeIntervalSince(opened[$0] ?? now) >= Bounds.age }
        let over = idle.prefix(max(0, opened.count - Bounds.sites))
        return Set(old).union(over)
    }

    /// The sites that no open pane shows, the one opened longest ago first.
    public func idle(open: Set<String>) -> [String] {
        opened.filter { site in !open.contains { Self.isSite(site.key, of: $0) } }
            .sorted { ($0.value, $0.key) < ($1.value, $1.key) }
            .map(\.key)
    }

    public mutating func remove(_ sites: Set<String>) {
        for site in sites { opened[site] = nil }
    }
}
