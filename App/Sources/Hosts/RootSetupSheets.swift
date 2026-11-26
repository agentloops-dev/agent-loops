import AgentsKitCore
import AppKit
import SwiftUI

/// Root set-up by hand (#7159, epic #7151; gate 2's *page root failed* and *own server*), behind
/// the `machine-scripts` flag. Where nothing runs root.sh for the person (their own server, or a
/// made machine whose root.sh failed), this Mac gives the line to run as root there: its own
/// approved root.sh, in base64. Check then reads the result on that machine again. Show Log reads
/// root.log from it. The web page is port epic #7160; the Remote does not have it (Mac + web).
enum RootSheet: Identifiable {
    /// The command, for a machine whose Root set-up failed (Check is Try Again) or that no
    /// provider made (Check reads the result alone).
    case command(failed: Bool)
    case log

    var id: String {
        switch self {
        case .command(let failed): "command-\(failed)"
        case .log: "log"
        }
    }
}

/// *Set up ‹machine› as root*: the command, Copy Command, Check and Done.
struct RootCommandSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let host: MachineRef
    let name: String
    let failed: Bool

    @State private var command: DaemonAPI.SetupRootCommand?
    @State private var problem: String?
    @State private var copied = false
    @State private var checking = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Set up \(name) as root").appText(.reading)
            Text(MachineSetupWords.rootCommandLead(name, failed: failed))
            if let command {
                Text(command.command)
                    .appText(.fine)
                    .monospaced()
                    .lineLimit(3)
                    .truncationMode(.middle)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(8)
                    .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 6))
                Text(MachineSetupWords.rootCommandAfter).appText(.fine).foregroundStyle(.secondary)
            } else if let problem {
                Text(problem).foregroundStyle(StateTint.failure.style(or: .primary))
            } else {
                ProgressView().controlSize(.small)
            }
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Image(icon: MachineSetupSection<EmptyView>.warningSymbol).accessibilityHidden(true)
                Text(MachineSetupWords.rootSudoWarning(name))
            }
            .appText(.fine)
            .foregroundStyle(StateTint.warning.style(or: .secondary))
            HStack {
                Spacer()
                Button(copied ? "Copied" : "Copy Command", action: copy)
                    .disabled(command == nil)
                Button("Check") { Task { await check() } }
                    .disabled(command == nil || checking)
                Button("Done") { dismiss() }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 520)
        .task { await read() }
    }

    /// This Mac's approved root.sh, as the line (`setup/rootCommand`).
    private func read() async {
        do {
            command = try await model.client(for: .mac).call(DaemonAPI.Method.setupRootCommand, DaemonAPI.Empty(),
                                                             returning: DaemonAPI.SetupRootCommand.self)
        } catch let error as JSONRPCError {
            problem = error.code == DaemonAPI.Failure.setupScriptChanged
                ? "root.sh changed since you approved it. Approve it when you make a new machine, then copy the command again."
                : error.message
        } catch {
            problem = "This Mac can’t give the command now: \(error.localizedDescription)"
        }
    }

    private func copy() {
        guard let command else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(command.command, forType: .string)
        copied = true
    }

    /// Check: Try Again on a failed Root set-up (the control plane asks the machine), else the
    /// machine reads the result alone.
    private func check() async {
        checking = true
        defer { checking = false }
        if failed {
            await model.retrySetup(host)
            dismiss()
            return
        }
        do {
            _ = try await model.client(for: host).call(DaemonAPI.Method.setupRootAgain, DaemonAPI.SetupRootAgain(rest: false),
                                                       returning: DaemonAPI.Empty.self)
            dismiss()
        } catch let error as JSONRPCError {
            problem = error.message
            command = nil
        } catch {
            problem = "\(name) can’t be asked now: \(error.localizedDescription)"
            command = nil
        }
    }
}

/// Show Log: root.log from the machine root.sh ran on (`setup/rootLog`).
struct RootLogSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let host: MachineRef
    let name: String

    @State private var log: DaemonAPI.SetupRootLog?
    @State private var problem: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("root.log on \(name)").appText(.reading)
            if let log {
                if log.cut { Text("Its last 64 KB.").appText(.fine).foregroundStyle(.secondary) }
                ScrollView {
                    Text(log.text.isEmpty ? "root.sh has not written to its log on \(name)." : log.text)
                        .appText(.fine)
                        .monospaced()
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(8)
                }
                .defaultScrollAnchor(.bottom)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 6))
            } else if let problem {
                Text(problem).foregroundStyle(StateTint.failure.style(or: .primary))
            } else {
                ProgressView().controlSize(.small)
            }
            HStack {
                Spacer()
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 640, height: 420)
        .task {
            do {
                log = try await model.client(for: host).call(DaemonAPI.Method.setupRootLog, DaemonAPI.Empty(),
                                                             returning: DaemonAPI.SetupRootLog.self)
            } catch let error as JSONRPCError {
                problem = error.message
            } catch {
                problem = "\(name) can’t be asked now: \(error.localizedDescription)"
            }
        }
    }
}

/// On the page of a Linux machine that no provider made (#7159): root.sh does not run there by
/// itself, so Set Up as Root… gives the command.
struct OwnServerRootSection: View {
    let name: String
    let onOpen: () -> Void

    var body: some View {
        Section("Root set-up") {
            Text("No provider makes \(name), so your root.sh does not run there by itself.")
                .foregroundStyle(.secondary)
            Button("Set Up as Root…", action: onOpen)
        }
    }
}
