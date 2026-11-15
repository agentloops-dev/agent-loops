import AgentsKitCore
import Foundation

/// New Session's Auto machine (#5375, specs/5373-auto-machine): the control plane picks the
/// machine and makes the words; the window draws them and starts where it says.
extension AppModel {
    /// The draft's folder, as the sidebar keys it.
    var draftFolderKey: FolderKey? {
        draftCwd.map { FolderKey(host: draftHost, folder: $0) }
    }

    /// The project the draft is in, by the control plane's id; nil for a folder that names none.
    var draftProjectID: String? {
        guard let key = draftFolderKey,
              let project = liveFolders.first(where: { $0.key == key })?.project, !project.isEmpty else { return nil }
        return project
    }

    /// The runtime Auto's machine is picked for: the one Auto (runtime) would use, or the one named.
    var draftAutoMachineRuntime: String? {
        isAutoDraft ? draftAutoPreview?.runtimeID : draftRuntimeID
    }

    /// Whether the draft starts on the machine Auto picks.
    var draftOnAutoMachine: Bool {
        guard let key = draftFolderKey else { return false }
        return AutoMachine.chosen(key, named: draftNamedMachine, pick: draftAutoMachine)
    }

    /// Whether two ids name one machine: this Mac is `mac` until the control plane lists it.
    func sameMachine(_ a: MachineRef, _ b: MachineRef) -> Bool {
        if a == b { return true }
        let mine: (MachineRef) -> Bool = { $0 == .mac || $0 == self.thisMacHostID }
        return mine(a) && mine(b)
    }

    /// What Auto would pick for `project` and `runtimeID`; nil when the control plane does
    /// not say (none, or one older than auto-machine/1).
    func previewAutoMachine(project: String, runtimeID: String) async -> DaemonAPI.AutoMachinePick? {
        guard let control = controlRequests else { return nil }
        return try? await control.call(DaemonAPI.Method.sessionsPreviewStart,
                                       DaemonAPI.AutoMachineRequest(project: project, runtimeID: runtimeID),
                                       returning: DaemonAPI.AutoMachinePick.self)
    }

    /// Asked again when the draft's project, machine or runtime moves.
    func refreshDraftAutoMachine() async {
        guard let project = draftProjectID, let runtime = draftAutoMachineRuntime else {
            draftAutoMachine = nil
            return
        }
        let pick = await previewAutoMachine(project: project, runtimeID: runtime)
        guard project == draftProjectID, runtime == draftAutoMachineRuntime else { return }
        draftAutoMachine = pick
    }

    /// Where a start goes when Auto is chosen: asked once more, at start, as the pick may
    /// have moved since the preview. Nil starts where the form is.
    func draftAutoMachineTarget() async -> AutoMachine.Target? {
        guard draftOnAutoMachine, let key = draftFolderKey, let project = draftProjectID,
              let runtime = draftAutoMachineRuntime,
              let choice = NewSessionProject.choice(of: key, in: newSessionProjects) else { return nil }
        let pick = await previewAutoMachine(project: project, runtimeID: runtime)
        return AutoMachine.target(from: key, project: choice.folders, pick: pick, same: sameMachine)
    }
}
