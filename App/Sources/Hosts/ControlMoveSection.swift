import AgentsKitCore
import SwiftUI

/// Make This the Control Plane… on a machine's page (#6614, gate 2 of epic #6608), for an admin
/// with the flag `control-plane-move`, on a machine that does not run the control plane now. It
/// reads the machine's checks (`control/eligible`) when the page opens: with a reason, the button
/// is off and the reasons are under it, in the control plane's words. During a move to this
/// machine and for a day after it, the move's steps; a failed one says which step, and Try Again….
/// The web page draws the same in `Web/src/views/ControlMove.tsx`.
struct ControlMoveSection: View {
    @Environment(AppModel.self) private var model
    /// The page's machine.
    let host: MachineRef
    /// Its record on the control plane.
    let record: DaemonAPI.ControlMachine

    @State private var asking = false

    private var checks: ControlMoveState.Checks? { model.controlMove.checks[host] }

    var body: some View {
        Section {
            if let move = model.controlMove.move(to: record.id) {
                VStack(alignment: .leading, spacing: 10) {
                    ControlMoveSteps(move: move)
                    if !ControlMoveWords.running(move) {
                        Text(ControlMoveWords.line(move))
                            .foregroundStyle(move.phase == .failed ? StateTint.failure.style(or: .secondary)
                                             : StateTint.vouched.style(or: .secondary))
                        if let until = ControlMoveWords.forwarding(move) {
                            Text(until).appText(.supporting).foregroundStyle(.secondary)
                        }
                    }
                    if move.phase == .failed {
                        Button(ControlMoveWords.tryAgain) { asking = true }
                            .disabled(model.controlMove.running != nil)
                            .help("Check \(record.name) again, then move the control plane to it")
                    }
                }
                .padding(.vertical, 4)
            } else {
                checksView
            }
        } header: {
            HStack {
                Text(ControlMoveWords.section)
                Spacer()
                if checks?.checking == true {
                    ProgressView().controlSize(.small).accessibilityLabel("Checking")
                }
                // During a move, when the window last heard it; else when the checks were read.
                let move = model.controlMove.move(to: record.id)
                if let read = move != nil ? model.controlMove.heardAt : checks?.readAt { LastUpdateText(read: read) }
            }
        }
        .task(id: host) { await check() }
        .sheet(isPresented: $asking) {
            ControlMoveSheet(host: host, record: record)
        }
    }

    @ViewBuilder private var checksView: some View {
        let answer = checks?.answer
        let another = model.controlMove.running
        let ok = answer?.ok == true && another == nil
        VStack(alignment: .leading, spacing: 8) {
            Button(ControlMoveWords.make) { asking = true }
                .disabled(!ok)
                .help(ok ? "Move the control plane, its store and its key, to \(record.name)"
                         : "\(record.name) can’t take the control plane now")
            if let failure = checks?.failure {
                Text(failure).appText(.supporting).foregroundStyle(StateTint.warning.style(or: .secondary))
            }
            if another != nil {
                Text(ControlMoveWords.anotherRuns).appText(.supporting).foregroundStyle(.secondary)
            } else if let answer, answer.ok {
                Text(ControlMoveWords.meets(answer)).appText(.supporting).foregroundStyle(.secondary)
            } else if let answer {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(answer.reasons, id: \.self) { reason in
                        Label(reason.text, icon: "xmark.circle")
                            .foregroundStyle(.secondary)
                    }
                }
                .appText(.supporting)
            } else if checks?.failure == nil {
                Text(ControlMoveWords.checking).appText(.supporting).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 4)
    }

    private func check() async {
        guard let control = model.controlRequests else { return }
        await model.controlMove.read(control)
        await model.controlMove.check(control, page: host, machine: record.id)
    }
}

/// The steps of a move, in the order it runs them, each done, under way, failed or still to do.
struct ControlMoveSteps: View {
    let move: DaemonAPI.ControlMove

    var body: some View {
        let states = ControlMoveWords.states(move)
        HStack(spacing: 6) {
            ForEach(Array(ControlMoveWords.steps.enumerated()), id: \.offset) { index, step in
                let state = states[index]
                HStack(spacing: 4) {
                    if state == .current { ProgressView().controlSize(.mini) }
                    Text(step.title)
                }
                .appText(.fine)
                .foregroundStyle(state == .todo ? AnyShapeStyle(.tertiary) : AnyShapeStyle(.primary))
                .frame(maxWidth: .infinity)
                .padding(.vertical, 3)
                .background(Self.fill(state), in: RoundedRectangle(cornerRadius: 5))
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(step.title): \(Self.spoken(state))")
            }
        }
    }

    static func fill(_ state: ControlMoveWords.StepState) -> AnyShapeStyle {
        switch state {
        case .done: AnyShapeStyle(StateTint.vouched.style(or: .secondary).opacity(0.25))
        case .current: AnyShapeStyle(Color.accentColor.opacity(0.25))
        case .failed: AnyShapeStyle(StateTint.failure.style(or: .secondary).opacity(0.25))
        case .todo: AnyShapeStyle(.quaternary)
        }
    }

    static func spoken(_ state: ControlMoveWords.StepState) -> String {
        switch state {
        case .done: "done"
        case .current: "under way"
        case .failed: "failed"
        case .todo: "to do"
        }
    }
}

/// The confirmation (gate 2): what happens, then Cancel and Make Control Plane. A refusal stays in
/// the sheet; once the move starts, the strip says its steps.
struct ControlMoveSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let host: MachineRef
    let record: DaemonAPI.ControlMachine

    @State private var starting = false
    @State private var refused: String?

    /// Where clients reach the control plane now.
    private var now: String? {
        guard case .remote(let membership) = ControlConfig.endpoint,
              let url = membership.url.flatMap(URL.init(string:)), let address = url.host() else { return nil }
        return address
    }

    var body: some View {
        let address = model.controlMove.checks[host]?.answer?.address
        VStack(alignment: .leading, spacing: 12) {
            Text(ControlMoveWords.question(record.name)).appText(.reading).fontWeight(.semibold)
            VStack(alignment: .leading, spacing: 6) {
                ForEach(ControlMoveWords.happens(name: record.name, address: address, now: now), id: \.self) { line in
                    Label(line, icon: "circle.fill").labelStyle(BulletLabelStyle())
                }
            }
            .foregroundStyle(.secondary)
            if let refused {
                Text(refused).foregroundStyle(StateTint.failure.style(or: .secondary))
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(ControlMoveWords.confirm) { Task { await start() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(starting)
            }
        }
        .padding(20)
        .frame(width: 460)
    }

    private func start() async {
        guard let control = model.controlRequests else {
            refused = "The control plane can’t be reached."
            return
        }
        starting = true
        defer { starting = false }
        if let why = await model.controlMove.start(control, machine: record.id) {
            refused = why
        } else {
            dismiss()
        }
    }
}

/// A small dot before each line of the confirmation.
private struct BulletLabelStyle: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text("\u{2022}").accessibilityHidden(true)
            configuration.title
        }
    }
}

/// Across the top of the window while the control plane moves, and for a day after (#6614): the
/// step under way with "Last update", or which step failed and that the old one still serves, or
/// where it is now and until when the old address forwards. Show Machine opens the machine's page.
struct ControlMoveStrip: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if model.showsControlPlaneMove, let move = model.controlMove.strip {
            let running = ControlMoveWords.running(move)
            Strip(tone: move.phase == .failed ? .warning : .neutral,
                  busy: running,
                  line: ControlMoveWords.line(move),
                  detail: ControlMoveWords.forwarding(move),
                  primary: StripButton(title: "Show Machine", help: "Its page, with the move's steps") {
                      // This Mac's page is `.mac`, not its record's id.
                      model.machinePage = move.to == ThisMacHost.resolve(model.controlPlaneHosts) ? .mac : move.to
                  },
                  second: running ? nil : StripButton(title: "Close", help: "Hide this until the next move") {
                      model.controlMove.close(move)
                  }) {
                if running, let heard = model.controlMove.heardAt { LastUpdateText(read: heard) }
            }
            .paneBannerInset()
        }
    }
}
