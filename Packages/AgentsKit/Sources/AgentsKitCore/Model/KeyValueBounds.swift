import Foundation

/// A client's key-value store with a limit (#6669, docs/explanation/data-stores.md, #7252): a write to `UserDefaults` of
/// anything but one fixed number, mark or id goes through `putEntry`, so each entry has a largest size, and
/// `scripts/check-store-bounds.py` sees each store's declaration at its call. The web page's is `Web/src/model/keyValue.ts`.
///
/// It refuses and never evicts. A refused write changes nothing that is kept; what it was for holds while the app runs.
extension UserDefaults {
    /// Puts `value` under `key` when it is at most `most` bytes; whether it was kept.
    @discardableResult
    public func putEntry(_ value: String, forKey key: String, most: Int) -> Bool {
        put(value, value.utf8.count, key, most)
    }

    @discardableResult
    public func putEntry(_ value: Data, forKey key: String, most: Int) -> Bool {
        put(value, value.count, key, most)
    }

    /// A list, measured as the bytes of its names.
    @discardableResult
    public func putEntry(_ value: [String], forKey key: String, most: Int) -> Bool {
        put(value, value.reduce(0) { $0 + $1.utf8.count }, key, most)
    }

    /// A map, measured as the bytes of its keys and values.
    @discardableResult
    public func putEntry(_ value: [String: String], forKey key: String, most: Int) -> Bool {
        put(value, value.reduce(0) { $0 + $1.key.utf8.count + $1.value.utf8.count }, key, most)
    }

    private func put(_ value: Any, _ bytes: Int, _ key: String, _ most: Int) -> Bool {
        guard bytes <= most else { return false }
        set(value, forKey: key) // store-bounds: not a store: each caller of putEntry declares its own
        return true
    }

    /// Whether an entry can be kept under `key`: it is there already, or fewer than `most` keys pass `counted`. It reads
    /// every key, so only a new entry pays for it.
    public func hasRoom(forKey key: String, most: Int, counted: (String) -> Bool) -> Bool {
        object(forKey: key) != nil || dictionaryRepresentation().keys.lazy.filter(counted).count < most
    }
}

/// The limits of what the Mac window and the Remote keep in defaults.
public enum KeyValueBounds {
    /// A list of names: at most `count` of `bytes` each.
    public struct Names: Sendable {
        public let count: Int
        public let bytes: Int

        /// The names that are kept: the ones there first stay.
        public func kept(_ names: some Sequence<String>) -> [String] {
            Array(names.lazy.filter { $0.utf8.count <= bytes }.prefix(count))
        }

        /// The most bytes of the whole list.
        public var total: Int { count * bytes }
    }

    /// The folds of one scope that are open, and as many that are closed.
    public static let folds = Names(count: 1_000, bytes: 2_048)
    /// New Session's last folder for each project: an id and a folder together are one entry.
    public static let projectFolders = Names(count: 1_000, bytes: 2_048)
    /// One folder (`host|path`), by itself.
    public static let folder = 4_096
    /// The achievements whose card was shown, and the runtimes that the start-up sheet offered.
    public static let shown = Names(count: 100, bytes: 64)
    /// The folder windows' widths and panes.
    public static let folderWindows = 2_000
    /// A Work page's filters, sort and range.
    public static let workFilters = 16 * 1024
    /// This Mac's host, as the control plane last listed it.
    public static let thisMacHost = 64 * 1024
    /// A choice that the host keeps, mirrored here; also the runtime a start form had.
    public static let choice = 4_096
    /// A machine-lock pin, with its name; and how many pins and recovery marks.
    public static let lockPin = 1_024
    public static let lockPins = 5_000
    /// The whole lock last checked, beside its pin; and how many.
    public static let lock = 1024 * 1024
    public static let locks = 100
    /// The dates of the browser pane's sites.
    public static let browserSites = 512 * 1024
}
