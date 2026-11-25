@testable import AgentsKitCore
import Foundation
import Testing

/// The control plane as a machine in Machines (#6591).
struct ControlPlaneMachineTests {
    private func row(_ id: String, roles: [String]?) throws -> DaemonAPI.ControlMachine {
        var json: [String: Any] = ["id": id, "name": id, "platform": "Linux", "version": "1", "state": "online", "reach": "dialOut"]
        if let roles { json["roles"] = roles }
        return try JSONDecoder().decode(DaemonAPI.ControlMachine.self, from: JSONSerialization.data(withJSONObject: json))
    }

    private func look(_ copy: String, machine: String?, level: DaemonAPI.CapacityLevel = .near,
                      reason: String = "the control plane is near capacity: its disk is busy",
                      at: Date = Date(timeIntervalSince1970: 1000)) -> DaemonAPI.ControlCapacityCopy {
        DaemonAPI.ControlCapacityCopy(copy: copy, machine: machine, level: level, reason: reason, loadPerCore: 1.42,
                                      loadPerCore5: 1.1, pressureMemoryFullAvg60: 0, pressureIOFullAvg60: 16.7,
                                      memoryAvailableBytes: 184 * 1_048_576, swapUsedPercent: 33, heapInuseBytes: 96 * 1_048_576,
                                      goroutines: 87, openFiles: 42, storeFiles: 1284, storeBytes: 68 * 1_048_576, at: at)
    }

    @Test func rolesSayWhichMachinesRunTheControlPlane() throws {
        let control = try row("control", roles: ["control"])
        let both = try row("mac", roles: ["agents", "control"])
        let agents = try row("box", roles: ["agents"])
        let older = try row("old", roles: nil)
        #expect(ControlPlaneMachine.controlOnly(control))
        #expect(!ControlPlaneMachine.controlOnly(both) && ControlPlaneMachine.runsControl(both))
        #expect(!ControlPlaneMachine.runsControl(agents) && !ControlPlaneMachine.runsControl(older))
        #expect(ControlPlaneMachine.first([agents, older, control]).map(\.id.rawValue) == ["control", "box", "old"])
    }

    @Test func theLookIsTheCopyThatNamesTheMachine() {
        let mine = look("a", machine: "control", at: Date(timeIntervalSince1970: 10))
        let newer = look("b", machine: "control", at: Date(timeIntervalSince1970: 20))
        let other = look("c", machine: "elsewhere")
        let ref = MachineRef(rawValue: "control")
        #expect(ControlPlaneMachine.look(for: ref, in: [mine, other, newer], controlMachines: 2)?.copy == "b")
        #expect(ControlPlaneMachine.look(for: ref, in: [other], controlMachines: 2) == nil)
        // A copy from before #6591 names no machine: it is the one control machine's only.
        let unnamed = look("d", machine: nil)
        #expect(ControlPlaneMachine.look(for: ref, in: [unnamed], controlMachines: 1)?.copy == "d")
        #expect(ControlPlaneMachine.look(for: ref, in: [unnamed], controlMachines: 2) == nil)
    }

    @Test func theRowSaysTheLevelAndWhatSetIt() {
        #expect(ControlPlaneMachine.rowLine(nil) == "No capacity reading yet")
        #expect(ControlPlaneMachine.rowLine(look("a", machine: "m")) == "Near capacity · I/O pressure 16.7% · Load 1.4 per core")
        #expect(ControlPlaneMachine.rowLine(look("a", machine: "m", level: .ok, reason: "the control plane is within capacity"))
                == "OK · Load 1.4 per core")
        #expect(ControlPlaneMachine.level(look("a", machine: "m", level: .full)) == "At capacity")
        var loadOnly = look("a", machine: "m", level: .full, reason: "the control plane is at capacity: its load is high")
        loadOnly.pressureIOFullAvg60 = 0
        #expect(ControlPlaneMachine.rowLine(loadOnly) == "At capacity · Load 1.4 per core")
        #expect(ControlPlaneMachine.summary(loadOnly).first?.value == "Full")
        #expect(ControlPlaneMachine.agentsRowLine(look("a", machine: "m")) == "Control plane near capacity")
        #expect(ControlPlaneMachine.agentsRowLine(look("a", machine: "m", level: .ok)) == nil)
    }

    @Test func theSentenceIsTheReasonAsASentence() {
        #expect(ControlPlaneMachine.sentence(look("a", machine: "m")) == "The control plane is near capacity: its disk is busy.")
        #expect(ControlPlaneMachine.sentence(look("a", machine: "m", level: .full, reason: "")) == "The control plane is at capacity.")
    }

    @Test func theStripSaysTheWorstCopy() {
        let ok = look("a", machine: "m", level: .ok)
        let full = look("b", machine: "n", level: .full)
        #expect(ControlPlaneMachine.worst([ok, full])?.copy == "b")
        #expect(ControlPlaneMachine.worst([]) == nil)
    }

    @Test func thePageHasEachFigure() {
        let figures = ControlPlaneMachine.summary(look("a", machine: "m"))
        #expect(figures.map(\.label) == ["Capacity", "Load per core", "Memory pressure", "I/O pressure"])
        #expect(figures.map(\.value) == ["Near", "1.42", "0.0%", "16.7%"])
        let sections = ControlPlaneMachine.sections(look("a", machine: "m"))
        #expect(sections.map(\.title) == ["Memory", "Process", "Store"])
        #expect(sections[1].figures.map(\.value) == ["87", "42"])
    }
}
