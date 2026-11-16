import Foundation
import Testing
@testable import AgentsKitCore

/// The Mac's Machines page (#5567), as the web page's tests check its `machineList.ts` (#5285): the
/// query of a page, the chips, the selected machines' actions and Delete's question.
@Suite("Machine list choice")
struct MachineListChoiceTests {
    private func made(_ id: String, _ state: String, canStart: Bool = true, canStop: Bool = true,
                      version: String = "0.18.1") -> DaemonAPI.ControlMachine {
        var row = DaemonAPI.ControlMachine(id: MachineRef(rawValue: id), name: id, platform: "darwin", version: version,
                                           state: state, reach: "direct", machineID: nil)
        row.workspace = DaemonAPI.MachineWorkspace(provider: ProviderID(rawValue: "do"), providerName: "DigitalOcean",
                                                   workspace: id, workspaceName: id, workspaceState: state,
                                                   canStart: canStart, canStop: canStop)
        return row
    }

    @Test func aPageAsksOnlyForWhatIsChosenThenTheCursor() {
        #expect(MachineListChoice().query() == DaemonAPI.MachineListQuery(sort: .name, limit: 50))
        let choice = MachineListChoice(search: " box ").toggled(.state, "online").toggled(.labels, "gpu")
        #expect(choice.query(cursor: "c1") == DaemonAPI.MachineListQuery(query: "box", state: "online", labels: ["gpu"],
                                                                          sort: .name, limit: 50, cursor: "c1"))
    }

    @Test func theQueryIsWrittenAsTheWireSaysIt() throws {
        let json = try JSONValue.encoding(MachineListChoice(sort: .lastSeen).query())
        #expect(json == .object(["sort": .string("lastSeen"), "limit": .int(50)]))
    }

    @Test func aChipTogglesAnotherValueReplacesItLabelsAddUp() {
        var choice = MachineListChoice().toggled(.state, "online")
        #expect(choice.state == "online")
        choice = choice.toggled(.state, "stopped")
        #expect(choice.state == "stopped")
        #expect(choice.toggled(.state, "stopped").state == nil)
        choice = MachineListChoice().toggled(.labels, "a").toggled(.labels, "b")
        #expect(choice.labels == ["a", "b"])
        #expect(choice.toggled(.labels, "a").labels == ["b"])
        #expect(!MachineListChoice().narrowed)
        #expect(choice.narrowed)
        #expect(MachineListChoice(search: "x", state: "online", sort: .version).cleared == MachineListChoice(sort: .version))
    }

    @Test func chipsComeFromTheCountsBiggestFirstAndKeepAChosenValueAtZero() throws {
        let counts = DaemonAPI.MachineCounts(state: ["online": 3, "stopped": 7], labels: ["gpu": 2], os: ["darwin": 1])
        let chips = MachineListChoice(state: "failed").chips(counts)
        #expect(chips.map { "\($0.filter.rawValue):\($0.value):\($0.count):\($0.on)" }
            == ["state:stopped:7:false", "state:online:3:false", "state:failed:0:true", "os:darwin:1:false", "labels:gpu:2:false"])
        #expect(try #require(chips.first).words == "stopped 7")
        #expect(try #require(chips.last).words == "# gpu 2")
        #expect(MachineListChoice().chips(nil).isEmpty)
    }

    @Test func aControlPlaneWithNoPagesIsFilteredSortedAndCountedHere() {
        let all = [made("b", "online"), made("a", "stopped"), made("c", "online", version: "0.17.0")]
        #expect(MachineListChoice().filtered(all).map(\.name) == ["a", "b", "c"])
        #expect(MachineListChoice(state: "online", sort: .version).filtered(all).map(\.name) == ["c", "b"])
        #expect(MachineListChoice(search: "B").filtered(all).map(\.name) == ["b"])
        #expect(MachineListChoice.counts(of: all).state == ["online": 2, "stopped": 1])
        #expect(MachineListChoice.counts(of: all).provider == ["DigitalOcean": 3])
    }

    @Test func anActionReachesOnlyTheSelectedMachinesItFits() {
        var registered = made("r", "online")
        registered.workspace = nil
        var mac = made("mac", "stopped")
        mac.id = .mac
        let actions = MachineBulkActions([made("a", "online"), made("b", "stopped"), registered, mac])
        #expect(actions.start.map(\.name) == ["b"])
        #expect(actions.stop.map(\.name) == ["a"])
        #expect(actions.delete.map(\.name) == ["a", "b"])
        #expect(MachineBulkActions.label("Stop", reached: 1, selected: 3) == "Stop 1")
        #expect(MachineBulkActions.label("Stop", reached: 3, selected: 3) == "Stop")
        #expect(MachineBulkActions.label("Stop", reached: 0, selected: 3) == "Stop")
        #expect(MachineBulkActions.selectedWords(2) == "2 selected")
    }

    @Test func deleteAsksNamesTheMachinesAndListsTheWorkNotPushed() {
        #expect(MachineBulkActions.deleteQuestion(["a"], unpushed: []).hasPrefix("Delete a?"))
        let question = MachineBulkActions.deleteQuestion(["a", "b", "c", "d", "e", "f", "g"], unpushed: ["a: w"])
        #expect(question.contains("7 machines (a, b, c, d, e and 2 more)"))
        #expect(question.hasSuffix("Work not pushed on it: a: w."))
        let previews: [(name: String, preview: DaemonAPI.StartedMachinePreview?)] = [
            ("a", DaemonAPI.StartedMachinePreview(host: MachineRef(rawValue: "a"), name: "a", state: "online", unknown: "It did not answer.")),
            ("b", nil),
        ]
        #expect(MachineBulkActions.unpushed(previews) == ["a: It did not answer."])
    }

    @Test func theFootSaysHowManyAreShown() {
        #expect(MachineListChoice.countWords(shown: 50, total: 120) == "50 of 120 machines")
        #expect(MachineListChoice.countWords(shown: 1, total: 1) == "1 machine")
        #expect(MachineListChoice.showMoreWords(shown: 50, total: 120) == "Show More (50 of 120)")
        #expect(MachineListChoice.stateWords(made("a", "needsUpdate")) == "Needs an update")
        #expect(MachineListChoice.stateWords(made("a", "online")) == "Online")
    }
}
