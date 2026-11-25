import Foundation
import SQLite3

/// Work's client cache (#6352, epic #6348): a SQLite file of the Work records this device read, so a list it showed
/// before draws at once, with no call, also when its machine does not answer.
///
/// The model (#6358's decision): **items by id** and **query answers as ids**.
/// - Each issue or pull request is kept once, by its node (kind and ref), with the forge's `updatedAt` and when this
///   device read it. A copy as new or newer replaces it, so a change shows in every answer that holds the item.
/// - Each query's answer is kept per machine as the refs it returned, **in the server's order**, page by page, each
///   page with its cursor and when it was read, and the total, the forges and the errors.
/// - A page whose answer is of its own shape (an epic's plan, Releases, the boards, Timeline, Deploys; #6573) keeps
///   it whole, by the page and what it asked, with when it was read.
/// - A refresh replaces page 1 and keeps the pages read before, each marked stale with its own time (#6636, option (c)).
///   Scrolling asks the next page by the cursor of the last **fresh** page, never a stale one, also when a stale page
///   said "the end". Each fresh page takes out of the stale pages the items it holds (an item is drawn once, the
///   newest copy) and the items that the server's order puts before its last item (they left the list: closed, or
///   out of the query). A fresh page at the end drops the stale pages.
///
/// Nothing here syncs or sorts: the caller asks the forge and gives the answer to `keepPage`. The bounds are in
/// `WorkCache.Bounds`. SQLite is the platform's (`SQLite3`, on macOS and iOS), so this adds no package.
public protocol WorkCaching: CacheStore {
    /// The kept answer to a query, with its items, or nil when it was never answered on this device.
    func answer(_ query: WorkCache.Query) async -> WorkCache.Answer?
    /// Keeps one page of a machine's answer: page 0 replaces the first page and marks the pages read before stale; the page
    /// after the last fresh one is added after it; any other fresh page is replaced. The stale pages lose what the
    /// fresh ones cover (#6636). Its items replace older copies.
    func keepPage(_ query: WorkCache.Query, host: MachineRef, page: Int, list: DaemonAPI.ForgeList) async
    /// The machine did not answer the query: its kept pages stay, with the errors and when it failed.
    func keepFailure(_ query: WorkCache.Query, host: MachineRef, errors: [DaemonAPI.ForgeListError]) async
    /// The machine is not asked the query, as another machine reads what it reads (#6641): its kept answer goes, so
    /// its rows do not draw as old ones.
    func forget(_ query: WorkCache.Query, host: MachineRef) async
    /// Items read or written outside a list (a write's answer, #6351, or an item's page): each replaces an older copy.
    func keep(items: [DaemonAPI.ForgeItem]) async
    /// Changes one kept item at once (an optimistic write, #6354). Its `updatedAt` stays, so the forge's newer copy
    /// replaces it; a copy as old (a list read while the write runs) does not, until `settle`.
    func edit(_ node: DaemonAPI.LineageNode, _ change: @Sendable (inout DaemonAPI.ForgeItem) -> Void) async
    /// The write of an edited item ended: a copy as old as the kept one replaces it again.
    func settle(_ node: DaemonAPI.LineageNode) async
    /// One kept item, or nil when this device never read it.
    func item(_ node: DaemonAPI.LineageNode) async -> DaemonAPI.ForgeItem?
    /// An item's kept page: its body and its first comments.
    func detail(_ node: DaemonAPI.LineageNode) async -> WorkCache.Detail?
    func keepDetail(_ detail: DaemonAPI.ForgeItemDetail) async
    /// A page's kept answer of its own shape (#6573): an epic's plan, Releases, the boards, Timeline, Deploys. `page`
    /// names the page and `asked` what it asked; nil when it was never answered on this device.
    func kept(_ page: String, asked: String) async -> WorkCache.PageAnswer?
    /// Keeps a page's answer for what it asked, read now, in place of the last one.
    func keep(_ page: String, asked: String, json: Data) async
}

extension WorkCaching {
    /// A page's kept answer, decoded, with when it was read; nil with none, or one of another form.
    public func kept<Value: Decodable>(_: Value.Type, _ page: String, asked: String) async -> (value: Value, read: Date)? {
        guard let kept = await kept(page, asked: asked),
              let value = try? JSONDecoder().decode(Value.self, from: kept.json) else { return nil }
        return (value, kept.read)
    }

    /// Keeps a page's answer, encoded.
    public func keep(_ value: some Encodable, _ page: String, asked: String) async {
        guard let json = try? JSONEncoder().encode(value) else { return }
        await keep(page, asked: asked, json: json)
    }
}

public actor WorkCache: WorkCaching {
    /// What a list asks the forge (#6358): the parts that change the server's answer. Words in a filter field are not
    /// part of it: the client narrows the kept answer with them.
    public struct Query: Codable, Hashable, Sendable {
        public var kind: WorkList.Kind
        public var state: String
        public var folder: String?
        public var board: String?
        public var label: String?
        public var assignee: String?
        public var feature: String?
        /// The server's order (updated, priority, …), as the client asks it.
        public var sort: String

        public init(kind: WorkList.Kind, state: String = "open", folder: String? = nil, board: String? = nil,
                    label: String? = nil, assignee: String? = nil, feature: String? = nil, sort: String = "updated") {
            self.kind = kind
            self.state = state
            self.folder = folder
            self.board = board
            self.label = label
            self.assignee = assignee
            self.feature = feature
            self.sort = sort
        }

        /// The query's key in the file: its JSON with sorted keys, so equal queries have one key.
        var key: String {
            let encoder = JSONEncoder()
            encoder.outputFormatting = .sortedKeys
            return (try? encoder.encode(self)).map { String(decoding: $0, as: UTF8.self) } ?? "\(self)"
        }
    }

    /// One kept page of a machine's answer: how many items it held, its cursor to the next page (nil at the end) and
    /// when it was read. A stale page was read before the last refresh (#6636): it draws, but its cursor is never asked.
    public struct Page: Codable, Hashable, Sendable {
        public var refs: [String]
        public var cursor: String?
        public var read: Date
        public var stale: Bool

        public init(refs: [String], cursor: String?, read: Date, stale: Bool = false) {
            self.refs = refs
            self.cursor = cursor
            self.read = read
            self.stale = stale
        }

        public init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            refs = try c.decode([String].self, forKey: .refs)
            cursor = try c.decodeIfPresent(String.self, forKey: .cursor)
            read = try c.decode(Date.self, forKey: .read)
            // A file kept before #6636 has no stale pages.
            stale = try c.decodeIfPresent(Bool.self, forKey: .stale) ?? false
        }
    }

    /// A machine's kept answer to a query, its items in the server's order.
    public struct HostAnswer: Sendable {
        public var host: MachineRef
        /// The kept pages' items, in order; an item that two pages hold is drawn once, where it was first.
        public var items: [DaemonAPI.ForgeItem]
        public var pages: [Page]
        public var total: Int?
        public var forges: [DaemonAPI.ForgeCapabilities]?
        public var errors: [DaemonAPI.ForgeListError]?
        /// When the machine last failed to answer, after its last page was read; nil when it answered.
        public var failed: Date?

        /// When page 1 was read: the "Last update" of the list (#5139).
        public var read: Date? { pages.first?.read }
        /// How many pages were read since the last refresh: the next page is kept after them (#6636).
        public var fresh: Int { WorkCache.freshCount(pages) }
        /// When the oldest stale page was read; nil with none.
        public var staleRead: Date? { pages.filter(\.stale).map(\.read).min() }
        /// The cursor to the page after the fresh ones; nil when they reach the end. A stale page's is never asked.
        public var cursor: String? { fresh > 0 ? pages[fresh - 1].cursor : nil }
        /// As the wire says a list, for a caller that draws one.
        public var list: DaemonAPI.ForgeList {
            DaemonAPI.ForgeList(items: items, cursor: cursor, errors: errors, forges: forges, total: total)
        }
    }

    public struct Answer: Sendable {
        public var hosts: [HostAnswer]
        /// The oldest page 1 of the machines: what the list says it shows.
        public var read: Date? { hosts.compactMap(\.read).min() }

        /// The machines' kept answers to draw (#6635): those asked now first, in their order, then every other machine
        /// the cache holds an answer of, in its kept order. A machine that is not known or online yet, as at the start,
        /// keeps its rows on screen, so the list never says "No open issues" while the cache holds them.
        public func shown(first asked: [MachineRef]) -> [HostAnswer] {
            let by = Dictionary(hosts.map { ($0.host, $0) }, uniquingKeysWith: { a, _ in a })
            let firsts = asked.compactMap { by[$0] }
            let named = Set(asked)
            return firsts + hosts.filter { !named.contains($0.host) }
        }
    }

    /// A page's kept answer (#6573): its JSON, as the page encodes it, and when it was read.
    public struct PageAnswer: Sendable {
        public var json: Data
        public var read: Date
    }

    /// An item's kept page.
    public struct Detail: Sendable {
        public var detail: DaemonAPI.ForgeItemDetail
        public var read: Date
    }

    /// The cache's bounds (#6352): an item's row, a body and its comments, any other record (#6573, #6788), the whole
    /// file, and a record's age since this device read it from its source (#6788). Over `bytes`, bodies go first, then the pages' answers and the lists' answers, each the least recently
    /// drawn first, with the items no other answer holds.
    public struct Bounds: Sendable {
        public var itemBytes: Int
        public var bodyBytes: Int
        public var comments: Int
        /// The largest record. A page's answer or an item's page over it is not kept, so the page asks again next time;
        /// a list's answer loses its last pages.
        public var recordBytes: Int
        public var bytes: Int
        /// The items kept decoded in memory (#6643). Over it, a load keeps only the items it asks for.
        public var memoItems: Int
        /// A record goes this long after it was read from the forge (#6788); drawing it does not make it newer.
        public var age: TimeInterval

        public init(itemBytes: Int = 4 * 1024, bodyBytes: Int = 64 * 1024, comments: Int = 50,
                    recordBytes: Int = 4 * 1024 * 1024, bytes: Int = 200 * 1024 * 1024, memoItems: Int = 5000,
                    age: TimeInterval = cacheAgeLimit) {
            self.itemBytes = itemBytes
            self.bodyBytes = bodyBytes
            self.comments = comments
            self.recordBytes = recordBytes
            self.bytes = bytes
            self.memoItems = memoItems
            self.age = min(age, cacheAgeLimit)
        }

        /// The Mac's Work cache.
        public static let mac = Bounds()
        /// The Remote's (#6357): 50 MB, as the web page's (#6356) and the Remote's local copy.
        public static let remote = Bounds(bytes: 50 * 1024 * 1024)
    }

    public struct CacheError: LocalizedError {
        public var message: String
        public var errorDescription: String? { message }
    }

    /// A machine's answer as the file keeps it; the items are in `item`.
    private struct Kept: Codable {
        var pages: [Page]
        var total: Int?
        var forges: [DaemonAPI.ForgeCapabilities]?
        var errors: [DaemonAPI.ForgeListError]?
        var failed: Date?

        /// When its oldest page was read; with no page, when the machine failed to answer.
        var oldest: Date { pages.map(\.read).min() ?? failed ?? .distantPast }

        /// The answer less what was read before `cutoff`; nil when nothing is left. The pages read since a refresh
        /// follow one another, so those after one that goes go too.
        func since(_ cutoff: Date) -> Kept? {
            var kept = self
            let fresh = WorkCache.freshCount(pages)
            kept.pages = Array(pages[..<fresh].prefix { $0.read >= cutoff }) + pages[fresh...].filter { $0.read >= cutoff }
            if let at = failed, at < cutoff {
                kept.failed = nil
                kept.errors = nil
            }
            guard kept.pages.isEmpty else { return kept }
            kept.total = nil
            kept.forges = nil
            return kept.failed == nil ? nil : kept
        }
    }

    /// The open database, closed when the cache goes.
    private final class Handle: @unchecked Sendable {
        let db: OpaquePointer?
        init(_ db: OpaquePointer?) { self.db = db }
        deinit { sqlite3_close_v2(db) }
    }

    private let handle: Handle
    private var db: OpaquePointer? { handle.db }
    private let bounds: Bounds
    private let now: @Sendable () -> Date
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()
    /// Items decoded once, by key: a draw after the first decodes nothing. The file stays the record. At most
    /// `bounds.memoItems`, plus the items of the one load that is over it.
    private var memo: [String: DaemonAPI.ForgeItem] = [:]
    /// When each query's answer was last drawn, not written yet (#6643): a draw writes nothing, and `trim` writes
    /// these first, so the least recently drawn answer still goes first.
    private var drawn: [String: Date] = [:]
    /// The items an optimistic write changed, until it ends (#6354): a copy as old as theirs does not replace them.
    private var edited: Set<String> = []
    /// When the records were last aged out; nil since the cache opened.
    private var aged: Date?
    private static let sweepEvery: TimeInterval = 60 * 60

    /// The cache in `file`, made when missing; nil keeps it in memory only (the tests).
    public init(file: URL?, bounds: Bounds = .mac, now: @escaping @Sendable () -> Date = { Date() }) throws {
        self.bounds = bounds
        self.now = now
        var opened: OpaquePointer?
        let flags = SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_NOMUTEX
        // store-bounds: kind=cache; size=200MiB; age=90d; action=evict; record=4MiB; applied=on write, on open and hourly
        guard sqlite3_open_v2(file?.path ?? ":memory:", &opened, flags, nil) == SQLITE_OK else {
            let message = opened.map { String(cString: sqlite3_errmsg($0)) } ?? "out of memory"
            sqlite3_close_v2(opened)
            throw CacheError(message: "could not open \(file?.path ?? "memory"): \(message)")
        }
        handle = Handle(opened)
        // A cache that just closed can still be in its sweep (#6788): wait for its lock, do not fail to open.
        sqlite3_busy_timeout(opened, 5000)
        let tables = ["item", "answer", "detail", "page"]
        // The kept rows' bytes are a running count in `size`, kept by triggers (#6643), so `bytes()` reads one row.
        // An older file counts its rows once. `recursive_triggers` makes a REPLACE fire the delete trigger.
        let sums = tables.map { "(SELECT ifnull(sum(length(json)), 0) FROM \($0))" }.joined(separator: " + ")
        let triggers = tables.flatMap { table in [
            "CREATE TRIGGER IF NOT EXISTS \(table)_in AFTER INSERT ON \(table) BEGIN UPDATE size SET bytes = bytes + length(NEW.json); END",
            "CREATE TRIGGER IF NOT EXISTS \(table)_out AFTER DELETE ON \(table) BEGIN UPDATE size SET bytes = bytes - length(OLD.json); END",
            "CREATE TRIGGER IF NOT EXISTS \(table)_set AFTER UPDATE OF json ON \(table) BEGIN UPDATE size SET bytes = bytes - length(OLD.json) + length(NEW.json); END",
        ] }
        for sql in [
            "PRAGMA auto_vacuum=INCREMENTAL",
            "PRAGMA recursive_triggers=ON",
            "PRAGMA journal_mode=WAL",
            "PRAGMA synchronous=NORMAL",
            "CREATE TABLE IF NOT EXISTS item(key TEXT PRIMARY KEY, updated REAL NOT NULL, read REAL NOT NULL, json BLOB NOT NULL)",
            "CREATE TABLE IF NOT EXISTS answer(query TEXT NOT NULL, host TEXT NOT NULL, used REAL NOT NULL, json BLOB NOT NULL, PRIMARY KEY(query, host))",
            "CREATE TABLE IF NOT EXISTS detail(key TEXT PRIMARY KEY, read REAL NOT NULL, used REAL NOT NULL, json BLOB NOT NULL)",
            "CREATE TABLE IF NOT EXISTS page(page TEXT NOT NULL, asked TEXT NOT NULL, read REAL NOT NULL, used REAL NOT NULL, json BLOB NOT NULL, PRIMARY KEY(page, asked))",
            "BEGIN",
            "CREATE TABLE IF NOT EXISTS size(bytes INTEGER NOT NULL)",
            "INSERT INTO size(bytes) SELECT \(sums) WHERE NOT EXISTS (SELECT 1 FROM size)",
        ] + triggers + ["COMMIT"] where sqlite3_exec(opened, sql, nil, nil, nil) != SQLITE_OK {
            throw CacheError(message: String(cString: sqlite3_errmsg(opened)))
        }
        // An answer's oldest read (#6788), so ageing reads only the answers that lose something. It fails when the column
        // is there; an answer of an older file has 0, and the first sweep reads it once and sets it.
        sqlite3_exec(opened, "ALTER TABLE answer ADD COLUMN read REAL NOT NULL DEFAULT 0", nil, nil, nil)
        for table in tables where sqlite3_exec(opened, "CREATE INDEX IF NOT EXISTS \(table)_read ON \(table)(read)", nil, nil, nil) != SQLITE_OK {
            throw CacheError(message: String(cString: sqlite3_errmsg(opened)))
        }
        // The hourly sweep, which ends with the cache. At once too: the file can be old when it opens.
        Task { [weak self] in
            while true {
                guard await self?.sweep() != nil else { return }
                try? await Task.sleep(for: .seconds(Self.sweepEvery))
            }
        }
    }

    // MARK: Answers

    public func answer(_ query: WorkCache.Query) -> WorkCache.Answer? {
        ageIfDue()
        let key = query.key
        var kept: [(host: MachineRef, kept: Kept)] = []
        try? rows("SELECT host, json FROM answer WHERE query = ? ORDER BY host", [.text(key)]) { s in
            if let answer = try? decoder.decode(Kept.self, from: blob(s, 1)) {
                kept.append((MachineRef(rawValue: text(s, 0)), answer))
            }
        }
        guard !kept.isEmpty else { return nil }
        drawn[key] = now()
        let all = kept.flatMap { $0.kept.pages.flatMap(\.refs) }
        load(all)
        return Answer(hosts: kept.map { host, kept in
            var seen = Set<String>()
            let items = kept.pages.flatMap(\.refs).compactMap { ref -> DaemonAPI.ForgeItem? in
                guard seen.insert(ref).inserted else { return nil }
                return memo[ref]
            }
            return HostAnswer(host: host, items: items, pages: kept.pages, total: kept.total, forges: kept.forges,
                              errors: kept.errors, failed: kept.failed)
        })
    }

    public func keepPage(_ query: WorkCache.Query, host: MachineRef, page: Int, list: DaemonAPI.ForgeList) {
        let key = query.key
        let at = now()
        let kept = keptAnswer(key, host: host)
        let had = kept?.pages ?? []
        let fresh = Self.freshCount(had)
        guard page >= 0, page <= fresh else { return }
        keep(items: list.items, at: at)
        let refs = list.items.map { Self.key($0.node) }.filter { memo[$0] != nil }
        // The forges' read time, not this call's (#6642): the daemon can answer from a copy up to a minute old.
        let read = Page(refs: refs, cursor: list.cursor, read: min(list.readAt ?? at, at))
        var reads = Array(had.prefix(fresh))
        var stale = Array(had.dropFirst(fresh))
        if page == 0 {
            // A refresh: every page read before is stale now, the old page 1 too (an item of it may be on page 2
            // now), before what was stale already; the new page 1 takes out what it holds.
            stale = reads.map { Page(refs: $0.refs, cursor: $0.cursor, read: $0.read, stale: true) } + stale
            reads = [read]
        } else if page == reads.count {
            reads.append(read)
        } else {
            reads[page] = read
        }
        let pages = reads + covered(stale, by: reads, sort: query.sort)
        let answer = Kept(pages: pages, total: list.total ?? kept?.total, forges: list.forges ?? kept?.forges,
                          errors: list.errors, failed: nil)
        write(answer, key: key, host: host, used: at)
        trim()
    }

    /// The pages before the first stale one: read since the last refresh.
    static func freshCount(_ pages: [Page]) -> Int {
        pages.firstIndex(where: \.stale) ?? pages.count
    }

    /// The stale pages less what the fresh pages cover (#6636): the items they hold, and the items the server's order
    /// puts before the last fresh item, which left the list. None when the fresh pages reach the end. An empty page
    /// goes.
    private func covered(_ stale: [Page], by fresh: [Page], sort: String) -> [Page] {
        guard !stale.isEmpty, let end = fresh.last, end.cursor != nil else { return [] }
        let held = Set(fresh.flatMap(\.refs))
        load(stale.flatMap(\.refs))
        let last = fresh.lazy.reversed().compactMap { $0.refs.last.flatMap { self.memo[$0] } }.first
        return stale.compactMap { page in
            var page = page
            page.refs = page.refs.filter { ref in
                guard !held.contains(ref), let item = memo[ref] else { return false }
                return last.map { !Self.before(item, $0, sort: sort) } ?? true
            }
            return page.refs.isEmpty ? nil : page
        }
    }

    /// `a` comes strictly before `b` in the server's order: by priority first for "priority" (high, medium, low, none),
    /// then the last updated first, as the daemon merges the forges' pages (#6538). A tie is not before: it may be on
    /// the next page.
    static func before(_ a: DaemonAPI.ForgeItem, _ b: DaemonAPI.ForgeItem, sort: String) -> Bool {
        if sort == "priority" {
            let ra = rank(a), rb = rank(b)
            if ra != rb { return ra < rb }
        }
        return a.updatedAt > b.updatedAt
    }

    private static func rank(_ item: DaemonAPI.ForgeItem) -> Int {
        switch item.priority?.lowercased() {
        case "high": 0
        case "medium": 1
        case "low": 2
        default: 3
        }
    }

    public func keepFailure(_ query: WorkCache.Query, host: MachineRef, errors: [DaemonAPI.ForgeListError]) {
        let key = query.key
        var answer = keptAnswer(key, host: host) ?? Kept(pages: [])
        answer.errors = errors
        answer.failed = now()
        write(answer, key: key, host: host, used: now())
        trim()
    }

    public func forget(_ query: WorkCache.Query, host: MachineRef) {
        try? run("DELETE FROM answer WHERE query = ? AND host = ?", [.text(query.key), .text(host.rawValue)])
    }

    private func keptAnswer(_ key: String, host: MachineRef) -> Kept? {
        var kept: Kept?
        try? rows("SELECT json FROM answer WHERE query = ? AND host = ?", [.text(key), .text(host.rawValue)]) { s in
            kept = try? decoder.decode(Kept.self, from: blob(s, 0))
        }
        return kept
    }

    private func write(_ answer: Kept, key: String, host: MachineRef, used: Date) {
        var answer = answer
        guard var json = try? encoder.encode(answer) else { return }
        while json.count > bounds.recordBytes, !answer.pages.isEmpty {
            answer.pages.removeLast()
            guard let less = try? encoder.encode(answer) else { return }
            json = less
        }
        guard json.count <= bounds.recordBytes, !answer.pages.isEmpty || answer.failed != nil else {
            try? run("DELETE FROM answer WHERE query = ? AND host = ?", [.text(key), .text(host.rawValue)])
            return
        }
        try? run("INSERT OR REPLACE INTO answer(query, host, used, read, json) VALUES(?, ?, ?, ?, ?)",
                 [.text(key), .text(host.rawValue), .real(used), .real(answer.oldest), .blob(json)])
    }

    // MARK: Items

    static func key(_ node: DaemonAPI.LineageNode) -> String { "\(node.kind) \(node.ref)" }

    public func keep(items: [DaemonAPI.ForgeItem]) {
        keep(items: items, at: now())
        trim()
    }

    /// Writes items in one transaction. A row is replaced only by a copy as new or newer, so a late answer does not
    /// undo a newer one; an item whose row is over `bounds.itemBytes` after trimming is not kept, and
    /// its earlier copy goes, so no answer read now draws the item as it was (#7198).
    private func keep(items: [DaemonAPI.ForgeItem], at: Date) {
        guard !items.isEmpty, (try? run("BEGIN")) != nil else { return }
        defer { try? run("COMMIT") }
        load(items.map { Self.key($0.node) })
        for item in items {
            let key = Self.key(item.node)
            if let had = memo[key], had.updatedAt > item.updatedAt || (edited.contains(key) && had.updatedAt == item.updatedAt) {
                try? run("UPDATE item SET read = ? WHERE key = ?", [.real(at), .text(key)])
                continue
            }
            edited.remove(key)
            guard let fit = fitted(item) else {
                memo[key] = nil
                try? run("DELETE FROM item WHERE key = ?", [.text(key)])
                continue
            }
            memo[key] = fit.item
            try? run("""
            INSERT INTO item(key, updated, read, json) VALUES(?, ?, ?, ?)
            ON CONFLICT(key) DO UPDATE SET updated = excluded.updated, read = excluded.read, json = excluded.json
            """, [.text(key), .real(fit.item.updatedAt), .real(at), .blob(fit.json)])
        }
    }

    /// The item and its row, cut to `bounds.itemBytes`: a long title, then labels and assignees beyond the first.
    private func fitted(_ item: DaemonAPI.ForgeItem) -> (item: DaemonAPI.ForgeItem, json: Data)? {
        var item = item
        for step in 0..<4 {
            guard let json = try? encoder.encode(item) else { return nil }
            if json.count <= bounds.itemBytes { return (item, json) }
            switch step {
            case 0: item.title = String(item.title.prefix(256))
            case 1: item.labels = Array(item.labels.prefix(10))
            case 2: item.assignees = Array(item.assignees.prefix(5))
            default: return nil
            }
        }
        return nil
    }

    public func edit(_ node: DaemonAPI.LineageNode, _ change: @Sendable (inout DaemonAPI.ForgeItem) -> Void) {
        let key = Self.key(node)
        load([key])
        guard var item = memo[key] else { return }
        change(&item)
        item.updatedAt = memo[key]?.updatedAt ?? item.updatedAt
        guard let fit = fitted(item) else { return }
        memo[key] = fit.item
        edited.insert(key)
        try? run("UPDATE item SET json = ? WHERE key = ?", [.blob(fit.json), .text(key)])
    }

    public func settle(_ node: DaemonAPI.LineageNode) {
        edited.remove(Self.key(node))
    }

    public func item(_ node: DaemonAPI.LineageNode) -> DaemonAPI.ForgeItem? {
        ageIfDue()
        let key = Self.key(node)
        load([key])
        return memo[key]
    }

    /// Decodes the keys' items that are not in memory yet, from the file.
    private func load(_ keys: [String]) {
        var missing = Array(Set(keys.filter { memo[$0] == nil }))
        if memo.count + missing.count > bounds.memoItems {
            let asked = Set(keys)
            memo = memo.filter { asked.contains($0.key) }
            missing = Array(asked.subtracting(memo.keys))
        }
        let chunk = 500
        for start in stride(from: 0, to: missing.count, by: chunk) {
            let part = missing[start..<min(start + chunk, missing.count)]
            let marks = Array(repeating: "?", count: part.count).joined(separator: ",")
            try? rows("SELECT key, json FROM item WHERE key IN (\(marks))", part.map { .text($0) }) { s in
                if let item = try? decoder.decode(DaemonAPI.ForgeItem.self, from: blob(s, 1)) {
                    memo[text(s, 0)] = item
                }
            }
        }
    }

    // MARK: Details

    public func detail(_ node: DaemonAPI.LineageNode) -> WorkCache.Detail? {
        ageIfDue()
        let key = Self.key(node)
        var found: Detail?
        try? rows("SELECT read, json FROM detail WHERE key = ?", [.text(key)]) { s in
            if let detail = try? decoder.decode(DaemonAPI.ForgeItemDetail.self, from: blob(s, 1)) {
                found = Detail(detail: detail, read: Date(timeIntervalSince1970: sqlite3_column_double(s, 0)))
            }
        }
        guard var found else { return nil }
        try? run("UPDATE detail SET used = ? WHERE key = ?", [.real(now()), .text(key)])
        load([key])
        // The kept item is never older than the page's: an optimistic change (#6354) keeps its updatedAt.
        if let item = memo[key], item.updatedAt >= found.detail.item.updatedAt { found.detail.item = item }
        return found
    }

    public func keepDetail(_ detail: DaemonAPI.ForgeItemDetail) {
        let at = now()
        keep(items: [detail.item], at: at)
        var detail = detail
        if detail.body.utf8.count > bounds.bodyBytes {
            detail.body = Self.prefix(detail.body, bytes: bounds.bodyBytes)
        }
        if detail.comments.count > bounds.comments {
            detail.comments = Array(detail.comments.prefix(bounds.comments))
        }
        guard let json = try? encoder.encode(detail) else { return }
        guard json.count <= bounds.recordBytes else {
            try? run("DELETE FROM detail WHERE key = ?", [.text(Self.key(detail.item.node))])
            trim()
            return
        }
        try? run("INSERT OR REPLACE INTO detail(key, read, used, json) VALUES(?, ?, ?, ?)",
                 [.text(Self.key(detail.item.node)), .real(at), .real(at), .blob(json)])
        trim()
    }

    /// The longest prefix of `text` of at most `bytes` in UTF-8, cut at a character.
    static func prefix(_ text: String, bytes: Int) -> String {
        var used = 0
        var end = text.startIndex
        for index in text.indices {
            let next = text.index(after: index)
            used += text[index..<next].utf8.count
            guard used <= bytes else { break }
            end = next
        }
        return String(text[..<end])
    }

    // MARK: Pages

    public func kept(_ page: String, asked: String) -> WorkCache.PageAnswer? {
        ageIfDue()
        var found: PageAnswer?
        try? rows("SELECT read, json FROM page WHERE page = ? AND asked = ?", [.text(page), .text(asked)]) { s in
            found = PageAnswer(json: blob(s, 1), read: Date(timeIntervalSince1970: sqlite3_column_double(s, 0)))
        }
        guard found != nil else { return nil }
        try? run("UPDATE page SET used = ? WHERE page = ? AND asked = ?", [.real(now()), .text(page), .text(asked)])
        return found
    }

    public func keep(_ page: String, asked: String, json: Data) {
        guard json.count <= bounds.recordBytes else {
            try? run("DELETE FROM page WHERE page = ? AND asked = ?", [.text(page), .text(asked)])
            return
        }
        let at = now()
        try? run("INSERT OR REPLACE INTO page(page, asked, read, used, json) VALUES(?, ?, ?, ?, ?)",
                 [.text(page), .text(asked), .real(at), .real(at), .blob(json)])
        trim()
    }

    // MARK: Bounds

    /// The kept rows' bytes: what `bounds.bytes` bounds. A running count, so it costs one row (#6643).
    public func bytes() -> Int {
        var total = 0
        try? rows("SELECT bytes FROM size") { s in total = Int(sqlite3_column_int64(s, 0)) }
        return total
    }

    /// The kept rows' bytes, summed over every row: what the running count must equal (the tests).
    func summedBytes() -> Int {
        var total = 0
        try? rows("""
        SELECT (SELECT ifnull(sum(length(json)), 0) FROM item) + (SELECT ifnull(sum(length(json)), 0) FROM answer)
          + (SELECT ifnull(sum(length(json)), 0) FROM detail) + (SELECT ifnull(sum(length(json)), 0) FROM page)
        """) { s in total = Int(sqlite3_column_int64(s, 0)) }
        return total
    }

    public func sweep() {
        trim()
    }

    public func stats() -> CacheStats {
        var stats = CacheStats(count: 0, bytes: bytes(), oldest: nil)
        for table in ["item", "answer", "detail", "page"] {
            try? rows("SELECT count(*), min(read) FROM \(table)") { s in
                let count = Int(sqlite3_column_int64(s, 0))
                guard count > 0 else { return }
                stats.count += count
                let oldest = Date(timeIntervalSince1970: sqlite3_column_double(s, 1))
                stats.oldest = min(stats.oldest ?? oldest, oldest)
            }
        }
        return stats
    }

    /// Ages records out before a draw, at most once an hour: the first draw can come before the first sweep.
    private func ageIfDue() {
        if let aged, abs(now().timeIntervalSince(aged)) < Self.sweepEvery { return }
        guard (try? run("BEGIN")) != nil else { return }
        let gone = ageOut()
        try? run("COMMIT")
        if gone { vacuum() }
    }

    /// Removes what was read longer than `bounds.age` ago (#6788); true when something went. In a transaction. Each
    /// table has an index on `read`, so it costs only what goes.
    private func ageOut() -> Bool {
        let at = now()
        aged = at
        let cutoff = at - bounds.age
        let before = bytes()
        try? run("DELETE FROM detail WHERE read < ?", [.real(cutoff)])
        try? run("DELETE FROM page WHERE read < ?", [.real(cutoff)])
        var old: [(query: String, host: String, kept: Kept?)] = []
        try? rows("SELECT query, host, json FROM answer WHERE read < ?", [.real(cutoff)]) { s in
            old.append((text(s, 0), text(s, 1), try? decoder.decode(Kept.self, from: blob(s, 2))))
        }
        for answer in old {
            if let kept = answer.kept?.since(cutoff), let json = try? encoder.encode(kept) {
                try? run("UPDATE answer SET read = ?, json = ? WHERE query = ? AND host = ?",
                         [.real(kept.oldest), .blob(json), .text(answer.query), .text(answer.host)])
            } else {
                try? run("DELETE FROM answer WHERE query = ? AND host = ?", [.text(answer.query), .text(answer.host)])
            }
        }
        // An item is read again with each page that holds it, so a kept page's items are never older than the page.
        var items: [String] = []
        try? rows("SELECT key FROM item WHERE read < ?", [.real(cutoff)]) { s in items.append(text(s, 0)) }
        try? run("DELETE FROM item WHERE read < ?", [.real(cutoff)])
        for key in items {
            memo[key] = nil
            edited.remove(key)
        }
        return bytes() != before
    }

    /// Gives the freed pages back. It is stepped to its end: each step frees only some of them.
    private func vacuum() {
        try? rows("PRAGMA incremental_vacuum") { _ in }
    }

    /// Writes when the answers were last drawn.
    private func writeDrawn() {
        for (key, at) in drawn {
            try? run("UPDATE answer SET used = ? WHERE query = ?", [.real(at), .text(key)])
        }
        drawn = [:]
    }

    /// Ages records out, then brings the file under `bounds.bytes`, to nine tenths of it so the next page does not trim again: bodies first,
    /// the least recently opened first; then the pages' answers, the least recently drawn first; then the items that
    /// no kept answer or body holds; then the lists' answers, the least recently drawn first, with the items that no
    /// other kept answer holds. One transaction, and each table is read once (#6643).
    func trim() {
        guard (try? run("BEGIN")) != nil else { return }
        writeDrawn()
        let gone = ageOut()
        var total = bytes()
        guard total > bounds.bytes else {
            try? run("COMMIT")
            if gone { vacuum() }
            return
        }
        defer {
            try? run("COMMIT")
            vacuum()
        }
        let target = bounds.bytes / 10 * 9
        var details: [(key: String, bytes: Int)] = []
        try? rows("SELECT key, length(json) FROM detail ORDER BY used") { s in
            details.append((text(s, 0), Int(sqlite3_column_int64(s, 1))))
        }
        for detail in details where total > target {
            try? run("DELETE FROM detail WHERE key = ?", [.text(detail.key)])
            total -= detail.bytes
        }
        var pages: [(page: String, asked: String, bytes: Int)] = []
        try? rows("SELECT page, asked, length(json) FROM page ORDER BY used") { s in
            pages.append((text(s, 0), text(s, 1), Int(sqlite3_column_int64(s, 2))))
        }
        for page in pages where total > target {
            try? run("DELETE FROM page WHERE page = ? AND asked = ?", [.text(page.page), .text(page.asked)])
            total -= page.bytes
        }
        guard total > target else { return }
        // How many kept answers and bodies hold each item: an item goes when the last one goes.
        var holders: [String: Int] = [:]
        var answers: [(query: String, host: String, bytes: Int, refs: Set<String>)] = []
        try? rows("SELECT query, host, length(json), json FROM answer ORDER BY used") { s in
            let refs = Set((try? decoder.decode(Kept.self, from: blob(s, 3)))?.pages.flatMap(\.refs) ?? [])
            for ref in refs { holders[ref, default: 0] += 1 }
            answers.append((text(s, 0), text(s, 1), Int(sqlite3_column_int64(s, 2)), refs))
        }
        try? rows("SELECT key FROM detail") { s in holders[text(s, 0), default: 0] += 1 }
        var sizes: [String: Int] = [:]
        try? rows("SELECT key, length(json) FROM item") { s in sizes[text(s, 0)] = Int(sqlite3_column_int64(s, 1)) }
        var drop: [String] = []
        for (key, size) in sizes where holders[key] == nil {
            drop.append(key)
            total -= size
        }
        for answer in answers where total > target {
            try? run("DELETE FROM answer WHERE query = ? AND host = ?", [.text(answer.query), .text(answer.host)])
            total -= answer.bytes
            for ref in answer.refs {
                holders[ref, default: 1] -= 1
                guard holders[ref] == 0, let size = sizes[ref] else { continue }
                drop.append(ref)
                total -= size
            }
        }
        let chunk = 500
        for start in stride(from: 0, to: drop.count, by: chunk) {
            let part = drop[start..<min(start + chunk, drop.count)]
            let marks = Array(repeating: "?", count: part.count).joined(separator: ",")
            try? run("DELETE FROM item WHERE key IN (\(marks))", part.map { .text($0) })
            for key in part { memo[key] = nil }
        }
    }

    public func forget() {
        try? run("DELETE FROM item")
        try? run("DELETE FROM answer")
        try? run("DELETE FROM detail")
        try? run("DELETE FROM page")
        vacuum()
        memo = [:]
        drawn = [:]
        edited = []
    }

    // MARK: SQLite

    private enum Value {
        case text(String), real(Date), blob(Data)
    }

    private static let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    private func prepare(_ sql: String, _ values: [Value]) throws -> OpaquePointer? {
        var statement: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &statement, nil) == SQLITE_OK else {
            throw CacheError(message: String(cString: sqlite3_errmsg(db)))
        }
        for (offset, value) in values.enumerated() {
            let index = Int32(offset + 1)
            switch value {
            case .text(let text): sqlite3_bind_text(statement, index, text, -1, Self.transient)
            case .real(let date): sqlite3_bind_double(statement, index, date.timeIntervalSince1970)
            case .blob(let data):
                _ = data.withUnsafeBytes { sqlite3_bind_blob(statement, index, $0.baseAddress, Int32(data.count), Self.transient) }
            }
        }
        return statement
    }

    private func run(_ sql: String, _ values: [Value] = []) throws {
        let statement = try prepare(sql, values)
        defer { sqlite3_finalize(statement) }
        let result = sqlite3_step(statement)
        guard result == SQLITE_DONE || result == SQLITE_ROW else {
            throw CacheError(message: String(cString: sqlite3_errmsg(db)))
        }
    }

    private func rows(_ sql: String, _ values: [Value] = [], _ each: (OpaquePointer?) throws -> Void) throws {
        let statement = try prepare(sql, values)
        defer { sqlite3_finalize(statement) }
        while sqlite3_step(statement) == SQLITE_ROW { try each(statement) }
    }

    private func text(_ s: OpaquePointer?, _ i: Int32) -> String {
        sqlite3_column_text(s, i).map { String(cString: $0) } ?? ""
    }

    private func blob(_ s: OpaquePointer?, _ i: Int32) -> Data {
        guard let bytes = sqlite3_column_blob(s, i) else { return Data() }
        return Data(bytes: bytes, count: Int(sqlite3_column_bytes(s, i)))
    }
}
