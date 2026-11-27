import AgentsKitCore
import SwiftUI

/// Across the top of the window while a copy of the control plane is near or at capacity (#6591),
/// as a machine's capacity warning (#4582), for an admin. It says why, in the
/// control plane's words, opens the control plane's machine, and opens the guide that says what
/// to do (#6589). The control plane keeps serving at every level: the strip only tells. While the
/// control plane is away it is hidden (#7363): its level is the last one known, not now.
struct ControlCapacityStrip: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openURL) private var openURL

    var body: some View {
        if model.showsControlPlaneMachine, let look = model.controlCapacity.warning(controlAway: model.controlPlaneAway) {
            let machine = look.machine.map(MachineRef.init(rawValue:))
                ?? (model.controlPlaneHosts ?? []).first { ControlPlaneMachine.controlRole($0) }?.id
            let rows = model.controlPlaneHosts ?? []
            Strip(tone: .warning,
                  line: ControlPlaneMachine.sentence(look, on: ControlPlaneMachine.label(rows.first { $0.id == machine }, among: rows)),
                  primary: machine.map { host in
                      StripButton(title: "Show Control Plane", help: "Its level and figures, on its page in Machines") {
                          model.machinePage = host
                      }
                  },
                  second: StripButton(title: "What to Do", help: "Run the control plane in the cloud: a larger machine, more memory, a faster disk or a store sweep") {
                      openURL(ControlPlaneMachine.guide)
                  })
                .paneBannerInset()
        }
    }
}
