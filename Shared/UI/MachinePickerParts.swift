import AgentsKitCore
import SwiftUI

// The parts of the one machine picker that the Mac (#5621) and the Remote (#5622) share; the web's is #5287.

/// A machine's state dot: green online, amber needing an update, grey stopped, red anything else that does not answer.
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

extension View {
    /// The picker's read of a page (#5621, #5622): again when the words change (after a moment's wait while
    /// typing) or when a machine joins, leaves or changes state (`machines`, a key of each and its state).
    func machinePickerReads(_ search: String, machines: String, read: @escaping (String) async -> Void) -> some View {
        task(id: "\(search)|\(machines)") {
            if !search.isEmpty { try? await Task.sleep(for: .milliseconds(200)) }
            guard !Task.isCancelled else { return }
            await read(search)
        }
    }
}

/// The pin of a machine's row, filled while it is pinned.
struct MachinePickerPin: View {
    let isPinned: Bool

    var body: some View {
        Image(icon: isPinned ? "pin.fill" : "pin")
            .foregroundStyle(isPinned ? StateTint.attention.style(or: .secondary) : AnyShapeStyle(.tertiary))
    }
}
