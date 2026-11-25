import Foundation
import SQLite3
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

    /// #6642: a daemon's kept answer was read before it came: "Last update" is the forges' read, never later than now.
    @Test func aPageIsReadWhenTheForgesWereRead() async throws {
        let clock = Clock()
        let cache = try makeCache(clock)
        await cache.keepPage(issues, host: mac, page: 0, list: DaemonAPI.ForgeList(items: [item(1)], readAt: clock.now - 40))
        #expect(try #require(await cache.answer(issues)).read == clock.now - 40)
        await cache.keepPage(issues, host: mac, page: 0, list: DaemonAPI.ForgeList(items: [item(1)], readAt: clock.now + 30))
        #expect(try #require(await cache.answer(issues)).read == clock.now)
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

    /// #6635: at the start no machine is online or known yet, and the kept answer of every machine still draws; once
    /// machines are asked, theirs come first, in their order, and a machine not asked keeps its rows after them.
    @Test func theKeptAnswerDrawsEveryMachineAlsoWithNoneOnline() async throws {
        let cache = try makeCache(Clock())
        let devbox = MachineRef(rawValue: "devbox01")
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        await cache.keepPage(issues, host: devbox, page: 0, list: page(4...5))
        let answer = try #require(await cache.answer(issues))
        let kept = answer.hosts.map(\.host)
        #expect(answer.shown(first: []).map(\.host) == kept)
        #expect(answer.shown(first: []).reduce(0) { $0 + $1.items.count } == 5)
        #expect(answer.shown(first: [devbox]).map(\.host) == [devbox] + kept.filter { $0 != devbox })
        #expect(answer.shown(first: [devbox, mac]).map(\.host) == [devbox, mac])
        #expect(answer.shown(first: [MachineRef(rawValue: "new"), mac]).map(\.host) == [mac, devbox])
    }

    /// #6358, finding 2, and #6636 (c): a refresh replaces page 1 and keeps the pages read by scrolling, each marked
    /// stale with its own time, the old page 1 too; the list asks on from the new page 1's cursor, also when a stale page said "the end".
    @Test func aRefreshKeepsThePagesReadBeforeStaleAndAsksOnFromTheNewFirstPage() async throws {
        let clock = Clock()
        let cache = try makeCache(clock)
        let first = clock.now
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3, cursor: "p2"))
        clock.move(10)
        await cache.keepPage(issues, host: mac, page: 1, list: page(4...6, cursor: "p3"))
        let second = clock.now
        clock.move(10)
        await cache.keepPage(issues, host: mac, page: 2, list: page(7...8))
        clock.move(60)
        await cache.keepPage(issues, host: mac, page: 0, list: page(9...11, cursor: "p2b"))
        let answer = try #require(await cache.answer(issues)?.hosts.first)
        #expect(numbers(WorkCache.Answer(hosts: [answer])) == ["9", "10", "11", "1", "2", "3", "4", "5", "6", "7", "8"])
        #expect(answer.pages.map(\.stale) == [false, true, true, true])
        #expect(answer.fresh == 1)
        #expect(answer.read == clock.now)
        #expect(answer.pages.map(\.read).dropFirst() == [first, second, second + 10])
        #expect(answer.staleRead == first)
        #expect(answer.cursor == "p2b", "a stale page's end is not the end")
    }

    /// #6636: a fresh page at the end of the list drops the stale pages: what they still held left the list.
    @Test func aFreshPageAtTheEndDropsTheStalePages() async throws {
        let cache = try makeCache(Clock())
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...2, cursor: "p2"))
        await cache.keepPage(issues, host: mac, page: 1, list: page(3...4))
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        let answer = try #require(await cache.answer(issues)?.hosts.first)
        #expect(numbers(WorkCache.Answer(hosts: [answer])) == ["1", "2", "3"])
        #expect(answer.pages.count == 1)
        #expect(answer.cursor == nil)
    }

    /// #6636: the next page goes after the fresh ones and takes its items out of the stale pages, and the stale items
    /// the server's order puts before its last item, which closed.
    @Test func aFreshPageCoversTheStaleItemsBeforeItsLast() async throws {
        let updated = WorkCache.Query(kind: .issue, sort: "updated")
        let cache = try makeCache(Clock())
        func list(_ numbers: [Int], cursor: String?) -> DaemonAPI.ForgeList {
            DaemonAPI.ForgeList(items: numbers.map { item($0, updated: TimeInterval($0)) }, cursor: cursor)
        }
        await cache.keepPage(updated, host: mac, page: 0, list: list([9, 8], cursor: "a"))
        await cache.keepPage(updated, host: mac, page: 1, list: list([7, 6], cursor: "b"))
        await cache.keepPage(updated, host: mac, page: 2, list: list([5, 4], cursor: nil))
        // 12 and 11 are new; 8 closed; 7 moved from page 2 to page 3.
        await cache.keepPage(updated, host: mac, page: 0, list: list([12, 11], cursor: "c"))
        #expect(numbers(await cache.answer(updated)) == ["12", "11", "9", "8", "7", "6", "5", "4"],
                "nothing read before is lost to the refresh")
        await cache.keepPage(updated, host: mac, page: 1, list: list([9, 7], cursor: "d"))
        let answer = try #require(await cache.answer(updated)?.hosts.first)
        #expect(numbers(WorkCache.Answer(hosts: [answer])) == ["12", "11", "9", "7", "6", "5", "4"], "8 closed")
        #expect(answer.fresh == 2)
        #expect(answer.cursor == "d")
    }

    /// Review #6355's case (#6636): 300 open issues read to the end, then 5,000 (a big project) and a Refresh. A scroll
    /// asks on from the new page 1 until the forge's end, and the list is then the forge's list: no item lost, none
    /// twice, and the closed ones gone.
    @Test func aRefreshFrom300To5000ScrollsToTheEndWithNoItemLost() async throws {
        let updated = WorkCache.Query(kind: .issue, sort: "updated")
        let clock = Clock()
        let cache = try makeCache(clock)
        func forge(_ open: [DaemonAPI.ForgeItem], at cursor: String?) -> DaemonAPI.ForgeList {
            let from = cursor.flatMap(Int.init) ?? 0
            let to = min(from + 100, open.count)
            return DaemonAPI.ForgeList(items: Array(open[from ..< to]), cursor: to < open.count ? "\(to)" : nil, total: open.count)
        }
        func ordered(_ items: [DaemonAPI.ForgeItem]) -> [DaemonAPI.ForgeItem] { items.sorted { $0.updatedAt > $1.updatedAt } }
        let before = ordered((1...300).map { item($0, updated: TimeInterval($0)) })
        var after = before
        // Then: 4,700 new issues, #150 changed (to the top), #120 and #250 closed.
        after.removeAll { [120, 150, 250].contains(Int(String($0.node.ref.split(separator: "#").last ?? "")) ?? 0) }
        after += (301...5000).map { item($0, updated: TimeInterval($0)) }
        after.append(item(150, title: "Changed", updated: 6000))
        after = ordered(after)

        var cursor: String?
        for n in 0..<3 {
            let read = forge(before, at: cursor)
            await cache.keepPage(updated, host: mac, page: n, list: read)
            cursor = read.cursor
        }
        #expect(numbers(await cache.answer(updated)).count == 300)
        clock.move(3600)
        await cache.keepPage(updated, host: mac, page: 0, list: forge(after, at: nil))
        let refreshed = try #require(await cache.answer(updated)?.hosts.first)
        #expect(refreshed.items.count == 100 + 299, "page 1 and the 299 items read before that it does not hold")
        #expect(refreshed.cursor == "100", "the list asks on from the new page 1")
        #expect(refreshed.total == 4998)

        var asks = 0
        while let host = await cache.answer(updated)?.hosts.first, let next = host.cursor, asks < 100 {
            await cache.keepPage(updated, host: mac, page: host.fresh, list: forge(after, at: next))
            clock.move(1)
            asks += 1
        }
        #expect(asks == 49)
        let end = try #require(await cache.answer(updated)?.hosts.first)
        #expect(end.items.map(\.node.ref) == after.map(\.node.ref), "the forge's 4,998 open issues, in its order")
        #expect(end.items.first?.title == "Changed")
        #expect(end.pages.allSatisfy { !$0.stale })
        #expect(end.cursor == nil)
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
        #expect(numbers(await cache.answer(issues)) == ["4", "1", "2", "3"], "2 is kept stale until a fresh page covers it (#6636)")
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

    @Test func aListReadWhileTheWriteRunsDoesNotUndoTheOptimisticValue() async throws {
        let cache = try makeCache(Clock())
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...1))
        let node = item(1).node
        await cache.edit(node) { $0.priority = "high" }
        // A refresh with the copy as old as the kept one (#6354): the optimistic value stays until its own answer.
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...1))
        #expect(await cache.item(node)?.priority == "high")
        // The write ended (refused, and changed back): the next copy as old replaces it again.
        await cache.settle(node)
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...1))
        #expect(await cache.item(node)?.priority == nil)
    }

    @Test func anItemsKeptPageShowsItsOptimisticValue() async throws {
        let cache = try makeCache(Clock())
        let node = item(1).node
        await cache.keepDetail(DaemonAPI.ForgeItemDetail(item: item(1), body: "Body", comments: []))
        await cache.edit(node) { $0.state = "closed" }
        #expect(await cache.detail(node)?.detail.item.state == "closed")
        #expect(await cache.detail(node)?.detail.body == "Body")
    }

    /// A machine left out of a query, as another reads what it reads (#6641), draws no rows of its own.
    @Test func aMachineLeftOutLosesItsKeptAnswer() async throws {
        let cache = try makeCache(Clock())
        let devbox = MachineRef(rawValue: "devbox01")
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        await cache.keepPage(issues, host: devbox, page: 0, list: page(1...3))
        await cache.forget(issues, host: devbox)
        #expect(await cache.answer(issues)?.hosts.map(\.host) == [mac])
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
        let cache = try makeCache(Clock(), bounds: WorkCache.Bounds(recordBytes: 64))
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

    /// #6643: the bound is checked on a running count, not a sum over every row; it stays equal to the sum through
    /// each write, replace, cut and trim.
    @Test func theRunningByteCountEqualsTheRowsSum() async throws {
        let clock = Clock()
        let cache = try makeCache(clock, bounds: WorkCache.Bounds(bytes: 24 * 1024, memoItems: 30))
        for index in 0..<6 {
            clock.move(1)
            let query = WorkCache.Query(kind: .issue, label: "l\(index)")
            await cache.keepPage(query, host: mac, page: 0, list: page((index * 20 + 1)...(index * 20 + 20)))
            #expect(await cache.bytes() == cache.summedBytes())
        }
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...5))
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        await cache.keep(items: [item(2, title: "Renamed", updated: 10)])
        await cache.edit(item(3).node) { $0.title = "Edited" }
        await cache.keepDetail(DaemonAPI.ForgeItemDetail(item: item(1), body: "body"))
        await cache.keepDetail(DaemonAPI.ForgeItemDetail(item: item(1), body: "a longer body"))
        await cache.keep("plan", "plan", asked: "a")
        await cache.keep(String(repeating: "x", count: 5 * 1024 * 1024), "plan", asked: "a")
        #expect(await cache.bytes() == cache.summedBytes())
        #expect(await cache.bytes() <= 24 * 1024)
        #expect(await cache.answer(issues)?.hosts.first?.items.count == 3)
        await cache.forget()
        #expect(await cache.summedBytes() == 0)
    }

    private let day: TimeInterval = 24 * 60 * 60

    /// #6788: each kind of record goes 90 days after it was read; drawing it does not keep it.
    @Test func everyRecordAgesOutAt90Days() async throws {
        let clock = Clock()
        let cache = try makeCache(clock)
        let began = clock.now
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        await cache.keepFailure(epics, host: mac, errors: [])
        await cache.keepDetail(DaemonAPI.ForgeItemDetail(item: item(9), body: "body"))
        await cache.keep("plan", "plan", asked: "a")
        clock.move(90 * day)
        await cache.sweep()
        #expect(await cache.stats().oldest == began, "90 days is kept")
        #expect(numbers(await cache.answer(issues)) == ["1", "2", "3"])
        clock.move(1)
        await cache.sweep()
        #expect(await cache.stats() == CacheStats(count: 0, bytes: 0, oldest: nil))
        #expect(await cache.summedBytes() == 0)
        #expect(await cache.item(item(1).node) == nil, "also from memory")
    }

    @Test func aWriteAgesOldRecordsOut() async throws {
        let clock = Clock()
        let cache = try makeCache(clock)
        await cache.keep("plan", "plan", asked: "a")
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        clock.move(91 * day)
        await cache.keep(items: [item(50)])
        #expect(await cache.stats().count == 1)
        #expect(await cache.stats().oldest == clock.now)
    }

    @Test func aFileOpenedAfterItAgedDrawsNothing() async throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("work-cache-\(UUID().uuidString).sqlite")
        defer {
            for suffix in ["", "-wal", "-shm"] { try? FileManager.default.removeItem(atPath: file.path + suffix) }
        }
        let clock = Clock()
        do {
            let cache = try makeCache(clock, file: file)
            await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        }
        clock.move(91 * day)
        let again = try makeCache(clock, file: file)
        #expect(await again.answer(issues) == nil)
        #expect(await again.stats().count == 0)
    }

    @Test func anAnswerLosesOnlyItsAgedPages() async throws {
        let clock = Clock()
        let cache = try makeCache(clock)
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3, cursor: "p2"))
        await cache.keepPage(issues, host: mac, page: 1, list: page(4...6, cursor: "p3"))
        clock.move(60 * day)
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...3, cursor: "p2"))
        #expect(numbers(await cache.answer(issues)) == ["1", "2", "3", "4", "5", "6"])
        clock.move(31 * day)
        await cache.sweep()
        #expect(numbers(await cache.answer(issues)) == ["1", "2", "3"])
        #expect(await cache.answer(issues)?.hosts.first?.cursor == "p2")
        #expect(await cache.item(item(5).node) == nil)
        #expect(await cache.bytes() == cache.summedBytes())
    }

    /// An answer of a file from before #6788 has no read time of its own: the first sweep sets it and keeps the pages.
    @Test func anOlderFilesAnswersKeepTheirPages() async throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("work-cache-\(UUID().uuidString).sqlite")
        defer {
            for suffix in ["", "-wal", "-shm"] { try? FileManager.default.removeItem(atPath: file.path + suffix) }
        }
        let clock = Clock()
        do {
            let cache = try makeCache(clock, file: file)
            await cache.keepPage(issues, host: mac, page: 0, list: page(1...3))
        }
        var db: OpaquePointer?
        #expect(sqlite3_open(file.path, &db) == SQLITE_OK)
        sqlite3_busy_timeout(db, 5000)
        #expect(sqlite3_exec(db, "DROP INDEX answer_read; ALTER TABLE answer DROP COLUMN read", nil, nil, nil) == SQLITE_OK)
        sqlite3_close(db)
        let again = try makeCache(clock, file: file)
        #expect(numbers(await again.answer(issues)) == ["1", "2", "3"])
        #expect(await again.stats().oldest == clock.now)
    }

    @Test func aRecordOverTheLargestIsCutOrNotKept() async throws {
        let cache = try makeCache(Clock(), bounds: WorkCache.Bounds(recordBytes: 1024))
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...10, cursor: "p2"))
        await cache.keepPage(issues, host: mac, page: 1, list: page(11...60, cursor: "p3"))
        #expect(await cache.answer(issues)?.hosts.first?.items.count == 10, "the last page went")
        let long = [DaemonAPI.ForgeComment(author: "a", body: String(repeating: "c", count: 2048), at: start)]
        await cache.keepDetail(DaemonAPI.ForgeItemDetail(item: item(1), body: "body"))
        await cache.keepDetail(DaemonAPI.ForgeItemDetail(item: item(1), body: "body", comments: long))
        #expect(await cache.detail(item(1).node) == nil)
    }

    /// #6908: a page's record at its limit is kept; one over it is not, and nor is what was there before.
    @Test func aPageAtTheLargestIsKeptAndOneOverItIsNot() async throws {
        let cache = try makeCache(Clock(), bounds: WorkCache.Bounds(recordBytes: 1024))
        var size = 1024
        while size > 900 {
            await cache.keep(String(repeating: "x", count: size), "plan", asked: "a")
            if await cache.kept(String.self, "plan", asked: "a") != nil { break }
            size -= 1
        }
        #expect(size > 900)
        await cache.keep(String(repeating: "x", count: size + 1), "plan", asked: "a")
        #expect(await cache.kept(String.self, "plan", asked: "a") == nil)
        await cache.keep(String(repeating: "é", count: size), "plan", asked: "a")
        #expect(await cache.kept(String.self, "plan", asked: "a") == nil, "the limit is in bytes")
    }

    /// #6908: an item over its limit after the cut does not show its earlier copy as new.
    @Test func anItemOverItsLimitAfterTheCutIsNotDrawnAsItWas() async throws {
        let cache = try makeCache(Clock())
        await cache.keepPage(issues, host: mac, page: 0, list: page(1...2))
        var long = item(1, updated: 10)
        long.state = "closed"
        long.url = "https://x/" + String(repeating: "u", count: 5000)
        await cache.keepPage(issues, host: mac, page: 0, list: DaemonAPI.ForgeList(items: [long, item(2)]))
        let drawn = await cache.answer(issues)?.hosts.first?.items.first { $0.node == long.node }
        #expect(drawn == nil || drawn?.state == "closed")
    }

    @Test func noBoundIsOver90Days() {
        #expect(WorkCache.Bounds.mac.age == 90 * day)
        #expect(WorkCache.Bounds.remote.age == 90 * day)
        #expect(WorkCache.Bounds(age: 400 * day).age == 90 * day)
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
