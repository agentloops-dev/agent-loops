import Foundation
import SQLite3
import os
import AgentsKitCore

/// A proof of concept (#6358, epic #6348): Work ▸ Issues and Pull requests read only this device's SQLite cache of forge
/// items, one row per item, and a sync fills it in the background. The cache can be deleted at any time: the next sync
/// fills it again. Off unless the default `WorkLocalCachePoC` is on.
///
/// The sync needs no new wire method: `forge/listAll` gives each machine's items newest `updatedAt` first, so a read
/// that stops at the first page older than the cursor (less one minute of overlap) is a "changed since" read.
actor WorkCache {
    /// The default that turns the proof of concept on: `defaults write <bundle id> WorkLocalCachePoC -bool YES`.
    static let flag = "WorkLocalCachePoC"
    /// One line for each draw and sync, in the unified log (category work-cache): the PoC's figures.
    nonisolated static func note(_ line: String) {
        Logger(subsystem: "com.alexecollins.agents", category: "work-cache").notice("\(line, privacy: .public)")
    }

    @MainActor static var enabled: Bool { WindowDefaults.store.bool(forKey: flag) }

    /// The Mac window's cache: in its root's cache folder, so a scratch root has its own.
    static let shared: WorkCache? = {
        let folder = StoreLocations.default.cacheRoot
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        return try? WorkCache(file: folder.appendingPathComponent("work-cache.sqlite"))
    }()

    /// What the cache knows of one machine's list of one kind.
    struct Sync: Sendable {
        /// The newest `updatedAt` the machine sent: the forge's time, not this device's clock.
        var cursor: Date?
        /// The first fill read to the end.
        var filled: Bool
        /// When this device last finished a sync of it.
        var syncedAt: Date?
        var forges: [DaemonAPI.ForgeCapabilities]?
    }

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
            """
            CREATE TABLE IF NOT EXISTS sync(kind TEXT NOT NULL, host TEXT NOT NULL, cursor REAL, filled INTEGER NOT NULL,
              synced REAL, forges BLOB, PRIMARY KEY(kind, host))
            """,
        ] where sqlite3_exec(opened, sql, nil, nil, nil) != SQLITE_OK {
            throw CacheError(message: String(cString: sqlite3_errmsg(opened)))
        }
    }

    struct CacheError: Error { var message: String }

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

    /// Every cached item of a kind, as one list per machine, newest first. A read of an index: no forge call.
    func lists(_ kind: WorkList.Kind) -> [(host: MachineRef, list: DaemonAPI.ForgeList)] {
        guard let s = try? prepare("SELECT host, json FROM item WHERE kind = ? ORDER BY updated DESC") else { return [] }
        defer { sqlite3_finalize(s) }
        bind(s, 1, kind.rawValue)
        var items: [String: [DaemonAPI.ForgeItem]] = [:]
        var order: [String] = []
        while sqlite3_step(s) == SQLITE_ROW {
            guard let text = sqlite3_column_text(s, 0) else { continue }
            let host = String(cString: text)
            guard let item = try? decoder.decode(DaemonAPI.ForgeItem.self, from: column(s, 1)) else { continue }
            if items[host] == nil { order.append(host) }
            items[host, default: []].append(item)
        }
        let syncs = order.map { (host: $0, sync: sync(kind, MachineRef(rawValue: $0))) }
        return syncs.map { entry in
            (MachineRef(rawValue: entry.host), DaemonAPI.ForgeList(items: items[entry.host] ?? [], forges: entry.sync.forges,
                                                                    total: items[entry.host]?.count))
        }
    }

    func count(_ kind: WorkList.Kind) -> Int {
        guard let s = try? prepare("SELECT count(*) FROM item WHERE kind = ?") else { return 0 }
        defer { sqlite3_finalize(s) }
        bind(s, 1, kind.rawValue)
        return sqlite3_step(s) == SQLITE_ROW ? Int(sqlite3_column_int64(s, 0)) : 0
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
        for item in items {
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
        guard let json = try? encoder.encode(item),
              let u = try? prepare("UPDATE item SET json = ? WHERE kind = ? AND host = ? AND ref = ?") else { return }
        defer { sqlite3_finalize(u) }
        bind(u, 1, json)
        bind(u, 2, kind.rawValue)
        bind(u, 3, host.rawValue)
        bind(u, 4, ref)
        sqlite3_step(u)
    }

    func sync(_ kind: WorkList.Kind, _ host: MachineRef) -> Sync {
        guard let s = try? prepare("SELECT cursor, filled, synced, forges FROM sync WHERE kind = ? AND host = ?") else {
            return Sync(filled: false)
        }
        defer { sqlite3_finalize(s) }
        bind(s, 1, kind.rawValue)
        bind(s, 2, host.rawValue)
        guard sqlite3_step(s) == SQLITE_ROW else { return Sync(filled: false) }
        func date(_ i: Int32) -> Date? {
            sqlite3_column_type(s, i) == SQLITE_NULL ? nil : Date(timeIntervalSince1970: sqlite3_column_double(s, i))
        }
        let forges = sqlite3_column_type(s, 3) == SQLITE_NULL ? nil
            : try? decoder.decode([DaemonAPI.ForgeCapabilities].self, from: column(s, 3))
        return Sync(cursor: date(0), filled: sqlite3_column_int(s, 1) != 0, syncedAt: date(2), forges: forges)
    }

    func setSync(_ kind: WorkList.Kind, _ host: MachineRef, _ sync: Sync) {
        guard let s = try? prepare("""
        INSERT OR REPLACE INTO sync(kind, host, cursor, filled, synced, forges) VALUES(?, ?, ?, ?, ?, ?)
        """) else { return }
        defer { sqlite3_finalize(s) }
        bind(s, 1, kind.rawValue)
        bind(s, 2, host.rawValue)
        if let cursor = sync.cursor { sqlite3_bind_double(s, 3, cursor.timeIntervalSince1970) } else { sqlite3_bind_null(s, 3) }
        sqlite3_bind_int(s, 4, sync.filled ? 1 : 0)
        if let at = sync.syncedAt { sqlite3_bind_double(s, 5, at.timeIntervalSince1970) } else { sqlite3_bind_null(s, 5) }
        if let forges = sync.forges, let json = try? encoder.encode(forges) { bind(s, 6, json) } else { sqlite3_bind_null(s, 6) }
        sqlite3_step(s)
    }

    /// The overlap before the cursor: forge times have a precision of one second, and items can share one.
    static let overlap: TimeInterval = 60
    /// At most this many pages of 100 in one sync, so a first fill of a big project goes on in the next one.
    static let pagesPerSync = 20

    /// Whether a machine's page reached what the cache has: its oldest item is older than the cursor less the overlap.
    nonisolated static func reachedCursor(_ items: [DaemonAPI.ForgeItem], cursor: Date?) -> Bool {
        guard let cursor, let oldest = items.map(\.updatedAt).min() else { return false }
        return oldest < cursor.addingTimeInterval(-overlap)
    }
}
