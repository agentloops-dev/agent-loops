import AgentsKitCore
import Foundation
import Testing

/// "Your scripts" in words (#7155, gate 2 of #7151).
struct MachineScriptWordsTests {
    private func step(_ state: DaemonAPI.MachineSetupStep.State, hash: String? = "aa", error: String? = nil) -> DaemonAPI.MachineSetupStep {
        DaemonAPI.MachineSetupStep(id: "user", state: state, startedAt: Date(timeIntervalSince1970: 0),
                                   endedAt: Date(timeIntervalSince1970: 60), error: error, hash: hash)
    }

    private let script = DaemonAPI.SetupScript(name: "setup", present: true, text: "echo hi\n", hash: "aa", size: 8)

    @Test func standingComparesWhatRanWithTheFile() {
        #expect(MachineScriptWords.standing(step: nil, script: script) == .notSent)
        #expect(MachineScriptWords.standing(step: step(.waiting), script: script) == .notSent)
        #expect(MachineScriptWords.standing(step: step(.running), script: script) == .running)
        #expect(MachineScriptWords.standing(step: step(.done), script: script) == .same)
        #expect(MachineScriptWords.standing(step: step(.done, hash: "bb"), script: script) == .changed)
        #expect(MachineScriptWords.standing(step: step(.failed), script: script) == .failed)
        #expect(MachineScriptWords.standing(step: nil, script: DaemonAPI.SetupScript(name: "setup", present: false)) == .none)
        #expect(MachineScriptWords.note(.changed, sender: "studio") == "changed on studio")
        #expect(MachineScriptWords.note(.same, sender: "studio") == nil)
    }

    @Test func linesSayWhatRanAndWhatToDo() {
        let now = Date(timeIntervalSince1970: 3600)
        #expect(MachineScriptWords.line(.notSent, step: nil, machine: "dev-3", sender: "studio") == "Not sent to dev-3 yet.")
        #expect(MachineScriptWords.line(.same, step: step(.done), machine: "dev-3", sender: "studio", now: now)
            .hasSuffix("The same as on studio."))
        #expect(MachineScriptWords.line(.changed, step: step(.done), machine: "dev-3", sender: "studio", now: now)
            .hasPrefix("dev-3 ran the one of "))
        #expect(MachineScriptWords.line(.failed, step: step(.failed, error: "setup.sh failed (exit status 1).\nX bad"),
                                        machine: "dev-3", sender: "studio") == "setup.sh failed (exit status 1).")
        #expect(MachineScriptWords.sendIsMain(.changed) && !MachineScriptWords.sendIsMain(.same))
        #expect(MachineScriptWords.canRunAgain(.same) && !MachineScriptWords.canRunAgain(.running) && !MachineScriptWords.canRunAgain(.notSent))
    }

    @Test func theDiffMarksEachLine() {
        let lines = MachineScriptWords.diff("a\nb\nc\n", "a\nc\nd\n")
        #expect(lines.map { "\($0.mark)\($0.text)" } == [" a", "-b", " c", "+d"])
        #expect(MachineScriptWords.diff(nil, "x\ny\n").map { "\($0.mark)\($0.text)" } == ["+x", "+y"])
    }

    @Test func theTwoNewStepsHaveNames() {
        #expect(MachineSetupWords.name(DaemonAPI.MachineSetupStep(id: "root", state: .waiting)) == "Root set-up")
        #expect(MachineSetupWords.name(DaemonAPI.MachineSetupStep(id: "user", state: .waiting)) == "Your set-up")
    }

    @Test func aFailedRunAgainSaysFailedOnAReadyMachine() {
        let setup = DaemonAPI.MachineSetup(steps: [step(.failed, error: "setup.sh failed")], startedAt: Date(timeIntervalSince1970: 0),
                                           readyAt: Date(timeIntervalSince1970: 30))
        #expect(MachineSetupWords.chip(setup) == "Set-up failed · step 1 of 1")
    }
}
