import Foundation

/// A client store of the **cache** kind (#6669, docs/explanation/data-stores.md): data this device can delete and read
/// again. It evicts; it never refuses a write because it is full. A miss is always a valid answer.
public protocol CacheStore: Sendable {
    /// Brings the store under its bounds: the age first, then the size. The store runs it each hour itself.
    func sweep() async
    /// What the store holds now, for its health.
    func stats() async -> CacheStats
    /// Removes everything; the next read fills it again.
    func forget() async
}

/// The size of a cache: never what it holds.
public struct CacheStats: Equatable, Sendable {
    public var count: Int
    public var bytes: Int
    /// When the oldest record was read from its source; nil when the cache is empty.
    public var oldest: Date?
}

/// The longest that any cache keeps a record: 90 days (#6669, the time rule).
public let cacheAgeLimit: TimeInterval = 90 * 24 * 60 * 60
