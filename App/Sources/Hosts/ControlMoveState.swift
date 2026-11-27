import AgentsKitCore
import Foundation
import Observation

/// Make This the Control Plane… (#6614, epic #6608), for
/// an admin: the moves from `control/moves/list` when the window connects, each step
/// from `control/moveChanged`, and each machine's checks from `control/eligible` while its page
/// is open. The web page keeps the same in `Web/src/model/controlMove.ts`.
@MainActor
@Observable
final class ControlMoveState {
    /// The newest first, as the control plane lists them.
    private(set) var moves: [DaemonAPI.ControlMove] = []
    /// When the window last heard how the moves stand (#5139): the strip's "Last update".
    private(set) var heardAt: Date?
    /// The moves the person closed on the strip.
    private(set) var closed: Set<String> = []

    /// One machine's checks, as last read.
    struct Checks: Equatable {
        var answer: DaemonAPI.ControlEligible?
        var failure: String?
        var readAt: Date?
        var checking = false
    }

    private(set) var checks: [MachineRef: Checks] = [:]

    /// The move the strip says: the newest, while it runs or for a day after, unless closed.
    var strip: DaemonAPI.ControlMove? {
        guard let move = moves.first, ControlMoveWords.shown(move), !closed.contains(move.id) else { return nil }
        return move
    }

    /// A move runs now.
    var running: DaemonAPI.ControlMove? { moves.first(where: ControlMoveWords.running) }

    /// The newest move to that machine, while it runs or for a day after.
    func move(to machine: MachineRef) -> DaemonAPI.ControlMove? {
        moves.first { $0.to == machine }.flatMap { ControlMoveWords.shown($0) ? $0 : nil }
    }

    func close(_ move: DaemonAPI.ControlMove) { closed.insert(move.id) }

    func read(_ control: DaemonClient) async {
        guard let list = try? await control.call(DaemonAPI.Method.controlMovesList, DaemonAPI.Empty(),
                                                 returning: DaemonAPI.ControlMoves.self) else { return }
        moves = list.moves
        heardAt = Date()
    }

    /// `control/moveChanged`: a move changed its step. True when it was that notice.
    func heard(_ method: String, _ params: JSONValue?) -> Bool {
        guard method == DaemonAPI.Notification.controlMoveChanged else { return false }
        if let move = try? params?.decode(DaemonAPI.ControlMove.self) { take(move) }
        return true
    }

    private func take(_ move: DaemonAPI.ControlMove) {
        moves.removeAll { $0.id == move.id }
        moves.insert(move, at: 0)
        moves.sort { $0.startedAt > $1.startedAt }
        heardAt = Date()
    }

    /// Reads the machine's checks; the last answer stays under a read that failed.
    func check(_ control: DaemonClient, page: MachineRef, machine: MachineRef) async {
        checks[page, default: Checks()].checking = true
        defer { checks[page]?.checking = false }
        do {
            let answer = try await control.call(DaemonAPI.Method.controlEligible,
                                                DaemonAPI.ControlEligibleRequest(machine: machine),
                                                returning: DaemonAPI.ControlEligible.self)
            checks[page] = Checks(answer: answer, failure: nil, readAt: Date())
        } catch let error as JSONRPCError {
            checks[page]?.failure = HostProblem.controlRefusal(error)
        } catch {
            checks[page]?.failure = "The control plane can’t be reached."
        }
    }

    /// Make Control Plane: starts the move. Its steps come by `control/moveChanged`. Nil when
    /// it started, else why not.
    func start(_ control: DaemonClient, machine: MachineRef) async -> String? {
        do {
            let started = try await control.call(DaemonAPI.Method.controlMove, DaemonAPI.ControlMoveRequest(machine: machine),
                                                 returning: DaemonAPI.ControlMoveResult.self)
            take(started.move)
            closed.remove(started.move.id)
            return nil
        } catch let error as JSONRPCError {
            return HostProblem.controlRefusal(error)
        } catch {
            return "The control plane can’t be reached."
        }
    }
}
