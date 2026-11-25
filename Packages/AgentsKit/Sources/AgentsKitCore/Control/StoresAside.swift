import Foundation

/// The whole stores Agents Host sets aside in the control plane's home (`store-before-*`), on
/// a store switch or a move back, and this Mac's control plane keys set aside on a move back
/// (`control-key-before-*`). Only the newest `kept` of each stay (#3671): a move back needs
/// only the last one, and each folder holds an audit, sealed secrets and server backups. The
/// folders also have a size and an age (#6769).
public enum StoresAside {
    public static let kept = 2
    /// The most bytes of all the folders together, but the newest is never deleted for size.
    public static let mostBytes = 1 << 30
    /// The most age of a folder, once the control plane here has taken its place.
    public static let folderAge: TimeInterval = 90 * 86_400
    public static let folderPrefix = "store-before-"
    public static let keyPrefix = "control-key-before-"
    /// The most age of a key no kept store needs (#6770).
    public static let keyAge: TimeInterval = 90 * 86_400

    /// When `name` was set aside, from its name: `store-before-<seconds>` (a switch),
    /// `store-before-<yyyy-mm-dd>-<seconds % 100000>` (a move back) or
    /// `control-key-before-<seconds>`. Nil for any other name.
    public static func time(of name: String) -> Date? {
        let rest: Substring
        if name.hasPrefix(folderPrefix) {
            rest = name.dropFirst(folderPrefix.count)
        } else if name.hasPrefix(keyPrefix) {
            rest = name.dropFirst(keyPrefix.count)
        } else {
            return nil
        }
        if let seconds = Int(rest) { return Date(timeIntervalSince1970: TimeInterval(seconds)) }
        // A day (UTC, as `.iso8601` writes it) and the seconds modulo 100,000: a day is
        // shorter than 100,000 seconds, so at most one time in that day fits.
        guard rest.count > 11, let low = Int(rest.dropFirst(11)), rest[rest.index(rest.startIndex, offsetBy: 10)] == "-",
              let day = try? Date(String(rest.prefix(10)), strategy: .iso8601.year().month().day()) else { return nil }
        let start = Int(day.timeIntervalSince1970)
        let seconds = start + ((low - start) % 100_000 + 100_000) % 100_000
        return Date(timeIntervalSince1970: TimeInterval(seconds < start + 86_400 ? seconds : start))
    }

    /// The names past the newest `keep`, oldest first. Names with no time are left alone.
    public static func beyond(_ names: [String], keep: Int = kept) -> [String] {
        let dated = names.compactMap { name in time(of: name).map { (name, $0) } }
            .sorted { $0.1 != $1.1 ? $0.1 > $1.1 : $0.0 > $1.0 }
        return dated.dropFirst(keep).reversed().map(\.0)
    }

    /// The keys among `names` to delete: those past the newest `keep`, and those at 90 days
    /// or older (#6770). A key's age waits while any store set aside is left, whatever its
    /// name's time: a store can be put back only with its key, and no name pairs the two.
    /// `stores` is nil when the stores could not be read, and then they may be there.
    public static func expiredKeys(_ names: [String], stores: [String]?, now: Date = Date(), keep: Int = kept) -> [String] {
        let keys = names.filter { $0.hasPrefix(keyPrefix) }
        let past = beyond(keys, keep: keep)
        guard let stores, !stores.contains(where: { $0.hasPrefix(folderPrefix) }) else { return past }
        let aged = keys.filter { key in time(of: key).map { now.timeIntervalSince($0) >= keyAge } ?? false }
        return past + aged.filter { !past.contains($0) }
    }

    /// Whether the control plane that runs on this Mac takes the place of the folders.
    public enum Plane: Sendable, Equatable {
        /// It serves here from its own store, and no move back is pending.
        case serving
        /// It moved away at that time, or a move back is not finished: nothing ages out, and
        /// the store it left here stays.
        case away(since: Date)
        /// Not known, or never moved: nothing is deleted.
        case unknown
    }

    /// A folder set aside, and its bytes; nil when they could not be counted.
    public struct Folder: Sendable {
        public let name: String
        public let bytes: Int?

        public init(name: String, bytes: Int?) {
            self.name = name
            self.bytes = bytes
        }
    }

    /// The folders to delete, oldest first (#6769): those past 90 days, the newest too; then
    /// the oldest past the newest `keep`, or past `mostBytes` in all. Count and size never
    /// take the newest. While the control plane is away nothing ages out, and the store it
    /// left here, the first set aside since, stays: a move back that starts again sets
    /// aside only an empty store. Names with no time are left alone, and a size
    /// that could not be counted deletes nothing for size.
    public static func expiredFolders(_ folders: [Folder], plane: Plane, now: Date = Date(),
                                      keep: Int = kept, mostBytes: Int = mostBytes) -> [String] {
        if plane == .unknown { return [] }
        let dated: [(folder: Folder, time: Date)] = folders
            .compactMap { folder in time(of: folder.name).map { (folder: folder, time: $0) } }
            .filter { $0.folder.name.hasPrefix(folderPrefix) }
            .sorted { $0.time != $1.time ? $0.time < $1.time : $0.folder.name < $1.folder.name }
        var gone: [String] = []
        var left = dated
        var held = Set(dated.suffix(1).map { $0.folder.name })
        switch plane {
        case .serving:
            gone = dated.filter { now.timeIntervalSince($0.time) >= folderAge }.map { $0.folder.name }
            left.removeAll { gone.contains($0.folder.name) }
        case .away(let since):
            // A name's time has whole seconds.
            let start = since.timeIntervalSince1970.rounded(.down)
            if let first = dated.first(where: { $0.time.timeIntervalSince1970 >= start }) { held.insert(first.folder.name) }
        case .unknown:
            break
        }
        while true {
            let sizes = left.compactMap { $0.folder.bytes }
            let tooLarge = sizes.count == left.count && sizes.reduce(0, +) > mostBytes
            guard left.count > keep || tooLarge,
                  let oldest = left.firstIndex(where: { !held.contains($0.folder.name) }) else { return gone }
            gone.append(left.remove(at: oldest).folder.name)
        }
    }

    // store-bounds: kind=blob; count=2; size=1GiB; age=90d; action=evict; record=1GiB; applied=on write and hourly, the newest by age only, and no age while the control plane is away
    /// Deletes the `store-before-*` folders in `home` that are past a limit. `home`'s own
    /// `store` with no records takes no folder's place. Answers the names deleted; one that
    /// could not be deleted is left for the next time.
    @discardableResult
    public static func pruneFolders(in home: URL, plane: Plane, now: Date = Date()) -> [String] {
        let files = FileManager.default
        if plane == .serving {
            let records = home.appendingPathComponent("store/v1").path
            guard let kept = try? files.contentsOfDirectory(atPath: records), !kept.isEmpty else { return [] }
        }
        let names = (try? files.contentsOfDirectory(atPath: home.path)) ?? []
        let folders = names.filter { $0.hasPrefix(folderPrefix) }
            .map { Folder(name: $0, bytes: bytes(of: home.appendingPathComponent($0))) }
        return expiredFolders(folders, plane: plane, now: now).filter {
            (try? files.removeItem(at: home.appendingPathComponent($0))) != nil
        }
    }

    /// The bytes of the files in `folder`, or nil when one cannot be read.
    static func bytes(of folder: URL) -> Int? {
        var unreadable = false
        let keys: Set<URLResourceKey> = [.isRegularFileKey, .fileSizeKey]
        guard let walk = FileManager.default.enumerator(at: folder, includingPropertiesForKeys: Array(keys), errorHandler: { _, _ in
            unreadable = true
            return false
        }) else { return nil }
        var total = 0
        for case let file as URL in walk {
            guard let values = try? file.resourceValues(forKeys: keys) else { return nil }
            if values.isRegularFile == true { total += values.fileSize ?? 0 }
        }
        return unreadable ? nil : total
    }
}
