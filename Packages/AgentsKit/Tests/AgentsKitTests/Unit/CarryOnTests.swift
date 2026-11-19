@testable import AgentsKitCore
import Foundation
import Testing

/// "Carry on" in the bar of a chat an error stopped: once for each stop, never over what is
/// typed. The page holds the same rule in `Web/test/carryOn.test.mjs`.
@Suite("Carry on after an error")
@MainActor
struct CarryOnTests {
    private func agent(_ state: AgentState, _ reason: EndedReason?, at seconds: TimeInterval = 1) -> Agent {
        var agent = Agent(runtimeID: "claude", cwd: URL(fileURLWithPath: "/tmp"))
        agent.state = state
        agent.endedReason = reason
        agent.lastActivityAt = Date(timeIntervalSince1970: seconds)
        return agent
    }

    @Test func onlyAnErrorThatAnotherTurnCanGetPastOffersTheWords() {
        let offers: Set<EndedReason> = [.maxTokens, .maxTurnRequests, .processDied, .unrecognised, .runtimeError, .rateLimited]
        for reason in EndedReason.allCases {
            #expect(CarryOn.follows(reason) == offers.contains(reason), "\(reason)")
        }
        #expect(!CarryOn.follows(nil as EndedReason?))
        #expect(!CarryOn.follows(agent(.running, .processDied)))
        #expect(CarryOn.follows(agent(.stopped, .processDied)))
    }

    @Test func theWordsComeOnceForEachStopAndNeverOverWhatIsTyped() {
        let offers = CarryOnOffers()
        let stopped = agent(.stopped, .runtimeError)
        #expect(offers.words(for: stopped, typed: "half a thought") == nil)
        #expect(offers.words(for: stopped, typed: "") == "Carry on")
        #expect(offers.words(for: stopped, typed: "") == nil)
        #expect(offers.words(for: nil, typed: "") == nil)
        #expect(offers.words(for: agent(.stopped, .runtimeError, at: 2), typed: " ") == "Carry on")
        #expect(offers.words(for: agent(.stopped, .cancelled, at: 3), typed: "") == nil)
    }
}
