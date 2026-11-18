import Foundation
import Observation
import Synchronization
import Testing
@testable import AgentsKitCore

/// The sidebar's Machines group, its rows and the Machines page's Working column read each
/// machine's session counts (P20261117-1). They are kept as agents are filed, so an update
/// that moves no count redraws none of them, and they agree with `MachineSessions`, which the
/// machine's own page still counts from its agents.
@Suite("Each machine's session counts")
@MainActor
struct MachineSessionCountsTests {
    private static let box = MachineRef(rawValue: "box")

    private func agent(_ state: AgentState, on host: MachineRef = .mac, at offset: Double = 0) -> Agent {
        var agent = Agent(runtimeID: "claude", cwd: URL(fileURLWithPath: "/tmp/work"), state: state,
                          lastActivityAt: Date(timeIntervalSince1970: offset))
        agent.host = host
        return agent
    }

    /// What `MachineSessions` says, the full count the machine's page draws.
    private func full(_ model: AgentsModel, _ host: MachineRef) -> MachineSessionCounts {
        let sessions = MachineSessions(model.agents.filter { $0.host == host })
        return MachineSessionCounts(working: sessions.agents(.working).count, waiting: sessions.agents(.waiting).count)
    }

    private func expectAgree(_ model: AgentsModel, sourceLocation: SourceLocation = #_sourceLocation) {
        for host in [MachineRef.mac, Self.box] {
            #expect(model.sessionCounts(on: host) == full(model, host), sourceLocation: sourceLocation)
        }
    }

    @Test func theCountsFollowEveryAgentThatComesChangesMovesOrGoes() {
        let model = AgentsModel()
        var running = agent(.running, at: 1)
        let waiting = agent(.waitingOnUser, at: 2)
        let far = agent(.starting, on: Self.box, at: 3)
        model.replaceAgents([running, waiting, far, agent(.archived, at: 4)])
        #expect(model.sessionCounts(on: .mac) == MachineSessionCounts(working: 1, waiting: 1))
        #expect(model.sessionCounts(on: Self.box) == MachineSessionCounts(working: 1))
        expectAgree(model)

        running.state = .waitingOnUser
        model.upsert(running)
        #expect(model.sessionCounts(on: .mac) == MachineSessionCounts(waiting: 2))
        expectAgree(model)

        running.host = Self.box
        model.upsert(running)
        #expect(model.sessionCounts(on: Self.box) == MachineSessionCounts(working: 1, waiting: 1))
        expectAgree(model)

        model.forget([waiting.id, far.id])
        #expect(model.sessionCounts(on: .mac) == MachineSessionCounts())
        expectAgree(model)

        model.upsert(agent(.running, on: Self.box, at: 6))
        model.replaceAgents([agent(.running, at: 7)], from: .mac)
        #expect(model.sessionCounts(on: .mac) == MachineSessionCounts(working: 1))
        #expect(model.sessionCounts(on: Self.box) == MachineSessionCounts(working: 1, waiting: 1))
        expectAgree(model)

        model.replaceAgents([], from: Self.box)
        #expect(model.sessionCounts(on: Self.box) == MachineSessionCounts())
        #expect(!model.sessionCounts(on: Self.box).busy)
        #expect(model.sessionCounts(on: .mac).runningLine == "1 running")
    }

    /// The cost #165 removed and #5286 brought back: an agent's update that moves no count
    /// redraws the Machines group and every row. What they read must not change.
    @Test func anUpdateThatMovesNoCountRedrawsNoMachine() {
        let model = AgentsModel()
        var busy = agent(.running, at: 1)
        model.replaceAgents([busy, agent(.waitingOnUser, at: 2), agent(.running, on: Self.box, at: 3)])

        let moved = Mutex(false)
        withObservationTracking {
            for host in [MachineRef.mac, Self.box] { _ = model.sessionCounts(on: host) }
        } onChange: { moved.withLock { $0 = true } }
        busy.lastActivityAt = Date(timeIntervalSince1970: 10)
        busy.title = "A new title"
        model.upsert(busy)
        #expect(!moved.withLock { $0 })

        busy.state = .waitingOnUser
        model.upsert(busy)
        #expect(moved.withLock { $0 })
        #expect(model.sessionCounts(on: .mac) == MachineSessionCounts(waiting: 2))
    }

    /// The measurement for P20261117-1, run only when asked (`AGENTS_BENCH_MACHINES=1`):
    /// 5,000 agents on 10 machines, 2,000 updates that move no count. After each update that
    /// changed what the sidebar read, the sidebar reads it again, as SwiftUI would: every
    /// machine's `active` (10 machines is past the seven) and each of the seven rows' line.
    @Test(.enabled(if: ProcessInfo.processInfo.environment["AGENTS_BENCH_MACHINES"] == "1"))
    func benchmarkUpdatesThatMoveNoCount() {
        let model = AgentsModel()
        let hosts = [MachineRef.mac] + (1..<10).map { MachineRef(rawValue: "box-\($0)") }
        let states: [AgentState] = [.running, .waitingOnUser, .stopped, .finished, .archived]
        var held: [Agent] = []
        for index in 0..<5_000 {
            held.append(agent(states[index % states.count], on: hosts[index % hosts.count], at: Double(index)))
        }
        model.replaceAgents(held)

        let invalidated = Mutex(false)
        func read() {
            withObservationTracking {
                for host in hosts { _ = Self.sidebarRead(model, host).busy }
                for host in hosts.prefix(MachinesGroup.limit) { _ = Self.sidebarRead(model, host).runningLine }
            } onChange: { invalidated.withLock { $0 = true } }
        }
        read()
        var redraws = 0
        let updates = 2_000
        let clock = ContinuousClock()
        let took = clock.measure {
            for step in 0..<updates {
                var changed = held[(step * 7) % held.count]
                changed.lastActivityAt = Date(timeIntervalSince1970: Double(10_000 + step))
                model.upsert(changed)
                if invalidated.withLock({ let was = $0; $0 = false; return was }) {
                    redraws += 1
                    read()
                }
            }
        }
        let ms = Double(took.components.attoseconds) / 1e15 + Double(took.components.seconds) * 1_000
        print("BENCH machines updates=\(updates) redraws=\(redraws) total_ms=\(String(format: "%.1f", ms))")
        #expect(redraws == 0)
    }

    /// What the sidebar reads of one machine's sessions.
    private static func sidebarRead(_ model: AgentsModel, _ host: MachineRef) -> MachineSessionCounts {
        model.sessionCounts(on: host)
    }
}
