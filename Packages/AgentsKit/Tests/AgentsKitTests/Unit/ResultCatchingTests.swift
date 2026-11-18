@testable import AgentsKitCore
import Foundation
import Testing

/// `Result.catching` (#5946): a task-group child builds its result first and returns its
/// tuple from one place, so an optimised build keeps the tuple's key (swiftlang/swift#92698).
@Suite("Result.catching")
struct ResultCatchingTests {
    private struct Failed: Error {}

    private static func read(_ machine: MachineRef) async throws -> [String] {
        try await Task.sleep(for: .milliseconds(1))
        if machine.rawValue.hasSuffix("3") { throw Failed() }
        return [machine.rawValue]
    }

    @Test func aValueIsASuccessAndAThrowIsAFailure() async {
        let got = await Result.catching { try await Self.read(MachineRef(rawValue: "m1")) }
        #expect((try? got.get()) == ["m1"])
        #expect(got.error == nil)
        let failed = await Result.catching { try await Self.read(MachineRef(rawValue: "m3")) }
        #expect(failed.error is Failed)
    }

    @Test func everyChildKeepsItsMachine() async {
        let machines = (0..<6).map { MachineRef(rawValue: "machine-\($0)") }
        let got = await withTaskGroup(of: (MachineRef, Result<[String], any Error>).self) { group in
            for machine in machines {
                group.addTask {
                    let read = await Result.catching { try await Self.read(machine) }
                    return (machine, read)
                }
            }
            var got: [MachineRef: Result<[String], any Error>] = [:]
            for await (machine, read) in group { got[machine] = read }
            return got
        }
        #expect(Set(got.keys) == Set(machines))
        #expect(got[MachineRef(rawValue: "machine-3")]?.error is Failed)
        #expect((try? got[MachineRef(rawValue: "machine-2")]?.get()) == ["machine-2"])
    }
}
