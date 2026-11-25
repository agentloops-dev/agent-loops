import Foundation
import Testing
@testable import AgentsKitCore

/// Agents Host keeps only the newest 2 stores and control plane keys set aside (#3671).
@Suite("Stores set aside")
struct StoresAsideTests {
    /// The name `prepareReturn` gives a store set aside on a move back.
    private func returnName(_ seconds: Int) -> String {
        let day = Date(timeIntervalSince1970: TimeInterval(seconds)).formatted(.iso8601.year().month().day())
        return "store-before-\(day)-\(seconds % 100_000)"
    }

    @Test func eachNameGivesTheTimeItWasSetAside() {
        #expect(StoresAside.time(of: "store-before-1793000123") == Date(timeIntervalSince1970: 1_793_000_123))
        #expect(StoresAside.time(of: "control-key-before-1793000123") == Date(timeIntervalSince1970: 1_793_000_123))
        for seconds in [1_793_000_123, 1_793_059_999, 1_793_060_000, 1_792_972_800, 1_793_059_199] {
            #expect(StoresAside.time(of: returnName(seconds)) == Date(timeIntervalSince1970: TimeInterval(seconds)))
        }
        #expect(StoresAside.time(of: "store") == nil)
        #expect(StoresAside.time(of: "store-before-notes") == nil)
        #expect(StoresAside.time(of: "control-key") == nil)
    }

    @Test func onlyTheNewestTwoStayAndOtherNamesAreLeftAlone() {
        let names = ["store-before-1000", returnName(1_793_000_123), "store-before-3000", "store-before-x",
                     "store-before-1793100000"]
        #expect(StoresAside.beyond(names) == ["store-before-1000", "store-before-3000"])
        #expect(StoresAside.beyond(["control-key-before-5", "control-key-before-9"]).isEmpty)
        #expect(StoresAside.beyond(["control-key-before-5", "control-key-before-9", "control-key-before-7"])
            == ["control-key-before-5"])
    }

    @Test func aKeyGoesAtNinetyDaysOnlyOnceNoStoreIsLeft() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        func key(days: Int) -> String { "control-key-before-\(1_800_000_000 - days * 86_400)" }
        let aged = key(days: 90), young = key(days: 89)
        let names = ["control-key", "bucket-keys", "control-key-before-notes", aged, young]

        #expect(StoresAside.expiredKeys(names, stores: ["store"], now: now) == [aged])
        // Any store set aside holds them, also one with no time in its name.
        for store in ["store-before-1", "store-before-1799999999", returnName(1_793_000_123), "store-before-notes"] {
            #expect(StoresAside.expiredKeys(names, stores: ["store", store], now: now).isEmpty)
        }
        // Stores that could not be read may be there.
        #expect(StoresAside.expiredKeys(names, stores: nil, now: now).isEmpty)
        // The newest 2 is still the most there are, held or not, and each is named once.
        let oldest = key(days: 300)
        let three = [oldest, key(days: 200), key(days: 100)]
        #expect(StoresAside.expiredKeys(three, stores: ["store-before-1"], now: now) == [oldest])
        #expect(StoresAside.expiredKeys(three, stores: [], now: now) == three)
    }

    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func folder(days: Int, bytes: Int? = 1) -> StoresAside.Folder {
        .init(name: "store-before-\(1_800_000_000 - days * 86_400)", bytes: bytes)
    }

    private func expired(_ folders: [StoresAside.Folder], _ plane: StoresAside.Plane = .serving) -> [String] {
        StoresAside.expiredFolders(folders, plane: plane, now: now, mostBytes: 100)
    }

    @Test func aFolderGoesAtNinetyDaysTheNewestToo() {
        let aged = folder(days: 90), older = folder(days: 200), young = folder(days: 89)
        #expect(expired([young, aged]) == [aged.name])
        #expect(expired([aged, older]) == [older.name, aged.name])
        #expect(expired([.init(name: "store-before-notes", bytes: 1), .init(name: "store", bytes: 1), young]).isEmpty)
    }

    @Test func countAndSizeNeverTakeTheNewest() {
        let a = folder(days: 3), b = folder(days: 2), c = folder(days: 1)
        #expect(expired([c, a, b]) == [a.name])
        #expect(expired([folder(days: 2, bytes: 50), folder(days: 1, bytes: 50)]).isEmpty)
        #expect(expired([folder(days: 2, bytes: 1), folder(days: 1, bytes: 100)]) == [b.name])
        #expect(expired([folder(days: 1, bytes: 101)]).isEmpty)
        // A size that could not be counted deletes nothing for size.
        #expect(expired([folder(days: 2, bytes: nil), folder(days: 1, bytes: 101)]).isEmpty)
    }

    @Test func whileThePlaneIsAwayNothingAgesOutAndItsStoreStays() {
        let away = StoresAside.Plane.away(since: now.addingTimeInterval(-100 * 86_400 + 0.5))
        let before = folder(days: 150), real = folder(days: 100, bytes: 101)
        let empty = folder(days: 2, bytes: 0), emptyAgain = folder(days: 1, bytes: 0)
        #expect(expired([real], away).isEmpty)
        #expect(expired([real, empty], away).isEmpty)
        #expect(expired([before, real, empty, emptyAgain], away) == [before.name, empty.name])
        #expect(expired([before, real, empty, emptyAgain], .unknown).isEmpty)
    }

    @Test func pruningAScratchHomeDeletesOnlyTheOldStores() throws {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("stores-aside-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: home) }
        let newest = returnName(1_793_000_123)
        let names = ["store-before-100", newest, "store-before-300", "store-before-notes", "store"]
        for name in names {
            try FileManager.default.createDirectory(at: home.appendingPathComponent(name), withIntermediateDirectories: true)
        }
        try Data("key".utf8).write(to: home.appendingPathComponent("control-key-before-100"))
        try Data("12345".utf8).write(to: home.appendingPathComponent("\(newest)/record"))
        #expect(StoresAside.bytes(of: home.appendingPathComponent(newest)) == 5)
        let today = Date(timeIntervalSince1970: 1_793_000_200)

        // A store with no records takes no folder's place.
        #expect(StoresAside.pruneFolders(in: home, plane: .serving, now: today).isEmpty)
        let records = home.appendingPathComponent("store/v1")
        try FileManager.default.createDirectory(at: records, withIntermediateDirectories: true)
        try Data("{}".utf8).write(to: records.appendingPathComponent("control.json"))
        #expect(StoresAside.pruneFolders(in: home, plane: .serving, now: today) == ["store-before-100", "store-before-300"])
        let left = try FileManager.default.contentsOfDirectory(atPath: home.path).sorted()
        #expect(left == ["control-key-before-100", "store", "store-before-notes", newest].sorted())
    }
}
