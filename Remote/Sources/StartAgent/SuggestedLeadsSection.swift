import AgentsKitCore
import SwiftUI

/// New Session's **Suggested** section (#5460, epic #5457): at most 3 open epics with no lead,
/// as the daemon ranks them, each a row that fills the form as Start lead does. It starts
/// nothing: the person still presses Start. A row swipes to Dismiss; Hide folds the section to
/// its heading, on this device, until Show. "N more in Work" opens Work ▸ Timeline.
///
/// Placed under the project and the persona, above kept starts (#5460): a tap there fills the
/// prompt card below, which is in view with the keyboard down or up. Not drawn when every epic
/// has a lead, or when the folder's host cannot say; a first read that fails says why in one line.
struct SuggestedLeadsSection: View {
    @Environment(RemoteModel.self) private var model
    let key: FolderKey
    /// Fill the form with the row's Start lead.
    let pick: (DaemonAPI.WorkLeadSuggestion) -> Void
    /// Folded to its heading (Hide), the same in every folder on this device.
    @AppStorage("startSuggestedHidden") private var hidden = false
    @State private var note: String?

    var body: some View {
        if let read = model.startSuggestions[key], !read.value.suggestions.isEmpty {
            Section {
                if !hidden {
                    ForEach(read.value.suggestions) { row($0) }
                    if model.flags.isOn(.work), let more = SuggestedLeads.more(read.value.more) {
                        Button {
                            model.showWork(WorkPlace(kind: .timeline))
                        } label: {
                            HStack {
                                Text(more)
                                Spacer(minLength: 8)
                                Image(icon: "chevron.right")
                                    .appText(.fine)
                                    .foregroundStyle(Paper.tertiary)
                                    .accessibilityHidden(true)
                            }
                        }
                        .foregroundStyle(Paper.accent)
                        .accessibilityHint("Opens Work, Timeline")
                    }
                }
                if let note {
                    Text(note)
                        .appText(.supporting)
                        .foregroundStyle(Paper.secondary)
                }
            } header: {
                header(read)
            }
            .disabled(model.isStarting)
        } else if let why = model.startSuggestions[key].map({ SuggestedLeads.unread($0.value) }) ?? model.startSuggestionsFailed[key] {
            // A first read that failed, or a forge it could not read and no rows, says why (#5790).
            Section {
                Text(why)
                    .appText(.supporting)
                    .foregroundStyle(Paper.secondary)
            } header: {
                Text(SuggestedLeads.heading)
            }
        }
    }

    private func header(_ read: RemoteModel.SuggestionsRead) -> some View {
        HStack(spacing: 8) {
            Text(SuggestedLeads.heading)
            // From an earlier read (#5139): the device's copy, or a read that did not get through.
            if !read.fresh { LastUpdateText(read: read.at).textCase(nil) }
            Spacer(minLength: 8)
            Button(hidden ? "Show" : "Hide") {
                withAnimation { hidden.toggle() }
            }
            .appText(.supporting)
            .textCase(nil)
            .accessibilityLabel(hidden ? "Show suggestions" : "Hide suggestions")
        }
    }

    private func row(_ suggestion: DaemonAPI.WorkLeadSuggestion) -> some View {
        Button {
            pick(suggestion)
        } label: {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Image(icon: "sparkle")
                    .foregroundStyle(Paper.accent)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 2) {
                    Text(SuggestedLeads.title(suggestion))
                        .appText(.reading)
                        .foregroundStyle(Paper.ink)
                        .lineLimit(2)
                    let detail = SuggestedLeads.detail(suggestion)
                    if !detail.isEmpty {
                        Text(detail)
                            .appText(.supporting)
                            .foregroundStyle(Paper.secondary)
                            .monospacedDigit()
                    }
                }
                Spacer(minLength: 0)
            }
            .contentShape(Rectangle())
        }
        .accessibilityLabel(SuggestedLeads.title(suggestion))
        .accessibilityValue(SuggestedLeads.spokenDetail(suggestion))
        .accessibilityHint("Fills in the new session to lead this epic. Starts nothing.")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button("Dismiss", role: .destructive) { dismiss(suggestion) }
        }
        .contextMenu {
            Button { pick(suggestion) } label: { Label("Fill In", icon: "sparkle") }
            Button(role: .destructive) { dismiss(suggestion) } label: { Label("Dismiss", icon: "xmark") }
        }
        .accessibilityAction(named: "Dismiss") { dismiss(suggestion) }
    }

    private func dismiss(_ suggestion: DaemonAPI.WorkLeadSuggestion) {
        note = nil
        Task { note = await model.dismissSuggestion(suggestion, in: key) }
    }
}
