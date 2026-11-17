import AgentsKitCore
import Foundation

/// New Session's Auto machine (#5375, specs/5373-auto-machine): the control plane picks the
/// machine and makes the words; the Remote draws them and starts where it says.
extension RemoteModel {
    /// The project `folder` is in, by the control plane's id; nil for a folder that names none.
    func autoMachineProject(_ folder: FolderKey) -> String? {
        guard let project = work.folder(folder)?.project, !project.isEmpty else { return nil }
        return project
    }

    /// The runtime Auto's machine is picked for: the one Auto (runtime) would use, or the one named.
    var startAutoMachineRuntime: String? {
        isAutoStart ? startAutoPreview?.runtimeID : startRuntimeID
    }

    /// Whether the page on `folder` starts on the machine Auto picks.
    func startsOnAutoMachine(_ folder: FolderKey) -> Bool {
        AutoMachine.chosen(folder, named: startNamedMachine, pick: startAutoMachine)
    }

    /// What Auto would pick for `project` and `runtimeID`; nil when the control plane does
    /// not say (none, or one older than auto-machine/1).
    func previewAutoMachine(project: String, runtimeID: String) async -> DaemonAPI.AutoMachinePick? {
        guard let control = controlPlane else { return nil }
        return try? await control.call(DaemonAPI.Method.sessionsPreviewStart,
                                       DaemonAPI.AutoMachineRequest(project: project, runtimeID: runtimeID),
                                       returning: DaemonAPI.AutoMachinePick.self)
    }

    /// Asked again when the page's project, machine or runtime moves.
    func refreshStartAutoMachine(_ folder: FolderKey) async {
        guard let project = autoMachineProject(folder), let runtime = startAutoMachineRuntime else {
            startAutoMachine = nil
            return
        }
        let pick = await previewAutoMachine(project: project, runtimeID: runtime)
        guard runtime == startAutoMachineRuntime, startingIn == nil || startingIn == folder else { return }
        startAutoMachine = pick
    }

    /// Where a start goes when Auto is chosen: asked once more, at start, as the pick may
    /// have moved since the preview. Nil starts where the page is.
    func startAutoMachineTarget(from folder: FolderKey) async -> AutoMachine.Target? {
        guard startsOnAutoMachine(folder), let project = autoMachineProject(folder),
              let runtime = startAutoMachineRuntime,
              let choice = NewSessionProject.choice(of: folder, in: newSessionProjects) else { return nil }
        let pick = await previewAutoMachine(project: project, runtimeID: runtime)
        return AutoMachine.target(from: folder, project: choice.folders, pick: pick)
    }
}
