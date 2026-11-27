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

    private func approval(_ scope: String) -> DaemonAPI.LockRecord {
        DaemonAPI.LockRecord(v: 1, person: "p", root: Data(), kind: "setup.script", subject: "root", scope: scope, seq: 1,
                             content: Data(), key: Data(), sig: Data())
    }

    /// #7157: a make asks for a script that is new, changed, or approved only for one machine;
    /// nothing changed asks nothing.
    @Test func aMakeAsksForANewOrChangedScriptOnly() {
        let new = DaemonAPI.SetupScript(name: "root", present: true, text: "apt-get install -y gh\n", hash: "bb", size: 22, changed: true)
        #expect(new.needsApprovalForMade)
        #expect(MachineScriptWords.reviewTitle([new]) == "Approve root.sh?")
        #expect(MachineScriptWords.reviewLine(new, sender: "studio").contains("new · runs as root on each new machine"))
        var changed = new
        changed.approvedText = "apt-get install -y jq\n"
        #expect(MachineScriptWords.reviewTitle([changed]) == "root.sh changed since you approved it")
        #expect(MachineScriptWords.reviewLine(changed, sender: "studio").contains("+1 −1"))
        #expect(MachineScriptWords.reviewTitle([changed, script]) == "Approve your scripts?")
        var same = changed
        same.changed = false
        same.approvals = [approval("m:h-2")]
        #expect(same.needsApprovalForMade, "an approval for one machine is not one for new machines")
        same.approvals = [approval("made")]
        #expect(!same.needsApprovalForMade)
        #expect(MachineScriptWords.makeRow(same).hasSuffix("· approved"))
        #expect(!DaemonAPI.SetupScript(name: "setup", present: false).needsApprovalForMade)
        #expect(!DaemonAPI.SetupScript(name: "root", present: true, problem: "root.sh is over 10 KiB.").needsApprovalForMade)
    }

    /// #7157, board *approve token*: a text that looks like a token warns, with its line.
    @Test func aTokenInAScriptIsFound() {
        #expect(MachineScriptWords.tokenLine("echo hi\nexport GH_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123\n") == 2)
        #expect(MachineScriptWords.tokenLine("token=\"$(agents-secret GH_TOKEN)\"\ngh auth login --with-token\n") == nil)
        #expect(MachineScriptWords.tokenLine("aws configure set aws_access_key_id AKIAABCDEFGHIJKLMNOP\n") == 1)
    }

    /// #7512, Settings ▸ Machines ▸ Machine scripts: each row says what the script does, when it
    /// runs, whether it is approved, and the secrets setup.sh reads.
    @Test func theSettingsPageSaysWhatEachScriptDoes() {
        var setup = DaemonAPI.SetupScript(name: "setup", present: true, text: MachineScriptWords.exampleSetup, hash: "aa", size: 10,
                                          changed: true)
        #expect(MachineScriptWords.filesNote(setup) == "new, not approved")
        #expect(MachineScriptWords.filesLine(setup) == "Runs as your user, after the machine joins. Reads GH_TOKEN.")
        setup.approvedText = "echo old\n"
        #expect(MachineScriptWords.filesNote(setup) == "changed, not approved")
        var root = DaemonAPI.SetupScript(name: "root", present: true, text: "apt-get install -y gh\n", hash: "bb", size: 22,
                                         approvedText: "apt-get install -y gh\n", approvals: [approval("made")])
        #expect(MachineScriptWords.filesNote(root) == nil)
        #expect(MachineScriptWords.filesLine(root) == "Runs once, as root, when a machine is made. Approved for new machines.")
        root.problem = "root.sh is 11 KiB, over its limit of 10 KiB."
        #expect(MachineScriptWords.filesLine(root)
            == "root.sh is 11 KiB, over its limit of 10 KiB. New machines can’t be made until you fix it. Then you can review it.")
        // #7548: a setup.sh over its limit says new machines get none, and why there is no Review.
        let large = DaemonAPI.SetupScript(name: "setup", present: true, size: 68 << 10, problem: "setup.sh is 68 KiB, over its limit of 64 KiB.")
        #expect(MachineScriptWords.filesLine(large)
            == "setup.sh is 68 KiB, over its limit of 64 KiB. New machines get no setup.sh until you fix it. Then you can review it.")
        #expect(MachineScriptWords.filesNote(large) == nil && !large.needsApprovalForMade)
        #expect(MachineScriptWords.filesLine(DaemonAPI.SetupScript(name: "root", present: false)).hasPrefix("None."))
    }

    /// The `# secrets:` lines are read as the machine reads them: only in the comments at the top.
    @Test func declaredSecretsAreTheTopCommentsOnly() {
        #expect(MachineScriptWords.declaredSecrets("#!/bin/bash\n\n# secrets: A B\n# hi\n#secrets: B C\necho\n# secrets: D\n")
            == ["A", "B", "C"])
        #expect(MachineScriptWords.declaredSecrets("echo\n# secrets: A\n").isEmpty)
    }

    /// The examples New root.sh and New setup.sh write fit their limits, hold no token, and
    /// setup.sh declares the secret it reads.
    @Test func theExamplesAreFitToUse() {
        for name in ["root", "setup"] {
            let text = MachineScriptWords.example(name)
            #expect(text.hasPrefix("#!/bin/bash\n") && text.hasSuffix("\n"))
            #expect(text.utf8.count < 10 << 10)
            #expect(MachineScriptWords.tokenLine(text) == nil)
        }
        #expect(MachineScriptWords.declaredSecrets(MachineScriptWords.exampleSetup) == ["GH_TOKEN"])
        #expect(MachineScriptWords.declaredSecrets(MachineScriptWords.exampleRoot).isEmpty)
    }
}
