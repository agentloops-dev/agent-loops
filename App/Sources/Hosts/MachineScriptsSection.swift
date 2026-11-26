import AgentsKitCore
import SwiftUI

/// "Your scripts" on a machine's page (#7155, gate 2 of #7151, boards *page done*, *page changed*
/// and *page sender offline*): what the machine ran of the person's `setup.sh`, against the file
/// on this Mac, the machine that sends it, with Send Set-up… and Run Again. Under the steps, as
/// those fold into one line after 24 hours and a changed script must still show. Read with
/// `setup/scripts` when the page opens, so it says how old that read is. Behind the flag
/// machine-setup; the Mac only (web: #7160; the Remote does not have it).
struct MachineScriptsSection: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef
    let row: DaemonAPI.ControlMachine

    @State private var scripts: DaemonAPI.SetupScripts?
    @State private var readAt: Date?
    @State private var failure: String?
    @State private var sending: DaemonAPI.SetupScript?

    private var sender: String { model.hosts.label(.mac) }
    private var step: DaemonAPI.MachineSetupStep? { MachineScriptWords.step(row.setup) }
    private var rootStep: DaemonAPI.MachineSetupStep? { row.setup?.steps.first { $0.id == "root" } }

    var body: some View {
        Section {
            if let failure, scripts == nil {
                Text(failure).foregroundStyle(.secondary)
            } else if scripts == nil {
                ProgressView()
            } else {
                if let rootStep, rootStep.state == .done {
                    rowView(name: "root.sh", note: nil, line: "Ran \(Self.when(rootStep)), as root.") { EmptyView() }
                }
                setupRow
            }
        } header: {
            HStack {
                Text("Your scripts")
                Spacer()
                if let readAt {
                    LastUpdateTimeline(read: readAt) { now in
                        Text(LastUpdate.line(since: readAt, now: now)).appText(.fine).foregroundStyle(.secondary)
                    }
                }
            }
        }
        .task(id: host) { await read() }
        .onChange(of: step?.state) { Task { await read() } }
        .sheet(item: $sending) { script in
            SendSetupSheet(host: host, machine: row.name, sender: sender, script: script) {
                sending = nil
                Task { await read() }
            }
            .paperSheet()
        }
    }

    @ViewBuilder
    private var setupRow: some View {
        let script = scripts?.script("setup")
        let pending = model.setupSends.pending[host]
        let standing = MachineScriptWords.standing(step: step, script: script)
        if let pending {
            rowView(name: "setup.sh", note: pending.note, noteTint: .attention,
                    line: "Sends when \(pending.waitsFor) is back. \(row.name) keeps the one it has.") {
                Button("Cancel Send") { model.setupSends.cancel(host) }
            }
        } else {
            rowView(name: "setup.sh", note: MachineScriptWords.note(standing, sender: sender), noteTint: .warning,
                    line: MachineScriptWords.line(standing, step: step, machine: row.name, sender: sender)) {
                if let script, script.present, script.text != nil {
                    Button("Send Set-up…") { sending = script }
                        .modifier(MainButton(isMain: MachineScriptWords.sendIsMain(standing)))
                        .help("Send \(sender)’s setup.sh to \(row.name), and run it there")
                }
                if MachineScriptWords.canRunAgain(standing) {
                    Button("Run Again") { Task { await model.runSetupAgain(host) } }
                        .help("Run the setup.sh that \(row.name) has again. It must be safe to run again.")
                }
            }
        }
    }

    private func rowView(name: String, note: String?, noteTint: StateTint = .warning, line: String,
                         @ViewBuilder buttons: () -> some View) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 0) {
                    Text(name)
                    if let note { Text(" · \(note)").foregroundStyle(noteTint.style(or: .secondary)) }
                }
                Text(line).appText(.fine).foregroundStyle(.secondary).lineLimit(2)
            }
            Spacer(minLength: 8)
            buttons()
        }
    }

    static func when(_ step: DaemonAPI.MachineSetupStep) -> String {
        guard let at = step.endedAt ?? step.startedAt else { return "" }
        return at.formatted(date: .abbreviated, time: .shortened)
    }

    private func read() async {
        do {
            scripts = try await model.readSetupScripts()
            readAt = Date()
            failure = nil
        } catch let error as JSONRPCError {
            failure = error.message
        } catch {
            failure = "\(sender)’s scripts can’t be read now: \(error.localizedDescription)"
        }
    }
}

/// The main button of a row, drawn as the one to press.
private struct MainButton: ViewModifier {
    let isMain: Bool
    func body(content: Content) -> some View {
        if isMain { content.buttonStyle(.borderedProminent) } else { content }
    }
}

/// Send Set-up… (#7155, gate 2 board *send setup*): the change since the text approved last, and
/// Approve and Send, which signs an approval of this text for this machine only with this Mac's
/// lock key and sends it.
struct SendSetupSheet: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef
    let machine: String
    let sender: String
    let script: DaemonAPI.SetupScript
    let done: () -> Void

    @State private var working = false
    @State private var problem: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Send setup.sh to \(machine)?").appText(.reading)
            LabeledContent("From", value: sender)
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    let lines = MachineScriptWords.diff(script.changed ? script.approvedText : nil, script.text ?? "")
                    ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                        Text("\(String(line.mark)) \(line.text)")
                            .font(.system(.caption, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .foregroundStyle(line.mark == " " ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary))
                            .background(Self.tint(line.mark))
                    }
                }
                .textSelection(.enabled)
                .padding(8)
            }
            .frame(minHeight: 120, maxHeight: 320)
            .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 6))
            Text("It runs on \(machine) now, as your user, and is approved for \(machine) only. Scripts are not secret: do not put a token in one.")
                .appText(.fine).foregroundStyle(.secondary)
            if let problem {
                Text(problem).foregroundStyle(StateTint.failure.style(or: .primary))
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel, action: done).keyboardShortcut(.cancelAction)
                Button("Approve and Send") {
                    working = true
                    Task {
                        problem = await model.sendSetup(host, script: script)
                        working = false
                        if problem == nil { done() }
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(working)
            }
        }
        .padding(20)
        .frame(width: 560)
    }

    static func tint(_ mark: Character) -> Color {
        switch mark {
        case "+": StateTint.vouched.color?.opacity(0.15) ?? .clear
        case "-": StateTint.failure.color?.opacity(0.15) ?? .clear
        default: .clear
        }
    }
}
