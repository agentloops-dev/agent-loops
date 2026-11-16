import AgentsKitCore
import SwiftUI

/// New Session's first two choices (#3279): the **project**, then the machine it is **on**,
/// as the Mac's page and the web page have them. A project with one folder hides On, so it
/// looks as a folder does today. Adding a folder on another machine is Mac + web (#2798).
struct ProjectRows: View {
    @Environment(RemoteModel.self) private var model
    let folder: FolderKey

    var body: some View {
        let projects = model.newSessionProjects
        if let summary = model.work.folder(folder), let current = NewSessionProject.choice(of: summary.key, in: projects) {
            Section {
                projectRow(projects, current: current, summary: summary)
                if !current.hidesMachine {
                    machineRow(current, summary: summary)
                }
            }
            .paperListRow()
        }
    }

    private func projectRow(_ projects: [NewSessionProject.Choice], current: NewSessionProject.Choice,
                            summary: DaemonAPI.FolderSummary) -> some View {
        Menu {
            ForEach(projects) { project in
                Button {
                    model.showProject(project)
                } label: {
                    choiceLabel(label(project), chosen: project.id == current.id)
                }
            }
        } label: {
            LabeledContent("Project") {
                Text(label(current)).foregroundStyle(Paper.secondary)
            }
            .appText(.reading)
        }
        .accessibilityLabel("Project, \(label(current))")
    }

    private func machineRow(_ project: NewSessionProject.Choice, summary: DaemonAPI.FolderSummary) -> some View {
        let pick = model.startAutoMachine
        let onAuto = model.startsOnAutoMachine(summary.key)
        let shown = onAuto ? AutoMachine.rowWords(pick) : model.hostLabel(summary.host)
        let withFolder = Set(project.folders.map(\.host))
        // The one machine picker (#5622, the Mac's #5621): Auto first, chosen at every start (#5375),
        // greyed with the control plane's words when no machine fits; then the machines with a folder
        // of the project.
        return RemoteMachinePickerRow(
            label: "On", value: onAuto ? nil : summary.host, shown: shown,
            keep: { withFolder.contains($0.id) },
            any: AutoMachine.offered(pick)
                ? .init(title: AutoMachine.rowWords(pick), disabled: !AutoMachine.usable(pick), help: AutoMachine.offWords(pick))
                : nil
        ) { picked in
            guard let picked else {
                model.startNamedMachine = nil
                return
            }
            // A machine named by hand starts there and is not remembered (#5375); the folder here
            // first when the project has two on that machine.
            guard let key = project.folders.first(where: { $0 == summary.key && $0.host == picked })
                ?? project.folders.first(where: { $0.host == picked }) else { return }
            model.startNamedMachine = key
            model.sidebarItem = .folder(key)
        }
        .accessibilityHint(AutoMachine.offWords(pick) ?? "")
        // Asked again when the project, the machine or the runtime moves (#5375).
        .task(id: "\(summary.key.stored)|\(summary.project ?? "")|\(model.startAutoMachineRuntime ?? "")|"
              + project.folders.map { model.hostIsOffline($0.host) ? "0" : "1" }.joined()) {
            await model.refreshStartAutoMachine(summary.key)
        }
    }

    /// A project with one folder is named as its folder is today: `server:Name` when the
    /// folder is on a server. One with folders on several machines is named alone.
    private func label(_ project: NewSessionProject.Choice) -> String {
        guard project.hidesMachine, let folder = model.work.folder(project.folders.first) else { return project.name }
        return SidebarOrder.label(folder) { model.hostLabel($0) }
    }

    @ViewBuilder
    private func choiceLabel(_ title: String, chosen: Bool) -> some View {
        if chosen { Label(title, icon: "checkmark") } else { Text(title) }
    }
}
