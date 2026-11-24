import Foundation
import Testing
@testable import AgentsKitCore

/// The lookup that the conversation windows share (#7075).
@MainActor
@Suite("Conversation lookup")
struct ConversationLookupTests {
    static let machines = [MachineRef.mac]

    /// Pages of one conversation each, `slack:0` the first. Each read waits until `open`, and is counted.
    @MainActor
    final class Pages {
        var reads: [Int] = []
        let count: Int
        private var gate: [CheckedContinuation<Void, Never>] = []
        private var isOpen: Bool
        private var begun: [CheckedContinuation<Void, Never>] = []

        init(_ count: Int, open: Bool = true) {
            self.count = count
            isOpen = open
        }

        func open() {
            isOpen = true
            gate.forEach { $0.resume() }
            gate = []
        }

        /// Returns when a read has begun.
        func firstRead() async {
            if reads.isEmpty { await withCheckedContinuation { begun.append($0) } }
        }

        func read(_ cursor: String?, _ into: [MessagesList.HostConversations]) async -> ConversationLookup.Page {
            let at = cursor.flatMap(Int.init) ?? 0
            reads.append(at)
            begun.forEach { $0.resume() }
            begun = []
            if !isOpen { await withCheckedContinuation { gate.append($0) } }
            let item = DaemonAPI.MessagesConversation(
                server: "slack", folder: "/p",
                conversation: .init(source: "slack", id: String(at), title: String(at), kind: .channel))
            let lists = [MessagesList.HostConversations(host: .mac, name: "This Mac",
                                                        conversations: (into.first?.conversations ?? []) + [item])]
            return (lists, at + 1 < count ? String(at + 1) : nil)
        }
    }

    static func found(_ answer: ConversationLookup.Answer) -> String? {
        if case let .found(row) = answer { row.id } else { nil }
    }

    static func missed(_ answer: ConversationLookup.Answer) -> ConversationLookup.Sweep? {
        if case let .missing(sweep) = answer { sweep } else { nil }
    }

    /// Six windows at launch: five keys on the five pages and one on none. Each page is read once.
    @Test func sixWindowsReadEachPageOnce() async {
        let pages = Pages(9, open: false)
        let lookup = ConversationLookup()
        let windows = (0 ..< 6).map { n in
            Task { await lookup.find("slack:\(n == 5 ? "gone" : String(n))", machines: Self.machines, read: pages.read) }
        }
        await pages.firstRead()
        pages.open()
        var answers: [ConversationLookup.Answer] = []
        for window in windows { answers.append(await window.value) }
        #expect(answers.map(Self.found) == ["slack:0", "slack:1", "slack:2", "slack:3", "slack:4", nil])
        #expect(Self.missed(answers[5]) != nil)
        #expect(pages.reads == [0, 1, 2, 3, 4])
    }

    @Test func noPageIsReadAfterEachKeyIsFound() async {
        let pages = Pages(5)
        let lookup = ConversationLookup()
        #expect(Self.found(await lookup.find("slack:1", machines: Self.machines, read: pages.read)) == "slack:1")
        #expect(pages.reads == [0, 1])
        // A window a moment later starts from the pages read, and goes on after them.
        #expect(Self.found(await lookup.find("slack:0", machines: Self.machines, read: pages.read)) == "slack:0")
        #expect(Self.found(await lookup.find("slack:2", machines: Self.machines, read: pages.read)) == "slack:2")
        #expect(pages.reads == [0, 1, 2])
    }

    @Test func aKnownRowReadsNothing() async {
        let pages = Pages(5)
        let row = MessagesList.Row(host: .mac, item: .init(
            server: "slack", folder: "/p", conversation: .init(source: "slack", id: "x", title: "x", kind: .channel)))
        let answer = await ConversationLookup().find("slack:x", machines: Self.machines, known: { row }, read: pages.read)
        #expect(Self.found(answer) == "slack:x")
        #expect(pages.reads.isEmpty)
    }

    /// Try Again reads again; a second window's joins the read that the first began.
    @Test func tryAgainReadsAgainOnceForAll() async throws {
        let pages = Pages(1)
        let lookup = ConversationLookup()
        let old = try #require(Self.missed(await lookup.find("slack:gone", machines: Self.machines, read: pages.read)))
        let first = await lookup.find("slack:gone", machines: Self.machines, after: old, read: pages.read)
        let second = await lookup.find("slack:lost", machines: Self.machines, after: old, read: pages.read)
        #expect(Self.missed(first) === Self.missed(second))
        #expect(pages.reads == [0, 0])
    }

    @Test func otherMachinesOrOldPagesAreReadAgain() async {
        let pages = Pages(1)
        var now = Date(timeIntervalSince1970: 0)
        let lookup = ConversationLookup(now: { now })
        _ = await lookup.find("slack:0", machines: Self.machines, read: pages.read)
        _ = await lookup.find("slack:0", machines: Self.machines + [MachineRef(rawValue: "box")], read: pages.read)
        #expect(pages.reads == [0, 0])
        now += ConversationLookup.keep
        _ = await lookup.find("slack:0", machines: Self.machines + [MachineRef(rawValue: "box")], read: pages.read)
        #expect(pages.reads == [0, 0, 0])
    }

    /// A window that closes while its page is read does not stop the others, and no further page is read for it.
    @Test func aClosedWindowDoesNotStopTheOthers() async {
        let pages = Pages(5, open: false)
        let lookup = ConversationLookup()
        let closed = Task { await lookup.find("slack:4", machines: Self.machines, read: pages.read) }
        await pages.firstRead()
        let other = Task { await lookup.find("slack:1", machines: Self.machines, read: pages.read) }
        closed.cancel()
        pages.open()
        #expect(Self.found(await other.value) == "slack:1")
        if case .cancelled = await closed.value {} else { Issue.record("the closed window's lookup was not cancelled") }
        #expect(pages.reads == [0, 1])
    }
}
