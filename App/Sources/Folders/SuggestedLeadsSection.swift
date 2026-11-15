import AgentsKitCore
import SwiftUI

/// New Session's **Suggested** section (#5459, epic #5457): the epics with no lead, from the
/// daemon's `work/suggest-leads`: at most 3 rows, "N more in Work", Hide, and Dismiss on a row.
/// A tap fills the form as Start lead does (the epic's folder, its prompt and labels) and starts nothing.
struct SuggestedLeadsSection: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef
    /// Hide is kept for the person, until they bring the section back by clearing it in Work.
    @AppStorage("suggestedLeadsHidden", store: WindowDefaults.store) private var hidden = false
    @State private var answer: DaemonAPI.WorkSuggestLeads?
    /// When `answer` was read; the "Last update" line shows while a later read has failed (#5139).
    @State private var readAt: Date?
    @State private var stale = false
    @State private var said: String?

    private var canAsk: Bool {
        model.controlPlaneHosts?.first { $0.id == host }?.has(Capability.workSuggestLeads1) ?? false
    }

    var body: some View {
        Group {
            if !hidden, let answer, !answer.suggestions.isEmpty {
                section(answer)
            }
        }
        .task(id: host) { await load() }
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
            if let more = SuggestedLeads.more(answer.more) {
                Button(more) { model.showWork(WorkPlace(kind: .feature)) }
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
            said = nil
        } catch {
            stale = answer != nil
        }
    }
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
            Button(action: dismiss) { Image(systemName: "xmark").imageScale(.small) }
                .buttonStyle(.plain)
                .foregroundStyle(Paper.secondary)
                .help("Dismiss: leave this epic out until it changes")
                .accessibilityLabel("Dismiss \(SuggestedLeads.title(row))")
        }
        .padding(.vertical, 4)
    }
}
