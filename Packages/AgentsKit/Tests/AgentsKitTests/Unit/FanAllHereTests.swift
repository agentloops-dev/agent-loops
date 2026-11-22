import Foundation
import Testing
@testable import AgentsKitCore

/// #5996: in its lock, the window asks each machine itself for what the control plane's calls for every machine ask, as
/// the control plane's fanall.go and fanout.go answer them, and as Web/test/fanAllHere.test.mjs has it on the web page.
@Suite("Calls for every machine, answered here")
struct FanAllHereTests {
    static let hosts = [
        FanAllHere.Host(id: "a", name: "A"),
        FanAllHere.Host(id: "b", name: "B"),
        FanAllHere.Host(id: "c", name: "C"),
    ]

    /// What each machine was asked, in order of answer.
    final class Asked: @unchecked Sendable {
        private let lock = NSLock()
        private var calls: [(host: String, method: String, params: JSONValue)] = []
        func add(_ host: String, _ method: String, _ params: JSONValue) { lock.withLock { calls.append((host, method, params)) } }
        var all: [(host: String, method: String, params: JSONValue)] { lock.withLock { calls } }
    }

    static func answer<R: Decodable>(_ method: String, _ params: some Encodable, returning: R.Type, asked: Asked,
                                     answers: [String: JSONValue]) async throws -> R? {
        try await FanAllHere.answer(method, JSONValue.encoding(params), hosts: { _ in hosts }, ask: { method, params, host in
            asked.add(host, method, params)
            return answers[host]
        })?.decode(R.self)
    }

    @Test("forge/listAll asks each machine's forge/pulls itself, whatever its state; one that fails is unreached")
    func forgeList() async throws {
        let asked = Asked()
        let list = try JSONValue.encoding(DaemonAPI.ForgeList(cursor: "next"))
        let got = try #require(try await Self.answer(DaemonAPI.Method.forgeListAll,
                                                     DaemonAPI.ForgeListAllRequest(kind: "pulls", state: "open", limit: 500),
                                                     returning: DaemonAPI.ForgeListAll.self, asked: asked, answers: ["a": list]))
        #expect(got.lists.map(\.machine.rawValue) == ["a"])
        #expect(got.lists.first?.list.cursor == "next")
        #expect(got.unreached.map(\.machine.rawValue) == ["b", "c"])
        #expect(asked.all.map(\.host).sorted() == ["a", "b", "c"])
        #expect(asked.all.allSatisfy { $0.method == "forge/pulls" && $0.params["limit"]?.intValue == 100 })
    }

    @Test("forge/listAll gives each machine the label, the assignee and the sort, so a narrowed list is one call (#6383, #6634)")
    func forgeListNarrowed() async throws {
        let asked = Asked()
        _ = try await Self.answer(DaemonAPI.Method.forgeListAll,
                                  DaemonAPI.ForgeListAllRequest(kind: "issues", label: "bug", assignee: "alex", sort: "priority"),
                                  returning: DaemonAPI.ForgeListAll.self, asked: asked, answers: [:])
        #expect(asked.all.count == 3)
        #expect(asked.all.allSatisfy { $0.params["label"]?.stringValue == "bug" && $0.params["assignee"]?.stringValue == "alex"
            && $0.params["sort"]?.stringValue == "priority" })
    }

    @Test("docs/listAll merges the machines' docs newest first, and its cursor goes on from where each machine was")
    func docsPages() async throws {
        func doc(_ ref: String, _ updated: Double) -> JSONValue {
            .object(["ref": .string(ref), "title": .string(ref), "source": .string("s"), "updated": .double(updated)])
        }
        let answers: [String: JSONValue] = [
            "a": .object(["docs": .array([doc("a1", 9), doc("a2", 5)]), "sources": .array([])]),
            "b": .object(["docs": .array([doc("b1", 7), doc("b2", 1)]), "sources": .array([])]),
        ]
        let asked = Asked()
        let first = try #require(try await Self.answer(DaemonAPI.Method.docsListAll, DaemonAPI.DocsListAllRequest(limit: 3),
                                                       returning: DaemonAPI.DocsListAll.self, asked: asked, answers: answers))
        #expect(first.docs.map(\.doc.ref) == ["a1", "b1", "a2"])
        let cursor = try #require(first.cursor)
        let next = try #require(try await Self.answer(DaemonAPI.Method.docsListAll, DaemonAPI.DocsListAllRequest(limit: 3, cursor: cursor),
                                                      returning: DaemonAPI.DocsListAll.self, asked: asked, answers: answers))
        #expect(next.docs.map(\.doc.ref) == ["b2"])
        #expect(next.cursor == nil)
    }

    @Test("A machine whose call does not stop on a cancel is given up at the patience, not at its link's time-out (#6638)")
    func frozenMachine() async {
        let host = FanAllHere.Host(id: "frozen", name: "Frozen")
        let start = ContinuousClock.now
        // As a call to a stopped machine: it ends only at its own time-out, whatever the cancel.
        let got = await FanAllHere.askOne(host, "forge/issues", .object([:]), patience: .milliseconds(100)) { _, _, _ in
            await withCheckedContinuation { done in
                DispatchQueue.global().asyncAfter(deadline: .now() + 5) { done.resume(returning: .null) }
            }
        }
        #expect(got == nil)
        #expect(ContinuousClock.now - start < .seconds(3))
        let fast = await FanAllHere.askOne(host, "forge/issues", .object([:]), patience: .seconds(5)) { _, _, _ in .int(1) }
        #expect(fast == .int(1))
    }

    @Test("A method that is not a call for every machine is not answered here")
    func otherMethod() async throws {
        let got = try await FanAllHere.answer("forge/issues", .object([:]), hosts: { _ in Self.hosts }, ask: { _, _, _ in .null })
        #expect(got == nil)
        await #expect(throws: FanAllHere.Invalid.self) {
            _ = try await FanAllHere.answer("forge/listAll", .object(["kind": .string("epics")]), hosts: { _ in [] },
                                            ask: { _, _, _ in nil })
        }
    }
}
