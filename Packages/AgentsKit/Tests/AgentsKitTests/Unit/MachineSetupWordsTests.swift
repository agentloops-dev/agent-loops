import Foundation
import Testing
@testable import AgentsKitCore

/// A machine's set-up as steps (#5357): the chip, the sidebar's grey line and each step's line.
@Suite("A machine's set-up, in words")
struct MachineSetupWordsTests {
    private let start = Date(timeIntervalSince1970: 1_000_000)
    private let ids = ["create", "join", "install", "compare", "runtimes", "projects", "signIn", "smoke"]

    private func setup(done: Int, running: Bool = true, failed: Bool = false, readyAfter: TimeInterval? = nil) -> DaemonAPI.MachineSetup {
        let steps = ids.enumerated().map { index, id -> DaemonAPI.MachineSetupStep in
            if index < done { return .init(id: id, state: .done, startedAt: start, endedAt: start.addingTimeInterval(10)) }
            if index == done, failed { return .init(id: id, state: .failed, startedAt: start, error: "No quota") }
            if index == done, running { return .init(id: id, state: .running, startedAt: start) }
            return .init(id: id, state: .waiting)
        }
        return .init(steps: steps, startedAt: start, readyAt: readyAfter.map { start.addingTimeInterval($0) })
    }

    @Test func theChipSaysWhereTheRunIs() {
        #expect(MachineSetupWords.chip(setup(done: 6)) == "Setting up · 7 of 8")
        #expect(MachineSetupWords.chip(setup(done: 8, readyAfter: 222)) == "Ready · 3 min 42 s")
        #expect(MachineSetupWords.chip(setup(done: 2, failed: true)) == "Set-up failed · step 3 of 8")
    }

    @Test func theSidebarLineNamesTheStep() {
        #expect(MachineSetupWords.sidebarLine(setup(done: 6)) == "Setting up · 7 of 8 · Sign in")
        #expect(MachineSetupWords.sidebarLine(setup(done: 2, failed: true)) == "Set-up failed · Install Agents")
    }

    @Test func aReadyMachineIsNotSettingUp() {
        #expect(MachineSetupWords.isSettingUp(setup(done: 7)))
        #expect(!MachineSetupWords.isSettingUp(setup(done: 8, readyAfter: 5)))
        #expect(!MachineSetupWords.isSettingUp(nil))
        #expect(MachineSetupWords.noSessionYet("vm-1") == "vm-1 is still setting up")
        var row = DaemonAPI.ControlMachine(id: MachineRef(rawValue: "dev"), name: "dev", platform: "linux", version: "1",
                                           state: "online", reach: "direct", machineID: nil)
        #expect(MachineNeed.most(row: row, offline: false, runtimes: []) == nil)
        row.setup = setup(done: 3)
        #expect(MachineNeed.most(row: row, offline: false, runtimes: []) == .settingUp(setup(done: 3)))
    }

    @Test func aFailedScriptLetsSessionsStartWithAWarning() throws {
        func failing(_ id: String) -> DaemonAPI.MachineSetup {
            .init(steps: [.init(id: "install", state: .done), .init(id: id, state: .failed, error: "exit 1"),
                          .init(id: "smoke", state: .waiting)], startedAt: start)
        }
        let user = try #require(MachineSetupWords.failedScript(failing("user")))
        #expect(user.id == "user")
        #expect(MachineSetupWords.failedScript(failing("root"))?.id == "root")
        // Another failed step still holds new sessions back.
        #expect(MachineSetupWords.failedScript(failing("install")) == nil)
        #expect(MachineSetupWords.failedScript(setup(done: 3)) == nil)
        #expect(MachineSetupWords.failedScript(nil) == nil)
        #expect(MachineSetupWords.startsDespiteFailure(user, machine: "vm-1")
            == "Your set-up failed on vm-1. The session starts, but what setup.sh installs can be missing.")
        #expect(MachineSetupWords.startsDespiteFailure(.init(id: "root", state: .failed), machine: "vm-1")
            == "Root set-up failed on vm-1. The session starts, but what root.sh installs can be missing.")
    }

    @Test func aStepSaysWhatItDoes() {
        let running = DaemonAPI.MachineSetupStep(id: "install", state: .running, detail: "Agents: 62 of 140 MB", slower: true)
        #expect(MachineSetupWords.line(running) == "Agents: 62 of 140 MB · Slower than usual")
        let failed = DaemonAPI.MachineSetupStep(id: "create", state: .failed, error: "Size not in region")
        #expect(MachineSetupWords.line(failed) == "Size not in region")
        #expect(MachineSetupWords.line(.init(id: "smoke", state: .waiting)) == "Not yet")
    }

    @Test func timesAreShort() {
        #expect(MachineSetupWords.duration(42) == "42 s")
        #expect(MachineSetupWords.duration(120) == "2 min")
        #expect(MachineSetupWords.duration(3900) == "1 h 5 min")
    }

    @Test func stepsFoldAfterADay() {
        let ready = setup(done: 8, readyAfter: 222)
        #expect(MachineSetupWords.foldedLine(ready, now: start.addingTimeInterval(3600)) == nil)
        #expect(MachineSetupWords.foldedLine(ready, now: start.addingTimeInterval(222 + 86400)) == "Set up in 3 min 42 s")
    }

    @Test func theSetupDecodes() throws {
        let json = #"{"id":"d","name":"d","platform":"linux","version":"1","state":"online","reach":"direct","setup":{"steps":[{"id":"create","state":"needs-you"}],"startedAt":"2026-11-14T10:00:00Z"}}"#
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        let row = try decoder.decode(DaemonAPI.ControlMachine.self, from: Data(json.utf8))
        #expect(row.setup?.steps.first?.state == .needsYou)
    }

    /// #7154: Root set-up and Your set-up have names, a step done with a warning says so, and a
    /// failed Root set-up's line is its exit, with root.log's last lines in its detail.
    @Test func rootSetUpHasItsNameAndItsWarning() throws {
        #expect(MachineSetupWords.name(.init(id: "root", state: .waiting)) == "Root set-up")
        #expect(MachineSetupWords.name(.init(id: "user", state: .waiting)) == "Your set-up")
        let warned = DaemonAPI.MachineSetupStep(id: "root", state: .done,
                                                detail: "DigitalOcean can’t run root set-up. Put it in the image.", warning: true)
        #expect(MachineSetupWords.isWarned(warned))
        #expect(MachineSetupWords.line(warned) == "DigitalOcean can’t run root set-up. Put it in the image.")
        #expect(MachineSetupWords.time(warned) == nil)
        #expect(!MachineSetupWords.isWarned(.init(id: "root", state: .done, detail: "root.sh · exit 0")))
        let failed = DaemonAPI.MachineSetupStep(id: "root", state: .failed, detail: "E: Unable to locate package build-esential",
                                                error: "root.sh ended with exit 100")
        #expect(MachineSetupWords.line(failed) == "root.sh ended with exit 100")
        #expect(MachineSetupWords.rootFailedAdvice("dev-3").hasPrefix("Fix root.sh, then make a new machine: dev-3"))
        let json = #"{"id":"root","state":"done","detail":"x","warning":true,"hash":"ab12"}"#
        let step = try JSONDecoder().decode(DaemonAPI.MachineSetupStep.self, from: Data(json.utf8))
        #expect(step.warning == true && step.hash == "ab12")
    }

    /// #7159: the root command sheet's words, and what its calls answer.
    @Test func rootByHand() throws {
        #expect(MachineSetupWords.rootFailedAdvice("dev-3").contains("Or copy a command that you run as root there, then press Try Again."))
        #expect(MachineSetupWords.rootCommandLead("nas", failed: false).hasPrefix("No provider makes nas, so Agent Loops can’t be root there."))
        #expect(MachineSetupWords.rootCommandLead("dev-3", failed: true) == "Run this as root, or with sudo, on dev-3:")
        #expect(MachineSetupWords.rootSudoWarning("nas").contains("agents there can become root"))
        let log = try JSONDecoder().decode(DaemonAPI.SetupRootLog.self, from: Data(#"{"text":"E: x\n","cut":true}"#.utf8))
        #expect(log.cut && log.text == "E: x\n")
        let command = try JSONDecoder().decode(DaemonAPI.SetupRootCommand.self, from: Data(#"{"command":"printf %s YQ== | base64 -d | sudo sh","hash":"ab"}"#.utf8))
        #expect(command.command.hasSuffix("| sudo sh"))
        let again = String(decoding: try JSONEncoder().encode(DaemonAPI.SetupRootAgain(rest: true)), as: UTF8.self)
        #expect(again == #"{"rest":true}"#)
    }
}
