import AgentsKitCore
import SwiftUI

/// A machine's set-up as steps on its page (#5357, epic #5353, boards 2 and 3): a mark, the
/// name, one line and the time for each of the eight steps; a step done in a warm pool says
/// *pool* (#5366). A failed step stops the run, says why, and offers Try Again and Delete
/// Machine…. After 24 hours the steps fold into one line. Read from the control plane's row,
/// which it pushes as the machine changes. Drawn by the Mac window and the Remote (#6012); the
/// window puts its sign-in cards under step 7 (#5361).
struct MachineSetupSection<SignIn: View>: View {
    let row: DaemonAPI.ControlMachine
    let setup: DaemonAPI.MachineSetup
    /// How many sessions run there, for Delete's question.
    var sessions = 0
    /// Try Again: the control plane runs the failed step and the ones after it.
    let onRetry: () -> Void
    /// Delete, once the person said yes.
    let onDelete: () -> Void
    /// Under step 7, Sign in to runtimes.
    @ViewBuilder let signIn: () -> SignIn

    @State private var deleting = false
    /// Machine scripts, a flag in work (#7154, `FeatureFlag`): a failed Root set-up shows root.log's
    /// last lines and what to do.
    @AppStorage(FeatureFlag.machineScripts.key, store: WindowDefaults.store) private var scriptsOn = false

    var body: some View {
        if let folded = MachineSetupWords.foldedLine(setup) {
            Section("Agents") { Text(folded).foregroundStyle(.secondary) }
        } else {
            Section("Set-up") {
                ForEach(setup.steps) { step in
                    // A second's tick, so a running step's time moves. Inside the row, so the
                    // section stays a section in a List (the Remote) and a Form (the Mac).
                    TimelineView(.periodic(from: .now, by: 1)) { context in stepRow(step, now: context.date) }
                    if step.id == "signIn" { signIn() }
                }
                if let failed = MachineSetupWords.failedStep(setup) {
                    if scriptsOn, failed.id == "root" {
                        rootFailed(failed)
                    } else {
                        Text(failed.error ?? "\(MachineSetupWords.name(failed)) failed.")
                            .foregroundStyle(StateTint.failure.style(or: .primary))
                    }
                    HStack {
                        Button("Try Again", action: onRetry)
                        Button("Delete Machine…", role: .destructive) { deleting = true }
                    }
                    #if os(iOS)
                    .buttonStyle(.bordered)
                    #endif
                }
            }
            .confirmationDialog("Delete \(row.name)?", isPresented: $deleting, titleVisibility: .visible) {
                Button("Delete", role: .destructive, action: onDelete)
                Button("Cancel", role: .cancel) {}
            } message: {
                Text(MachineRowActions.deleteMessage(row, sessions: sessions))
            }
        }
    }

    /// A failed Root set-up (#7154, gate 2's *page root failed*): root.log's last lines, and that a
    /// fixed root.sh needs a new machine.
    @ViewBuilder
    private func rootFailed(_ step: DaemonAPI.MachineSetupStep) -> some View {
        Text("root.sh failed.").foregroundStyle(StateTint.failure.style(or: .primary))
        if let lines = step.detail, !lines.isEmpty {
            Text(lines)
                .appText(.fine)
                .monospaced()
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 6))
        }
        Text(MachineSetupWords.rootFailedAdvice(row.name))
            .appText(.fine)
            .foregroundStyle(.secondary)
    }

    private func stepRow(_ step: DaemonAPI.MachineSetupStep, now: Date) -> some View {
        let warned = MachineSetupWords.isWarned(step)
        return HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(icon: warned ? Self.warningSymbol : Self.symbol(step.state))
                .foregroundStyle((warned ? StateTint.warning : Self.tint(step.state)).style(or: .secondary))
                .frame(width: 18)
                .accessibilityLabel(warned ? "Done, with a warning" : Self.stateWords(step.state))
            VStack(alignment: .leading, spacing: 2) {
                Text(MachineSetupWords.name(step))
                Text(MachineSetupWords.line(step, now: now))
                    .appText(.fine)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            Spacer(minLength: 8)
            if let time = MachineSetupWords.time(step, now: now) {
                Text(time).monospacedDigit().foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
    }

    /// A step done with a warning (#7154).
    static var warningSymbol: String { "exclamationmark.triangle.fill" }

    static func symbol(_ state: DaemonAPI.MachineSetupStep.State) -> String {
        switch state {
        case .done: "checkmark.circle.fill"
        case .running: "arrow.triangle.2.circlepath"
        case .needsYou: "person.crop.circle.badge.exclamationmark"
        case .failed: "xmark.octagon.fill"
        case .waiting: "circle"
        }
    }

    static func tint(_ state: DaemonAPI.MachineSetupStep.State) -> StateTint {
        switch state {
        case .done: .vouched
        case .running: .none
        case .needsYou: .attention
        case .failed: .failure
        case .waiting: .none
        }
    }

    static func stateWords(_ state: DaemonAPI.MachineSetupStep.State) -> String {
        switch state {
        case .done: "Done"
        case .running: "Running"
        case .needsYou: "Needs you"
        case .failed: "Failed"
        case .waiting: "Not yet"
        }
    }
}

extension MachineSetupSection where SignIn == EmptyView {
    /// With nothing under step 7: the Remote signs a runtime in from its line under Runtimes.
    init(row: DaemonAPI.ControlMachine, setup: DaemonAPI.MachineSetup, sessions: Int = 0,
         onRetry: @escaping () -> Void, onDelete: @escaping () -> Void) {
        self.init(row: row, setup: setup, sessions: sessions, onRetry: onRetry, onDelete: onDelete) { EmptyView() }
    }
}
