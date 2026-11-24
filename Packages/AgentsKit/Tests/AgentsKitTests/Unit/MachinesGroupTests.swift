import Foundation
import Testing
@testable import AgentsKitCore

/// The sidebar's Machines group (#5286): at most seven rows, then All Machines (N). The web
/// page's machinesGroup is tested with the same cases.
@Suite("The Machines group, in the sidebar")
struct MachinesGroupTests {
    private func m(_ id: String, pinned: Bool = false, active: Bool = false) -> MachinesGroup.Machine {
        MachinesGroup.Machine(host: id == "mac" ? .mac : MachineRef(rawValue: id), pinned: pinned, active: active)
    }

    private func names(_ rows: MachinesGroup.Rows) -> [String] { rows.machines.map(\.rawValue) }

    @Test func sevenOrFewerAreAllShownInTheirOrder() {
        let machines = [m("mac"), m("a"), m("b", active: true), m("c", pinned: true)]
        let rows = MachinesGroup.rows(machines, making: 2)
        #expect(names(rows) == ["mac", "a", "b", "c"])
        #expect(rows.making == 2)
        #expect(rows.total == 6)
        #expect(!rows.bounded)
        #expect(!MachinesGroup.rows((0..<7).map { m("m\($0)") }, making: 0).bounded)
    }

    @Test func moreThanSevenShowsThisMacThenPinnedThenActiveThenTheRest() {
        let machines = [m("mac")] + (0..<20).map { i in m("m\(i)", pinned: i == 15, active: i == 3 || i == 12) }
        let rows = MachinesGroup.rows(machines, making: 0)
        #expect(names(rows) == ["mac", "m15", "m3", "m12", "m0", "m1", "m2"])
        #expect(rows.total == 21)
        #expect(rows.bounded)
    }

    @Test func machinesBeingMadeComeAfterTheActiveOnesAndCount() {
        let machines = [m("mac")] + (0..<10).map { m("m\($0)", active: $0 == 9) }
        let rows = MachinesGroup.rows(machines, making: 2)
        #expect(names(rows) == ["mac", "m9", "m0", "m1", "m2"])
        #expect(rows.making == 2)
        #expect(rows.total == 13)
    }

    @Test func neverMoreThanSevenAlsoWhenManyArePinnedOrActive() {
        let machines = (0..<30).map { m("m\($0)", pinned: $0 < 5, active: true) }
        let rows = MachinesGroup.rows(machines, making: 3)
        #expect(rows.machines.count == 7)
        #expect(rows.making == 0)
        #expect(names(rows).prefix(5) == ["m0", "m1", "m2", "m3", "m4"])
    }

    @Test func allDrawsEveryMachine() {
        let machines = (0..<12).map { m("m\($0)") }
        let rows = MachinesGroup.rows(machines, making: 1, all: true)
        #expect(rows.machines.count == 12)
        #expect(rows.making == 1)
        #expect(!rows.bounded)
        #expect(MachinesGroup.allTitle(rows.total) == "All Machines (13)")
    }

    @Test func pinsAreKeptAndLetGo() throws {
        let box = MachineRef(rawValue: "box"), dev = MachineRef(rawValue: "dev")
        let one = try #require(MachinePins.toggled(box, in: ""))
        #expect(MachinePins.pinned(one) == [box])
        let two = try #require(MachinePins.toggled(dev, in: one))
        #expect(MachinePins.pinned(two) == [box, dev])
        #expect(MachinePins.pinned(try #require(MachinePins.toggled(box, in: two))) == [dev])
        #expect(MachinePins.pinned("").isEmpty)
        #expect(MachinePins.menuTitle(pinned: true) == "Unpin from Sidebar")
    }
}
