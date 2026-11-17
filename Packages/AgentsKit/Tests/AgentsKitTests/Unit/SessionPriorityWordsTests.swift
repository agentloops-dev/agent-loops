import Foundation
import Testing
@testable import AgentsKitCore

@Suite("Session priority words (#5849)")
struct SessionPriorityWordsTests {
    private func agent(_ priority: SessionPriority?, set: SessionPriority? = nil,
                       source: SessionPrioritySource? = nil, labels: [String] = []) -> Agent {
        var a = Agent(runtimeID: "claude", cwd: URL(filePath: "/work/priority"),
                      labels: labels.map { SessionLabel(value: $0, owner: .agent) })
        a.priority = priority
        a.prioritySet = set
        a.prioritySource = source
        return a
    }

    @Test func chipOnlyForHighAndLow() {
        #expect(SessionPriorityWords.chip(agent(.high)) == "High")
        #expect(SessionPriorityWords.chip(agent(.low)) == "Low")
        #expect(SessionPriorityWords.chip(agent(.medium)) == nil)
        #expect(SessionPriorityWords.chip(agent(nil)) == nil)
    }

    @Test func helpSaysWhereItComesFrom() {
        #expect(SessionPriorityWords.help(agent(.high, source: .issue, labels: ["lane", "#5849"])) == "High, from #5849")
        #expect(SessionPriorityWords.help(agent(.high, source: .issue, labels: ["P#5895"])) == "High, from P#5895")
        #expect(SessionPriorityWords.help(agent(.high, source: .issue)) == "High, from its issue")
        #expect(SessionPriorityWords.help(agent(.low, source: .starter)) == "Low, from the session that started it")
        #expect(SessionPriorityWords.help(agent(.high, set: .high, source: .set)) == "High, set on this session")
        #expect(SessionPriorityWords.help(agent(.medium, source: .issue)) == nil)
    }

    @Test func menuFollowsTheIssueOrNone() {
        let noIssue = agent(nil)
        #expect(SessionPriorityWords.title(.follow, for: noIssue) == "None")
        #expect(SessionPriorityWords.checked(noIssue) == .follow)

        let fromIssue = agent(.high, source: .issue, labels: ["#5849"])
        #expect(SessionPriorityWords.title(.follow, for: fromIssue) == "From issue (High)")
        #expect(SessionPriorityWords.checked(fromIssue) == .follow)

        let setLow = agent(.low, set: .low, source: .set, labels: ["#5849"])
        #expect(SessionPriorityWords.title(.follow, for: setLow) == "From issue")
        #expect(SessionPriorityWords.checked(setLow) == .low)
        #expect(SessionPriorityWords.Choice.allCases.map { SessionPriorityWords.title($0, for: setLow) }
                == ["High", "Medium", "Low", "From issue"])
    }

    @Test func choiceSendsItsPriority() {
        #expect(SessionPriorityWords.Choice.high.priority == .high)
        #expect(SessionPriorityWords.Choice.follow.priority == nil)
    }
}
