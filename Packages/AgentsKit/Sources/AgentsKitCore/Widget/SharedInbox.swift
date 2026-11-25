// Not on Linux: the server build of agentsd has no app groups and no share sheet (037).
#if canImport(Darwin)
import Foundation
import UniformTypeIdentifiers

/// What the share sheet handed to Agents (#4915), waiting for New session to take it.
///
/// Words (selected text, a link) go into the prompt; pictures and files go as attachments,
/// by the paperclip's rules (`PhoneAttachment`); what the paperclip would refuse is kept as
/// its refusal, so New session says it in the paperclip's words. Several shares before the
/// Remote opens all wait here, in the order they were shared.
public struct SharedItems: Codable, Equatable, Sendable {
    public var texts: [String]
    public var attachments: [Attachment]
    public var refusals: [String]

    public init(texts: [String] = [], attachments: [Attachment] = [], refusals: [String] = []) {
        self.texts = texts
        self.attachments = attachments
        self.refusals = refusals
    }

    public var isEmpty: Bool { texts.isEmpty && attachments.isEmpty && refusals.isEmpty }

    /// Words, or a link, shared. Blank ones are nothing.
    public mutating func add(text: String) {
        let words = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !words.isEmpty else { return }
        texts.append(words)
    }

    /// A picture shared from Photos, a screenshot or a page, shrunk as the paperclip does.
    public mutating func add(picture data: Data, name: String) {
        take(PhoneAttachment.picture(data, name: name))
    }

    /// Why a file of this size is refused before it is read, kept as a refusal; or nil, when
    /// it may be read (#930).
    public mutating func refuse(name: String, size: Int, type: UTType?) -> Bool {
        guard let refused = PhoneAttachment.sizeRefusal(name: name, size: size, type: type) else { return false }
        refusals.append(refused.sentence)
        return true
    }

    /// A file shared: a picture as a picture, text as its contents, anything else refused.
    public mutating func add(file data: Data, name: String, type: UTType?) {
        take(PhoneAttachment.file(data, name: name, type: type))
    }

    /// Something shared that could not be read at all.
    public mutating func unreadable(_ name: String) {
        refusals.append("\(name) could not be read.")
    }

    /// These, after those already waiting.
    public mutating func append(_ other: SharedItems) {
        texts += other.texts
        attachments += other.attachments
        refusals += other.refusals
    }

    /// The prompt's words with what was shared after them, a blank line between each, as a
    /// quote is given back to a prompt.
    public func text(after typed: String) -> String {
        let kept = typed.trimmingCharacters(in: .whitespacesAndNewlines)
        return ([kept].filter { !$0.isEmpty } + texts).joined(separator: "\n\n")
    }

    /// The paperclip's words for what was refused, one sentence each, or nil.
    public var note: String? { refusals.isEmpty ? nil : refusals.joined(separator: " ") }

    private mutating func take(_ result: Result<Attachment, PhoneAttachment.Refusal>) {
        switch result {
        case .success(let attachment): attachments.append(attachment)
        case .failure(let refused): refusals.append(refused.sentence)
        }
    }
}

/// Where the share extension leaves `SharedItems` for the Remote (#4915): one file in the
/// Remote's app group, which the extension adds to and the Remote takes whole.
///
/// The group's `Library/Application Support`, not `Caches`: what was shared waits there until
/// the person opens the Remote, which may be days, and the system may empty a cache.
/// Both sides go through a file coordinator, so a share that lands while the Remote takes
/// is either taken or left for the next time, never lost.
///
/// Bounded (#6795): `most` shares and `bytesMost` in all. It is the person's own data, so a share
/// over either is refused, with a message in the share sheet, and nothing goes to make room. A
/// share not taken in `ageLimit` is deleted, and the next `take` says so.
public enum SharedInbox {
    public static let fileName = "shared-inbox.json"
    /// The most shares that wait.
    public static let most = 100
    /// The most bytes of the file, and so of one share: both sides read it whole, and a share
    /// extension has little memory.
    public static let bytesMost = 10_000_000
    public static let ageLimit: TimeInterval = 90 * 24 * 60 * 60
    public static let sweepEvery: TimeInterval = 60 * 60

    /// Why a share was not kept, in the share sheet's words.
    public enum Refusal: Equatable, Sendable {
        case nothing, full, tooBig, failed

        public var sentence: String {
            switch self {
            case .nothing: "There was nothing here Agent Loops can take."
            case .full: "Not saved. Too much is waiting in Agent Loops. Open Agent Loops to use it, then share this again."
            case .tooBig: "Not saved. This is too big to share with Agent Loops."
            case .failed: "Agent Loops could not save this."
            }
        }
    }

    /// The file. One kept before #6795 is a `SharedItems` alone.
    private struct Waiting: Codable {
        struct Share: Codable {
            var at: Date
            var items: SharedItems
        }

        var shares: [Share] = []
        /// How many were deleted at their age, until that is said.
        var expired = 0

        /// Lets go of the shares past their age. Whether any went.
        mutating func age(now: Date) -> Bool {
            let before = shares.count
            shares.removeAll { now.timeIntervalSince($0.at) > SharedInbox.ageLimit }
            expired += before - shares.count
            return shares.count < before
        }
    }

    /// The link the extension opens the Remote with. The Remote also looks each time it
    /// comes to the front, so the link is only a shortcut.
    public static var startURL: URL { AttentionLink.start.url }

    public static var container: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: AttentionSnapshotStore.appGroup)?
            .appendingPathComponent("Library/Application Support", isDirectory: true)
    }

    public static func url(in container: URL? = nil) -> URL? {
        (container ?? SharedInbox.container)?.appendingPathComponent(fileName)
    }

    /// Put these after whatever is already waiting. Why they were not kept, or nil when they were.
    @discardableResult
    public static func add(_ items: SharedItems, into container: URL? = nil, now: Date = Date()) -> Refusal? {
        guard !items.isEmpty else { return .nothing }
        guard let url = url(in: container) else { return .failed }
        var refusal: Refusal? = .failed
        coordinate(url, merging: true) { url in
            var waiting = Waiting()
            if FileManager.default.fileExists(atPath: url.path) {
                // One from before the limit can be too big for the extension to read.
                if size(url) > bytesMost { return refusal = .full }
                // Not read is not empty: what waits is never written over.
                guard let data = try? Data(contentsOf: url) else { return }
                waiting = decode(data, url) ?? Waiting()
            }
            _ = waiting.age(now: now)
            let share = Waiting(shares: [.init(at: now, items: items)])
            guard waiting.shares.count < most else { return refusal = .full }
            waiting.shares += share.shares
            guard let data = try? JSONEncoder().encode(waiting) else { return }
            guard data.count <= bytesMost else {
                let alone = (try? JSONEncoder().encode(share).count) ?? 0
                return refusal = alone > bytesMost ? .tooBig : .full
            }
            do {
                try FileManager.default.createDirectory(at: url.deletingLastPathComponent(),
                                                        withIntermediateDirectories: true)
                try write(data, to: url)
                refusal = nil
            } catch {}
        }
        return refusal
    }

    /// Deletes the shares past their age, and keeps how many for `take` to say. The Remote does
    /// this each hour, for shares with no folder to be offered in.
    public static func sweep(in container: URL? = nil, now: Date = Date()) {
        guard hasItems(in: container), let url = url(in: container) else { return }
        coordinate(url, merging: true) { url in
            guard let data = try? Data(contentsOf: url), var waiting = decode(data, url), waiting.age(now: now),
                  let kept = try? JSONEncoder().encode(waiting) else { return }
            try? write(kept, to: url)
        }
    }

    /// Whether anything waits, without taking it. Cheap: asked each time the Remote comes to
    /// the front.
    public static func hasItems(in container: URL? = nil) -> Bool {
        guard let url = url(in: container) else { return false }
        return FileManager.default.fileExists(atPath: url.path)
    }

    /// Everything waiting, in the order it was shared, and the file gone; nil when nothing
    /// waits. What was deleted at its age is said as a refusal.
    public static func take(from container: URL? = nil, now: Date = Date()) -> SharedItems? {
        guard let url = url(in: container) else { return nil }
        var taken: SharedItems?
        coordinate(url, merging: false) { url in
            if let data = try? Data(contentsOf: url), var waiting = decode(data, url) {
                _ = waiting.age(now: now)
                var items = SharedItems()
                for share in waiting.shares { items.append(share.items) }
                if waiting.expired > 0 { items.refusals.append(expiredWords(waiting.expired)) }
                taken = items
            }
            try? FileManager.default.removeItem(at: url)
        }
        return taken.flatMap { $0.isEmpty ? nil : $0 }
    }

    /// The pairing was forgotten (#3670): nothing shared waits for the next one.
    public static func clear(in container: URL? = nil) {
        guard let url = url(in: container) else { return }
        coordinate(url, merging: false) { try? FileManager.default.removeItem(at: $0) }
    }

    static func expiredWords(_ count: Int) -> String {
        count == 1 ? "1 share that waited more than 90 days was deleted."
            : "\(count) shares that waited more than 90 days were deleted."
    }

    private static func decode(_ data: Data, _ url: URL) -> Waiting? {
        if let waiting = try? JSONDecoder().decode(Waiting.self, from: data) { return waiting }
        guard let items = try? JSONDecoder().decode(SharedItems.self, from: data) else { return nil }
        let changed = (try? FileManager.default.attributesOfItem(atPath: url.path))?[.modificationDate] as? Date
        return Waiting(shares: [.init(at: changed ?? Date(), items: items)])
    }

    private static func size(_ url: URL) -> Int {
        (try? FileManager.default.attributesOfItem(atPath: url.path))?[.size] as? Int ?? 0
    }

    private static func write(_ data: Data, to url: URL) throws {
        // store-bounds: kind=blob; size=10MB; count=100; age=90d; applied=on write, on take, hourly while the Remote runs
        try data.write(to: url, options: .atomic)
    }

    private static func coordinate(_ url: URL, merging: Bool, _ body: (URL) -> Void) {
        var error: NSError?
        NSFileCoordinator().coordinate(writingItemAt: url, options: merging ? .forMerging : .forDeleting,
                                       error: &error, byAccessor: body)
    }
}
#endif
