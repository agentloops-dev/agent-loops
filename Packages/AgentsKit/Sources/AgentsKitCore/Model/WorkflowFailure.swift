import Foundation

extension DaemonAPI.FolderSummary {
    /// Whether a workflow here has stopped working (#989): the folder's row says "Needs
    /// you" for it, as for a session waiting.
    public var hasFailedWorkflow: Bool {
        approvals?.contains { $0.kind == .workflowFailed } == true
    }
}
