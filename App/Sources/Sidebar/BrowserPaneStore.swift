import AgentsKitCore
import WebKit

/// The browser pane's website data, and its bounds (#6796): 10 MB, 100 sites, and 90 days since a pane last opened
/// the site. It evicts: the caches first, then the site opened longest ago, with its sign-in. A site in an open pane
/// stays.
@MainActor
final class BrowserPaneStore {
    /// A scratch copy has the person's bundle id, so `.default()` would be the person's own store: it keeps its
    /// website data in memory instead, and never touches theirs.
    static let shared = WindowDefaults.scratchDomain == nil
        ? BrowserPaneStore(store: .default(), defaults: WindowDefaults.store)
        : BrowserPaneStore(store: .nonPersistent(), defaults: WindowDefaults.store)

    /// The caches the bound empties (#3727): what WebKit makes again, and what grows with each site the pane opens.
    static let cacheTypes: Set<String> = [
        WKWebsiteDataTypeDiskCache,
        WKWebsiteDataTypeMemoryCache,
        WKWebsiteDataTypeFetchCache,
    ]
    private static let sitesKey = "browserPane.sites"
    /// The caches are emptied at launch, then once a day: they keep nothing from one launch to the next.
    private static let cachesKept: TimeInterval = 24 * 3600

    let store: WKWebsiteDataStore
    private let defaults: UserDefaults
    private let sizes: Sizes
    private var sites: BrowserSites
    private let holders = NSHashTable<WebHolder>.weakObjects()
    private var cachesEmptied: Date?
    private var sweeping = false
    private var looked: Date?
    /// One more each time everything is forgotten, so a sweep that was waiting writes nothing after it.
    private var forgotten = 0

    init(store: WKWebsiteDataStore, defaults: UserDefaults, sizes: Sizes = Sizes()) {
        self.store = store
        self.defaults = defaults
        self.sizes = sizes
        sites = defaults.data(forKey: Self.sitesKey)
            .flatMap { try? JSONDecoder().decode(BrowserSites.self, from: $0) } ?? BrowserSites()
    }

    /// Sweeps at launch, then hourly. A Mac whose pane was never opened has no store, and none is made.
    static func run() async {
        guard WindowDefaults.scratchDomain == nil else { return }
        while !Task.isCancelled {
            if Sizes().exists { await shared.sweep() }
            try? await Task.sleep(for: .seconds(3600))
        }
    }

    /// Before a pane's first page: its site stays while the holder is there, and the caches of the last launch go.
    func add(_ holder: WebHolder) {
        holders.add(holder)
        if cachesEmptied == nil {
            cachesEmptied = Date()
            store.removeData(ofTypes: Self.cacheTypes, modifiedSince: .distantPast) {}
        }
    }

    /// A page, or a frame of one, loads.
    func opened(_ url: URL?) {
        guard let host = BrowserSites.host(of: url), sites.open(host, at: Date()) else { return }
        save()
    }

    /// A page is there. A store past its high-water mark is swept soon; it is looked at once a minute at most.
    func loaded() {
        guard store.isPersistent, looked.map({ Date().timeIntervalSince($0) >= 60 }) ?? true else { return }
        looked = Date()
        let sizes = sizes
        Task {
            if await sizes.all() > BrowserSites.Bounds.highWater { await sweep() }
        }
    }

    /// Brings the store under its bounds.
    func sweep(now: Date = Date()) async {
        guard !sweeping else { return }
        sweeping = true
        defer { sweeping = false }
        let generation = forgotten

        let cachesOld = cachesEmptied.map { now.timeIntervalSince($0) >= Self.cachesKept } ?? true
        var cachesOver = false
        if !cachesOld { cachesOver = await sizes.all() > BrowserSites.Bounds.bytes }
        if cachesOld || cachesOver {
            cachesEmptied = now
            await store.removeData(ofTypes: Self.cacheTypes, modifiedSince: .distantPast)
        }

        let types = WKWebsiteDataStore.allWebsiteDataTypes()
        let records = await store.dataRecords(ofTypes: types)
        guard generation == forgotten else { return }
        sites.fold(records: records.map(\.displayName), open: open, now: now)
        save()
        await remove(sites.expired(open: open, now: now), of: records)

        // One site at a time, and measured again after each, so no more go than must.
        while generation == forgotten, await bytes() > BrowserSites.Bounds.bytes {
            guard generation == forgotten,
                  let next = sites.idle(open: open).first(where: { name in records.contains { $0.displayName == name } })
            else { return }
            await remove([next], of: records)
        }
    }

    /// Forget This Mac: all the website data goes, and the dates. The open pages go first, so none writes again.
    func forget() async {
        forgotten += 1
        sites = BrowserSites()
        defaults.removeObject(forKey: Self.sitesKey)
        for holder in holders.allObjects { holder.clear() }
        guard !store.isPersistent || sizes.exists else { return }
        await store.removeData(ofTypes: WKWebsiteDataStore.allWebsiteDataTypes(), modifiedSince: .distantPast)
    }

    /// The hosts of the pages in the panes now.
    private var open: Set<String> {
        Set(holders.allObjects.compactMap { BrowserSites.host(of: $0.view.url ?? $0.current) })
    }

    private func remove(_ names: Set<String>, of records: [WKWebsiteDataRecord]) async {
        let gone = records.filter { names.contains($0.displayName) }
        guard !gone.isEmpty else { return }
        sites.remove(names)
        save()
        await store.removeData(ofTypes: WKWebsiteDataStore.allWebsiteDataTypes(), for: gone)
    }

    /// The site storage on disk, and the cookies, which WebKit keeps in a file of the app's other cookies.
    private func bytes() async -> Int {
        let cookies = await store.httpCookieStore.allCookies()
        return await sizes.sites() + cookies.reduce(0) {
            $0 + $1.name.utf8.count + $1.value.utf8.count + $1.domain.utf8.count + $1.path.utf8.count
        }
    }

    private func save() {
        guard let data = try? JSONEncoder().encode(sites) else { return }
        // store-bounds: kind=key-value; count=1000; age=90d; action=evict; record=512B; applied=on write, hourly, and on Forget
        defaults.putEntry(data, forKey: Self.sitesKey, most: KeyValueBounds.browserSites)
    }

    /// The bytes of WebKit's folders, read off the main thread. WebKit has no call that says them.
    struct Sizes: Sendable {
        private static let bundle = Bundle.main.bundleIdentifier ?? "com.alexecollins.agents"
        var data = URL.libraryDirectory.appending(path: "WebKit/\(bundle)/WebsiteData")
        var caches = URL.cachesDirectory.appending(path: "\(bundle)/WebKit")

        var exists: Bool { FileManager.default.fileExists(atPath: data.path) }

        @concurrent func sites() async -> Int { Self.bytes(of: [data]) }
        @concurrent func all() async -> Int { Self.bytes(of: [data, caches]) }

        private static func bytes(of folders: [URL]) -> Int {
            var total = 0
            for folder in folders {
                let files = FileManager.default.enumerator(at: folder, includingPropertiesForKeys: [.fileSizeKey])
                while let file = files?.nextObject() as? URL {
                    total += (try? file.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
                }
            }
            return total
        }
    }
}
