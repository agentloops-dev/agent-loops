import Foundation
import Testing
@testable import AgentsKitCore

/// Labels on a machine (#5284).
@Suite("Machine labels")
struct MachineLabelsTests {
    private func machine(_ labels: [String]?, auto: [String]? = nil) -> DaemonAPI.ControlMachine {
        var row = DaemonAPI.ControlMachine(id: MachineRef(rawValue: "m"), name: "M", platform: "linux arm64", version: "1",
                                           state: "online", reach: "dialOut", machineID: nil)
        row.labels = labels
        row.autoLabels = auto
        return row
    }

    @Test func aPersonsLabelsAreTheOnesTheProviderDidNotSet() {
        let row = machine(["arch=arm64", "gpu", "os=linux", "team=a"], auto: ["os=linux", "arch=arm64"])
        #expect(row.ownLabels == ["gpu", "team=a"])
    }

    @Test func rolesAreReadWhenSaidAndNilFromAnOlderControlPlane() throws {
        let said = Data(#"{"id":"m","name":"M","platform":"linux","version":"1","state":"online","reach":"dialOut","roles":["agents","control"]}"#.utf8)
        #expect(try JSONDecoder().decode(DaemonAPI.ControlMachine.self, from: said).roles == ["agents", "control"])
        let older = Data(#"{"id":"m","name":"M","platform":"linux","version":"1","state":"online","reach":"dialOut"}"#.utf8)
        #expect(try JSONDecoder().decode(DaemonAPI.ControlMachine.self, from: older).roles == nil)
    }

    @Test func typedLabelsAreSplitAndKeptOnce() {
        #expect(MachineLabels.parse("gpu, team=a  GPU") == ["gpu", "team=a"])
    }

    @Test func aProviderKeyOrAnOddLabelIsRefused() {
        #expect(MachineLabels.problem("os=linux") != nil)
        #expect(MachineLabels.problem("has/odd chars!") != nil)
        #expect(MachineLabels.problem("team-a") == nil)
        #expect(MachineLabels.problem("client=x") == nil)
    }

    @Test func everyLabelOnTheMachinesOnceSorted() {
        let all = MachineLabels.all([machine(["gpu", "os=linux"]), machine(["GPU", "a"])])
        #expect(all == ["a", "gpu", "os=linux"])
    }
}
