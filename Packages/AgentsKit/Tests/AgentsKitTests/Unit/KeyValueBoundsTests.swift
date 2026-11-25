import Foundation
import Testing
@testable import AgentsKitCore

/// The largest record of what the Mac window and the Remote keep (#7252): at the limit it is kept, above it it is not, and
/// what was kept stays.
@MainActor
@Suite("Client stores have a largest record")
struct KeyValueBoundsTests {
    private let day: TimeInterval = 86_400

    private func size(_ url: URL) -> Int {
        ((try? FileManager.default.attributesOfItem(atPath: url.path))?[.size] as? Int) ?? 0
    }

    @Test func anEntryIsMeasuredInBytesAndOneOverIsRefused() {
        let defaults = MemoryDefaults()
        #expect(defaults.putEntry("ééééé", forKey: "s", most: 10))
        #expect(!defaults.putEntry("éééééa", forKey: "s", most: 10))
        #expect(defaults.string(forKey: "s") == "ééééé")
        #expect(defaults.putEntry(Data(count: 4), forKey: "d", most: 4))
        #expect(!defaults.putEntry(Data(count: 5), forKey: "d", most: 4))
        #expect(defaults.data(forKey: "d")?.count == 4)
        #expect(defaults.putEntry(["ab", "cd"], forKey: "l", most: 4))
        #expect(!defaults.putEntry(["ab", "cde"], forKey: "l", most: 4))
        #expect(defaults.putEntry(["ab": "cd"], forKey: "m", most: 4))
        #expect(!defaults.putEntry(["ab": "cde"], forKey: "m", most: 4))
        #expect(defaults.dictionary(forKey: "m") as? [String: String] == ["ab": "cd"])
    }

    @Test func aNewKeyPastTheCountHasNoRoomAndOneThereHas() {
        let defaults = MemoryDefaults()
        for n in 0..<3 { defaults.set(true, forKey: "pin.\(n)") }
        let pins = { (key: String) in key.hasPrefix("pin.") }
        #expect(defaults.hasRoom(forKey: "pin.3", most: 4, counted: pins))
        #expect(!defaults.hasRoom(forKey: "pin.3", most: 3, counted: pins))
        #expect(defaults.hasRoom(forKey: "pin.0", most: 3, counted: pins))
    }

    // MARK: The sidebar

    @Test func aFoldPastTheLimitHoldsWhileTheAppRuns() {
        let defaults = MemoryDefaults()
        let bounds = SidebarFolds.bounds
        defaults.set((1..<bounds.count).map { "mac|/f\($0)" }, forKey: "sidebar.folds")
        let folds = SidebarFolds(defaults: defaults, scope: "")
        let key = { (path: String) in FolderKey(folder: URL(filePath: path)) }
        folds.set(key("/at"), open: true)
        folds.set(key("/over"), open: true)
        #expect(defaults.stringArray(forKey: "sidebar.folds")?.count == bounds.count)
        #expect(folds.isOpen(key("/at")) && folds.isOpen(key("/over")))
        folds.set(key("/other"), open: true)
        #expect(folds.isOpen(key("/over")), "another change keeps it")
        folds.set(key("/over"), open: false)
        #expect(!folds.isOpen(key("/over")))
        let again = SidebarFolds(defaults: defaults, scope: "")
        #expect(again.isOpen(key("/at")) && !again.isOpen(key("/other")))
    }

    @Test func aFoldWithALongNameIsNotKeptAndAnOldListIsReadToTheLimit() {
        let defaults = MemoryDefaults()
        let long = FolderKey(folder: URL(filePath: "/" + String(repeating: "x", count: SidebarFolds.bounds.bytes)))
        let folds = SidebarFolds(defaults: defaults, scope: "")
        folds.set(long, open: true)
        #expect(folds.isOpen(long) && defaults.stringArray(forKey: "sidebar.folds") == [])
        defaults.set((0...SidebarFolds.bounds.count).map { "mac|/f\($0)" }, forKey: "sidebar.folds")
        #expect(SidebarFolds(defaults: defaults, scope: "").open.count == SidebarFolds.bounds.count)
    }

    @Test func aProjectsFolderPastALimitIsNotKept() {
        let bounds = KeyValueBounds.projectFolders
        var map = Dictionary(uniqueKeysWithValues: (1..<bounds.count).map { ("p\($0)", "mac|/a") })
        map = ClientDefaults.projectFolders(map, remembering: "mac|/b", for: "at")
        #expect(map["at"] == "mac|/b")
        #expect(ClientDefaults.projectFolders(map, remembering: "mac|/b", for: "over") == map)
        #expect(ClientDefaults.projectFolders(map, remembering: "mac|/c", for: "at")["at"] == "mac|/c")
        #expect(ClientDefaults.projectFolders([:], remembering: String(repeating: "x", count: bounds.bytes - 1), for: "p").count == 1)
        #expect(ClientDefaults.projectFolders([:], remembering: String(repeating: "x", count: bounds.bytes), for: "p").isEmpty)
        let defaults = MemoryDefaults()
        ClientDefaults.keepProjectFolders(map, forKey: "k", in: defaults)
        #expect((defaults.dictionary(forKey: "k") as? [String: String]) == map)
    }

    @Test func aFolderOverTheLimitLeavesTheEarlierOne() {
        let defaults = MemoryDefaults()
        let one = FolderKey(folder: URL(filePath: "/one"))
        ClientDefaults.keepFolder(one, forKey: "f", in: defaults)
        let long = URL(filePath: "/" + String(repeating: "x", count: KeyValueBounds.folder))
        ClientDefaults.keepFolder(FolderKey(folder: long), forKey: "f", in: defaults)
        #expect(defaults.string(forKey: "f") == one.stored)
        ClientDefaults.keepFolder(nil, forKey: "f", in: defaults)
        #expect(defaults.object(forKey: "f") == nil)
    }

    @Test func aRecentMachineWithALongIDIsNotKept() {
        let defaults = MemoryDefaults()
        let at = String(repeating: "m", count: MachinePins.idMost)
        #expect(MachinePickerChoice.remember(defaults, at) == [at])
        #expect(MachinePickerChoice.remember(defaults, at + "m") == [at])
    }

    @Test func theNamesShownAreReadToTheLimit() {
        let shown = KeyValueBounds.shown
        let long = String(repeating: "x", count: shown.bytes + 1)
        #expect(shown.kept([long, "a"]) == ["a"])
        #expect(shown.kept((0...shown.count).map(String.init)).count == shown.count)
    }

    /// A word over the limit is none here, and this client does not send its own back.
    @Test func aHostsWordOverTheLimitIsNotMirroredAndNotUndone() async {
        let defaults = MemoryDefaults()
        let key = DisplayPreference.skillsMarketplace
        var asked = 0
        let mirror = DisplayPreferenceMirror(defaults: defaults, send: { asked += 1; return $0 }, failed: { _ in })
        mirror.hostSaid([key: .string(String(repeating: "x", count: KeyValueBounds.choice))])
        #expect(defaults.string(forKey: key)?.count == KeyValueBounds.choice)
        mirror.hostSaid([key: .string(String(repeating: "x", count: KeyValueBounds.choice + 1))])
        #expect(defaults.object(forKey: key) == nil)
        NotificationCenter.default.post(name: UserDefaults.didChangeNotification, object: defaults)
        await Task.yield()
        #expect(asked == 0)
    }

    // MARK: Drafts and the first build

    @Test func aStartFormOverTheLimitLeavesTheEarlierOne() {
        let store = DraftStore(defaults: MemoryDefaults())
        #expect(store.saveStartDraft(StartDraft(runtimeID: "claude")))
        #expect(!store.saveStartDraft(StartDraft(runtimeID: String(repeating: "x", count: DraftStore.draftBytes))))
        #expect(store.startDraft()?.runtimeID == "claude")
    }

    @Test func aFormerDraftOverTheLimitIsReadWhereItIsAndGoesWithItsDraft() throws {
        let defaults = MemoryDefaults()
        let store = DraftStore(defaults: defaults)
        let folder = URL(filePath: "/src/Agents")
        let project = DraftKey.newSession(project: "p1", folder: folder)
        let former = store.defaultsKey(for: .newAgent(folder: folder))
        let long = Draft(text: String(repeating: "x", count: DraftStore.draftBytes), attachments: [], editedAt: Date())
        defaults.set(try JSONEncoder().encode(long), forKey: former)
        #expect(store.draft(for: project) == long)
        #expect(defaults.object(forKey: former) != nil && defaults.object(forKey: store.defaultsKey(for: project)) == nil)
        #expect(store.save(Draft(text: "shorter", attachments: [], editedAt: Date()), for: project) == .kept)
        #expect(defaults.object(forKey: former) == nil)
        defaults.set(try JSONEncoder().encode(long), forKey: former)
        store.clear(project)
        #expect(store.draft(for: project) == nil, "a draft that was sent does not come back")
    }

    @Test func anIdeaOverTheLimitIsTooLong() {
        #expect(!FirstBuild.isTooLong(String(repeating: "x", count: FirstBuild.ideaBytes)))
        #expect(FirstBuild.isTooLong(String(repeating: "x", count: FirstBuild.ideaBytes + 1)))
    }

    // MARK: The Live Activity's turns

    @Test func theNewestTurnsAreKeptTo90Days() throws {
        let now = Date()
        var turns: [UUID: LiveActivityTurn] = [:]
        for n in 0...LiveActivityTurn.most {
            turns[UUID()] = LiveActivityTurn(startedAt: now - Double(n), place: String(repeating: "é", count: 500))
        }
        let kept = LiveActivityTurn.kept(turns, now: now)
        #expect(kept.count == LiveActivityTurn.most)
        #expect(kept.values.map(\.startedAt).min() == now - Double(LiveActivityTurn.most - 1), "the oldest went")
        #expect(kept.values.allSatisfy { $0.place.utf8.count == LiveActivityTurn.placeMost })
        #expect(try JSONEncoder().encode(kept).count <= LiveActivityTurn.bytes)
        let old = [UUID(): LiveActivityTurn(startedAt: now - 90 * day, place: ""), UUID(): LiveActivityTurn(startedAt: now - 90 * day - 1, place: "")]
        #expect(LiveActivityTurn.kept(old, now: now).count == 1)
    }

    // MARK: The widget's file

    private func row(_ title: String) -> AttentionSnapshotSession {
        AttentionSnapshotSession(id: UUID(), folder: "api", title: title, wanted: nil, kind: .permission, since: Date())
    }

    @Test func aSnapshotOverTheLimitLosesItsLastRowsAndKeepsItsCount() throws {
        let directory = try TemporaryDirectory()
        defer { directory.remove() }
        let long = AttentionSnapshot(writtenAt: Date(), total: 9, sessions: (0..<9).map { _ in row(String(repeating: "x", count: 10_000)) })
        #expect(AttentionSnapshotStore.write(long, into: directory.url))
        let kept = try #require(AttentionSnapshotStore.read(from: directory.url))
        #expect(kept.total == 9 && kept.sessions == Array(long.sessions.prefix(6)))
        #expect(!AttentionSnapshotStore.write(long, into: directory.url), "the same again is no write")
        let url = try #require(AttentionSnapshotStore.url(in: directory.url))
        #expect(size(url) <= AttentionSnapshotStore.mostBytes)
        // At the limit to the byte, and one over it.
        let empty = AttentionSnapshot(writtenAt: long.writtenAt, total: 1, sessions: [row("")])
        let room = AttentionSnapshotStore.mostBytes - (try JSONEncoder().encode(empty)).count
        var at = empty
        at.sessions[0].title = String(repeating: "x", count: room)
        #expect(AttentionSnapshotStore.fitted(at)?.snapshot == at)
        at.sessions[0].title += "x"
        #expect(AttentionSnapshotStore.fitted(at)?.snapshot.sessions == [])
    }

    @Test func anOldOrTooLargeSnapshotIsNoneAndGoes() throws {
        let directory = try TemporaryDirectory()
        defer { directory.remove() }
        let url = try #require(AttentionSnapshotStore.url(in: directory.url))
        let now = Date()
        #expect(AttentionSnapshotStore.write(AttentionSnapshot(writtenAt: now, total: 1, sessions: []), into: directory.url))
        #expect(AttentionSnapshotStore.read(from: directory.url, now: now + 90 * day - 1) != nil)
        #expect(AttentionSnapshotStore.read(from: directory.url, now: now + 90 * day + 1) == nil)
        #expect(!FileManager.default.fileExists(atPath: url.path))
        try JSONEncoder().encode(AttentionSnapshot(writtenAt: now, total: 1, sessions: [row(String(repeating: "x", count: AttentionSnapshotStore.mostBytes))])).write(to: url)
        #expect(AttentionSnapshotStore.read(from: directory.url) == nil)
        #expect(!FileManager.default.fileExists(atPath: url.path))
    }

    // MARK: The device log

    @Test func aLongLineIsCutSoTheTwoFilesStayUnderTwiceTheLimit() throws {
        let directory = try TemporaryDirectory()
        defer { directory.remove() }
        let log = RollingLog(url: directory.url.appendingPathComponent("remote.log"), limit: 1_024)
        for _ in 0..<20 { log.append(String(repeating: "é", count: 5_000)) }
        log.flush()
        #expect(size(log.url) > 0 && size(log.url) <= 1_024)
        #expect(size(log.previous) > 0 && size(log.previous) <= 1_024)
        #expect(try String(contentsOf: log.url, encoding: .utf8).hasSuffix("… (cut)\n"))
        #expect(RollingLog.cut(String(repeating: "x", count: 256), to: 256).count == 256)
        #expect(RollingLog.cut(String(repeating: "x", count: 257), to: 256).utf8.count == 256)
    }

    @Test func aLineOfTheLogGoesBy90Days() throws {
        let directory = try TemporaryDirectory()
        defer { directory.remove() }
        let log = RollingLog(url: directory.url.appendingPathComponent("remote.log"))
        let now = Date()
        log.append("one", at: now)
        log.flush()
        log.append("two", at: now + 45 * day + 1)
        log.flush()
        // The file was last written 45 days before the second line, so it went whole.
        #expect(try String(contentsOf: log.url, encoding: .utf8).hasSuffix(" two\n"))
        #expect(!FileManager.default.fileExists(atPath: log.previous.path))
        // Begun 45 days ago and written since: it becomes the file before, which goes 45 days after its last line.
        try FileManager.default.setAttributes([.creationDate: now - 45 * day - 1, .modificationDate: now], ofItemAtPath: log.url.path)
        log.append("three", at: now)
        log.flush()
        #expect(try String(contentsOf: log.previous, encoding: .utf8).hasSuffix(" two\n"))
        try FileManager.default.setAttributes([.modificationDate: now - 45 * day - 1], ofItemAtPath: log.previous.path)
        _ = RollingLog(url: log.url).flush()
        #expect(!FileManager.default.fileExists(atPath: log.previous.path), "also at start, with no line")
    }

    // MARK: Hang notes

    #if canImport(Darwin)
    @Test func aHangNoteIsCutAndGoesAt90Days() throws {
        #expect(HangWatchdog.cut(String(repeating: "x", count: 100), to: 100).utf8.count == 100)
        let cut = HangWatchdog.cut(String(repeating: "é", count: 100), to: 100)
        #expect(cut.utf8.count <= 100 && cut.hasSuffix("(cut)\n"))
        let directory = try TemporaryDirectory()
        defer { directory.remove() }
        let now = Date()
        for (name, age) in [("a.txt", 89.0), ("b.txt", 91.0)] {
            let file = directory.url.appending(path: name)
            try Data("hang".utf8).write(to: file)
            try FileManager.default.setAttributes([.modificationDate: now - age * day], ofItemAtPath: file.path)
        }
        HangWatchdog(folder: directory.url, watched: 0, label: "main") { $0() }.prune(now: now)
        #expect(try FileManager.default.contentsOfDirectory(atPath: directory.url.path) == ["a.txt"])
    }
    #endif

    // MARK: The caches' records of an older build

    @Test func anOlderBuildsLargeRecordIsNotReadAndTheSweepRemovesIt() async throws {
        let directory = try TemporaryDirectory()
        defer { directory.remove() }
        let before = LocalCopy(folder: directory.url, cap: LocalCopy.macCap)
        await before.keepWork(Data(count: 8_192), .image, "large")
        await before.keepWork(Data(count: 8), .image, "small")
        let copy = LocalCopy(folder: directory.url, cap: LocalCopy.macCap, recordCap: 4_096)
        #expect(await copy.work(Data.self, .image, "large") == nil)
        #expect(await copy.work(Data.self, .image, "small") != nil)
        await copy.sweep()
        #expect(await copy.stats().count == 1)
    }

    @Test func anOlderBuildsLargeWorkRecordIsNotReadAndTheSweepRemovesIt() async throws {
        let directory = try TemporaryDirectory()
        defer { directory.remove() }
        let file = directory.url.appendingPathComponent("work.sqlite")
        let before = try WorkCache(file: file)
        await before.keep("plan", asked: "large", json: Data(count: 2_048))
        await before.keep("plan", asked: "small", json: Data(count: 8))
        let cache = try WorkCache(file: file, bounds: WorkCache.Bounds(recordBytes: 1_024))
        #expect(await cache.kept("plan", asked: "large") == nil)
        #expect(await cache.kept("plan", asked: "small") != nil)
        await cache.sweep()
        #expect(await cache.stats().count == 1)
        #expect(await cache.bytes() == 8)
    }
}
