import AgentsKitCore
import Foundation
import Observation

/// The control plane's capacity, for an admin's Machines (#6591, epic #6593): the latest look of
/// each live copy, from `control/capacity` (#6590) when the window connects, when the flag turns
/// on (#7364) and while a control plane machine's page is open, and each move from
/// `control/capacityChanged`. Behind the flag `control-plane-machine`; a member reads nothing, as the control plane answers admins only.
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
    /// When the reads that worked began to answer with no copy, while they still do (#7364).
    private(set) var emptySince: Date?

    /// Why the window has no reading (#7364): the row's line and the page's sentence.
    var noReadingWords: (line: String, detail: String) {
        ControlPlaneMachine.noReadingWords(failed: readAt == nil && failure != nil,
                                           emptyFor: readAt.flatMap { read in emptySince.map { read.timeIntervalSince($0) } })
    }

    var looks: [DaemonAPI.ControlCapacityCopy] { Array(copies.values) }

    /// The copies once a read has worked; nil before (#7393): which machine runs the control plane
    /// then falls back to the role.
    var readLooks: [DaemonAPI.ControlCapacityCopy]? { readAt == nil ? nil : looks }

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
            let now = Date()
            readAt = now
            failure = nil
            emptySince = copies.isEmpty ? (emptySince ?? now) : nil
        } catch let error as JSONRPCError {
            failure = HostProblem.controlRefusal(error)
        } catch {
            failure = ControlPlaneMachine.refreshFailed
        }
    }

    /// When the window comes to show the control plane (#7364): the flag turns on, or the person
    /// becomes an admin. Reads at once, not at the next connect, and once more after a look when
    /// the answer is empty, so the words can say that no reading comes.
    func shown(_ control: DaemonClient) async {
        await read(control)
        guard readAt != nil, copies.isEmpty else { return }
        try? await Task.sleep(for: .seconds(ControlPlaneMachine.lookSeconds))
        if !Task.isCancelled { await read(control) }
    }

    /// `control/capacityChanged`: one copy's level moved. True when it was that notice.
    func heard(_ method: String, _ params: JSONValue?) -> Bool {
        guard method == DaemonAPI.Notification.controlCapacityChanged else { return false }
        if let look = try? params?.decode(DaemonAPI.ControlCapacityCopy.self) {
            copies[look.copy] = look
            emptySince = nil
        }
        return true
    }
}
