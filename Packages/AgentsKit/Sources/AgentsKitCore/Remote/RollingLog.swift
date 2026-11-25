import Foundation

/// A device's own log, kept in a file that can be read off it after the fact (#175).
///
/// The moment worth reading is the one nobody was watching: a Remote stuck until it is
/// killed takes its console with it. Each line is stamped, and the file is kept small:
/// past `limit` bytes it becomes `<name>.1` (the one before that is dropped) and a new
/// file is begun, so the two together never pass twice the limit: a line
/// is cut to a quarter of it. A file holds the lines of 45 days at most, so a line goes by 90 days (#7252).
///
/// Lines are written in order on a queue of the log's own, never on the caller's thread.
public final class RollingLog: @unchecked Sendable {
    public let url: URL
    public let limit: Int
    private let queue: DispatchQueue
    private let stamp = Date.ISO8601FormatStyle(includingFractionalSeconds: true)

    public init(url: URL, limit: Int = 256 * 1024) {
        self.url = url
        self.limit = max(1_024, limit)
        queue = DispatchQueue(label: "com.alexecollins.agents.log.\(url.lastPathComponent)")
        queue.async { [self] in age(Date()) }
    }

    /// The most time one file holds.
    static let fileAge: TimeInterval = 45 * 86_400

    /// A line of at most `most` bytes: a longer one is cut at a character and says so.
    static func cut(_ line: String, to most: Int) -> String {
        guard line.utf8.count > most else { return line }
        let mark = "… (cut)"
        return WorkCache.prefix(line, bytes: most - mark.utf8.count) + mark
    }

    /// The file before this one.
    public var previous: URL {
        url.deletingPathExtension().appendingPathExtension("1." + (url.pathExtension.isEmpty ? "log" : url.pathExtension))
    }

    public func append(_ line: String, at date: Date = Date()) {
        let stamped = Data((date.formatted(stamp) + " " + Self.cut(line, to: limit / 4) + "\n").utf8)
        queue.async { [self] in write(stamped, at: date) }
    }

    /// Waits until every line appended so far is on disk.
    public func flush() {
        queue.sync {}
    }

    /// Drops the file before this one when its last line is 45 days old, and begins a new file when this one was begun
    /// 45 days ago.
    private func age(_ now: Date) {
        let files = FileManager.default
        let old = { (date: Any?) in (date as? Date).map { now.timeIntervalSince($0) >= Self.fileAge } ?? false }
        if old((try? files.attributesOfItem(atPath: previous.path))?[.modificationDate]) { try? files.removeItem(at: previous) }
        guard let this = try? files.attributesOfItem(atPath: url.path) else { return }
        if old(this[.modificationDate]) {
            try? files.removeItem(at: url)
        } else if old(this[.creationDate]) {
            try? files.removeItem(at: previous)
            try? files.moveItem(at: url, to: previous)
        }
    }

    private func write(_ data: Data, at now: Date) {
        let files = FileManager.default
        age(now)
        let size = ((try? files.attributesOfItem(atPath: url.path))?[.size] as? NSNumber)?.intValue ?? 0
        if size > 0, size + data.count > limit {
            try? files.removeItem(at: previous)
            try? files.moveItem(at: url, to: previous)
        }
        guard let handle = try? FileHandle(forWritingTo: url) else {
            try? files.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            // store-bounds: kind=log; size=512KiB; age=90d; action=evict; record=64KiB; applied=on write and at start
            try? data.write(to: url)
            return
        }
        defer { try? handle.close() }
        _ = try? handle.seekToEnd()
        try? handle.write(contentsOf: data)
    }
}
