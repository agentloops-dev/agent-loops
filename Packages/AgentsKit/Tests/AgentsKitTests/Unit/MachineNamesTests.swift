@testable import AgentsKitCore
import Foundation
import Testing

/// A list of machine names stops at three (#5289, #5620), as the web page's `namesWords` says it.
struct MachineNamesTests {
    @Test func upToThreeNamesThenTheRestCounted() {
        #expect(MachineNames.words([]) == "")
        #expect(MachineNames.words(["a"]) == "a")
        #expect(MachineNames.words(["a", "b", "c"]) == "a, b, c")
        #expect(MachineNames.words(["a", "b", "c", "d"]) == "a, b, c and 1 more")
        #expect(MachineNames.words((0..<100).map { "m\($0)" }) == "m0, m1, m2 and 97 more")
    }

    @Test func andBeforeTheLastUpToThree() {
        #expect(MachineNames.and(["a"]) == "a")
        #expect(MachineNames.and(["a", "b"]) == "a and b")
        #expect(MachineNames.and(["a", "b", "c"]) == "a, b and c")
        #expect(MachineNames.and(["a", "b", "c", "d", "e"]) == "a, b, c and 2 more")
    }

    @Test func theLinesThatNameMachinesStopAtThree() {
        let behind = (1...5).map { DaemonAPI.PolicyBehindMachine(host: "h\($0)", name: "box-\($0)") }
        #expect(PolicyLayersWords.behindLine(behind)
            == "box-1, box-2, box-3 and 2 more do not follow your policy yet. Update them to the newest version.")
        #expect(WhereAgentsRun.line(others: ["a", "b", "c", "d"], providers: [])
            == "Agents run where this folder is. Your other machines: a, b, c and 1 more.")
    }
}
