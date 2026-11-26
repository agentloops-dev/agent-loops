import Foundation
import Testing
@testable import AgentsKitCore

/// Make This the Control Plane… (#6614): the strip's steps and words.
@Suite("Control move words")
struct ControlMoveTests {
    static func move(_ phase: DaemonAPI.ControlMovePhase, failed: DaemonAPI.ControlMovePhase? = nil,
                     error: String? = nil, until: Date? = nil, updated: Date = Date()) -> DaemonAPI.ControlMove {
        DaemonAPI.ControlMove(id: "m1", from: "copy", to: MachineRef(rawValue: "droplet"), toName: "droplet-ams3-1",
                              phase: phase, failedPhase: failed, forwardingUntil: until,
                              startedAt: updated, updatedAt: updated, error: error, by: "alex")
    }

    @Test func theStepsGoInTheOrderTheMoveRunsThem() {
        #expect(ControlMoveWords.steps.map(\.title) == ["Check", "Receive", "Freeze", "Copy store", "Forward"])
    }

    @Test func aRunningMoveHasItsStepCurrentAndTheEarlierOnesDone() {
        #expect(ControlMoveWords.states(Self.move(.frozen)) == [.done, .done, .current, .todo, .todo])
        #expect(ControlMoveWords.line(Self.move(.copying))
                == "Moving the control plane to droplet-ams3-1: copying the store.")
        #expect(ControlMoveWords.running(Self.move(.checking)))
    }

    @Test func aFailedMoveSaysWhichStepAndThatTheOldOneServes() {
        let failed = Self.move(.failed, failed: .copying, error: "The store did not copy: no space left.")
        #expect(ControlMoveWords.states(failed) == [.done, .done, .done, .failed, .todo])
        #expect(ControlMoveWords.line(failed)
                == "The move to droplet-ams3-1 stopped at “Copy store”: The store did not copy: no space left. The control plane still serves where it was.")
        #expect(!ControlMoveWords.running(failed))
    }

    @Test func anEndedMoveHasEveryStepDoneAndSaysUntilWhenItForwards() {
        let until = Date(timeIntervalSince1970: 1_798_000_000)
        let forwarding = Self.move(.forwarding, until: until)
        #expect(ControlMoveWords.states(forwarding).allSatisfy { $0 == .done })
        #expect(ControlMoveWords.line(forwarding) == "droplet-ams3-1 is the control plane.")
        #expect(ControlMoveWords.forwarding(forwarding)?.hasPrefix("The old address forwards to it until ") == true)
        #expect(ControlMoveWords.forwarding(Self.move(.done)) == nil)
    }

    @Test func anEndedMoveShowsForADay() {
        let now = Date()
        #expect(ControlMoveWords.shown(Self.move(.done, updated: now.addingTimeInterval(-3_600)), now: now))
        #expect(!ControlMoveWords.shown(Self.move(.done, updated: now.addingTimeInterval(-90_000)), now: now))
        #expect(ControlMoveWords.shown(Self.move(.copying, updated: now.addingTimeInterval(-90_000)), now: now))
    }

    @Test func theChecksAndTheConfirmationSayWhatHappens() {
        let eligible = DaemonAPI.ControlEligible(ok: true, reasons: [], address: "ams3-1.example.dev",
                                                 memoryBytes: 2 << 30, diskFreeBytes: 40 << 30)
        #expect(ControlMoveWords.meets(eligible)
                == "It meets the checks: reachable at ams3-1.example.dev, always on, 2 GB memory, 40 GB free disk.")
        let moving = ControlMoveWords.happens(name: "droplet-ams3-1", address: "ams3-1.example.dev", now: "control.example.dev")
        #expect(moving.contains { $0.hasPrefix("The address changes to ams3-1.example.dev. The old address forwards") })
        let same = ControlMoveWords.happens(name: "droplet-ams3-1", address: "control.example.dev", now: "control.example.dev")
        #expect(same.contains("The address stays control.example.dev."))
        #expect(same.count == 4)
    }

    /// #7395: the strip counts the copy's records, and the confirmation's time is the store's.
    @Test func theCopyCountsItsRecordsAndTheConfirmationSaysHowLongItIsFrozen() {
        var copying = Self.move(.copying)
        copying.copied = 1_200
        copying.total = 5_000
        #expect(ControlMoveWords.line(copying)
                == "Moving the control plane to droplet-ams3-1: copying the store, \(Int64(1_200).formatted()) of \(Int64(5_000).formatted()) records.")
        copying.total = 0
        #expect(ControlMoveWords.line(copying) == "Moving the control plane to droplet-ams3-1: copying the store.")
        #expect(ControlMoveWords.about(5) == "about 10 seconds")
        #expect(ControlMoveWords.about(35) == "about 40 seconds")
        #expect(ControlMoveWords.about(80) == "about a minute")
        #expect(ControlMoveWords.about(271) == "about 5 minutes")
        let lines = ControlMoveWords.happens(name: "droplet-ams3-1", address: nil, now: nil, frozenSeconds: 35)
        #expect(lines.last
                == "Agents keep working. For about 40 seconds, the control plane is frozen: it refuses changes, and screens can’t reach machines.")
        #expect(ControlMoveWords.happens(name: "droplet-ams3-1", address: nil, now: nil).last
                == "Agents keep working. While the store copies, the control plane is frozen: it refuses changes, and screens can’t reach machines.")
    }
}
