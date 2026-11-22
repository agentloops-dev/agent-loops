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
/// - A refresh replaces page 1 and keeps the later pages, each with its own time, until they are read again.
///   Scrolling to the end of the kept pages asks the next page by the last page's cursor.
///
/// Nothing here syncs or sorts: the caller asks the forge and gives the answer to `keepPage`. The bounds are in
/// `WorkCache.Bounds`. SQLite is the platform's (`SQLite3`, on macOS and iOS), so this adds no package.
public protocol WorkCaching: Sendable {
    /// The kept answer to a query, with its items, or nil when it was never answered on this device.
    func answer(_ query: WorkCache.Query) async -> WorkCache.Answer?
    /// Keeps one page of a machine's answer: page 0 replaces the first page and keeps the later ones; the page after
    /// the last kept one is added; any other kept page is replaced. Its items replace older copies.
    func keepPage(_ query: WorkCache.Query, host: MachineRef, page: Int, list: DaemonAPI.ForgeList) async
    /// The machine did not answer the query: its kept pages stay, with the errors and when it failed.
    func keepFailure(_ query: WorkCache.Query, host: MachineRef, errors: [DaemonAPI.ForgeListError]) async
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
    /// Removes everything; the next read fills it again.
    func forget() async
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
    /// when it was read.
    public struct Page: Codable, Hashable, Sendable {
        public var refs: [String]
        public var cursor: String?
        public var read: Date
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
        /// The cursor to the page after the kept ones; nil when the kept pages reach the end.
        public var cursor: String? { pages.last?.cursor }
        /// As the wire says a list, for a caller that draws one.
        public var list: DaemonAPI.ForgeList {
            DaemonAPI.ForgeList(items: items, cursor: cursor, errors: errors, forges: forges, total: total)
        }
    }

    public struct Answer: Sendable {
        public var hosts: [HostAnswer]
        /// The oldest page 1 of the machines: what the list says it shows.
        public var read: Date? { hosts.compactMap(\.read).min() }
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

    /// The cache's bounds (#6352): an item's row, a body and its comments, a page's kept answer (#6573), and the whole
    /// file. Over `bytes`, bodies go first, then the pages' answers and the lists' answers, each the least recently
    /// drawn first, with the items no other answer holds.
    public struct Bounds: Sendable {
        public var itemBytes: Int
        public var bodyBytes: Int
        public var comments: Int
        /// A page's kept answer: one over it is not kept, so the page asks again next time.
        public var pageBytes: Int
        public var bytes: Int

        public init(itemBytes: Int = 4 * 1024, bodyBytes: Int = 64 * 1024, comments: Int = 50,
                    pageBytes: Int = 4 * 1024 * 1024, bytes: Int = 200 * 1024 * 1024) {
            self.itemBytes = itemBytes
            self.bodyBytes = bodyBytes
            self.comments = comments
            self.pageBytes = pageBytes
            self.bytes = bytes
        }

        /// The Mac's Work cache.
        public static let mac = Bounds()
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
    /// Items decoded once, by key: a draw after the first decodes nothing. The file stays the record.
    private var memo: [String: DaemonAPI.ForgeItem] = [:]
    /// The items an optimistic write changed, until it ends (#6354): a copy as old as theirs does not replace them.
    private var edited: Set<String> = []

    /// The cache in `file`, made when missing; nil keeps it in memory only (the tests).
    public init(file: URL?, bounds: Bounds = .mac, now: @escaping @Sendable () -> Date = { Date() }) throws {
        self.bounds = bounds
        self.now = now
        var opened: OpaquePointer?
        let flags = SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_NOMUTEX
        guard sqlite3_open_v2(file?.path ?? ":memory:", &opened, flags, nil) == SQLITE_OK else {
            let message = opened.map { String(cString: sqlite3_errmsg($0)) } ?? "out of memory"
            sqlite3_close_v2(opened)
            throw CacheError(message: "could not open \(file?.path ?? "memory"): \(message)")
        }
        handle = Handle(opened)
        for sql in [
            "PRAGMA auto_vacuum=INCREMENTAL",
            "PRAGMA journal_mode=WAL",
            "PRAGMA synchronous=NORMAL",
            "CREATE TABLE IF NOT EXISTS item(key TEXT PRIMARY KEY, updated REAL NOT NULL, read REAL NOT NULL, json BLOB NOT NULL)",
            "CREATE TABLE IF NOT EXISTS answer(query TEXT NOT NULL, host TEXT NOT NULL, used REAL NOT NULL, json BLOB NOT NULL, PRIMARY KEY(query, host))",
            "CREATE TABLE IF NOT EXISTS detail(key TEXT PRIMARY KEY, read REAL NOT NULL, used REAL NOT NULL, json BLOB NOT NULL)",
            "CREATE TABLE IF NOT EXISTS page(page TEXT NOT NULL, asked TEXT NOT NULL, read REAL NOT NULL, used REAL NOT NULL, json BLOB NOT NULL, PRIMARY KEY(page, asked))",
        ] where sqlite3_exec(opened, sql, nil, nil, nil) != SQLITE_OK {
            throw CacheError(message: String(cString: sqlite3_errmsg(opened)))
        }
    }

    // MARK: Answers

    public func answer(_ query: WorkCache.Query) -> WorkCache.Answer? {
        let key = query.key
        var kept: [(host: MachineRef, kept: Kept)] = []
        try? rows("SELECT host, json FROM answer WHERE query = ? ORDER BY host", [.text(key)]) { s in
            if let answer = try? decoder.decode(Kept.self, from: blob(s, 1)) {
                kept.append((MachineRef(rawValue: text(s, 0)), answer))
            }
        }
        guard !kept.isEmpty else { return nil }
        try? run("UPDATE answer SET used = ? WHERE query = ?", [.real(now()), .text(key)])
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
        var pages = kept?.pages ?? []
        guard page >= 0, page <= pages.count else { return }
        keep(items: list.items, at: at)
        let refs = list.items.map { Self.key($0.node) }.filter { memo[$0] != nil }
        let read = Page(refs: refs, cursor: list.cursor, read: at)
        if page == pages.count { pages.append(read) } else { pages[page] = read }
        let answer = Kept(pages: pages, total: list.total ?? kept?.total, forges: list.forges ?? kept?.forges,
                          errors: list.errors, failed: nil)
        write(answer, key: key, host: host, used: at)
        trim()
    }

    public func keepFailure(_ query: WorkCache.Query, host: MachineRef, errors: [DaemonAPI.ForgeListError]) {
        let key = query.key
        var answer = keptAnswer(key, host: host) ?? Kept(pages: [])
        answer.errors = errors
        answer.failed = now()
        write(answer, key: key, host: host, used: now())
    }

    private func keptAnswer(_ key: String, host: MachineRef) -> Kept? {
        var kept: Kept?
        try? rows("SELECT json FROM answer WHERE query = ? AND host = ?", [.text(key), .text(host.rawValue)]) { s in
            kept = try? decoder.decode(Kept.self, from: blob(s, 0))
        }
        return kept
    }

    private func write(_ answer: Kept, key: String, host: MachineRef, used: Date) {
        guard let json = try? encoder.encode(answer) else { return }
        try? run("INSERT OR REPLACE INTO answer(query, host, used, json) VALUES(?, ?, ?, ?)",
                 [.text(key), .text(host.rawValue), .real(used), .blob(json)])
    }

    // MARK: Items

    static func key(_ node: DaemonAPI.LineageNode) -> String { "\(node.kind) \(node.ref)" }

    public func keep(items: [DaemonAPI.ForgeItem]) {
        keep(items: items, at: now())
    }

    /// Writes items in one transaction. A row is replaced only by a copy as new or newer, so a late answer does not
    /// undo a newer one; an item whose row is over `bounds.itemBytes` after trimming is not kept.
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
            guard let fit = fitted(item) else { continue }
            edited.remove(key)
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
        let key = Self.key(node)
        load([key])
        return memo[key]
    }

    /// Decodes the keys' items that are not in memory yet, from the file.
    private func load(_ keys: [String]) {
        let missing = Array(Set(keys.filter { memo[$0] == nil }))
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
        var found: PageAnswer?
        try? rows("SELECT read, json FROM page WHERE page = ? AND asked = ?", [.text(page), .text(asked)]) { s in
            found = PageAnswer(json: blob(s, 1), read: Date(timeIntervalSince1970: sqlite3_column_double(s, 0)))
        }
        guard found != nil else { return nil }
        try? run("UPDATE page SET used = ? WHERE page = ? AND asked = ?", [.real(now()), .text(page), .text(asked)])
        return found
    }

    public func keep(_ page: String, asked: String, json: Data) {
        guard json.count <= bounds.pageBytes else {
            try? run("DELETE FROM page WHERE page = ? AND asked = ?", [.text(page), .text(asked)])
            return
        }
        let at = now()
        try? run("INSERT OR REPLACE INTO page(page, asked, read, used, json) VALUES(?, ?, ?, ?, ?)",
                 [.text(page), .text(asked), .real(at), .real(at), .blob(json)])
        trim()
    }

    // MARK: Bounds

    /// The kept rows' bytes: what `bounds.bytes` bounds.
    public func bytes() -> Int {
        var total = 0
        try? rows("""
        SELECT (SELECT ifnull(sum(length(json)), 0) FROM item) + (SELECT ifnull(sum(length(json)), 0) FROM answer)
          + (SELECT ifnull(sum(length(json)), 0) FROM detail) + (SELECT ifnull(sum(length(json)), 0) FROM page)
        """) { s in total = Int(sqlite3_column_int64(s, 0)) }
        return total
    }

    /// Brings the file under `bounds.bytes`, to nine tenths of it so the next page does not trim again: bodies first,
    /// the least recently opened first; then the pages' answers, the least recently drawn first; then the lists'
    /// answers, the least recently drawn first, with the items that no kept answer holds.
    func trim() {
        var total = bytes()
        guard total > bounds.bytes else { return }
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
        var answers: [(query: String, host: String)] = []
        try? rows("SELECT query, host FROM answer ORDER BY used") { s in answers.append((text(s, 0), text(s, 1))) }
        for answer in answers where total > target {
            try? run("DELETE FROM answer WHERE query = ? AND host = ?", [.text(answer.query), .text(answer.host)])
            dropUnheldItems()
            total = bytes()
        }
        try? run("PRAGMA incremental_vacuum")
    }

    /// Deletes the items that no kept answer or page holds.
    private func dropUnheldItems() {
        var held = Set<String>()
        try? rows("SELECT json FROM answer") { s in
            if let kept = try? decoder.decode(Kept.self, from: blob(s, 0)) {
                held.formUnion(kept.pages.flatMap(\.refs))
            }
        }
        try? rows("SELECT key FROM detail") { s in held.insert(text(s, 0)) }
        var all: [String] = []
        try? rows("SELECT key FROM item") { s in all.append(text(s, 0)) }
        for key in all where !held.contains(key) {
            try? run("DELETE FROM item WHERE key = ?", [.text(key)])
            memo[key] = nil
        }
    }

    public func forget() {
        try? run("DELETE FROM item")
        try? run("DELETE FROM answer")
        try? run("DELETE FROM detail")
        try? run("DELETE FROM page")
        try? run("PRAGMA incremental_vacuum")
        memo = [:]
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
