import AgentsKitCore
import SwiftUI

/// New Session's **Suggested** section (#5459, epic #5457): the epics with no lead, from the
/// daemon's `work/suggest-leads`: at most 3 rows, "N more in Work", Hide, and Dismiss on a row.
/// A tap fills the form as Start lead does (the epic's folder, its prompt and labels) and starts nothing.
/// A first read that fails says why in one line; Hide leaves one line with Show (#5790).
struct SuggestedLeadsSection: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef
    /// Hide is kept for the person, the same in every folder, until Show on the line it leaves.
    @AppStorage("suggestedLeadsHidden", store: WindowDefaults.store) private var hidden = false
    @State private var answer: DaemonAPI.WorkSuggestLeads?
    /// When `answer` was read; the "Last update" line shows while a later read has failed (#5139).
    @State private var readAt: Date?
    @State private var stale = false
    /// Why the first read failed, while there is no answer to show (#5790).
    @State private var failed: String?
    @State private var said: String?

    private var canAsk: Bool { SuggestedLeads.canAsk(host, in: model.controlPlaneHosts) }

    var body: some View {
        // A VStack, not a Group: a Group with nothing in it is no view, so its .task never ran, and
        // the section, which draws nothing until its first read, never read (#5790).
        VStack(spacing: 0) {
            if canAsk, hidden {
                hiddenLine
            } else if canAsk, let answer, !answer.suggestions.isEmpty {
                section(answer)
            } else if canAsk, let why = answer.map(SuggestedLeads.unread) ?? failed {
                // A read that failed, or a forge it could not read and no rows: say why (#5790).
                failedLine(why)
            }
        }
        // Read again when the host's list comes in after the view opens, and when Show brings the
        // section back: every folder on this Mac has the same host, so the host alone never changes (#5790).
        .task(id: SuggestedLeadsAsk(host: host, can: canAsk, hidden: hidden)) {
            if !hidden { await load() }
        }
    }

    /// What Hide leaves: one line, and Show to bring the section back (#5790).
    private var hiddenLine: some View {
        HStack(spacing: 6) {
            Text(SuggestedLeads.hiddenLine).appText(.supporting).foregroundStyle(Paper.secondary)
            Button("Show") { hidden = false }
                .buttonStyle(.plain)
                .appText(.supporting)
                .foregroundStyle(Paper.accent)
                .help("Show Suggested on New Session")
            Spacer()
        }
        .chatColumn()
        .padding(.bottom, 12)
    }

    private func failedLine(_ why: String) -> some View {
        HStack(spacing: 6) {
            Text(SuggestedLeads.heading).appText(.supporting).fontWeight(.semibold).foregroundStyle(Paper.secondary)
            Text(why).appText(.supporting).foregroundStyle(Paper.secondary).lineLimit(1).truncationMode(.tail)
                .help(why)
            Spacer()
        }
        .chatColumn()
        .padding(.bottom, 12)
    }

    private func section(_ answer: DaemonAPI.WorkSuggestLeads) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(SuggestedLeads.heading).appText(.supporting).fontWeight(.semibold).foregroundStyle(Paper.secondary)
                Spacer()
                if stale, let readAt { LastUpdateText(read: readAt) }
                Button("Hide") { hidden = true }
                    .buttonStyle(.plain)
                    .appText(.supporting)
                    .foregroundStyle(Paper.secondary)
                    .help("Hide Suggested on New Session")
            }
            ForEach(answer.suggestions) { row in
                SuggestedLeadRow(row: row, take: { take(row) }, dismiss: { Task { await dismiss(row) } })
            }
            if model.flags.isOn(.work), let more = SuggestedLeads.more(answer.more) {
                Button(more) { model.showWork(WorkPlace(kind: .timeline)) }
                    .buttonStyle(.plain)
                    .appText(.supporting)
                    .foregroundStyle(Paper.secondary)
            }
            if let said { Text(said).appText(.supporting).foregroundStyle(Paper.secondary) }
        }
        .chatColumn()
        .padding(.bottom, 12)
    }

    private func take(_ row: DaemonAPI.WorkLeadSuggestion) {
        guard let folder = row.folder else {
            said = "No folder on this machine is on this epic's repo."
            return
        }
        let offer = AppViewStart(prompt: row.start.prompt, labels: row.start.labels, worktree: row.start.worktree)
        said = model.offerStart(offer, in: FolderKey(host: host, folder: URL(fileURLWithPath: folder)))
    }

    private func dismiss(_ row: DaemonAPI.WorkLeadSuggestion) async {
        do {
            _ = try await model.forge(DaemonAPI.Method.workDismissLead, DaemonAPI.WorkDismissLeadRequest(node: row.node),
                                      returning: DaemonAPI.ForgeDone.self, on: host)
            await load()
        } catch {
            said = "Could not dismiss: \(error.localizedDescription)"
        }
    }

    private func load() async {
        guard canAsk else { return }
        do {
            answer = try await model.forge(DaemonAPI.Method.workSuggestLeads, DaemonAPI.WorkSuggestLeadsRequest(),
                                           returning: DaemonAPI.WorkSuggestLeads.self, on: host)
            readAt = Date()
            stale = false
            failed = nil
            said = nil
        } catch {
            stale = answer != nil
            if let why = SuggestedLeads.failure(error) { failed = why }
        }
    }
}

/// What the Suggested read waits on: the host, whether it can answer now, and Hide (#5790).
private struct SuggestedLeadsAsk: Hashable {
    let host: MachineRef
    let can: Bool
    let hidden: Bool
}

private struct SuggestedLeadRow: View {
    let row: DaemonAPI.WorkLeadSuggestion
    let take: () -> Void
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Button(action: take) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(SuggestedLeads.title(row)).appText(.reading).lineLimit(1)
                    let detail = SuggestedLeads.detail(row)
                    if !detail.isEmpty {
                        Text(detail).appText(.supporting).foregroundStyle(Paper.secondary).lineLimit(1)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .help("Fill the form to start a lead for this epic. Nothing starts until you send it.")
            Button(action: dismiss) { Image(icon: "xmark").appText(.supporting) }
                .buttonStyle(.plain)
                .foregroundStyle(Paper.secondary)
                .help("Dismiss: leave this epic out until it changes")
                .accessibilityLabel("Dismiss \(SuggestedLeads.title(row))")
        }
        .padding(.vertical, 4)
    }
}
