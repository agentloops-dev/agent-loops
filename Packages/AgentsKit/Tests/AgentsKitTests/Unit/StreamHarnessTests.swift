import Foundation
import Testing
@testable import AgentsKitCore

/// The client model reaches the daemon's state after drops, duplicates and reconnects (#6187).
///
/// Plays the recorded streams in `Packages/Wire/streams` into `AgentsModel`; `Web/test/streamHarness.test.mjs`
/// plays the same files into the web model, so a difference between the clients is a failed test. The
/// README next to the files says the format. The runner below is the part another feature (#6188, #6189)
/// reuses: `StreamClient` is the small surface a client gives, and `Scenario.play` drives it.
@MainActor
@Suite("Recorded streams")
struct StreamHarnessTests {
    @Test(arguments: StreamFile.all.flatMap { file in file.scenarios.map { (file, $0) } }.map { StreamCase(file: $0.0, scenario: $0.1) })
    func reachesTheDaemonsState(_ run: StreamCase) throws {
        if run.scenario.fault != nil { return }
        let client = ModelStreamClient(session: run.file.session, host: run.file.host)
        try run.scenario.play(run.file, on: client)
        let held = client.view()
        #expect(held == (try run.file.view(after: run.scenario.expect)).tail(run.scenario.tail, held: held))
    }
}

/// What a client holds that the streams check: each agent's id, title and state, the open chat's entries,
/// and the permission and question cards (#6188), each as `kind|id|agentID`, sorted.
struct StreamView: Equatable, CustomTestStringConvertible {
    var agents: [String]
    var entries: [String]
    var cards: [String] = []
    var testDescription: String { "agents \(agents) entries \(entries.count): \(entries.suffix(3)) cards \(cards)" }

    static func cards(_ permissions: [PermissionRequest], _ elicitations: [ElicitationRequest]) -> [String] {
        (permissions.map { "permission|\($0.id.uuidString)|\($0.agentID.uuidString)" }
            + elicitations.map { "question|\($0.id.uuidString)|\($0.agentID.uuidString)" }).sorted()
    }

    /// With `tail` (a long chat), the daemon's last entries, as many as the client kept and at least
    /// `tail`: a client keeps a chat's end (`AgentsModel.entriesKept`), so a turn lost or doubled in it fails.
    func tail(_ tail: Int?, held: StreamView) -> StreamView {
        guard let tail else { return self }
        var view = self
        view.entries = Array(entries.suffix(max(tail, min(held.entries.count, entries.count))))
        return view
    }
}

/// The surface a client gives the runner. Notifications go through `notify`; `catchUp` reads a snapshot
/// as a reconnect's read, hearing `during` while the read is on its way.
@MainActor
protocol StreamClient {
    func notify(_ method: String, _ params: JSONValue)
    func catchUp(_ snapshot: StreamSnapshot, during: [(String, JSONValue)]) throws
}

struct StreamSnapshot {
    var agents: [Agent]
    var page: TranscriptPage
    /// The pending reads (`permissions/pending`, `elicitations/pending`); empty in a stream with no cards.
    var permissions: [PermissionRequest] = []
    var elicitations: [ElicitationRequest] = []
}

struct StreamCase: CustomTestStringConvertible, Sendable {
    var file: StreamFile
    var scenario: StreamScenario
    var testDescription: String { "\(file.name): \(scenario.name)" }
}

struct StreamScenario: Sendable {
    var name: String
    var ops: [[String: JSONValue]]
    var expect: Int
    /// The bug a scenario fails on, until its fix removes the field from the file: the scenario is skipped.
    var fault: String?
    /// The client may keep only the chat's end, but at least this many of its entries (README.md).
    var tail: Int?

    @MainActor
    func play(_ file: StreamFile, on client: some StreamClient) throws {
        for op in ops {
            switch op["op"]?.stringValue {
            case "deliver":
                let (method, params) = try file.note(op["step"]?.intValue ?? -1)
                client.notify(method, params)
            case "reconnect":
                let during = try (op["during"]?.arrayValue ?? []).map { try file.note($0.intValue ?? -1) }
                try client.catchUp(file.snapshot(after: op["read"]?.intValue ?? -1), during: during)
            case "drop":
                break
            default:
                Issue.record("unknown op \(String(describing: op["op"]))")
            }
        }
    }
}

struct StreamFile: Sendable {
    var name: String
    var session: UUID
    var host: MachineRef
    var start: JSONValue
    var steps: [JSONValue]
    var scenarios: [StreamScenario]

    static let directory = URL(filePath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appending(path: "Wire/streams")

    static let all: [StreamFile] = {
        let names = (try? FileManager.default.contentsOfDirectory(atPath: directory.path()))?.filter { $0.hasSuffix(".json") }.sorted() ?? []
        return names.compactMap { name in
            guard let data = try? Data(contentsOf: directory.appending(path: name)),
                  let root = try? JSONDecoder().decode(JSONValue.self, from: data),
                  let session = root["session"]?.stringValue.flatMap(UUID.init(uuidString:)),
                  let start = root["start"], let steps = root["steps"]?.arrayValue,
                  let scenarios = root["scenarios"]?.arrayValue else { return nil }
            return StreamFile(name: name, session: session, host: MachineRef(rawValue: root["host"]?.stringValue ?? "mac"),
                              start: start, steps: steps, scenarios: scenarios.compactMap {
                guard let name = $0["name"]?.stringValue, let ops = $0["ops"]?.arrayValue,
                      let expect = $0["expect"]?.intValue else { return nil }
                return StreamScenario(name: name, ops: ops.compactMap(\.objectValue), expect: expect, fault: $0["fault"]?.stringValue,
                                      tail: $0["tail"]?.intValue)
            })
        }
    }()

    func note(_ step: Int) throws -> (String, JSONValue) {
        guard steps.indices.contains(step), let method = steps[step]["method"]?.stringValue, let params = steps[step]["params"]
        else { throw StreamError.noStep(step) }
        return (method, params)
    }

    /// A read that says `startEntries: n` begins with the first n entries of `start` (a long chat, README.md).
    private func state(after step: Int) throws -> JSONValue {
        if step < 0 { return start }
        guard steps.indices.contains(step), case .object(var after) = steps[step]["after"] else { throw StreamError.noStep(step) }
        guard case .object(var transcript) = after["transcript"], let base = transcript["startEntries"]?.intValue
        else { return .object(after) }
        let held = Array((start["transcript"]?["entries"]?.arrayValue ?? []).prefix(base))
        transcript["entries"] = .array(held + (transcript["entries"]?.arrayValue ?? []))
        transcript["startEntries"] = nil
        after["transcript"] = .object(transcript)
        return .object(after)
    }

    func snapshot(after step: Int) throws -> StreamSnapshot {
        let state = try state(after: step)
        let decoder = JSONDecoder()
        func decode<T: Decodable>(_ type: T.Type, _ value: JSONValue?) throws -> T {
            try decoder.decode(type, from: JSONEncoder().encode(value))
        }
        return StreamSnapshot(agents: try decode([Agent].self, state["agents"]),
                              page: try decode(TranscriptPage.self, state["transcript"]),
                              permissions: try state["permissions"].map { try decode([PermissionRequest].self, $0) } ?? [],
                              elicitations: try state["elicitations"].map { try decode([ElicitationRequest].self, $0) } ?? [])
    }

    /// The file's own record of what the daemon holds after `step`, as a client's view of it.
    func view(after step: Int) throws -> StreamView {
        let snapshot = try snapshot(after: step)
        return StreamView(agents: snapshot.agents.map { "\($0.id.uuidString)|\($0.title ?? "")|\($0.state)" }.sorted(),
                          entries: snapshot.page.entries.map(\.streamLine),
                          cards: StreamView.cards(snapshot.permissions, snapshot.elicitations))
    }
}

enum StreamError: Error { case noStep(Int) }

extension TranscriptEntry {
    var streamLine: String {
        switch kind {
        case .agentMessage(_, let text, _): "\(id.uuidString)|\(text)"
        case .userMessage(let text, _, _): "\(id.uuidString)|\(text)"
        default: "\(id.uuidString)|"
        }
    }
}

/// `AgentsModel` as a client, doing what a reconnect does: a list replaces the host's agents, and the open
/// chat is resumed from the first entry it holds (#6282), or opened whole when the page does not join (`loadPage`'s choices).
@MainActor
final class ModelStreamClient: StreamClient {
    let model = AgentsModel()
    let host: MachineRef

    init(session: UUID, host: MachineRef) {
        self.host = host
        model.watching = session
    }

    func notify(_ method: String, _ params: JSONValue) {
        model.apply(method, params, from: host)
    }

    func catchUp(_ snapshot: StreamSnapshot, during: [(String, JSONValue)]) throws {
        let load = model.beginTranscriptLoad()
        model.replaceAgents(snapshot.agents, from: host)
        for (method, params) in during { model.apply(method, params, from: host) }
        // As AppModel's catch-up of a host: its pending reads replace its agents' cards (#6188).
        model.replaceCards(from: host, permissions: snapshot.permissions, elicitations: snapshot.elicitations)
        if let start = model.resumeStart, snapshot.page.total >= start {
            let rest = TranscriptPage(firstIndex: start, total: snapshot.page.total,
                                      entries: Array(snapshot.page.entries.dropFirst(start - snapshot.page.firstIndex)))
            if model.takeResumed(rest, load: load) { return }
        }
        model.takeOpening(ChatOpening.Loaded(turns: TurnsPage(turns: [], firstTurn: 0, openStart: 0), page: snapshot.page, turnsFailure: nil), load: load)
    }

    func view() -> StreamView {
        StreamView(agents: model.agents.filter { $0.host == host }.map { "\($0.id.uuidString)|\($0.title ?? "")|\($0.state)" }.sorted(),
                   entries: model.entries.map(\.streamLine),
                   cards: StreamView.cards(model.permissions, model.elicitations))
    }
}
