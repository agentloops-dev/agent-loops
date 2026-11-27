import AgentsKitCore
import AppKit
import SwiftUI
import UniformTypeIdentifiers

/// Settings ▸ Machines ▸ Machine scripts (#7512, gate 2 of #7151, boards *files two*, *files none*
/// and *files offline*): the person's `root.sh` and `setup.sh` in `~/.agents/machine-setup/` on
/// this Mac, the machine that sends them, with Open and Reveal in Finder, and Review… for one
/// that is new or changed since it was approved for new machines. There is no editor: the files
/// are the person's, and their dotfiles can keep them. New root.sh and New setup.sh write an
/// example where there is none. Read with `setup/scripts`, the same read as New Machine and a
/// machine's page, when the page opens and when the app comes to the front again. Behind the
/// flag `machine-scripts`; the Mac only (web: #7160; the Remote does not have it).
struct MachineScriptsSettingsView: View {
    @Environment(AppModel.self) private var model

    @State private var scripts: DaemonAPI.SetupScripts?
    @State private var readAt: Date?
    @State private var failure: String?
    @State private var problem: String?
    @State private var reviewing: Review?

    struct Review: Identifiable {
        let id = UUID()
        var scripts: [DaemonAPI.SetupScript]
    }

    private var sender: String { model.hosts.label(.mac) }

    var body: some View {
        Form {
            Section {
                content
                if let failure, scripts != nil {
                    Text(failure).foregroundStyle(.secondary)
                }
                if let problem {
                    Text(problem).foregroundStyle(StateTint.failure.style(or: .primary))
                }
            } header: {
                VStack(alignment: .leading, spacing: 12) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Machine scripts").appText(.reading)
                        Text(MachineScriptWords.filesLead).appText(.supporting).fontWeight(.regular).foregroundStyle(.secondary)
                    }
                    HStack {
                        Text("On \(sender)")
                        Spacer()
                        if let readAt { LastUpdateText(read: readAt) }
                    }
                }
            } footer: {
                Text(MachineScriptWords.filesClear).appText(.fine).foregroundStyle(.secondary)
            }
            .paperListRow()
        }
        .paperForm()
        .task { await read() }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            // The person edits the files in their own editor: read them again on the way back.
            Task { await read() }
        }
        .sheet(item: $reviewing) { review in
            SetupScriptReviewSheet(scripts: review.scripts, sender: sender, action: "Approve",
                                   approve: { await model.approveSetupScripts($0) }) {
                reviewing = nil
                Task { await read() }
            }
            .paperSheet()
        }
    }

    @ViewBuilder
    private var content: some View {
        if let scripts {
            let present = scripts.scripts.filter(\.present)
            if present.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    Text(scripts.folder == nil ? "\(sender) has no home folder for scripts." : MachineScriptWords.filesNone)
                    HStack {
                        newButton("root", folder: scripts.folder)
                        newButton("setup", folder: scripts.folder)
                        Spacer()
                        Link("An example with gh", destination: MachineScriptWords.exampleGuide)
                    }
                }
            } else {
                ForEach(scripts.scripts) { script in row(script, folder: scripts.folder) }
            }
        } else if let failure {
            Text(failure).foregroundStyle(.secondary)
        } else {
            ProgressView().controlSize(.small)
        }
    }

    private func row(_ script: DaemonAPI.SetupScript, folder: String?) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 0) {
                    Text(script.file)
                    if let note = MachineScriptWords.filesNote(script) {
                        Text(" · \(note)").foregroundStyle(StateTint.warning.style(or: .secondary))
                    }
                }
                Text(MachineScriptWords.filesLine(script)).appText(.fine)
                    .foregroundStyle(script.problem == nil ? AnyShapeStyle(.secondary) : StateTint.warning.style(or: .secondary))
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 8)
            if !script.present {
                newButton(script.name, folder: folder)
            } else if let folder {
                if script.needsApprovalForMade {
                    Button("Review…") { reviewing = Review(scripts: [script]) }
                        .buttonStyle(.paperProminent)
                        .help("See what changed in \(script.file), and approve it for new machines")
                }
                let file = URL(fileURLWithPath: folder).appendingPathComponent(script.file)
                Button("Open") { Self.open(file) }
                    .help("Open \(script.file) in your editor")
                Button("Reveal in Finder") { NSWorkspace.shared.activateFileViewerSelecting([file]) }
            }
        }
    }

    @ViewBuilder
    private func newButton(_ name: String, folder: String?) -> some View {
        if let folder {
            Button("New \(name).sh") {
                problem = AppModel.writeExampleScript(name, folder: folder)
                if problem == nil { Self.open(URL(fileURLWithPath: folder).appendingPathComponent(name + ".sh")) }
                Task { await read() }
            }
            .help("Write an example \(name).sh in \(folder), and open it")
        }
    }

    /// Opens a script in the app that opens it, but never in a terminal, which would run it:
    /// then in the editor for plain text.
    static func open(_ file: URL) {
        let workspace = NSWorkspace.shared
        let terminals: Set<String> = ["com.apple.Terminal", "com.googlecode.iterm2", "dev.warp.Warp-Stable", "com.mitchellh.ghostty"]
        var app = workspace.urlForApplication(toOpen: file)
        if let found = app, let id = Bundle(url: found)?.bundleIdentifier, terminals.contains(id) { app = nil }
        guard let editor = app ?? workspace.urlForApplication(toOpen: .plainText) else { return }
        workspace.open([file], withApplicationAt: editor, configuration: NSWorkspace.OpenConfiguration())
    }

    private func read() async {
        do {
            scripts = try await model.readSetupScripts()
            readAt = Date()
            failure = nil
        } catch let error as JSONRPCError {
            failure = error.message
        } catch {
            failure = "\(sender) is offline, so its scripts can’t be read now."
        }
    }
}
