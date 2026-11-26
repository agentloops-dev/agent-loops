import AgentsKitCore
import Foundation
import Observation

/// The control plane's capacity, for an admin's Machines (#6591, epic #6593): the latest look of
/// each live copy, from `control/capacity` (#6590) when the window connects and while a control
/// plane machine's page is open, and each move from `control/capacityChanged`. Behind the flag
/// `control-plane-machine`; a member reads nothing, as the control plane answers admins only.
@MainActor
@Observable
final class ControlCapacityState {
    /// The latest look of each copy, by copy id.
    private(set) var copies: [String: DaemonAPI.ControlCapacityCopy] = [:]
    /// When the last read worked (#5139): a page says "Last update" from it.
    private(set) var readAt: Date?
    /// Why the last read did not work; the page keeps the last looks under it.
    private(set) var failure: String?
    /// A read is under way.
    private(set) var refreshing = false

    var looks: [DaemonAPI.ControlCapacityCopy] { Array(copies.values) }

    /// The latest look of the copy that runs on `machine`.
    func look(for machine: MachineRef, controlMachines: Int) -> DaemonAPI.ControlCapacityCopy? {
        ControlPlaneMachine.look(for: machine, in: looks, controlMachines: controlMachines)
    }

    /// The copy the window's strip names: the highest level, near or full only; none while the
    /// control plane is away (#7363).
    func warning(controlAway: Bool) -> DaemonAPI.ControlCapacityCopy? {
        ControlPlaneMachine.warning(looks, controlAway: controlAway)
    }

    func read(_ control: DaemonClient) async {
        refreshing = true
        defer { refreshing = false }
        do {
            let board = try await control.call(DaemonAPI.Method.controlCapacity, DaemonAPI.Empty(),
                                               returning: DaemonAPI.ControlCapacityBoard.self)
            copies = Dictionary(board.copies.map { ($0.copy, $0) }, uniquingKeysWith: { $0.at > $1.at ? $0 : $1 })
            readAt = Date()
            failure = nil
        } catch let error as JSONRPCError {
            failure = HostProblem.controlRefusal(error)
        } catch {
            failure = ControlPlaneMachine.refreshFailed
        }
    }

    /// `control/capacityChanged`: one copy's level moved. True when it was that notice.
    func heard(_ method: String, _ params: JSONValue?) -> Bool {
        guard method == DaemonAPI.Notification.controlCapacityChanged else { return false }
        if let look = try? params?.decode(DaemonAPI.ControlCapacityCopy.self) {
            copies[look.copy] = look
        }
        return true
    }
}
