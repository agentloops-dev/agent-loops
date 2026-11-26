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
        #expect(!ControlPlaneMachine.controlOnly(both) && ControlPlaneMachine.controlRole(both))
        #expect(!ControlPlaneMachine.controlRole(agents) && !ControlPlaneMachine.controlRole(older))
        #expect(ControlPlaneMachine.first([agents, older, control]).map(\.id.rawValue) == ["control", "box", "old"])
    }

    /// #7393: a control-only machine that runs no copy is the one a team moves to, so a copy, not
    /// the role, says which machine runs the control plane.
    @Test func aCopyNotTheRoleSaysWhichMachineRunsTheControlPlane() throws {
        let running = try row("vm-1", roles: ["control"])
        let target = try row("vm-2", roles: ["control"])
        let agents = try row("box", roles: ["agents"])
        let copies = [look("a", machine: "vm-1")]
        #expect(ControlPlaneMachine.runsControl(running, copies: copies, controlMachines: 2))
        #expect(!ControlPlaneMachine.runsControl(target, copies: copies, controlMachines: 2))
        #expect(!ControlPlaneMachine.runsControl(agents, copies: copies, controlMachines: 2))
        // Before the first read, the role says it: nothing offers a move to the running copy.
        #expect(ControlPlaneMachine.runsControl(target, copies: nil, controlMachines: 2))
        #expect(!ControlPlaneMachine.runsControl(agents, copies: nil, controlMachines: 2))
        // A copy from before #6591 names no machine: the only control machine runs it.
        let unnamed = [look("b", machine: nil)]
        #expect(ControlPlaneMachine.runsControl(running, copies: unnamed, controlMachines: 1))
        #expect(!ControlPlaneMachine.runsControl(agents, copies: unnamed, controlMachines: 1))
        #expect(!ControlPlaneMachine.runsControl(running, copies: unnamed, controlMachines: 2))
        #expect(!ControlPlaneMachine.runsControl(nil, copies: copies, controlMachines: 1))
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

    @Test func theStripSaysNothingWhileTheControlPlaneIsAway() {
        let ok = look("a", machine: "m", level: .ok)
        let full = look("b", machine: "n", level: .full)
        #expect(ControlPlaneMachine.warning([ok, full], controlAway: false)?.copy == "b")
        #expect(ControlPlaneMachine.warning([ok], controlAway: false) == nil)
        // #7363: the last level is not now while the window cannot reach the control plane.
        #expect(ControlPlaneMachine.warning([ok, full], controlAway: true) == nil)
    }

    @Test func thePageHasEachFigure() throws {
        let figures = ControlPlaneMachine.summary(look("a", machine: "m"))
        #expect(figures.map(\.label) == ["Capacity", "Load per core", "Memory pressure", "I/O pressure"])
        #expect(figures.map(\.value) == ["Near", "1.42", "0.0%", "16.7%"])
        let sections = ControlPlaneMachine.sections(look("a", machine: "m"))
        try #require(sections.map(\.title) == ["Memory", "Process", "Store"])
        #expect(sections[1].figures.map(\.value) == ["87", "42"])
        #expect(!ControlPlaneMachine.bytes(0).contains("Zero"))
    }

    /// Two control-only machines have different titles and sentences; one keeps "Control plane" (#7362).
    @Test func severalControlOnlyMachinesAreNamedApart() throws {
        let one = try row("control", roles: ["control"])
        var two = try row("h-2", roles: ["control"])
        let both = try row("mac", roles: ["agents", "control"])
        #expect(ControlPlaneMachine.title(one, among: [one, both]) == "Control plane")
        #expect(ControlPlaneMachine.label(one, among: [one, both]) == nil)
        #expect(ControlPlaneMachine.title(one, among: [one, two]) == "Control plane · control")
        #expect(ControlPlaneMachine.title(two, among: [one, two]) == "Control plane · h-2")
        // The same name on both: the id tells them apart.
        var first = one
        first.name = "eu-1"
        two.name = "eu-1"
        #expect(ControlPlaneMachine.label(first, among: [first, two]) == "control")
        two.name = "eu-2"
        #expect(ControlPlaneMachine.label(two, among: [first, two]) == "eu-2")
        let near = look("a", machine: "h-2")
        #expect(ControlPlaneMachine.sentence(near, on: "eu-2") == "The control plane on eu-2 is near capacity: its disk is busy.")
        #expect(ControlPlaneMachine.sentence(near, on: nil) == "The control plane is near capacity: its disk is busy.")
        #expect(ControlPlaneMachine.sentence(look("a", machine: "h-2", reason: "disk is busy"), on: "eu-2") == "eu-2: Disk is busy.")
    }
}
