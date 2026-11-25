import Foundation

/// The words of a blocked image and of Settings ▸ Image Hosts (#7139, epic #7136).
public enum ImageHostsWords {
    public static let title = "Image Hosts"
    public static let symbol = "photo.slash"
    public static let show = "Show image"
    public static let showShort = "Show"
    public static let more = "More"
    public static let reading = "Reading…"
    public static let tryAgain = "Try Again"
    public static let remove = "Remove"
    public static let none = "No allowed hosts"
    public static let noneDetail = "Open a Work page with a blocked image and press Always allow."
    public static let firstRead = "Reading the list…"
    public static let notRemoved = "Not removed. Try again."

    /// The longest host that a button and the line under the host say in full.
    static let longest = 40

    /// What a button says for a host: the host, or the end of a long one, which says who owns it.
    public static func short(_ host: String) -> String {
        guard host.count > longest else { return host }
        let end = host.split(separator: ".").suffix(3).joined(separator: ".")
        return "…" + end.suffix(longest)
    }

    public static func allow(_ host: String) -> String { "Always allow \(short(host))" }
    /// The button's whole words, for its tooltip and VoiceOver.
    public static func allowFull(_ host: String) -> String { "Always allow \(host)" }

    public static func blocked(_ blocked: DaemonAPI.ImageBlocked) -> String {
        blocked.from.map { "Image blocked: \($0) redirects to \(blocked.host)" } ?? "Image blocked: \(blocked.host)"
    }

    public static func blockedShort(_ blocked: DaemonAPI.ImageBlocked) -> String { "Blocked: \(blocked.host)" }

    public static func learns(_ host: String, machine: String) -> String {
        "If you show it, \(host.count > longest ? "this host" : host) learns that \(inSentence(machine)) read it."
    }

    public static func menuLearns(machine: String) -> String { "The host learns that \(inSentence(machine)) read the image." }
    public static func shownOnce(_ host: String) -> String { "Shown once from \(host)" }
    public static func failed(_ why: String) -> String { "The image could not be read: \(why.hasSuffix(".") ? why : why + ".")" }
    public static func notAllowed(_ host: String, machine: String, why: String) -> String {
        "\(host) was not allowed on \(inSentence(machine)). \(why)"
    }

    public static func allowedOn(_ date: Date, now: Date = Date(), calendar: Calendar = .current, locale: Locale = .current) -> String {
        if calendar.isDate(date, inSameDayAs: now) { return "Allowed today" }
        return "Allowed \(LastUpdate.date(date, now: now, calendar: calendar, locale: locale))"
    }

    public static func footer(machine: String) -> String {
        "Work pages on this machine load images from these hosts. Each host learns that \(inSentence(machine)) read the image. "
            + "Connected forges’ own image hosts always load and are not listed. Add a host with Always allow on a blocked image."
    }

    public static func offline(machine: String) -> String { "\(machine) is offline. Connect it to remove a host." }
    public static func couldNotRemove(_ host: String, machine: String) -> String { "\(host) could not be removed on \(inSentence(machine))." }
    public static func couldNotRead(machine: String) -> String { "The list on \(inSentence(machine)) could not be read." }

    /// "This Mac" is a name in a list and words in a sentence.
    private static func inSentence(_ machine: String) -> String { machine == "This Mac" ? "this Mac" : machine }
}

/// A read of one image on a Work page, as the machine answered it.
public enum PageImageAnswer: Sendable {
    case image(Data)
    case blocked(DaemonAPI.ImageBlocked)
    /// Why, for a person, and whether reading it again can help.
    case failed(String, again: Bool)

    /// What a failed `forge/image` says.
    public init(_ error: any Error) {
        guard let error = error as? JSONRPCError else {
            self = .failed(error.localizedDescription, again: true)
            return
        }
        switch error.code {
        case DaemonAPI.Failure.imageBlocked:
            guard let blocked = try? error.data?.decode(DaemonAPI.ImageBlocked.self) else { fallthrough }
            self = .blocked(blocked)
        default:
            self = .failed(error.message, again: error.code != DaemonAPI.Failure.imageNotPublic)
        }
    }
}

/// The remote images of one open Work page (#7139): what each shows, and the person's choices for
/// it. It lives as long as the page, so an image that was shown once stays until the page closes,
/// also when the page is read again, and the next page is blocked again. `Picture` is the app's
/// own kind of image.
@MainActor
@Observable
public final class PageImages<Picture: Sendable> {
    public typealias Read = @MainActor (_ url: URL, _ show: [String], _ host: MachineRef) async -> PageImageAnswer
    public typealias Allow = @MainActor (_ imageHost: String, _ host: MachineRef) async throws -> Void

    public enum Phase {
        case loading
        case image(Picture)
        /// `problem`: why Always allow did not work.
        case blocked(DaemonAPI.ImageBlocked, problem: String?)
        /// Being read since the person pressed a button.
        case reading(DaemonAPI.ImageBlocked)
        /// It failed after the person chose to read it.
        case failed(String, again: Bool)
        /// It failed as it loaded: the alternative text.
        case none
    }

    private struct Entry {
        var machine: MachineRef
        var phase: Phase = .loading
        /// The hosts the person showed for this image, in order.
        var show: [String] = []
        /// What the person last chose to read past.
        var blocked: DaemonAPI.ImageBlocked?
        /// Why Always allow on a shown image did not work.
        var problem: String?
        /// The latest read, so an older one's answer is not drawn.
        var serial = 0

        var blockedBy: DaemonAPI.ImageBlocked? {
            if case .blocked(let blocked, _) = phase { blocked } else { nil }
        }
    }

    private var entries: [URL: Entry] = [:]
    /// The hosts allowed from this page, by machine.
    private var allowed: [MachineRef: Set<String>] = [:]
    @ObservationIgnored private let read: Read
    @ObservationIgnored private let allowing: Allow
    @ObservationIgnored private let decode: @MainActor (Data) -> Picture?

    public init(read: @escaping Read, allow: @escaping Allow, decode: @escaping @MainActor (Data) -> Picture?) {
        self.read = read
        allowing = allow
        self.decode = decode
    }

    public func phase(_ url: URL) -> Phase { entries[url]?.phase ?? .loading }

    /// The host to say under an image that is here for one press: the last one shown and not allowed since.
    public func shownOnce(_ url: URL) -> String? {
        guard let entry = entries[url], case .image = entry.phase else { return nil }
        return entry.show.last { allowed[entry.machine]?.contains($0) != true }
    }

    /// Why Always allow under a shown image did not work.
    public func problem(_ url: URL) -> String? { entries[url]?.problem }

    /// The image is on screen: read once from the machine that read the page. Another machine has
    /// its own list, so what the first one refused is asked again there; an image, and one
    /// that the person just asked for, stays.
    public func load(_ url: URL, on machine: MachineRef) {
        if let entry = entries[url] {
            if case .image = entry.phase { return }
            if case .reading = entry.phase { return }
            if entry.machine == machine { return }
        }
        var entry = Entry(machine: machine)
        entry.serial = (entries[url]?.serial ?? 0) + 1
        entries[url] = entry
        start(url)
    }

    /// Show image: this one, this time. One press accepts one host.
    public func show(_ url: URL) {
        guard let entry = entries[url], case .blocked(let blocked, _) = entry.phase else { return }
        if !entry.show.contains(blocked.host) { entries[url]?.show.append(blocked.host) }
        press(url, blocked)
        start(url)
    }

    /// Try Again, after a failure.
    public func again(_ url: URL) {
        guard let entry = entries[url], case .failed(_, again: true) = entry.phase, let blocked = entry.blocked else { return }
        press(url, blocked)
        start(url)
    }

    /// Always allow: the host, and then each of the page's images that names it. At once; changed
    /// back, with why, when the machine refuses.
    public func allow(_ imageHost: String, from url: URL, machineName: String) async {
        guard let machine = entries[url]?.machine, allowed[machine]?.contains(imageHost) != true else { return }
        let waiting = entries.filter { $0.value.machine == machine && $0.value.blockedBy?.host == imageHost }.map(\.key)
        for url in waiting {
            if let blocked = entries[url]?.blockedBy { press(url, blocked) }
        }
        entries[url]?.problem = nil
        allowed[machine, default: []].insert(imageHost)
        do {
            try await allowing(imageHost, machine)
            for url in waiting where entries[url]?.machine == machine { start(url) }
        } catch {
            allowed[machine]?.remove(imageHost)
            let why = ImageHostsWords.notAllowed(imageHost, machine: machineName, why: WorkList.failureWords(error))
            for each in waiting {
                guard let entry = entries[each], entry.machine == machine, case .reading(let blocked) = entry.phase else { continue }
                entries[each]?.phase = .blocked(blocked, problem: each == url ? why : nil)
            }
            if let entry = entries[url], case .image = entry.phase { entries[url]?.problem = why }
        }
    }

    private func press(_ url: URL, _ blocked: DaemonAPI.ImageBlocked) {
        entries[url]?.blocked = blocked
        entries[url]?.phase = .reading(blocked)
    }

    /// Not a view's own task: a view that goes off screen does not stop a read that the person asked for.
    private func start(_ url: URL) {
        guard var entry = entries[url] else { return }
        entry.serial += 1
        entries[url] = entry
        let (serial, show, machine) = (entry.serial, entry.show, entry.machine)
        Task {
            let answer = await read(url, show, machine)
            guard let now = entries[url], now.serial == serial, now.machine == machine else { return }
            entries[url]?.phase = phase(answer, pressed: now.blocked != nil)
        }
    }

    private func phase(_ answer: PageImageAnswer, pressed: Bool) -> Phase {
        switch answer {
        case .image(let data):
            if let picture = decode(data) { return .image(picture) }
            return pressed ? .failed("it is not an image", again: true) : .none
        case .blocked(let blocked):
            return .blocked(blocked, problem: nil)
        case .failed(let why, let again):
            return pressed ? .failed(why, again: again) : .none
        }
    }
}

/// Settings ▸ Machines ▸ Image Hosts' list for one machine (#7139): the kept list first, then the
/// machine's; Remove at once, and back with why when the machine refuses.
@MainActor
@Observable
public final class ImageHostsList {
    public struct Calls {
        public var kept: @MainActor () async -> (hosts: DaemonAPI.ImageHosts, at: Date)?
        public var list: @MainActor () async throws -> DaemonAPI.ImageHosts
        public var remove: @MainActor (String) async throws -> DaemonAPI.ImageHosts
        public var keep: @MainActor (DaemonAPI.ImageHosts) -> Void

        public init(kept: @escaping @MainActor () async -> (hosts: DaemonAPI.ImageHosts, at: Date)?,
                    list: @escaping @MainActor () async throws -> DaemonAPI.ImageHosts,
                    remove: @escaping @MainActor (String) async throws -> DaemonAPI.ImageHosts,
                    keep: @escaping @MainActor (DaemonAPI.ImageHosts) -> Void) {
            self.kept = kept
            self.list = list
            self.remove = remove
            self.keep = keep
        }
    }

    /// Nil until something was read.
    public private(set) var hosts: [DaemonAPI.ImageHost]?
    public private(set) var readAt: Date?
    public private(set) var reading = false
    /// The machine did not answer the list.
    public private(set) var problem: String?
    /// The last host that was not removed, and what the machine said.
    public private(set) var notRemoved: (host: String, why: String)?
    @ObservationIgnored private let calls: Calls
    /// Counts the reads and the changes: a list that was asked before a later one is not drawn.
    @ObservationIgnored private var serial = 0
    @ObservationIgnored private var removing: Set<String> = []

    public init(_ calls: Calls) { self.calls = calls }

    /// `live` false for a machine that is offline: the kept list only.
    public func load(live: Bool) async {
        notRemoved = nil
        if hosts == nil, let kept = await calls.kept(), hosts == nil {
            (hosts, readAt) = (kept.hosts.hosts, kept.at)
        }
        guard live else { return }
        serial += 1
        let mine = serial
        reading = true
        defer { if mine == serial { reading = false } }
        do {
            let list = try await calls.list()
            guard mine == serial else { return }
            took(list)
        } catch {
            if mine == serial, !(error is CancellationError) { problem = WorkList.failureWords(error) }
        }
    }

    public func remove(_ host: String) async {
        guard let had = hosts, let index = had.firstIndex(where: { $0.host == host }), removing.insert(host).inserted else { return }
        defer { removing.remove(host) }
        serial += 1
        reading = false
        let mine = serial
        let row = had[index]
        hosts?.remove(at: index)
        notRemoved = nil
        do {
            let list = try await calls.remove(host)
            guard mine == serial else { return }
            took(list)
        } catch {
            var back = hosts ?? []
            if !back.contains(where: { $0.host == host }) { back.insert(row, at: min(index, back.count)) }
            hosts = back
            notRemoved = (host, WorkList.failureWords(error))
        }
    }

    private func took(_ list: DaemonAPI.ImageHosts) {
        hosts = list.hosts.filter { !removing.contains($0.host) }
        readAt = Date()
        problem = nil
        calls.keep(list)
    }
}
