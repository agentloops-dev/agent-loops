import Foundation
import Testing
@testable import AgentsKitCore

/// The one machine picker (#5621), as the web page's `machinePicker.test.mjs` checks its model (#5287): the query, the
/// sections (pinned, recent, then groups by label) and what it keeps.
@Suite("Machine picker choice")
struct MachinePickerChoiceTests {
    private func made(_ id: String, _ state: String, _ labels: [String] = [], auto: [String] = []) -> DaemonAPI.ControlMachine {
        var row = DaemonAPI.ControlMachine(id: MachineRef(rawValue: id), name: id, platform: "linux", version: "1",
                                           state: state, reach: "direct", machineID: nil)
        row.labels = labels
        row.autoLabels = auto
        return row
    }

    private func titles(_ sections: [MachinePickerChoice.Section]) -> [[String]] {
        sections.map { [$0.title] + $0.machines.map(\.id.rawValue) }
    }

    @Test func theQueryAsksForAPageByNameWithTheWordsWhenThereAreSome() {
        #expect(MachinePickerChoice.query("") == DaemonAPI.MachineListQuery(sort: .name, limit: 30))
        #expect(MachinePickerChoice.query("  gpu ") == DaemonAPI.MachineListQuery(query: "gpu", sort: .name, limit: 30))
    }

    @Test func pinnedThenRecentThenAGroupForEachLabelThenTheRestAMachineOnce() {
        let page = [made("a", "online", ["team=x", "os=linux"], auto: ["os=linux"]), made("b", "online", ["gpu"]),
                    made("c", "stopped"), made("d", "online", ["team=y"]), made("e", "offline", ["os=linux"], auto: ["os=linux"])]
        let sections = MachinePickerChoice.sections(page, pinned: ["c", "zz"], recent: ["d", "c"])
        #expect(titles(sections) == [["Pinned", "c"], ["Recent", "d"], ["gpu", "b"], ["team", "a"], ["Other machines", "e"]])
    }

    @Test func withNoLabelsTheRestIsMachinesAndKeepLeavesAMachineOut() {
        let sections = MachinePickerChoice.sections([made("a", "online"), made("b", "online")], pinned: [], recent: []) {
            $0.id.rawValue != "a"
        }
        #expect(titles(sections) == [["Machines", "b"]])
    }

    @Test func theDotSaysTheState() {
        #expect(MachinePickerChoice.dot(made("a", "online")) == .online)
        #expect(MachinePickerChoice.dot(made("a", "needsUpdate")) == .attention)
        #expect(MachinePickerChoice.dot(made("a", "stopped")) == .stopped)
        #expect(MachinePickerChoice.dot(made("a", "offline")) == .offline)
    }

    @Test func recentKeepsTheLastFiveNewestFirstAndAPinToggles() {
        let store = MemoryDefaults()
        for id in ["1", "2", "3", "4", "5", "6", "3"] { MachinePickerChoice.remember(store, id) }
        #expect(MachinePickerChoice.recent(store) == ["3", "6", "5", "4", "2"])
        #expect(MachinePickerChoice.togglePin(store, "a") == ["a"])
        #expect(MachinePickerChoice.togglePin(store, "a") == [])
    }

    @Test func aPinPastTheMostIsRefusedAndAtItIsLetGo() {
        let store = MemoryDefaults()
        for id in 0..<MachinePins.most { #expect(MachinePickerChoice.togglePin(store, "m\(id)") != nil) }
        #expect(MachinePickerChoice.togglePin(store, "over") == nil)
        #expect(MachinePickerChoice.pinned(store).count == MachinePins.most)
        #expect(MachinePickerChoice.togglePin(store, "m0")?.count == MachinePins.most - 1)
        #expect(MachinePickerChoice.togglePin(store, String(repeating: "a", count: 65)) == nil)
        #expect(MachinePickerChoice.togglePin(store, String(repeating: "a", count: 64)) != nil)
    }

    @Test func thePinsAreTheSidebarsAndGoWithTheirMachine() {
        let store = MemoryDefaults()
        store.set(["mac", "a", "gone"], forKey: MachinePins.key)
        #expect(MachinePickerChoice.pinned(store) == ["mac", "a", "gone"])
        MachinePins.keep(store, listed: [MachineRef(rawValue: "a"), MachineRef(rawValue: "b")])
        #expect(store.string(forKey: MachinePins.key) == "mac\na")
        #expect(MachinePickerChoice.togglePin(store, "b") == ["mac", "a", "b"])
    }

    @Test func anOlderControlPlaneIsFilteredByNameIdOrLabel() {
        let all = [made("zeta", "online"), made("alpha", "online", ["gpu"]), made("beta", "online")]
        #expect(MachinePickerChoice.filtered(all, search: "").map(\.name) == ["alpha", "beta", "zeta"])
        #expect(MachinePickerChoice.filtered(all, search: " GPU").map(\.name) == ["alpha"])
    }
}
