import Foundation
import Testing
@testable import AgentsKitCore

/// The sessions on Work's items and an epic's lead, as `Web/test/work-sessions.test.mjs` has it on the web page.
@Suite("Work sessions")
struct WorkSessionsTests {
    typealias Node = DaemonAPI.LineageNode
    let day = Date(timeIntervalSince1970: 1_793_000_000)

    func agent(_ labels: [String], state: AgentState, at: TimeInterval = 0) -> Agent {
        var a = Agent(runtimeID: "claude", cwd: URL(fileURLWithPath: "/tmp/work"))
        a.labels = labels.map { SessionLabel(value: $0, owner: .agent) }
        a.state = state
        a.createdAt = day.addingTimeInterval(at)
        return a
    }

    @Test func theLeadIsTheLiveSessionLabelledLeadAndTheEpic() {
        let epic = Node(kind: "issue", ref: "github:alexec/Agents#3265")
        let ended = agent(["#3265", "lead"], state: .stopped, at: 100)
        let live = agent(["#3265", "lead"], state: .running)
        let lane = agent(["#3265"], state: .running, at: 200)
        let other = agent(["#3100", "lead"], state: .running, at: 300)
        let archived = agent(["#3265", "lead"], state: .archived, at: 400)
        #expect(WorkSessions.lead(of: epic, in: [ended, live, lane, other, archived])?.id == live.id)
        #expect(WorkSessions.lead(of: epic, in: [ended, lane])?.id == ended.id)
        #expect(WorkSessions.lead(of: epic, in: [lane, other]) == nil)
        let linear = Node(kind: "issue", ref: "linear:ENG-5")
        #expect(WorkSessions.lead(of: linear, in: [agent(["ENG-5", "lead"], state: .finished)]) != nil)
    }

    @Test func aRowsSessionIsALiveOneLabelledWithTheItemOneThatNeedsYouFirst() {
        let issue = Node(kind: "issue", ref: "github:alexec/Agents#5453")
        let stopped = agent(["#5453"], state: .stopped, at: 900)
        let old = agent(["#5453"], state: .running, at: 100)
        let newer = agent(["#5453", "lane"], state: .starting, at: 200)
        let asks = agent(["#5453"], state: .waitingOnUser, at: 50)
        let other = agent(["#5454"], state: .running, at: 300)
        let pr = agent(["P#5453"], state: .running, at: 400)
        #expect(WorkSessions.workingSession(on: issue, in: [stopped, old, newer, other, pr])?.id == newer.id)
        #expect(WorkSessions.workingSession(on: issue, in: [old, asks, newer])?.id == asks.id)
        #expect(WorkSessions.workingSession(on: issue, in: [stopped, other, pr]) == nil)
        #expect(WorkSessions.workingSession(on: Node(kind: "pr", ref: "github:alexec/Agents#5453"), in: [old, pr])?.id == pr.id)
        let linear = agent(["ENG-5"], state: .running)
        #expect(WorkSessions.workingSession(on: Node(kind: "issue", ref: "linear:ENG-5"), in: [linear])?.id == linear.id)
        #expect(WorkSessions.itemLabel(Node(kind: "pr", ref: "gitlab:a/b!7")) == "P!7")
        #expect(WorkSessions.workingWords(.running) == "Working")
        #expect(WorkSessions.workingWords(.waitingOnUser) == "Needs you")
    }
}
