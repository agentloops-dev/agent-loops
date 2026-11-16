import Foundation
import Testing
@testable import AgentsKitCore

/// Records which hosts a search asked, and holds one host's answer until it is let go.
@MainActor
private final class Asked {
    var hosts: [MachineRef] = []
    var answered: [MachineRef] = []
    var queries: [MachineRef: String] = [:]
    let gate: AsyncStream<Void>
    let release: AsyncStream<Void>.Continuation

    init() {
        (gate, release) = AsyncStream.makeStream(of: Void.self)
    }
}

@MainActor
@Suite("Session search on every host (#5572)")
struct SessionSearchTests {
    private let slow = MachineRef(rawValue: "slow")
    private let fast = MachineRef(rawValue: "fast")

    @Test func asksEveryHostAtOnceAndKeepsEachAnswerAsItComes() async {
        let asked = Asked()
        let slow = slow
        let search = Task { @MainActor in
            await SessionSearch.askEveryHost([slow, fast], words: "parser", into: AgentsModel(),
                                             has: { _, _ in true }, client: { _ in nil },
                                             page: { host in
                asked.hosts.append(host)
                if host == slow { for await _ in asked.gate { break } }
                asked.answered.append(host)
            })
        }
        // The fast host answers while the slow one still holds its page.
        while asked.answered.isEmpty { await Task.yield() }
        #expect(Set(asked.hosts) == [slow, fast])
        #expect(asked.answered == [fast])
        asked.release.yield()
        await search.value
        #expect(asked.answered == [fast, slow])
    }

    @Test func asksEveryHostWithMoreForItsNextPage() async {
        let asked = Asked()
        let next: [MachineRef: DaemonAPI.ListRequest] = [
            slow: .init(archivedCommands: false, limit: 10, lean: true, query: "a"),
            fast: .init(archivedCommands: false, limit: 10, lean: true, query: "b"),
        ]
        await SessionSearch.askMore(next) { request, host in
            asked.hosts.append(host)
            asked.queries[host] = request.query
        }
        #expect(Set(asked.hosts) == [slow, fast])
        #expect(asked.queries == [slow: "a", fast: "b"])
    }
}
