import Foundation
import SQLite3
import os
import AgentsKitCore

/// A proof of concept (#6358, epic #6348): Work ▸ Issues and Pull requests read only this device's SQLite cache of forge
/// items, one row per item, and a sync fills it in the background. The cache can be deleted at any time: the next sync
/// fills it again. Off unless the default `WorkLocalCachePoC` is on.
///
/// Alex's model (2026-11-19, epic #6348): items are kept by id, and each query's answer is kept as the ids it returned,
/// in the server's order, page by page with each page's cursor. A list draws its kept answer at once, then asks the
/// query again; a page more is read when the list scrolls to its end. When the machine does not answer, the kept answer
/// stays. Nothing syncs in the background, and nothing is sorted here: the order is the server's.
actor WorkCache {
    /// The default that turns the proof of concept on: `defaults write <bundle id> WorkLocalCachePoC -bool YES`.
    static let flag = "WorkLocalCachePoC"
    /// One line for each draw and sync, in the unified log (category work-cache): the PoC's figures.
    nonisolated static func note(_ line: String) {
        Logger(subsystem: "com.alexecollins.agents", category: "work-cache").notice("\(line, privacy: .public)")
    }

    @MainActor static var enabled: Bool { WindowDefaults.store.bool(forKey: flag) }

    /// The window's cache, in the cache folder. A walk's or another root's copy of the window keeps a file of its own,
    /// named by its defaults domain, so it never writes the person's (run-app's `stop.sh` leaves it: delete it by hand).
    static let shared: WorkCache? = {
        let folder = StoreLocations.default.cacheRoot
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        #if os(macOS)
        let name = WindowDefaults.scratchDomain.map { "work-cache.\($0).sqlite" } ?? "work-cache.sqlite"
        #else
        let name = "work-cache.sqlite"
        #endif
        return try? WorkCache(file: folder.appendingPathComponent(name))
    }()

    /// The open database, closed when the cache goes.
    private final class Handle: @unchecked Sendable {
        let db: OpaquePointer?
        init(_ db: OpaquePointer?) { self.db = db }
        deinit { sqlite3_close_v2(db) }
    }

    private let handle: Handle
    private var db: OpaquePointer? { handle.db }
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()
    /// The decoded items of each kind, by machine and ref, read from the database once: a draw after the first decodes
    /// nothing (decoding 1,000 items took 1.2 to 2 s in a Debug build). The database stays the record.
    private var memo: [WorkList.Kind: [MachineRef: [String: DaemonAPI.ForgeItem]]] = [:]

    init(file: URL) throws {
        var opened: OpaquePointer?
        guard sqlite3_open_v2(file.path, &opened, SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_NOMUTEX, nil) == SQLITE_OK
        else {
            sqlite3_close_v2(opened)
            throw CacheError(message: "could not open \(file.path)")
        }
        handle = Handle(opened)
        for sql in [
            "PRAGMA journal_mode=WAL",
            "PRAGMA synchronous=NORMAL",
            """
            CREATE TABLE IF NOT EXISTS item(kind TEXT NOT NULL, host TEXT NOT NULL, ref TEXT NOT NULL, state TEXT NOT NULL,
              updated REAL NOT NULL, synced REAL NOT NULL, json BLOB NOT NULL, PRIMARY KEY(kind, host, ref))
            """,
            "CREATE INDEX IF NOT EXISTS item_updated ON item(kind, updated DESC)",
            "CREATE TABLE IF NOT EXISTS answer(query TEXT PRIMARY KEY, kind TEXT NOT NULL, read REAL NOT NULL, json BLOB NOT NULL)",
        ] where sqlite3_exec(opened, sql, nil, nil, nil) != SQLITE_OK {
            throw CacheError(message: String(cString: sqlite3_errmsg(opened)))
        }
    }

    struct CacheError: LocalizedError {
        var message: String
        var errorDescription: String? { message }
    }

    private func exec(_ sql: String) throws {
        guard sqlite3_exec(db, sql, nil, nil, nil) == SQLITE_OK else {
            throw CacheError(message: String(cString: sqlite3_errmsg(db)))
        }
    }

    private func prepare(_ sql: String) throws -> OpaquePointer? {
        var statement: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &statement, nil) == SQLITE_OK else {
            throw CacheError(message: String(cString: sqlite3_errmsg(db)))
        }
        return statement
    }

    private static let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    private func bind(_ s: OpaquePointer?, _ i: Int32, _ text: String) {
        sqlite3_bind_text(s, i, text, -1, Self.transient)
    }

    private func bind(_ s: OpaquePointer?, _ i: Int32, _ data: Data) {
        _ = data.withUnsafeBytes { sqlite3_bind_blob(s, i, $0.baseAddress, Int32(data.count), Self.transient) }
    }

    private func column(_ s: OpaquePointer?, _ i: Int32) -> Data {
        guard let bytes = sqlite3_column_blob(s, i) else { return Data() }
        return Data(bytes: bytes, count: Int(sqlite3_column_bytes(s, i)))
    }

    /// The kind's items from memory, read from the database the first time.
    private func items(_ kind: WorkList.Kind) -> [MachineRef: [String: DaemonAPI.ForgeItem]] {
        if let had = memo[kind] { return had }
        var byHost: [MachineRef: [String: DaemonAPI.ForgeItem]] = [:]
        if let s = try? prepare("SELECT host, json FROM item WHERE kind = ?") {
            bind(s, 1, kind.rawValue)
            while sqlite3_step(s) == SQLITE_ROW {
                guard let text = sqlite3_column_text(s, 0),
                      let item = try? decoder.decode(DaemonAPI.ForgeItem.self, from: column(s, 1)) else { continue }
                byHost[MachineRef(rawValue: String(cString: text)), default: [:]][item.node.ref] = item
            }
            sqlite3_finalize(s)
        }
        memo[kind] = byHost
        return byHost
    }

    /// A query's answer as this device keeps it: each machine's items in the machine's order, by ref (the items
    /// themselves are kept once, in `item`), its total, its forges and what could not be read.
    struct Answer: Codable, Sendable {
        var hosts: [MachineRef]
        var refs: [[String]]
        var totals: [Int?]
        var forges: [[DaemonAPI.ForgeCapabilities]?]
        var errors: [[DaemonAPI.ForgeListError]?]
        /// Each machine's cursor to its next page, nil at the end.
        var cursors: [String?]?
    }

    /// The kept answer to a query, as one list per machine, with when it was read; nil when it was never asked.
    func answer(_ query: String, kind: WorkList.Kind) -> (lists: [(host: MachineRef, list: DaemonAPI.ForgeList)], read: Date)? {
        guard let s = try? prepare("SELECT read, json FROM answer WHERE query = ?") else { return nil }
        defer { sqlite3_finalize(s) }
        bind(s, 1, query)
        guard sqlite3_step(s) == SQLITE_ROW,
              let kept = try? decoder.decode(Answer.self, from: column(s, 1)) else { return nil }
        let read = Date(timeIntervalSince1970: sqlite3_column_double(s, 0))
        let byHost = items(kind)
        let lists = kept.hosts.indices.map { i in
            let host = kept.hosts[i]
            let items = kept.refs[i].compactMap { byHost[host]?[$0] }
            return (host, DaemonAPI.ForgeList(items: items, cursor: kept.cursors?[i] ?? nil, errors: kept.errors[i],
                                              forges: kept.forges[i], total: kept.totals[i]))
        }
        return (lists, read)
    }

    /// Keeps a query's answer: its items in `item` (a newer one is never replaced by an older), the refs in order here.
    func keep(_ query: String, kind: WorkList.Kind, lists: [(host: MachineRef, list: DaemonAPI.ForgeList)], read: Date) {
        for entry in lists { apply(kind, host: entry.host, items: entry.list.items) }
        let kept = Answer(hosts: lists.map(\.host), refs: lists.map { $0.list.items.map(\.node.ref) },
                          totals: lists.map(\.list.total), forges: lists.map(\.list.forges), errors: lists.map(\.list.errors),
                          cursors: lists.map(\.list.cursor))
        guard let json = try? encoder.encode(kept),
              let s = try? prepare("INSERT OR REPLACE INTO answer(query, kind, read, json) VALUES(?, ?, ?, ?)") else { return }
        defer { sqlite3_finalize(s) }
        bind(s, 1, query)
        bind(s, 2, kind.rawValue)
        sqlite3_bind_double(s, 3, read.timeIntervalSince1970)
        bind(s, 4, json)
        sqlite3_step(s)
    }

    /// Writes the items a machine sent. An item's row is replaced only by one as new or newer, so a late answer does
    /// not undo a newer one. One transaction a page.
    func apply(_ kind: WorkList.Kind, host: MachineRef, items: [DaemonAPI.ForgeItem]) {
        guard !items.isEmpty, (try? exec("BEGIN")) != nil else { return }
        defer { try? exec("COMMIT") }
        guard let s = try? prepare("""
        INSERT INTO item(kind, host, ref, state, updated, synced, json) VALUES(?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(kind, host, ref) DO UPDATE SET state = excluded.state, updated = excluded.updated,
          synced = excluded.synced, json = excluded.json WHERE excluded.updated >= item.updated
        """) else { return }
        defer { sqlite3_finalize(s) }
        let now = Date().timeIntervalSince1970
        var held = self.items(kind)
        for item in items {
            if let had = held[host]?[item.node.ref], had.updatedAt > item.updatedAt { continue }
            held[host, default: [:]][item.node.ref] = item
            guard let json = try? encoder.encode(item) else { continue }
            sqlite3_reset(s)
            bind(s, 1, kind.rawValue)
            bind(s, 2, host.rawValue)
            bind(s, 3, item.node.ref)
            bind(s, 4, item.state)
            sqlite3_bind_double(s, 5, item.updatedAt.timeIntervalSince1970)
            sqlite3_bind_double(s, 6, now)
            bind(s, 7, json)
            sqlite3_step(s)
        }
        memo[kind] = held
    }

    /// Changes one cached item at once (an optimistic write), keeping its `updatedAt`, so the forge's answer replaces it.
    func edit(_ kind: WorkList.Kind, host: MachineRef, ref: String, _ change: @Sendable (inout DaemonAPI.ForgeItem) -> Void) {
        guard let s = try? prepare("SELECT json FROM item WHERE kind = ? AND host = ? AND ref = ?") else { return }
        bind(s, 1, kind.rawValue)
        bind(s, 2, host.rawValue)
        bind(s, 3, ref)
        let found = sqlite3_step(s) == SQLITE_ROW ? column(s, 0) : nil
        sqlite3_finalize(s)
        guard let found, var item = try? decoder.decode(DaemonAPI.ForgeItem.self, from: found) else { return }
        change(&item)
        if memo[kind] != nil { memo[kind]?[host]?[ref] = item }
        guard let json = try? encoder.encode(item),
              let u = try? prepare("UPDATE item SET json = ? WHERE kind = ? AND host = ? AND ref = ?") else { return }
        defer { sqlite3_finalize(u) }
        bind(u, 1, json)
        bind(u, 2, kind.rawValue)
        bind(u, 3, host.rawValue)
        bind(u, 4, ref)
        sqlite3_step(u)
    }
}
