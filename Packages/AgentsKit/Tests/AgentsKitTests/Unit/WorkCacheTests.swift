import Foundation
import Testing
@testable import AgentsKitCore

/// Work's client cache (#6352): items by id, each query's answer as ids in the server's order, page by page. Each test
/// has its own cache in memory (or its own file) and a fake clock, so the tests run in parallel and never sleep.
@Suite("Work's client cache")
struct WorkCacheTests {
    /// A clock the test moves by hand.
    final class Clock: @unchecked Sendable {
        private let lock = NSLock()
        private var at = Date(timeIntervalSince1970: 1_795_000_000)
        var now: Date { lock.withLock { at } }
        func move(_ seconds: TimeInterval) { lock.withLock { at += seconds } }
    }

    private let mac = MachineRef.mac
    private let issues = WorkCache.Query(kind: .issue, sort: "priority")
    private let epics = WorkCache.Query(kind: .issue, label: "epic", sort: "priority")
    private let start = Date(timeIntervalSince1970: 1_794_000_000)

    private func makeCache(_ clock: Clock, file: URL? = nil, bounds: WorkCache.Bounds = .mac) throws -> WorkCache {
        try WorkCache(file: file, bounds: bounds, now: { clock.now })
    }

    private func item(_ number: Int, title: String? = nil, updated: TimeInterval = 0) -> DaemonAPI.ForgeItem {
        DaemonAPI.ForgeItem(node: DaemonAPI.LineageNode(kind: "issue", ref: "github:acme/api#\(number)"),
                            title: title ?? "Issue \(number)", state: "open", updatedAt: start + updated)
    }

    private func page(_ numbers: ClosedRange<Int>, cursor: String? = nil, total: Int? = nil) -> DaemonAPI.ForgeList {
        DaemonAPI.ForgeList(items: numbers.map { item($0) }, cursor: cursor, total: total)
    }

    private func numbers(_ answer: WorkCache.Answer?) -> [String] {
        answer?.hosts.first?.items.map(\.node.ref).map { String($0.split(separator: "#").last ?? "") } ?? []
    }

    @Test func aQueryNeverAnsweredHasNoKeptAnswer() async throws {
        let cache = try makeCache(Clock())
        #expect(await cache.answer(issues) == nil)
    }

    @Test func aKeptAnswerDrawsInTheServersOrderWithWhenItWasRead() async throws {
        let clock = Clock()
        let cache = try makeCache(clock)
        let list = DaemonAPI.ForgeList(items: [item(3), item(1), item(2)], cursor: "c2", total: 40)
        await cache.keepPage(issues, host: mac, page: 0, list: list)
        let answer = try #require(await cache.answer(issues))
        #expect(numbers(answer) == ["3", "1", "2"])
        #expect(answer.read == clock.now)
        #expect(answer.hosts.first?.total == 40)
        #expect(answer.hosts.first?.cursor == "c2")
        #expect(await cache.answer(epics) == nil)
    }

    /// #6358, finding 2: a refresh replaces page 1 and keeps the pages read by scrolling, each with its own time.
    @Test func aRefreshKeepsTheLaterPages() async throws {
        let clock = Clock()
        let cache = try makeCache(clock)
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3, cursor: "p2"))
        clock.move(10)
        await cache.keepPage(issues, host: mac, page: 1, list: page(4...6, cursor: "p3"))
        let second = clock.now
        clock.move(10)
        await cache.keepPage(issues, host: mac, page: 2, list: page(7...8))
        clock.move(60)
        await cache.keepPage(issues, host: mac, page: 0, list: page(9...11, cursor: "p2b"))
        let answer = try #require(await cache.answer(issues)?.hosts.first)
        #expect(numbers(WorkCache.Answer(hosts: [answer])) == ["9", "10", "11", "4", "5", "6", "7", "8"])
        #expect(answer.pages.count == 3)
        #expect(answer.read == clock.now)
        #expect(answer.pages.dropFirst().first?.read == second)
        #expect(answer.cursor == nil, "the kept pages reach the end")
    }

    @Test func scrollingAsksThePageAfterTheKeptOnes() async throws {
        let cache = try makeCache(Clock())
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...2, cursor: "p2"))
        #expect(await cache.answer(issues)?.hosts.first?.cursor == "p2")
        await cache.keepPage(issues, host: mac, page: 1, list: page(3...4, cursor: "p3"))
        #expect(await cache.answer(issues)?.hosts.first?.cursor == "p3")
        await cache.keepPage(issues, host: mac, page: 5, list: page(9...9))
        #expect(numbers(await cache.answer(issues)) == ["1", "2", "3", "4"], "a page past the next is not kept")
    }

    @Test func anItemThatMovedUpAPageIsDrawnOnce() async throws {
        let cache = try makeCache(Clock())
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...2, cursor: "p2"))
        await cache.keepPage(issues, host: mac, page: 1, list: page(3...4))
        await cache.keepPage(issues, host: mac, page: 0, list: DaemonAPI.ForgeList(items: [item(4), item(1)], cursor: "p2"))
        #expect(numbers(await cache.answer(issues)) == ["4", "1", "3"])
    }

    @Test func aNewerItemReplacesTheOlderInEveryAnswerThatHoldsIt() async throws {
        let cache = try makeCache(Clock())
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        await cache.keepPage(epics, host: mac, page: 0, list: page(2...2))
        await cache.keepPage(issues, host: mac, page: 0,
                             list: DaemonAPI.ForgeList(items: [item(1), item(2, title: "Renamed", updated: 60), item(3)]))
        #expect(await cache.answer(epics)?.hosts.first?.items.first?.title == "Renamed")
        // A late answer with the older copy does not undo it.
        await cache.keepPage(epics, host: mac, page: 0, list: page(2...2))
        #expect(await cache.answer(epics)?.hosts.first?.items.first?.title == "Renamed")
        #expect(await cache.answer(issues)?.hosts.first?.items.dropFirst().first?.title == "Renamed")
    }

    @Test func aWritesAnswerReplacesItsOptimisticValue() async throws {
        let cache = try makeCache(Clock())
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...1))
        let node = item(1).node
        await cache.edit(node) { $0.priority = "high" }
        let edited = try #require(await cache.answer(issues)?.hosts.first?.items.first)
        #expect(edited.priority == "high")
        #expect(edited.updatedAt == start, "an optimistic value keeps its updatedAt")
        var written = item(1, updated: 5)
        written.priority = "medium"
        await cache.keep(items: [written])
        #expect(await cache.answer(issues)?.hosts.first?.items.first?.priority == "medium")
    }

    @Test func aFailureKeepsTheAnswerWithTheErrorAndTheReadTime() async throws {
        let clock = Clock()
        let cache = try makeCache(clock)
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        let read = clock.now
        clock.move(120)
        await cache.keepFailure(issues, host: mac, errors: [DaemonAPI.ForgeListError(forge: "github", message: "timed out")])
        let answer = try #require(await cache.answer(issues)?.hosts.first)
        #expect(answer.items.count == 3)
        #expect(answer.read == read)
        #expect(answer.failed == clock.now)
        #expect(answer.errors?.first?.message == "timed out")
        clock.move(10)
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        let again = try #require(await cache.answer(issues)?.hosts.first)
        #expect(again.failed == nil)
        #expect(again.errors == nil)
    }

    @Test func eachMachineKeepsItsOwnAnswer() async throws {
        let cache = try makeCache(Clock())
        let devbox = MachineRef(rawValue: "devbox01")
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...2))
        await cache.keepPage(issues, host: devbox, page: 0, list: page(5...5))
        let answer = try #require(await cache.answer(issues))
        #expect(Set(answer.hosts.map(\.host)) == [mac, devbox])
        #expect(answer.hosts.first { $0.host == devbox }?.items.count == 1)
    }

    @Test func anAnswerIsKeptAcrossALaunch() async throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("work-cache-\(UUID().uuidString).sqlite")
        defer {
            for suffix in ["", "-wal", "-shm"] { try? FileManager.default.removeItem(atPath: file.path + suffix) }
        }
        let clock = Clock()
        do {
            let cache = try makeCache(clock, file: file)
            await cache.keepPage(issues, host: mac, page: 0, list: page(1...3, cursor: "p2"))
        }
        let again = try makeCache(clock, file: file)
        #expect(numbers(await again.answer(issues)) == ["1", "2", "3"])
        #expect(await again.answer(issues)?.hosts.first?.cursor == "p2")
    }

    @Test func anItemRowIsCutToItsBound() async throws {
        let cache = try makeCache(Clock())
        var big = item(1, title: String(repeating: "t", count: 5000))
        big.labels = (0..<40).map { DaemonAPI.ForgeLabel(name: "label-\($0)") }
        await cache.keepPage(issues, host: mac, page: 0, list: DaemonAPI.ForgeList(items: [big]))
        let kept = try #require(await cache.answer(issues)?.hosts.first?.items.first)
        #expect(kept.title.count == 256)
        #expect(try JSONEncoder().encode(kept).count <= 4 * 1024)
    }

    @Test func aBodyAndItsCommentsAreCutToTheirBounds() async throws {
        let cache = try makeCache(Clock())
        let comments = (0..<80).map { DaemonAPI.ForgeComment(author: "a", body: "c\($0)", at: start) }
        let body = String(repeating: "é", count: 40 * 1024)
        await cache.keepDetail(DaemonAPI.ForgeItemDetail(item: item(1), body: body, comments: comments))
        let kept = try #require(await cache.detail(item(1).node))
        #expect(kept.detail.body.utf8.count <= 64 * 1024)
        #expect(kept.detail.body.utf8.count > 64 * 1024 - 2)
        #expect(kept.detail.comments.count == 50)
        #expect(kept.detail.comments.first?.body == "c0")
    }

    @Test func overTheBoundBodiesGoFirstThenTheAnswersLeastRecentlyDrawn() async throws {
        // Two answers' size, from a cache of their own, gives a bound that holds them and little more.
        let measure = try makeCache(Clock())
        await measure.keepPage(issues, host: mac, page: 0, list: page(1...20))
        await measure.keepPage(epics, host: mac, page: 0, list: page(21...40))
        let two = await measure.bytes()
        let clock = Clock()
        let cache = try makeCache(clock, bounds: WorkCache.Bounds(bytes: two + 2048))
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...20))
        clock.move(1)
        await cache.keepPage(epics, host: mac, page: 0, list: page(21...40))
        clock.move(1)
        _ = await cache.answer(issues)
        await cache.keepDetail(DaemonAPI.ForgeItemDetail(item: item(1), body: String(repeating: "b", count: 4096)))
        #expect(await cache.detail(item(1).node) == nil, "the body went first")
        #expect(await cache.answer(epics) != nil)
        #expect(await cache.answer(issues) != nil)
        clock.move(1)
        _ = await cache.answer(issues)
        let pulls = WorkCache.Query(kind: .pr)
        await cache.keepPage(pulls, host: mac, page: 0, list: page(41...60))
        #expect(await cache.bytes() <= two + 2048)
        #expect(await cache.answer(epics) == nil, "the answer drawn least recently went")
        #expect(await cache.answer(issues) != nil)
        #expect(await cache.answer(pulls) != nil)
    }

    /// #6573: a page's answer of its own shape (a plan, a timeline, the boards) is kept by the page and what it asked,
    /// with when it was read, and across a launch.
    @Test func aPagesAnswerIsKeptByThePageAndWhatItAsked() async throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("work-cache-\(UUID().uuidString).sqlite")
        defer {
            for suffix in ["", "-wal", "-shm"] { try? FileManager.default.removeItem(atPath: file.path + suffix) }
        }
        let clock = Clock()
        do {
            let cache = try makeCache(clock, file: file)
            #expect(await cache.kept([String].self, "plan", asked: "github:acme/api#1") == nil)
            await cache.keep(["one", "two"], "plan", asked: "github:acme/api#1")
            clock.move(5)
            await cache.keep(["three"], "plan", asked: "github:acme/api#1")
            await cache.keep(["other"], "plan", asked: "github:acme/api#2")
            await cache.keep(["boards"], "boards", asked: "github:acme/api#1")
        }
        let again = try makeCache(clock, file: file)
        let plan = try #require(await again.kept([String].self, "plan", asked: "github:acme/api#1"))
        #expect(plan.value == ["three"], "the newer answer replaced the older")
        #expect(plan.read == clock.now)
        #expect(await again.kept([String].self, "plan", asked: "github:acme/api#2")?.value == ["other"])
        #expect(await again.kept([String].self, "boards", asked: "github:acme/api#1")?.value == ["boards"])
        #expect(await again.kept(Int.self, "plan", asked: "github:acme/api#1") == nil, "one of another form")
    }

    @Test func aPagesAnswerOverItsBoundIsNotKept() async throws {
        let cache = try makeCache(Clock(), bounds: WorkCache.Bounds(pageBytes: 64))
        await cache.keep("small", "deploys", asked: "all")
        await cache.keep(String(repeating: "x", count: 100), "deploys", asked: "all")
        #expect(await cache.kept(String.self, "deploys", asked: "all") == nil, "the old answer went, so the page asks again")
    }

    @Test func overTheBoundThePagesAnswersGoBeforeTheLists() async throws {
        let measure = try makeCache(Clock())
        await measure.keepPage(issues, host: mac, page: 0, list: page(1...20))
        let list = await measure.bytes()
        let clock = Clock()
        let cache = try makeCache(clock, bounds: WorkCache.Bounds(bytes: list + 1024))
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...20))
        clock.move(1)
        await cache.keep(String(repeating: "t", count: 2048), "timeline", asked: "p")
        #expect(await cache.bytes() <= list + 1024)
        #expect(await cache.kept(String.self, "timeline", asked: "p") == nil)
        #expect(await cache.answer(issues) != nil)
    }

    @Test func forgetRemovesEverything() async throws {
        let cache = try makeCache(Clock())
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        await cache.keep("plan", "plan", asked: "a")
        await cache.forget()
        #expect(await cache.answer(issues) == nil)
        #expect(await cache.kept(String.self, "plan", asked: "a") == nil)
        #expect(await cache.bytes() == 0)
    }

    /// #6352: a kept answer of 300 ids draws in under 10 ms on this Mac. Timing, so quarantined in CI.
    @Test(.flakyUnderLoad) func aKeptAnswerOf300DrawsInUnder10ms() async throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("work-cache-\(UUID().uuidString).sqlite")
        defer {
            for suffix in ["", "-wal", "-shm"] { try? FileManager.default.removeItem(atPath: file.path + suffix) }
        }
        let clock = Clock()
        let cache = try makeCache(clock, file: file)
        for index in 0..<3 {
            await cache.keepPage(issues, host: mac, page: index,
                                 list: page((index * 100 + 1)...(index * 100 + 100), cursor: "p\(index + 2)"))
        }
        let cold = try makeCache(clock, file: file)
        let watch = ContinuousClock()
        let first = await watch.measure { _ = await cold.answer(issues) }
        var draws: [Duration] = []
        for _ in 0..<5 {
            draws.append(await watch.measure { #expect(await cold.answer(issues)?.hosts.first?.items.count == 300) })
        }
        let median = draws.sorted()[2] // index-ok: five draws, made above
        print("work-cache: 300 ids, first draw \(first), then \(median)")
        #expect(median < .milliseconds(10))
    }
}
