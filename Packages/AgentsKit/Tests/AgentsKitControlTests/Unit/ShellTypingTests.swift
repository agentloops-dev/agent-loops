import Foundation
import Testing
@testable import AgentsKitCore

/// Typing on a far machine (#7147): a host that puts inputs in order is sent each keystroke
/// without waiting for the last one's answer; an older one is still sent one at a time.
@Suite("Typing in a shell", .timeLimit(.minutes(1)))
@MainActor
struct ShellTypingTests {
    /// A host with a shell, that answers no input until told to.
    final class Host: DaemonLink, @unchecked Sendable {
        struct Input: Sendable { var id: Int; var bytes: String; var stream: String?; var seq: Int? }
        private let lock = NSLock()
        private let ordered: Bool
        private var held: [Input] = []
        private var far: (any LineTransport)?
        var inputs: [Input] { lock.withLock { held } }

        init(ordered: Bool) { self.ordered = ordered }

        func answer(_ input: Input, failing: Bool = false) throws {
            let far = lock.withLock { self.far }
            try far?.write(line: failing
                ? #"{"jsonrpc":"2.0","id":\#(input.id),"error":{"code":-32000,"message":"no"}}"#
                : #"{"jsonrpc":"2.0","id":\#(input.id),"result":{}}"#)
        }

        func transport() async throws -> any LineTransport {
            let (near, far) = PairedTransport.pair()
            lock.withLock { self.far = far }
            _ = Task { [weak self] in
                for try await line in far.lines() {
                    guard let self,
                          let object = try? JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: Any],
                          let id = object["id"] as? Int else { continue }
                    let params = object["params"] as? [String: Any]
                    switch object["method"] as? String {
                    case DaemonAPI.Method.shellInput:
                        let bytes = Data(base64Encoded: params?["bytes"] as? String ?? "") ?? Data()
                        self.lock.withLock {
                            self.held.append(Input(id: id, bytes: String(decoding: bytes, as: UTF8.self),
                                                   stream: params?["stream"] as? String, seq: params?["seq"] as? Int))
                        }
                    case DaemonAPI.Method.shellAttach:
                        let response = DaemonAPI.ShellAttachResponse(
                            state: .live, scrollback: Data(), dropped: 0, startedAt: Date(timeIntervalSince1970: 1),
                            folder: URL(filePath: "/tmp"), ordered: self.ordered ? true : nil)
                        let reply = try JSONRPCCodec.encode(.success(id: .number(id), result: JSONValue.encoding(response)))
                        try far.write(line: reply)
                    default:
                        try? far.write(line: #"{"jsonrpc":"2.0","id":\#(id),"result":{}}"#)
                    }
                }
            }
            return near
        }

        func start() async throws {}
    }

    private func attached(ordered: Bool) async throws -> (ShellClient, Host) {
        let host = Host(ordered: ordered)
        let client = DaemonClient(link: host)
        try await client.connect(startIfNeeded: false)
        let shell = ShellClient(agentID: UUID(), client: client) { "\($0)" }
        await shell.attach(rows: 24, cols: 80)
        #expect(shell.isAttached)
        return (shell, host)
    }

    private func until(_ holds: () -> Bool) async throws {
        for _ in 0..<1000 where !holds() { try await Task.sleep(for: .milliseconds(10)) }
        try #require(holds())
    }

    @Test func eachKeystrokeGoesWithoutWaitingForTheLast() async throws {
        let (shell, host) = try await attached(ordered: true)
        for key in ["a", "b", "c"] { shell.type(Data(key.utf8)) }
        try await until { host.inputs.count == 3 }
        let sent = host.inputs.sorted { ($0.seq ?? -1) < ($1.seq ?? -1) }
        #expect(sent.map(\.bytes) == ["a", "b", "c"])
        #expect(sent.map(\.seq) == [0, 1, 2])
        #expect(Set(sent.map(\.stream)).count == 1)
        for input in sent { try host.answer(input) }
    }

    @Test func pastTheMostInFlightWhatIsTypedGoesTogether() async throws {
        let (shell, host) = try await attached(ordered: true)
        for _ in 0..<(ShellClient.mostInFlight + 5) { shell.type(Data("x".utf8)) }
        try await until { host.inputs.count == ShellClient.mostInFlight }
        try host.answer(host.inputs[0])  // index-ok: until stops the test with fewer
        try await until { host.inputs.count == ShellClient.mostInFlight + 1 }
        #expect(host.inputs.last?.bytes == "xxxxx")
        for input in host.inputs.dropFirst() { try host.answer(input) }
    }

    @Test func anOlderHostIsSentOneAtATime() async throws {
        let (shell, host) = try await attached(ordered: false)
        for key in ["a", "b", "c"] { shell.type(Data(key.utf8)) }
        try await until { host.inputs.count == 1 }
        try await Task.sleep(for: .milliseconds(100))
        #expect(host.inputs.map(\.bytes) == ["a"])
        #expect(host.inputs[0].stream == nil && host.inputs[0].seq == nil)  // index-ok: until stops the test with fewer
        try host.answer(host.inputs[0])  // index-ok: until stops the test with fewer
        try await until { host.inputs.count == 2 }
        #expect(host.inputs[1].bytes == "bc")  // index-ok: until stops the test with fewer
        try host.answer(host.inputs[1])  // index-ok: until stops the test with fewer
    }

    @Test func aFailureIsSaidAndTypingStartsANewStream() async throws {
        let (shell, host) = try await attached(ordered: true)
        shell.type(Data("a".utf8))
        shell.type(Data("b".utf8))
        try await until { host.inputs.count == 2 }
        for input in host.inputs { try host.answer(input, failing: true) }
        try await until { shell.sendProblem != nil }
        shell.type(Data("c".utf8))
        try await until { host.inputs.count == 3 }
        #expect(host.inputs[2].seq == 0)  // index-ok: until stops the test with fewer
        #expect(host.inputs[2].stream != host.inputs[0].stream)  // index-ok: until stops the test with fewer
        try host.answer(host.inputs[2])  // index-ok: until stops the test with fewer
        try await until { shell.sendProblem == nil }
    }
}
