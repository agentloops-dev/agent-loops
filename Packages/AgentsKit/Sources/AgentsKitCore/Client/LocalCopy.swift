import Foundation

/// The Mac window's and the Remote's local copy (#4791; spec 4777-fast, "the copy is a
/// cache, never the record"): each machine's folders and lean session records (the live
/// ones and the archived ones on screen), and the end of the last 20 chats opened, as
/// plain JSON files in the platform's `Caches` folder. Keyed by ref (#4727). The client
/// draws it at once, before its host answers, and the host's answer always wins. The
/// system may delete the folder at any time; the client then fetches as before.
///
/// An actor, so encoding and the disk are off the main actor. One file per machine and
/// one per session: a list change writes one small file, and a chat opened writes its own.
///
/// A cache (#6669): it evicts by its age, its counts and its cap, on each write and hourly, and never refuses. A record's
/// age is since this device read it from its host, and its file's time says it; drawing it does not make it newer.
public actor LocalCopy: CacheStore {
    /// The most the copy holds on a Mac and on a phone (spec 4777-fast).
    public static let macCap = 200 * 1024 * 1024
    public static let remoteCap = 50 * 1024 * 1024
    /// The transcripts kept: the last ones opened.
    public static let transcriptsKept = 20
    /// A transcript's entries kept: its end, as one `agents/transcript` page.
    public static let entriesKept = ChatOpening.wholeTurn
    /// Archived records kept for one machine: a page's worth, not every fold ever opened.
    public static let archivedKept = 200

    /// The largest record. A machine over it loses its archived records and a chat its oldest entries; one still over
    /// it, or a Work answer over it, is not kept, so it is read again next time.
    public static let recordCap = 16 * 1024 * 1024

    /// One machine: its folders and session records.
    public struct Machine: Codable, Sendable, Equatable {
        public var host: MachineRef
        public var folders: [DaemonAPI.FolderSummary]
        public var agents: [Agent]
        /// Where `agents` are in the host's order of changes (#4794), so a launch asks only
        /// for what changed since. Nil from a copy made before it, or a host that predates it.
        public var point: DaemonAPI.ListRevision?

        public init(host: MachineRef, folders: [DaemonAPI.FolderSummary], agents: [Agent],
                    point: DaemonAPI.ListRevision? = nil) {
            self.host = host
            self.folders = folders
            self.agents = agents
            self.point = point
        }
    }

    /// One chat's end: its finished turns, its last page of entries, and the open turn's
    /// prompt when the page starts after it.
    public struct Transcript: Codable, Sendable, Equatable {
        public var session: UUID
        public var turns: TurnsPage
        public var page: TranscriptPage
        public var ask: TranscriptEntry?

        public init(session: UUID, turns: TurnsPage, page: TranscriptPage, ask: TranscriptEntry? = nil) {
            self.session = session
            self.turns = turns
            self.page = page
            self.ask = ask
        }

        /// What `takeOpening` puts on screen.
        public var opening: ChatOpening.Loaded {
            ChatOpening.Loaded(turns: turns, page: page, turnsFailure: nil, ask: ask)
        }
    }

    public let folder: URL
    public let cap: Int
    public let transcriptsKept: Int
    public let recordCap: Int
    /// A record goes this long after it was read from its host (#6789).
    public let age: TimeInterval
    let now: @Sendable () -> Date
    private static let sweepEvery: TimeInterval = 60 * 60

    public init(folder: URL, cap: Int, transcriptsKept: Int = LocalCopy.transcriptsKept,
                recordCap: Int = LocalCopy.recordCap, age: TimeInterval = cacheAgeLimit,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.folder = folder
        self.cap = cap
        self.transcriptsKept = transcriptsKept
        self.recordCap = recordCap
        self.age = min(age, cacheAgeLimit)
        self.now = now
        // The hourly sweep, which ends with the copy. The first waits for the launch's draws; a read never gives an aged
        // record.
        Task(priority: .utility) { [weak self] in
            var wait: TimeInterval = 10
            while true {
                try? await Task.sleep(for: .seconds(wait))
                guard await self?.sweep() != nil else { return }
                wait = Self.sweepEvery
            }
        }
    }

    /// The Mac window's copy: in its root's cache folder, so a scratch root has its own.
    public static func mac(_ locations: StoreLocations = .default) -> LocalCopy {
        LocalCopy(folder: locations.cacheRoot.appendingPathComponent("copy", isDirectory: true), cap: macCap)
    }

    /// The Remote's copy: in the app's own `Caches`.
    public static func remote() -> LocalCopy {
        let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        return LocalCopy(folder: caches.appendingPathComponent("copy", isDirectory: true), cap: remoteCap)
    }

    /// A machine's ref (#4727).
    public static func machineRef(_ host: MachineRef) -> WireRef {
        WireRef(provider: "agents", type: "machine", topic: "", id: host.rawValue)
    }

    /// A session's ref (#4727), as the web page's copy keys it: the topic is left out, as the
    /// id is unique without it.
    public static func sessionRef(_ session: UUID) -> WireRef {
        WireRef(provider: "agents", type: "session", topic: "", id: session.uuidString.lowercased())
    }

    private var machines: URL { folder.appendingPathComponent("machines", isDirectory: true) }
    private var transcripts: URL { folder.appendingPathComponent("transcripts", isDirectory: true) }

    /// A ref as a file's name: only letters, digits, `-` and `.`; the ref itself is in the file.
    static func fileName(_ ref: WireRef) -> String {
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-."))
        let name = String(ref.description.unicodeScalars.map { allowed.contains($0) ? Character($0) : "_" })
        return name + ".json"
    }

    /// The file's form: the ref beside what it holds, so a file is read back only for its ref.
    struct Record<Value: Codable & Sendable>: Codable, Sendable {
        var ref: WireRef
        var value: Value
        /// When the client read `value` from its host (#5141), for the "Last update" line.
        /// Nil in a file written before it: the file's own time is used then.
        var at: Date?
    }

    // MARK: Machines

    /// Every machine held, in no order.
    public func allMachines() -> [Machine] { machinesRead().map(\.machine) }

    /// Every machine held, in no order, with when it was read from its host (#5141).
    public func machinesRead() -> [(machine: Machine, at: Date)] {
        let files = (try? FileManager.default.contentsOfDirectory(at: machines, includingPropertiesForKeys: nil)) ?? []
        return files.compactMap { url -> (machine: Machine, at: Date)? in
            guard url.pathExtension == "json", let record = read(Machine.self, at: url) else { return nil }
            var machine = record.value
            // The host is the copy's to say, whatever a record's own coding keeps of it.
            machine.agents = machine.agents.map { var agent = $0; agent.host = machine.host; return agent }
            return (machine, record.at ?? .distantPast)
        }
    }

    /// `at` is when the client read the machine from its host; now when not given.
    /// One with nothing to draw is not kept: a machine let go of lists nothing (#6789).
    public func keep(_ machine: Machine, readAt at: Date? = nil) {
        if machine.agents.isEmpty, machine.folders.isEmpty {
            return drop(at: machines.appendingPathComponent(Self.fileName(Self.machineRef(machine.host))))
        }
        var machine = machine
        // The live ones whole, and the archived ones newest first, at most a page of them.
        let live = machine.agents.filter { $0.state != .archived }
        let archived = machine.agents.filter { $0.state == .archived }
            .sorted { ($0.archivedAt ?? $0.createdAt) > ($1.archivedAt ?? $1.createdAt) }
            .prefix(Self.archivedKept)
        machine.agents = live + archived
        let ref = Self.machineRef(machine.host)
        let url = machines.appendingPathComponent(Self.fileName(ref))
        let at = at ?? now()
        if !write(Record(ref: ref, value: machine, at: at), to: url) {
            machine.agents = live
            // The archived ones left out are not in the point's list.
            machine.point = nil
            write(Record(ref: ref, value: machine, at: at), to: url)
        }
        trim()
    }

    /// Drops each machine that is none of `hosts`, with the chats of its sessions (#6789).
    public func dropMachines(except hosts: Set<MachineRef>) {
        let names = Set(hosts.map { Self.fileName(Self.machineRef($0)) })
        for file in Self.files(machines) where !names.contains(file.url.lastPathComponent) { drop(at: file.url) }
    }

    private func drop(at url: URL) {
        for agent in read(Machine.self, at: url)?.value.agents ?? [] {
            try? FileManager.default.removeItem(at: transcriptFile(Self.sessionRef(agent.id)))
        }
        try? FileManager.default.removeItem(at: url)
    }

    // MARK: Transcripts

    private func transcriptFile(_ ref: WireRef) -> URL { transcripts.appendingPathComponent(Self.fileName(ref)) }

    public func transcript(of session: UUID) -> Transcript? {
        let ref = Self.sessionRef(session)
        guard let record = read(Transcript.self, at: transcriptFile(ref)),
              record.ref == ref else { return nil }
        return record.value
    }

    /// A chat's end, kept: at most `entriesKept` of its entries, then the copy trimmed to
    /// its caps, the oldest transcript first.
    public func keep(_ transcript: Transcript) {
        var transcript = transcript
        let drop = transcript.page.entries.count - Self.entriesKept
        if drop > 0 {
            transcript.page.entries.removeFirst(drop)
            transcript.page.firstIndex += drop
        }
        let ref = Self.sessionRef(transcript.session)
        let at = now()
        while !write(Record(ref: ref, value: transcript, at: at), to: transcriptFile(ref)), !transcript.page.entries.isEmpty {
            let less = (transcript.page.entries.count + 1) / 2
            transcript.page.entries.removeFirst(less)
            transcript.page.firstIndex += less
        }
        trim()
    }

    /// What has aged out or is over the largest record, then transcripts past the newest `transcriptsKept`, then, while the whole copy is over `cap`,
    /// the oldest Work entry (#4930), with none left the oldest transcript, and last the oldest machine.
    func trim() {
        let fm = FileManager.default
        let cutoff = now() - age
        for file in allFiles() where file.at < cutoff || file.bytes > recordCap { try? fm.removeItem(at: file.url) }
        var held = Self.files(transcripts).sorted { $0.at > $1.at }
        while held.count > transcriptsKept, let oldest = held.popLast() { try? fm.removeItem(at: oldest.url) }
        var work = trimWork().sorted { $0.at > $1.at }
        var kept = Self.files(machines).sorted { $0.at > $1.at }
        var total = (held + work + kept).reduce(0) { $0 + $1.bytes }
        while total > cap, let oldest = work.popLast() ?? held.popLast() ?? kept.popLast() {
            try? fm.removeItem(at: oldest.url)
            total -= oldest.bytes
        }
    }

    static func files(_ in: URL) -> [(url: URL, at: Date, bytes: Int)] {
        let keys: [URLResourceKey] = [.contentModificationDateKey, .fileSizeKey]
        return ((try? FileManager.default.contentsOfDirectory(at: `in`, includingPropertiesForKeys: keys)) ?? []).map { url in
            let values = try? url.resourceValues(forKeys: Set(keys))
            return (url, values?.contentModificationDate ?? .distantPast, values?.fileSize ?? 0)
        }
    }

    /// Every file of the copy, whatever wrote it.
    private func allFiles() -> [(url: URL, at: Date, bytes: Int)] {
        let keys: Set<URLResourceKey> = [.contentModificationDateKey, .fileSizeKey, .isRegularFileKey]
        guard let walk = FileManager.default.enumerator(at: folder, includingPropertiesForKeys: Array(keys)) else { return [] }
        return walk.compactMap { found in
            guard let url = found as? URL, let values = try? url.resourceValues(forKeys: keys), values.isRegularFile == true else { return nil }
            return (url, values.contentModificationDate ?? .distantPast, values.fileSize ?? 0)
        }
    }

    public func sweep() { trim() }

    public func stats() -> CacheStats {
        let files = allFiles()
        return CacheStats(count: files.count, bytes: files.reduce(0) { $0 + $1.bytes }, oldest: files.map(\.at).min())
    }

    /// Everything gone: the pairing went, and what this device kept of it goes with it.
    public func forget() {
        try? FileManager.default.removeItem(at: folder)
    }

    // MARK: Files

    func read<Value: Codable & Sendable>(_: Value.Type, at url: URL) -> Record<Value>? {
        // One over the largest size, which an older build kept, is a copy missing; the sweep removes it (#7252).
        guard let bytes = (try? FileManager.default.attributesOfItem(atPath: url.path))?[.size] as? Int, bytes <= recordCap,
              let data = try? Data(contentsOf: url) else { return nil }
        // A file that does not read (an older form, cut short) is only a copy missing.
        guard var record = try? JSONDecoder().decode(Record<Value>.self, from: data) else { return nil }
        let at = record.at ?? (try? FileManager.default.attributesOfItem(atPath: url.path)[.modificationDate]) as? Date
        record.at = at
        // Aged out between two sweeps.
        guard let at, at >= now() - age else { return nil }
        return record
    }

    /// False when the record is over `recordCap`: its file is then removed, as what it held is stale.
    @discardableResult
    func write<Value: Codable & Sendable>(_ record: Record<Value>, to url: URL) -> Bool {
        guard let data = try? JSONEncoder().encode(record), data.count <= recordCap else {
            try? FileManager.default.removeItem(at: url)
            return false
        }
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        // Atomic, so a reader never finds half a file.
        // store-bounds: kind=cache; size=200MiB; age=90d; action=evict; record=16MiB; applied=on write and hourly
        try? data.write(to: url, options: .atomic)
        // The file's time is when its record was read, which the sweep goes by.
        if let at = record.at { try? FileManager.default.setAttributes([.modificationDate: at], ofItemAtPath: url.path) }
        return true
    }
}
