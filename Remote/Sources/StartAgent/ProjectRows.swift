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
        return Menu {
            // Auto first, chosen at every start (#5375); greyed, with the control plane's
            // words, when no machine fits.
            if AutoMachine.offered(pick) {
                Button {
                    model.startNamedMachine = nil
                } label: {
                    choiceLabel(AutoMachine.rowWords(pick), chosen: onAuto)
                    if let off = AutoMachine.offWords(pick) { Text(off) }
                }
                .disabled(!AutoMachine.usable(pick))
                Divider()
            }
            ForEach(project.folders, id: \.self) { key in
                Button {
                    // A machine named by hand starts there and is not remembered (#5375).
                    model.startNamedMachine = key
                    model.sidebarItem = .folder(key)
                } label: {
                    choiceLabel(machine(key, in: project), chosen: key == summary.key && !onAuto)
                    if let state = state(key.host) { Text(state) }
                }
            }
        } label: {
            LabeledContent("On") {
                Text(shown).foregroundStyle(Paper.secondary)
            }
            .appText(.reading)
        }
        .accessibilityLabel("On, \(shown)")
        .accessibilityHint(AutoMachine.offWords(pick) ?? "")
        // Asked again when the project, the machine or the runtime moves (#5375).
        .task(id: "\(summary.key.stored)|\(summary.project ?? "")|\(model.startAutoMachineRuntime ?? "")") {
            await model.refreshStartAutoMachine(summary.key)
        }
    }

    /// A project with one folder is named as its folder is today: `server:Name` when the
    /// folder is on a server. One with folders on several machines is named alone.
    private func label(_ project: NewSessionProject.Choice) -> String {
        guard project.hidesMachine, let folder = model.work.folder(project.folders.first) else { return project.name }
        return SidebarOrder.label(folder) { model.hostLabel($0) }
    }

    /// The machine's name, with the path when the project has two folders on it.
    private func machine(_ key: FolderKey, in project: NewSessionProject.Choice) -> String {
        let name = model.hostLabel(key.host)
        guard project.folders.filter({ $0.host == key.host }).count > 1 else { return name }
        return "\(name) · \(key.folder.path(percentEncoded: false))"
    }

    /// `Stopped, starts when used` or `Offline`; nothing for a machine that answers.
    private func state(_ host: MachineRef) -> String? {
        if model.startsWhenUsed(host) { return "Stopped, starts when used" }
        if model.hostIsOffline(host) { return "Offline" }
        return nil
    }

    @ViewBuilder
    private func choiceLabel(_ title: String, chosen: Bool) -> some View {
        if chosen { Label(title, icon: "checkmark") } else { Text(title) }
    }
}
