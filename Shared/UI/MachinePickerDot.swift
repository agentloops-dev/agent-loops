import AgentsKitCore
import SwiftUI

/// A machine's state dot in the one machine picker (#5621 on the Mac, #5622 on the Remote, the web's #5287):
/// green online, amber needing an update, grey stopped, red anything else that does not answer.
struct MachinePickerDot: View {
    let dot: MachinePickerChoice.Dot
    var size: CGFloat = 7

    init(_ machine: DaemonAPI.ControlMachine, size: CGFloat = 7) {
        dot = MachinePickerChoice.dot(machine)
        self.size = size
    }

    var body: some View {
        Circle().fill(tint).frame(width: size, height: size).accessibilityHidden(true)
    }

    private var tint: AnyShapeStyle {
        switch dot {
        case .online: StateTint.vouched.style(or: .secondary)
        case .attention: StateTint.attention.style(or: .secondary)
        case .stopped: AnyShapeStyle(.tertiary)
        case .offline: StateTint.failure.style(or: .secondary)
        }
    }
}
