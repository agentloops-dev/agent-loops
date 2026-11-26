import Foundation

/// How large each declared data store is (#6906, epic #6669), as last measured: sizes and
/// counts only. A machine answers for its own stores, the control plane for its own and the
/// clients'. Reports on the web page reads them.
public extension DaemonAPI {
    enum StoreOwner: String, Codable, Sendable, Hashable, CaseIterable {
        case machine, controlPlane, client
    }

    /// What happens to a store over its limit (#7408): a write refused, the next write, or
    /// the hourly or daily sweep removes what is past it.
    enum StoreAtLimit: String, Codable, Sendable, Hashable, CaseIterable {
        case refuse, write, hourly, daily
    }

    enum StoreSizeState: String, Codable, Sendable, Hashable, CaseIterable {
        case current, over, stale, unavailable
    }

    /// One store. A measure or a percent that is not known is nil. With `limitPer`, the limits
    /// are for each such part and the percents are of the largest one.
    struct StoreSize: Codable, Sendable, Hashable, Identifiable {
        public var id: String
        public var owner: StoreOwner
        public var scope: String
        public var kind: String
        public var state: StoreSizeState
        public var why: String?
        public var bytes: Int?
        public var records: Int?
        public var averageRecordBytes: Int?
        public var maxBytes: Int?
        public var maxRecords: Int?
        public var limitPer: String?
        public var percentBytes: Double?
        public var percentRecords: Double?
        public var measuredAt: Date?
        public var atLimit: StoreAtLimit?
    }

    /// A page of at most 100, by id. `cursor` asks for the next; nil on the last.
    struct StoreSizes: Codable, Sendable, Hashable {
        public var stores: [StoreSize]
        public var total: Int
        public var cursor: String?
    }

    /// `limit` is 1 to 100, 100 when nil.
    struct StoreSizesQuery: Codable, Sendable, Hashable {
        public var cursor: String?
        public var limit: Int?
    }
}

public extension DaemonAPI.Method {
    /// `StoreSizesQuery` → `StoreSizes`: a machine's stores.
    static let storesList = "stores/list"
    /// `StoreSizesQuery` → `StoreSizes`: the control plane's, for an admin, and the clients'.
    static let reportsStoreSizes = "reports/storeSizes"
}
