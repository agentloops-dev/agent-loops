import Foundation
import Testing
@testable import AgentsKitCore

/// #5996: in its lock, the window asks each machine itself for what the control plane's calls for every machine ask, as
/// the control plane's fanall.go and fanout.go answer them, and as Web/test/fanAllHere.test.mjs has it on the web page.
@Suite("Calls for every machine, answered here")
struct FanAllHereTests {
    static let hosts = [
        FanAllHere.Host(id: "a", name: "A", online: true),
        FanAllHere.Host(id: "b", name: "B", online: true),
        FanAllHere.Host(id: "c", name: "C", online: false),
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

    @Test("forge/listAll asks each machine's forge/pulls itself; one that fails or is offline is unreached, and not asked offline")
    func forgeList() async throws {
        let asked = Asked()
        let list = try JSONValue.encoding(DaemonAPI.ForgeList(cursor: "next"))
        let got = try #require(try await Self.answer(DaemonAPI.Method.forgeListAll,
                                                     DaemonAPI.ForgeListAllRequest(kind: "pulls", state: "open", limit: 500),
                                                     returning: DaemonAPI.ForgeListAll.self, asked: asked, answers: ["a": list]))
        #expect(got.lists.map(\.machine.rawValue) == ["a"])
        #expect(got.lists.first?.list.cursor == "next")
        #expect(got.unreached.map(\.machine.rawValue) == ["b", "c"])
        #expect(asked.all.map(\.host).sorted() == ["a", "b"])
        #expect(asked.all.allSatisfy { $0.method == "forge/pulls" && $0.params["limit"]?.intValue == 100 })
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
